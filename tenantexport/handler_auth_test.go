package tenantexport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These cases never reach the database (db is nil): they check who is let in.

func TestHandlerRefusesWithoutKey(t *testing.T) {
	h := Handler(nil, Options{Service: "svc"})
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a handler with no InternalKey must refuse, got %d", rec.Code)
	}
}

func TestHandlerChecksKey(t *testing.T) {
	h := Handler(nil, Options{Service: "svc", InternalKey: "right-key"})
	for name, key := range map[string]string{"missing": "", "wrong": "wrong-key", "prefix": "right-ke"} {
		req := httptest.NewRequest(http.MethodGet, "/export", nil)
		req.Header.Set("X-Tenant-ID", uuid.NewString())
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s key: want 401, got %d", name, rec.Code)
		}
	}
	// A right key but no tenant is a 400, proving the key check passed before the tenant check.
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.Header.Set("X-API-Key", "right-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("right key without a tenant: want 400, got %d", rec.Code)
	}
}

func TestSafeFileName(t *testing.T) {
	if got := safeFileName.ReplaceAllString(`pos"; evil=1`, "_"); strings.ContainsAny(got, `";= `) {
		t.Fatalf("unsafe characters survived: %q", got)
	}
}
