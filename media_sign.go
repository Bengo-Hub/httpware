package httpware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MediaSigner issues and checks short-lived signed media URLs for private files (patient photos,
// KYC documents). A browser loads them with a plain <img src>, which cannot carry a bearer
// token, so the API signs the URL when it returns the record and StaticMedia refuses private
// files without a valid, unexpired signature.
//
// The signature covers the media path (the part after "/media/") and the expiry, keyed by a
// secret every replica shares.
type MediaSigner struct {
	key []byte
	ttl time.Duration
}

// NewMediaSigner derives a signing key from secret for this purpose only (so a shared service
// secret is never used directly) and signs URLs valid for ttl (default 12 hours).
func NewMediaSigner(secret string, ttl time.Duration) *MediaSigner {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("codevertex-media-url-signing-v1"))
	return &MediaSigner{key: m.Sum(nil), ttl: ttl}
}

func (s *MediaSigner) mac(mediaPath string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(strings.TrimPrefix(mediaPath, "/")))
	m.Write([]byte{0})
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Sign returns rawURL with exp and sig query parameters. URLs that are empty, data: URIs or not
// under "/media/" are returned unchanged; an existing signature is replaced.
func (s *MediaSigner) Sign(rawURL string) string {
	if s == nil || rawURL == "" || strings.HasPrefix(rawURL, "data:") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	i := strings.Index(u.Path, "/media/")
	if i < 0 {
		return rawURL
	}
	exp := time.Now().Add(s.ttl).Unix()
	q := u.Query()
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", s.mac(u.Path[i+len("/media/"):], exp))
	u.RawQuery = q.Encode()
	return u.String()
}

// Verify reports whether q carries a valid, unexpired signature for mediaPath (the path below
// the media root, e.g. "/uploads/patients/x.jpg").
func (s *MediaSigner) Verify(mediaPath string, q url.Values) bool {
	if s == nil {
		return false
	}
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(q.Get("sig")), []byte(s.mac(mediaPath, exp)))
}

// StripMediaSignature removes exp/sig from a media URL, so a signed URL a client sends back
// (e.g. an edit form re-submitting photo_url) is stored in its plain form.
func StripMediaSignature(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return rawURL
	}
	q := u.Query()
	if q.Get("sig") == "" {
		return rawURL
	}
	q.Del("sig")
	q.Del("exp")
	u.RawQuery = q.Encode()
	return u.String()
}
