// Package tenantexport writes one tenant's rows of a service database as a gzipped JSON Lines
// stream, for the tenant backups the SSO client area offers (auth-api collects one export per
// service). It replaces the per-service backup modules' in-memory json_agg dumps: rows are read
// and written one at a time, so memory stays flat however large the tenant is.
//
// Stream layout (one JSON object per line, gzip compressed):
//
//	{"type":"export","service":"pos","tenant_id":"...","schema":"public","generated_at":"..."}
//	{"type":"row","table":"orders","row":{...}}
//	...
//	{"type":"summary","tables":{"orders":120,...},"rows":340,"skipped":["..."]}
//
// Every table of the service's schema with a tenant_id column is exported. Columns whose names
// look like secrets (password, secret, token, api key, credential, private key, pin) are left out
// of each row; Options.ExcludeTables drops whole tables.
package tenantexport

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ContentType is the media type of an export stream.
const ContentType = "application/x-ndjson+gzip"

// Options configure an export.
type Options struct {
	// Service names the export (the file inside the tenant's archive).
	Service string
	// ExcludeTables are tables never exported (for example the outbox or the old backups table).
	ExcludeTables []string
	// SecretColumns overrides the column-name pattern of values that are never exported.
	SecretColumns *regexp.Regexp
}

// DefaultSecretColumns matches column names whose values must never leave the service.
var DefaultSecretColumns = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_?key|credential|private_?key|pin_hash|^pin$|otp|encrypted)`)

// alwaysExcluded are infrastructure tables that hold no tenant business data.
var alwaysExcluded = []string{"outbox_events", "processed_events", "backups", "backup_destinations", "atlas_schema_revisions"}

// Summary describes a finished export.
type Summary struct {
	Tables  map[string]int64 `json:"tables"`
	Rows    int64            `json:"rows"`
	Skipped []string         `json:"skipped,omitempty"`
}

type table struct {
	name       string
	tenantUUID bool
	secret     []string
}

// Write streams the tenant's export to w (gzip compressed). A table that cannot be read is
// listed in Summary.Skipped rather than failing the whole export.
func Write(ctx context.Context, db *sql.DB, tenantID uuid.UUID, w io.Writer, opt Options) (Summary, error) {
	sum := Summary{Tables: map[string]int64{}}
	secretRe := opt.SecretColumns
	if secretRe == nil {
		secretRe = DefaultSecretColumns
	}
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return sum, fmt.Errorf("tenantexport: schema: %w", err)
	}
	tables, err := tenantTables(ctx, db, schema, secretRe, append(append([]string{}, alwaysExcluded...), opt.ExcludeTables...))
	if err != nil {
		return sum, err
	}

	gz := gzip.NewWriter(w)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(map[string]any{
		"type": "export", "service": opt.Service, "tenant_id": tenantID.String(), "schema": schema,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return sum, err
	}
	for _, t := range tables {
		n, err := writeTable(ctx, db, schema, t, tenantID, enc)
		if err != nil {
			if ctx.Err() != nil {
				return sum, ctx.Err()
			}
			sum.Skipped = append(sum.Skipped, t.name+": "+err.Error())
			continue
		}
		sum.Tables[t.name] = n
		sum.Rows += n
	}
	if err := enc.Encode(map[string]any{"type": "summary", "tables": sum.Tables, "rows": sum.Rows, "skipped": sum.Skipped}); err != nil {
		return sum, err
	}
	return sum, gz.Close()
}

func tenantTables(ctx context.Context, db *sql.DB, schema string, secretRe *regexp.Regexp, exclude []string) ([]table, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = $1 AND t.table_type = 'BASE TABLE'
		  AND c.table_name IN (SELECT table_name FROM information_schema.columns WHERE table_schema = $1 AND column_name = 'tenant_id')
		ORDER BY c.table_name, c.ordinal_position`, schema)
	if err != nil {
		return nil, fmt.Errorf("tenantexport: tables: %w", err)
	}
	defer rows.Close()
	skip := map[string]bool{}
	for _, t := range exclude {
		skip[t] = true
	}
	byName := map[string]*table{}
	var order []string
	for rows.Next() {
		var name, col, typ string
		if err := rows.Scan(&name, &col, &typ); err != nil {
			return nil, err
		}
		if skip[name] || strings.HasSuffix(name, "_bk") || strings.Contains(name, "_bk_") {
			continue
		}
		t := byName[name]
		if t == nil {
			t = &table{name: name}
			byName[name] = t
			order = append(order, name)
		}
		if col == "tenant_id" {
			t.tenantUUID = typ == "uuid"
		} else if secretRe.MatchString(col) {
			t.secret = append(t.secret, col)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]table, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, nil
}

func writeTable(ctx context.Context, db *sql.DB, schema string, t table, tenantID uuid.UUID, enc *json.Encoder) (int64, error) {
	// Identifiers come from information_schema, quoted; the tenant id is a bound parameter cast to
	// the column's type so the tenant_id index is used.
	cast := ""
	if t.tenantUUID {
		cast = "::uuid"
	}
	q := fmt.Sprintf(`SELECT (to_jsonb(x) - $2::text[])::text FROM %s.%s x WHERE x.tenant_id = $1%s`,
		quoteIdent(schema), quoteIdent(t.name), cast)
	rows, err := db.QueryContext(ctx, q, tenantID.String(), pgTextArray(t.secret))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return n, err
		}
		if err := enc.Encode(struct {
			Type  string          `json:"type"`
			Table string          `json:"table"`
			Row   json.RawMessage `json:"row"`
		}{"row", t.name, json.RawMessage(raw)}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// pgTextArray renders a Postgres text[] literal (column names only, from information_schema).
func pgTextArray(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return "{" + strings.Join(q, ",") + "}"
}

// Handler serves GET exports for service-to-service callers (auth-api). Mount it behind the
// service's API-key middleware; the tenant comes from the X-Tenant-ID header.
func Handler(db *sql.DB, opt Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID, err := uuid.Parse(r.Header.Get("X-Tenant-ID"))
		if err != nil {
			http.Error(w, `{"error":"X-Tenant-ID must be a tenant uuid"}`, http.StatusBadRequest)
			return
		}
		// A large tenant takes longer than the server's write timeout; lift it for this response.
		// Mount the route outside any request-timeout middleware (or behind a streaming bypass).
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.jsonl.gz"`, opt.Service))
		// Errors after the first byte cannot change the status; the missing summary line tells
		// the caller the stream is incomplete.
		_, _ = Write(r.Context(), db, tenantID, w, opt)
	})
}
