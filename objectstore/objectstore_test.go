package objectstore

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware"
)

// fakeS3 keeps objects per bucket and checks that every request is signed.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // bucket/key
	headers map[string]http.Header
	puts    int
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}, headers: map[string]http.Header{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			f.objects[p] = b
			f.headers[p] = r.Header.Clone()
			f.puts++
		case http.MethodGet, http.MethodHead:
			b, ok := f.objects[p]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", f.headers[p].Get("Content-Type"))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
		case http.MethodDelete:
			delete(f.objects, p)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// remoteStore is an "inventory" store on a fake S3, with a media volume at root (may be "").
func remoteStore(t *testing.T, root string, opts ...Option) (*Store, *fakeS3) {
	f, srv := newFakeS3(t)
	st, err := New("inventory", Config{
		Endpoint: srv.URL, AccessKeyID: "AK", SecretAccessKey: "SK",
		PublicBucket: "pub", PrivateBucket: "priv", PublicBaseURL: "https://media.example.com/",
		LocalRoot: root, LocalURLBase: "https://inventoryapi.example.com",
	}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return st, f
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKeys(t *testing.T) {
	st, _ := remoteStore(t, "")
	k, err := st.NewKey(Owner{TenantID: "t1"}, TypeItemImages, "jpg")
	if err != nil || !strings.HasPrefix(k, "inventory/t1/item-images/") || !strings.HasSuffix(k, ".jpg") {
		t.Fatalf("new key %q %v", k, err)
	}
	if k, _ := st.Key(Owner{UserID: "u1"}, TypeAvatars, "a.png"); k != "inventory/users/u1/avatars/a.png" {
		t.Errorf("user key %q", k)
	}
	for _, bad := range []struct {
		o Owner
		t string
		p string
	}{
		{Owner{TenantID: "../x"}, TypeItemImages, "a"},
		{Owner{}, TypeItemImages, "a"},
		{Owner{TenantID: "t", UserID: "u"}, TypeItemImages, "a"},
		{Owner{TenantID: "t"}, "nope", "a"},
		{Owner{TenantID: "t"}, TypeItemImages, "../../etc"},
		{Owner{TenantID: "t1"}, TypeItemImages, "a b.jpg"},
	} {
		if _, err := st.Key(bad.o, bad.t, bad.p); err == nil {
			t.Errorf("expected an error for %+v", bad)
		}
	}
}

func TestRemotePutURLGetDelete(t *testing.T) {
	root := t.TempDir()
	st, f := remoteStore(t, root, WithLegacyPrivate(func(rel string) bool { return strings.HasPrefix(rel, "kyc/") }))
	ctx := context.Background()

	pub, _ := st.Key(Owner{TenantID: "t1"}, TypeItemImages, "2026/10/a_b.jpg")
	if err := st.Put(ctx, pub, []byte("img"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if f.headers["pub/"+pub].Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Error("public object must be cached immutably")
	}
	if u := st.URL(pub); u != "https://media.example.com/inventory/t1/item-images/2026/10/a_b.jpg" {
		t.Errorf("public url %q", u)
	}

	priv, _ := st.Key(Owner{TenantID: "t1"}, TypeKYC, "id.pdf")
	if err := st.Put(ctx, priv, []byte("pdf"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["priv/"+priv]; !ok {
		t.Fatal("private object must go to the private bucket")
	}
	if u := st.URL(priv); !strings.Contains(u, "/priv/inventory/t1/kyc/id.pdf?") || !strings.Contains(u, "X-Amz-Signature=") {
		t.Errorf("private url must be presigned: %q", u)
	}
	b, ct, err := st.Get(ctx, priv)
	if err != nil || string(b) != "pdf" || ct != "application/pdf" {
		t.Fatalf("get %q %q %v", b, ct, err)
	}

	// Old paths not relinked yet stay on the volume and keep their address on the media route.
	writeFile(t, root, "uploads/menu/x.jpg", "old")
	if u := st.URL("/media/uploads/menu/x.jpg"); u != "https://inventoryapi.example.com/media/uploads/menu/x.jpg" {
		t.Errorf("legacy url %q", u)
	}
	if b, _, err := st.Get(ctx, "/media/uploads/menu/x.jpg"); err != nil || string(b) != "old" {
		t.Errorf("legacy get %q %v", b, err)
	}
	if u := st.URL("https://cdn.example.com/a.png"); u != "https://cdn.example.com/a.png" {
		t.Error("external URLs pass through")
	}
	if st.URL("") != "" || st.URL("coffee") != "coffee" {
		t.Error("empty stays empty, unknown values pass through")
	}

	rc, size, ctype, err := st.Open(ctx, priv)
	if err != nil {
		t.Fatal(err)
	}
	streamed, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(streamed) != "pdf" || size != 3 || ctype != "application/pdf" {
		t.Errorf("open: %q %d %q", streamed, size, ctype)
	}
	if _, _, _, err := st.Open(ctx, "inventory/t1/kyc/missing.pdf"); err != ErrNotFound {
		t.Errorf("open missing: %v", err)
	}

	if err := st.Delete(ctx, priv); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Exists(ctx, priv); ok {
		t.Error("deleted object still exists")
	}
	if _, _, err := st.Get(ctx, priv); err != ErrNotFound {
		t.Errorf("missing object: %v", err)
	}
	if err := st.Delete(ctx, "https://elsewhere/x.png"); err != nil {
		t.Error("deleting an external URL is a no-op")
	}
}

func TestNormalizeOwnedIsLegacy(t *testing.T) {
	st, _ := remoteStore(t, t.TempDir(), WithLegacyBaseURL("http://localhost:4005/", ""))
	key, _ := st.Key(Owner{TenantID: "t1"}, TypeItemImages, "2026/10/a.jpg")
	priv, _ := st.Key(Owner{TenantID: "t1"}, TypeKYC, "id.pdf")
	cases := map[string]string{
		"https://media.example.com/" + key:                              key,
		st.URL(priv):                                                    priv,
		"https://inventoryapi.example.com/media/" + key:                 key,
		"https://inventoryapi.example.com/media/uploads/menu/x.jpg":     "/media/uploads/menu/x.jpg",
		"https://INVENTORYAPI.example.com/media/uploads/menu/x.jpg?v=1": "/media/uploads/menu/x.jpg",
		"http://localhost:4005/media/uploads/x.png":                     "/media/uploads/x.png",
		"https://inventoryapi.example.com/api/v1/other":                 "https://inventoryapi.example.com/api/v1/other",
		"https://cdn.example.com/a.png":                                 "https://cdn.example.com/a.png",
		"https://media.example.com/not/a/valid":                         "https://media.example.com/not/a/valid",
		"/media/uploads/menu/x.jpg":                                     "/media/uploads/menu/x.jpg",
		key:                                                             key,
		"":                                                              "",
		"data:image/png;base64,AAAA":                                    "data:image/png;base64,AAAA",
	}
	for in, want := range cases {
		if got := st.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if !st.Owned(key) || !st.Owned("https://media.example.com/"+key) {
		t.Error("own keys, in key or URL form, are owned")
	}
	for _, ref := range []string{"/media/uploads/menu/x.jpg", "https://cdn.example.com/a.png", "logistics/t1/kyc/a.pdf", "", "coffee"} {
		if st.Owned(ref) {
			t.Errorf("%q must not be owned", ref)
		}
	}
	if !st.IsLegacy("https://inventoryapi.example.com/media/uploads/menu/x.jpg") || st.IsLegacy(key) {
		t.Error("IsLegacy")
	}
	if _, _, err := st.Get(context.Background(), "inventory/t1/nope/x"); err == nil {
		t.Error("malformed key must fail")
	}
}

func TestRelink(t *testing.T) {
	root := t.TempDir()
	st, f := remoteStore(t, root)
	ctx := context.Background()
	writeFile(t, root, "uploads/menu/logo.png", "png-bytes")
	writeFile(t, root, "images/acme/items/a.jpg", "jpg-bytes")
	acme := Owner{TenantID: "t1"}

	rl := st.NewRelinker()
	k1, changed, err := rl.Relink(ctx, "https://inventoryapi.example.com/media/uploads/menu/logo.png", acme, TypeLogos)
	if err != nil || !changed || !strings.HasPrefix(k1, "inventory/t1/logos/") || !strings.HasSuffix(k1, ".png") {
		t.Fatalf("relink %q %v %v", k1, changed, err)
	}
	if string(f.objects["pub/"+k1]) != "png-bytes" {
		t.Fatal("file not uploaded to its key")
	}
	// The same file for the same owner (another row, a rerun) is not uploaded twice.
	k2, _, _ := rl.Relink(ctx, "/media/uploads/menu/logo.png", acme, TypeLogos)
	k3, _, _ := st.NewRelinker().Relink(ctx, "/media/uploads/menu/logo.png", acme, TypeLogos)
	if k2 != k1 || k3 != k1 || f.puts != 1 {
		t.Errorf("dedupe: %q %q puts=%d", k2, k3, f.puts)
	}
	// Another tenant gets its own copy.
	if k4, _, _ := rl.Relink(ctx, "/media/uploads/menu/logo.png", Owner{TenantID: "t2"}, TypeLogos); !strings.HasPrefix(k4, "inventory/t2/logos/") {
		t.Errorf("other owner key %q", k4)
	}
	// Already moved values, external URLs and empty values are left alone.
	for _, ref := range []string{k1, "https://cdn.example.com/a.png", ""} {
		if got, changed, err := rl.Relink(ctx, ref, acme, TypeLogos); got != ref || changed || err != nil {
			t.Errorf("relink(%q) = %q %v %v", ref, got, changed, err)
		}
	}
	if _, changed, err := rl.Relink(ctx, "/media/gone.png", acme, TypeLogos); changed || err != ErrNotFound || rl.Missing != 1 {
		t.Errorf("missing file: %v %v %d", changed, err, rl.Missing)
	}
	if rl.Moved != 2 || rl.Reused != 1 {
		t.Errorf("counts moved=%d reused=%d", rl.Moved, rl.Reused)
	}
}

func TestLocalMode(t *testing.T) {
	root := t.TempDir()
	signer := httpware.NewMediaSigner("secret", time.Hour)
	st, err := New("logistics", Config{LocalRoot: root, LocalURLBase: "http://localhost:4005/"}, WithLocalSigner(signer))
	if err != nil {
		t.Fatal(err)
	}
	if st.Remote() || !st.Enabled() {
		t.Fatal("local mode expected")
	}
	ctx := context.Background()
	k, _ := st.Key(Owner{TenantID: "t1"}, TypeKYC, "a.pdf")
	if err := st.Put(ctx, k, []byte("x"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(k))); err != nil {
		t.Fatal("file not written under the media root")
	}
	if u := st.URL(k); !strings.HasPrefix(u, "http://localhost:4005/media/logistics/t1/kyc/a.pdf") || !strings.Contains(u, "sig=") {
		t.Errorf("local private url must be signed: %q", u)
	}
	writeFile(t, root, "uploads/old.png", "o")
	if b, _, err := st.Get(ctx, "/media/uploads/old.png"); err != nil || string(b) != "o" {
		t.Errorf("legacy local get %q %v", b, err)
	}
	if u := st.URL("/media/uploads/old.png"); u != "http://localhost:4005/media/uploads/old.png" {
		t.Errorf("legacy local url %q", u)
	}

	h := st.MediaHandler()
	serve := func(h http.Handler, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		http.StripPrefix("/media", h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	if serve(h, strings.TrimPrefix(st.URL(k), "http://localhost:4005")).Code != http.StatusOK {
		t.Error("signed private link must be served")
	}
	if serve(h, "/media/"+k).Code != http.StatusNotFound {
		t.Error("unsigned private file must not be served")
	}
	pub, _ := st.Key(Owner{TenantID: "t1"}, TypeLogos, "l.png")
	_ = st.Put(ctx, pub, []byte("p"), "image/png")
	if serve(h, "/media/"+pub).Code != http.StatusOK {
		t.Error("public file must be served")
	}
	unsigned, _ := New("logistics", Config{LocalRoot: root})
	if serve(unsigned.MediaHandler(), "/media/"+k).Code != http.StatusNotFound {
		t.Error("without a signer private files are never served")
	}
	if err := st.Delete(ctx, k); err != nil {
		t.Fatal(err)
	}
	none, _ := New("x", Config{})
	if none.Enabled() || none.MediaHandler() != nil || none.Put(ctx, "x/t/kyc/a", nil, "") != ErrNotConfigured {
		t.Error("an unconfigured store must say so")
	}
}

func TestRemoteMediaHandler(t *testing.T) {
	root := t.TempDir()
	st, _ := remoteStore(t, root, WithLegacyPrivate(func(rel string) bool { return strings.HasPrefix(rel, "kyc/") }))
	writeFile(t, root, "uploads/menu/x.jpg", "old")
	writeFile(t, root, "kyc/a.pdf", "secret")
	h := st.MediaHandler()
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		http.StripPrefix("/media", h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	if rec := get("/media/uploads/menu/x.jpg"); rec.Code != http.StatusOK || rec.Body.String() != "old" {
		t.Errorf("an old public file is still served from the volume: %d", rec.Code)
	}
	if get("/media/kyc/a.pdf").Code != http.StatusNotFound {
		t.Error("an old private file needs a signed link")
	}
	rk, _ := st.Key(Owner{TenantID: "t1"}, TypeLogos, "l.png")
	if rec := get("/media/" + rk); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://media.example.com/"+rk {
		t.Errorf("a public key redirects to R2: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	pk, _ := st.Key(Owner{TenantID: "t1"}, TypeKYC, "a.pdf")
	if get("/media/"+pk).Code != http.StatusNotFound {
		t.Error("a private key must not redirect")
	}
	if get("/media/../../etc/passwd").Code != http.StatusNotFound {
		t.Error("a climbing path must not be served")
	}
	noVolume, _ := remoteStore(t, "")
	rec := httptest.NewRecorder()
	http.StripPrefix("/media", noVolume.MediaHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/uploads/menu/x.jpg", nil))
	if rec.Code != http.StatusNotFound {
		t.Error("without a volume old paths are gone")
	}
}

func TestPrepare(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var jp, pn bytes.Buffer
	_ = jpeg.Encode(&jp, img, nil)
	_ = png.Encode(&pn, img)

	up, err := Prepare(jp.Bytes(), TypeItemImages)
	if err != nil || up.ContentType != "image/jpeg" || up.Ext != ".jpg" {
		t.Fatalf("jpeg: %+v %v", up, err)
	}
	// Bytes appended after the image are dropped by the re-encode.
	withTail := append(append([]byte{}, pn.Bytes()...), []byte("<script>x</script>")...)
	up, err = Prepare(withTail, TypeAvatars)
	if err != nil || bytes.Contains(up.Data, []byte("script")) {
		t.Fatalf("png must be re-encoded: %v", err)
	}
	if _, err := Prepare([]byte("%PDF-1.4 hello"), TypeItemImages); !IsUploadError(err) {
		t.Error("a PDF is not an item image")
	}
	if up, err := Prepare([]byte("%PDF-1.4 hello"), TypeKYC); err != nil || up.Ext != ".pdf" {
		t.Errorf("KYC accepts PDFs: %v", err)
	}
	if _, err := Prepare(make([]byte, 3*mb), TypeAvatars); !IsUploadError(err) {
		t.Error("oversized upload must be refused")
	}
	if _, err := Prepare(nil, TypeAvatars); !IsUploadError(err) {
		t.Error("empty upload must be refused")
	}
}

func TestCheck(t *testing.T) {
	_, srv := newFakeS3(t)
	ok, _ := New("x", Config{Endpoint: srv.URL, AccessKeyID: "AK", SecretAccessKey: "SK", PublicBucket: "p", PrivateBucket: "q"})
	// The fake answers a bucket HEAD with 404 until something is stored in it.
	if err := ok.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing bucket must fail the check: %v", err)
	}
	denied, _ := New("x", Config{Endpoint: srv.URL, AccessKeyID: "WRONG", SecretAccessKey: "SK", PublicBucket: "p", PrivateBucket: "q"})
	if err := denied.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("bad credentials must fail the check: %v", err)
	}
	none, _ := New("x", Config{})
	if none.Check(context.Background()) != nil {
		t.Error("an unconfigured store has nothing to check")
	}
}
