package httpware

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMediaSignerFromEmptySecretVerifiesNothing(t *testing.T) {
	s := NewMediaSigner("", time.Minute)
	signed := s.Sign("https://x.test/media/uploads/a.jpg")
	u, err := url.Parse(signed)
	if err != nil || !strings.Contains(signed, "sig=") {
		t.Fatalf("the signer still produces a URL: %q %v", signed, err)
	}
	if s.Verify("uploads/a.jpg", u.Query()) {
		t.Fatal("a signer built from an empty secret must not verify its own signatures")
	}
	// A real secret round-trips.
	real := NewMediaSigner("a-secret", time.Minute)
	ru, _ := url.Parse(real.Sign("https://x.test/media/uploads/a.jpg"))
	if !real.Verify("uploads/a.jpg", ru.Query()) {
		t.Fatal("a keyed signer must verify its own signatures")
	}
}
