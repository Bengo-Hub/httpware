package httpware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticMedia(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "uploads", "menu"), 0o755)
	img := "3f0e7a2c-1111-4c3a-9a55-2f6d7c1b0e01_1720000000.png"
	_ = os.WriteFile(filepath.Join(root, "uploads", "menu", img), []byte("\x89PNG\r\n\x1a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "logo.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "x.svg"), []byte("<svg/>"), 0o644)
	h := StaticMedia(root, MediaOptions{})

	get := func(p string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/uploads/menu/" + img)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("fingerprinted: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing cross-origin or nosniff headers")
	}
	if again := get("/uploads/menu/"+img, "If-None-Match", rec.Header().Get("ETag")); again.Code != http.StatusNotModified {
		t.Fatalf("revalidation should be 304, got %d", again.Code)
	}
	if rec := get("/logo.png"); rec.Header().Get("Cache-Control") != "public, max-age=86400, stale-while-revalidate=604800" {
		t.Fatalf("plain name cache-control %q", rec.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/uploads/", "/uploads/menu", "/", "/.env", "/../../etc/passwd", "/uploads/../.env"} {
		if rec := get(p); rec.Code != http.StatusNotFound {
			t.Fatalf("%s should be 404, got %d", p, rec.Code)
		}
	}
	if rec := get("/x.svg"); rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("svg must carry a locked-down CSP")
	}
	req := httptest.NewRequest(http.MethodPost, "/logo.png", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST should be 405, got %d", rec.Code)
	}
}

func TestBypassForStreaming(t *testing.T) {
	blocked := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(599) })
	}
	h := BypassForStreaming(blocked)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	for _, hdr := range [][2]string{{"Upgrade", "websocket"}, {"Accept", "text/event-stream"}} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(hdr[0], hdr[1])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s should bypass, got %d", hdr[0], rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 599 {
		t.Fatal("normal request must go through the middleware")
	}
}

func TestStaticMediaPrivate(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "uploads", "kyc"), 0o755)
	name := "3f0e7a2c-1111-4c3a-9a55-2f6d7c1b0e01_1720000000.jpg"
	_ = os.WriteFile(filepath.Join(root, "uploads", "kyc", name), []byte("x"), 0o644)
	h := StaticMedia(root, MediaOptions{Private: func(p string) bool { return strings.HasPrefix(p, "/uploads/kyc/") }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/uploads/kyc/"+name, nil))
	if rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("private file got %q acao=%q", rec.Header().Get("Cache-Control"), rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
