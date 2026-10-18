package tenantexport

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPgTextArray(t *testing.T) {
	if got := pgTextArray(nil); got != "{}" {
		t.Fatalf("empty: %q", got)
	}
	if got := pgTextArray([]string{"api_key", `we"ird`}); got != `{"api_key","we\"ird"}` {
		t.Fatalf("quoted: %q", got)
	}
	for col, secret := range map[string]bool{"password_hash": true, "api_key": true, "refresh_token": true,
		"pin": true, "pin_hash": true, "encrypted_value": true, "name": false, "spinner": false, "tenant_id": false} {
		if DefaultSecretColumns.MatchString(col) != secret {
			t.Errorf("%s: secret=%v expected %v", col, !secret, secret)
		}
	}
}

// TestExportOnPostgres needs TENANTEXPORT_TEST_POSTGRES_URL (a scratch database).
func TestExportOnPostgres(t *testing.T) {
	dsn := os.Getenv("TENANTEXPORT_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TENANTEXPORT_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // keep the search_path below on the one connection
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS svc CASCADE`, `CREATE SCHEMA svc`, `SET search_path = svc`,
		`CREATE TABLE orders (id serial PRIMARY KEY, tenant_id uuid NOT NULL, total numeric, note text)`,
		`CREATE TABLE users (id serial PRIMARY KEY, tenant_id varchar(64), email text, password_hash text, api_key text)`,
		`CREATE TABLE outbox_events (id serial PRIMARY KEY, tenant_id uuid)`,
		`CREATE TABLE countries (code text PRIMARY KEY)`,
		`CREATE TABLE orders_bk_20261004 (id int, tenant_id uuid)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO orders (tenant_id, total, note) VALUES ($1, 10, 'first'), ($1, 20.5, 'second'), ($2, 99, 'other tenant')`, []any{a, b}},
		{`INSERT INTO users (tenant_id, email, password_hash, api_key) VALUES ($1, 'a@x', 'HASH', 'KEY'), ($2, 'b@x', 'H2', 'K2')`, []any{a.String(), b.String()}},
		{`INSERT INTO outbox_events (tenant_id) VALUES ($1)`, []any{a}},
		{`INSERT INTO orders_bk_20261004 (id, tenant_id) VALUES (1, $1)`, []any{a}},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	sum, err := Write(ctx, db, a, &buf, Options{Service: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rows != 3 || sum.Tables["orders"] != 2 || sum.Tables["users"] != 1 || len(sum.Skipped) != 0 {
		t.Fatalf("summary %+v", sum)
	}
	if _, ok := sum.Tables["outbox_events"]; ok {
		t.Fatal("infrastructure tables are not exported")
	}
	if _, ok := sum.Tables["orders_bk_20261004"]; ok {
		t.Fatal("leftover backup copies are not exported")
	}

	lines := readLines(t, buf.Bytes())
	if len(lines) != 5 || lines[0]["type"] != "export" || lines[0]["schema"] != "svc" || lines[4]["type"] != "summary" {
		t.Fatalf("layout: %v", lines)
	}
	gz, _ := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	var plain bytes.Buffer
	_, _ = plain.ReadFrom(gz)
	body := plain.String()
	for _, leak := range []string{"HASH", "KEY", "other tenant", "b@x"} {
		if strings.Contains(body, leak) {
			t.Fatalf("export leaks %q", leak)
		}
	}
	if !strings.Contains(body, `"note": "first"`) && !strings.Contains(body, `"note":"first"`) {
		t.Fatalf("missing tenant row: %s", body)
	}

	// Handler: tenant from the header, gzip stream body.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.Header.Set("X-Tenant-ID", b.String())
	Handler(db, Options{Service: "svc"}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ContentType {
		t.Fatalf("handler %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if l := readLines(t, rec.Body.Bytes()); len(l) != 4 || l[3]["rows"].(float64) != 2 {
		t.Fatalf("tenant b export: %v", l)
	}
	rec = httptest.NewRecorder()
	Handler(db, Options{Service: "svc"}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/export", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant must be refused: %d", rec.Code)
	}
}

func readLines(t *testing.T, gzBytes []byte) []map[string]any {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}
