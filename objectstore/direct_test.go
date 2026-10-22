package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// mp4Head is the start of an MP4 file: an "ftyp" box with an mp4 brand, as net/http sniffs it.
var mp4Head = []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00mp41avc1")

// movHead is the start of a QuickTime file, which net/http does not know.
var movHead = []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt  ")

func doPut(t *testing.T, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestUploadURL(t *testing.T) {
	ctx := context.Background()
	st, f := remoteStore(t, "")
	key, _ := st.NewKey(Owner{TenantID: "t1"}, TypeVideoSources, ".mov")

	up, err := st.UploadURL(ctx, key, "video/quicktime", 0)
	if err != nil {
		t.Fatal(err)
	}
	if up.Method != http.MethodPut || up.Headers["Cache-Control"] != "private, no-store" || !strings.Contains(up.URL, "/priv/") {
		t.Fatalf("unexpected upload %+v", up)
	}
	// A client sending another content type than was signed is refused.
	wrong := map[string]string{"Content-Type": "text/html", "Cache-Control": up.Headers["Cache-Control"]}
	if resp := doPut(t, up.URL, wrong, movHead); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("changed content type: status %d, want 403", resp.StatusCode)
	}
	body := append(append([]byte{}, movHead...), []byte("footage")...)
	if resp := doPut(t, up.URL, up.Headers, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned put: status %d", resp.StatusCode)
	}
	if !bytes.Equal(f.objects["priv/"+key], body) {
		t.Fatal("uploaded bytes not stored")
	}
	obj, err := st.VerifyUpload(ctx, key)
	if err != nil || obj.Size != int64(len(body)) || obj.ContentType != "video/quicktime" {
		t.Fatalf("verify: %+v %v", obj, err)
	}

	draft, _ := st.NewKey(Owner{TenantID: "t1"}, TypeVideoDrafts, ".mp4")
	dup, err := st.UploadURL(ctx, draft, "video/mp4", 0)
	if err != nil || dup.Headers["Cache-Control"] != "public, max-age=300" || !strings.Contains(dup.URL, "/pub/") {
		t.Fatalf("draft upload %+v %v", dup, err)
	}
}

func TestMultipart(t *testing.T) {
	ctx := context.Background()
	st, f := remoteStore(t, "")
	key, _ := st.NewKey(Owner{TenantID: "t1"}, TypeVideoSources, ".mp4")

	id, err := st.CreateMultipart(ctx, key, "video/mp4")
	if err != nil || id == "" {
		t.Fatalf("create: %q %v", id, err)
	}
	if f.headers["priv/"+key].Get("Content-Type") != "video/mp4" {
		t.Error("the content type is set when the upload starts")
	}
	chunks := [][]byte{append(append([]byte{}, mp4Head...), "first-"...), []byte("second")}
	var parts []Part
	for i, c := range chunks {
		u, err := st.PartURL(key, id, i+1, 0)
		if err != nil {
			t.Fatal(err)
		}
		resp := doPut(t, u, nil, c)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
			t.Fatalf("part %d: status %d", i+1, resp.StatusCode)
		}
		parts = append(parts, Part{Number: i + 1, ETag: resp.Header.Get("ETag")})
	}
	if err := st.CompleteMultipart(ctx, key, id, []Part{parts[0], {Number: 2, ETag: `"wrong"`}}); err == nil {
		t.Fatal("an error document in a 200 answer must fail the completion")
	}
	if err := st.CompleteMultipart(ctx, key, id, []Part{parts[0], parts[0]}); err == nil {
		t.Fatal("a part listed twice must be refused")
	}
	// Parts may be reported in any order.
	if err := st.CompleteMultipart(ctx, key, id, []Part{parts[1], parts[0]}); err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte{}, chunks[0]...), chunks[1]...); !bytes.Equal(f.objects["priv/"+key], want) {
		t.Fatalf("joined object %q", f.objects["priv/"+key])
	}
	if obj, err := st.VerifyUpload(ctx, key); err != nil || obj.ContentType != "video/mp4" {
		t.Fatalf("verify: %+v %v", obj, err)
	}

	if _, err := st.PartURL(key, id, 0, 0); err == nil {
		t.Error("part 0 must be refused")
	}
	if _, err := st.PartURL(key, id, MaxParts+1, 0); err == nil {
		t.Error("a part above MaxParts must be refused")
	}
	other, _ := st.CreateMultipart(ctx, key, "video/mp4")
	if err := st.AbortMultipart(ctx, key, other); err != nil {
		t.Fatal(err)
	}
	if err := st.AbortMultipart(ctx, key, other); err != nil {
		t.Errorf("aborting twice is not an error: %v", err)
	}
	if len(f.uploads) != 0 {
		t.Errorf("unfinished uploads left: %d", len(f.uploads))
	}
}

