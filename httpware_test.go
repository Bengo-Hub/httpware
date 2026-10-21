package httpware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(GetTenantID(r.Context()) + "|" + GetTenantSlug(r.Context())))
	})
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequestIDKeepsWellFormedAndReplacesBad(t *testing.T) {
	var got string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = GetRequestID(r.Context()) }))
	do(h, "GET", "/", map[string]string{HeaderRequestID: "req-123.abc:9"})
	if got != "req-123.abc:9" {
		t.Fatalf("a well formed id must be kept, got %q", got)
	}
	for _, bad := range []string{"has space", "new\nline", strings.Repeat("a", maxRequestIDLen+1), "<script>"} {
		got = ""
		rec := do(h, "GET", "/", map[string]string{HeaderRequestID: bad})
		if got == bad || got == "" || rec.Header().Get(HeaderRequestID) != got {
			t.Errorf("a bad id %q must be replaced, got %q", bad, got)
		}
	}
}

func TestRequireServiceKey(t *testing.T) {
	h := RequireServiceKey("secret-key")(ok())
	if rec := do(h, "GET", "/", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}
	if rec := do(h, "GET", "/", map[string]string{HeaderAPIKey: "secret-kex"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", rec.Code)
	}
	if rec := do(h, "GET", "/", map[string]string{HeaderAPIKey: "secret-key"}); rec.Code != http.StatusOK {
		t.Errorf("right key: %d", rec.Code)
	}

	// An empty configured key refuses everything, including an empty or any supplied header.
	closed := RequireServiceKey("")(ok())
	for _, hdr := range []map[string]string{nil, {HeaderAPIKey: ""}, {HeaderAPIKey: "anything"}} {
		if rec := do(closed, "GET", "/", hdr); rec.Code != http.StatusUnauthorized {
			t.Errorf("empty configured key must refuse, got %d for %v", rec.Code, hdr)
		}
	}

	// A custom header and the principal hook.
	called := false
	custom := RequireServiceKey("k", WithKeyHeader("X-Internal"), OnServiceAuthorized(func(r *http.Request) *http.Request {
		called = true
		return r.WithContext(WithTenantID(r.Context(), "svc"))
	}))(ok())
	rec := do(custom, "GET", "/", map[string]string{"X-Internal": "k"})
	if rec.Code != http.StatusOK || !called || !strings.HasPrefix(rec.Body.String(), "svc|") {
		t.Errorf("custom header and hook: %d %q called=%v", rec.Code, rec.Body.String(), called)
	}
}

func claimsFor(id, slug string, owner bool) func(context.Context) (string, string, bool, bool) {
	return func(context.Context) (string, string, bool, bool) { return id, slug, owner, true }
}

func TestTenantV2RejectsMismatchedTenant(t *testing.T) {
	const idA = "11111111-1111-1111-1111-111111111111"
	const idB = "22222222-2222-2222-2222-222222222222"
	param := ""
	cfg := TenantConfig{
		ClaimsExtractor: claimsFor(idA, "acme", false),
		URLParamFunc:    func(*http.Request, string) string { return param },
	}
	h := TenantV2(cfg)(ok())

	cases := []struct {
		name  string
		hdr   map[string]string
		param string
		want  int
	}{
		{"matching header id", map[string]string{HeaderTenantID: idA}, "", 200},
		{"matching header id, other case", map[string]string{HeaderTenantID: strings.ToUpper(idA)}, "", 200},
		{"other header id", map[string]string{HeaderTenantID: idB}, "", 403},
		{"other header slug", map[string]string{HeaderTenantSlug: "rival"}, "", 403},
		{"matching url slug", nil, "acme", 200},
		{"other url slug", nil, "rival", 403},
		{"matching url id", nil, idA, 200},
		{"other url id", nil, idB, 403},
	}
	for _, c := range cases {
		param = c.param
		if rec := do(h, "GET", "/", c.hdr); rec.Code != c.want {
			t.Errorf("%s: want %d, got %d (%s)", c.name, c.want, rec.Code, rec.Body.String())
		}
	}

	// The escape hatch for a service migrating in steps.
	cfg.AllowMismatch = true
	param = "rival"
	if rec := do(TenantV2(cfg)(ok()), "GET", "/", nil); rec.Code != 200 {
		t.Errorf("AllowMismatch must restore the old behaviour, got %d", rec.Code)
	}
}

func TestTenantV2PlatformOwnerAndNoClaims(t *testing.T) {
	// A platform owner may scope a request to any tenant through the URL.
	owner := TenantV2(TenantConfig{
		ClaimsExtractor: claimsFor("11111111-1111-1111-1111-111111111111", "platform", true),
		URLParamFunc:    func(*http.Request, string) string { return "rival" },
	})(ok())
	rec := do(owner, "GET", "/", map[string]string{HeaderTenantSlug: "rival"})
	if rec.Code != 200 || rec.Body.String() != "|rival" {
		t.Errorf("platform owner: %d %q", rec.Code, rec.Body.String())
	}

	// No claims (service call, public route): the header is used as before.
	anon := TenantV2(TenantConfig{ClaimsExtractor: func(context.Context) (string, string, bool, bool) { return "", "", false, false }})(ok())
	rec = do(anon, "GET", "/", map[string]string{HeaderTenantID: "abc", HeaderTenantSlug: "acme"})
	if rec.Code != 200 || rec.Body.String() != "abc|acme" {
		t.Errorf("no claims: %d %q", rec.Code, rec.Body.String())
	}

	// Required with nothing resolved answers 400 as JSON.
	req := TenantV2(TenantConfig{Required: true})(ok())
	rec = do(req, "GET", "/", nil)
	if rec.Code != 400 || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("required: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestCORS(t *testing.T) {
	cfg := StandardCORS([]string{"https://pos.example.com", "https://*.example.org"})
	h := CORS(cfg)(ok())

	// Allowed origin: reflected with credentials, Vary: Origin always.
	rec := do(h, "GET", "/", map[string]string{"Origin": "https://pos.example.com"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://pos.example.com" ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
		t.Errorf("allowed origin headers: %v", rec.Header())
	}
	// Wildcard subdomain pattern.
	if rec = do(h, "GET", "/", map[string]string{"Origin": "https://a.example.org"}); rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("a subdomain pattern must match")
	}
	// Not allowed: no CORS headers, but the request itself still runs.
	rec = do(h, "GET", "/", map[string]string{"Origin": "https://evil.example.net"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Code != 200 {
		t.Errorf("disallowed origin must get no CORS headers: %v", rec.Header())
	}
	// No Origin header: not a CORS request.
	if rec = do(h, "GET", "/", nil); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("no Origin, no CORS headers")
	}
	// Preflight: allowed 204, disallowed 403.
	pre := map[string]string{"Origin": "https://pos.example.com", "Access-Control-Request-Method": "POST"}
	rec = do(h, "OPTIONS", "/", pre)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Methods") == "" || rec.Header().Get("Access-Control-Max-Age") != "86400" {
		t.Errorf("allowed preflight: %d %v", rec.Code, rec.Header())
	}
	pre["Origin"] = "https://evil.example.net"
	if rec = do(h, "OPTIONS", "/", pre); rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disallowed preflight: %d %v", rec.Code, rec.Header())
	}

	// "*" never goes with credentials.
	any := CORS(CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true})(ok())
	rec = do(any, "GET", "/", map[string]string{"Origin": "https://anything.test"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("wildcard origin must not carry credentials: %v", rec.Header())
	}
}

func TestOriginPatternCannotCrossPath(t *testing.T) {
	if allowed, _ := originAllowed([]string{"https://*.example.org"}, "https://evil.test/.example.org"); allowed {
		t.Error("the wildcard must not match across a path")
	}
	if allowed, _ := originAllowed([]string{"https://*.example.org"}, "https://user@evil.test.example.org"); allowed {
		t.Error("the wildcard must not match userinfo")
	}
}

func TestRecover(t *testing.T) {
	log := zap.NewNop()
	// A panic before any write answers 500.
	h := Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	if rec := do(h, "GET", "/", nil); rec.Code != 500 {
		t.Errorf("panic: want 500, got %d", rec.Code)
	}
	// A panic after the response started must not append a second body.
	h = Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}))
	if rec := do(h, "GET", "/", nil); rec.Body.String() != "partial" {
		t.Errorf("late panic must not add a body, got %q", rec.Body.String())
	}
	// http.ErrAbortHandler is passed on to the server.
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Errorf("ErrAbortHandler must be re-panicked, got %v", r)
		}
	}()
	h = Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	do(h, "GET", "/", nil)
}
