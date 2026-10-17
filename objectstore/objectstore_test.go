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

func remoteStore(t *testing.T, opts ...Option) (*Store, *fakeS3) {
	f, srv := newFakeS3(t)
	st, err := New("inventory", Config{
		Endpoint: srv.URL, AccessKeyID: "AK", SecretAccessKey: "SK",
		PublicBucket: "pub", PrivateBucket: "priv", PublicBaseURL: "https://media.example.com/",
	}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return st, f
}

func TestKeys(t *testing.T) {
	st, _ := remoteStore(t)
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
	} {
		if _, err := st.Key(bad.o, bad.t, bad.p); err == nil {
			t.Errorf("expected an error for %+v", bad)
		}
	}
}

func TestRemotePutURLGetDelete(t *testing.T) {
	st, f := remoteStore(t, WithLegacyPrivate(func(rel string) bool { return strings.HasPrefix(rel, "kyc/") }))
	ctx := context.Background()

	if _, err := st.Key(Owner{TenantID: "t1"}, TypeItemImages, "a b.jpg"); err == nil {
		t.Error("spaces are not allowed in keys")
	}
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

	// Legacy paths resolve to their copies, private ones to the private bucket.
	if u := st.URL("/media/uploads/menu/x.jpg"); u != "https://media.example.com/inventory/legacy/public/uploads/menu/x.jpg" {
		t.Errorf("legacy public %q", u)
	}
	if u := st.URL("/media/kyc/doc.pdf"); !strings.Contains(u, "/priv/inventory/legacy/private/kyc/doc.pdf?") {
		t.Errorf("legacy private %q", u)
	}
	if u := st.URL("https://cdn.example.com/a.png"); u != "https://cdn.example.com/a.png" {
		t.Error("external URLs pass through")
	}
	if st.URL("") != "" {
		t.Error("empty stays empty")
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

func TestLegacyKeys(t *testing.T) {
	st, f := remoteStore(t, WithLegacyKeys(), WithLegacyPrivate(func(string) bool { return true }))
	f.objects["priv/inventory/legacy/private/documents/t1/bills/b1/a.pdf"] = []byte("old")
	b, _, err := st.Get(context.Background(), "documents/t1/bills/b1/a.pdf")
	if err != nil || string(b) != "old" {
		t.Fatalf("old key must read its copy: %q %v", b, err)
	}
	if u := st.URL("documents/t1/bills/b1/a.pdf"); !strings.Contains(u, "/priv/inventory/legacy/private/documents/t1/bills/b1/a.pdf?") {
		t.Errorf("old key url %q", u)
	}
	// A malformed key of this service is an error, not a legacy path.
	if _, _, err := st.Get(context.Background(), "inventory/t1/nope/x"); err == nil {
		t.Error("malformed own key must fail")
	}
	plain, _ := remoteStore(t)
	if _, _, err := plain.Get(context.Background(), "documents/t1/bills/b1/a.pdf"); err == nil {
		t.Error("without WithLegacyKeys an old key is invalid")
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
	// An old file stays where it was and keeps its path.
	_ = os.MkdirAll(filepath.Join(root, "uploads"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "uploads", "old.png"), []byte("o"), 0o644)
	if b, _, err := st.Get(ctx, "/media/uploads/old.png"); err != nil || string(b) != "o" {
		t.Errorf("legacy local get %q %v", b, err)
	}
	if u := st.URL("/media/uploads/old.png"); u != "http://localhost:4005/media/uploads/old.png" {
		t.Errorf("legacy local url %q", u)
	}
	if err := st.Delete(ctx, k); err != nil {
		t.Fatal(err)
	}

	none, _ := New("x", Config{})
	if none.Enabled() || none.Put(ctx, "x/t/kyc/a", nil, "") != ErrNotConfigured {
		t.Error("an unconfigured store must say so")
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

func TestCopyLegacyTree(t *testing.T) {
	st, f := remoteStore(t)
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "images", "acme", "items"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".trash"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "images", "acme", "items", "a.png"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(root, ".trash", "b.png"), []byte("b"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "c.tmp"), []byte("c"), 0o644)

	res, err := st.CopyLegacyTree(context.Background(), root)
	if err != nil || res.Copied != 1 || res.Failed != 0 {
		t.Fatalf("first run %+v %v", res, err)
	}
	if _, ok := f.objects["pub/inventory/legacy/public/images/acme/items/a.png"]; !ok {
		t.Fatal("file not copied to its legacy key")
	}
	if f.headers["pub/inventory/legacy/public/images/acme/items/a.png"].Get("Content-Type") != "image/png" {
		t.Error("content type from the extension")
	}
	res, _ = st.CopyLegacyTree(context.Background(), root)
	if res.Copied != 0 || res.Skipped != 1 {
		t.Errorf("second run must skip: %+v", res)
	}
}