func TestVerifyUploadRejects(t *testing.T) {
	ctx := context.Background()
	st, f := remoteStore(t, "")
	Register(MediaType{Name: "tiny-videos", MaxBytes: 32, Allowed: []string{"video/mp4"}})

	missing, _ := st.NewKey(Owner{TenantID: "t1"}, TypeVideoDrafts, ".mp4")
	if _, err := st.VerifyUpload(ctx, missing); !IsUploadError(err) {
		t.Fatalf("missing upload: %v", err)
	}
	for name, c := range map[string]struct {
		mediaType string
		body      []byte
	}{
		"wrong type": {TypeVideoDrafts, []byte("<html>not a video</html>")},
		"mov as mp4": {TypeVideoDrafts, movHead},
		"too large":  {"tiny-videos", append(append([]byte{}, mp4Head...), bytes.Repeat([]byte("x"), 40)...)},
	} {
		key, _ := st.NewKey(Owner{TenantID: "t1"}, c.mediaType, ".mp4")
		if err := st.Put(ctx, key, c.body, "video/mp4"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.VerifyUpload(ctx, key); !IsUploadError(err) {
			t.Errorf("%s: want an upload error, got %v", name, err)
		}
		if ok, _ := st.Exists(ctx, key); ok {
			t.Errorf("%s: a rejected upload must be deleted", name)
		}
	}
	draft, _ := st.NewKey(Owner{TenantID: "t1"}, TypeVideoDrafts, ".mp4")
	if err := st.Put(ctx, draft, mp4Head, "video/mp4"); err != nil {
		t.Fatal(err)
	}
	if f.headers["pub/"+draft].Get("Cache-Control") != "public, max-age=300" {
		t.Error("a video draft is cached briefly, not for a year")
	}
}

func TestOpenRangeAndStat(t *testing.T) {
	ctx := context.Background()
	remote, _ := remoteStore(t, "")
	local, err := New("auth", Config{LocalRoot: t.TempDir(), LocalURLBase: "http://localhost:4000"})
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]*Store{"remote": remote, "local": local} {
		key, _ := st.Key(Owner{TenantID: "t1"}, TypeVideoSources, "v.mp4")
		if err := st.Put(ctx, key, []byte("0123456789"), "video/mp4"); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			offset, length int64
			want           string
		}{{2, 3, "234"}, {5, 0, "56789"}, {8, 50, "89"}, {0, 1, "0"}} {
			rc, err := st.OpenRange(ctx, key, c.offset, c.length)
			if err != nil {
				t.Fatalf("%s %d+%d: %v", name, c.offset, c.length, err)
			}
			got, _ := io.ReadAll(rc)
			_ = rc.Close()
			if string(got) != c.want {
				t.Errorf("%s %d+%d = %q, want %q", name, c.offset, c.length, got, c.want)
			}
		}
		if _, err := st.OpenRange(ctx, key, 10, 1); !errors.Is(err, ErrInvalidRange) {
			t.Errorf("%s past the end: %v", name, err)
		}
		if _, err := st.OpenRange(ctx, key, -1, 1); err == nil {
			t.Errorf("%s negative offset must fail", name)
		}
		if obj, err := st.Stat(ctx, key); err != nil || obj.Size != 10 {
			t.Errorf("%s stat: %+v %v", name, obj, err)
		}
		if _, err := st.Stat(ctx, key+"x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s stat missing: %v", name, err)
		}
	}

	key, _ := local.NewKey(Owner{TenantID: "t1"}, TypeVideoSources, ".mp4")
	if _, err := local.UploadURL(ctx, key, "video/mp4", 0); !errors.Is(err, ErrRemoteOnly) {
		t.Errorf("local upload url: %v", err)
	}
	if _, err := local.CreateMultipart(ctx, key, "video/mp4"); !errors.Is(err, ErrRemoteOnly) {
		t.Errorf("local multipart: %v", err)
	}
}
