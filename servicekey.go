package httpware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// HeaderAPIKey is the header service-to-service callers send the shared platform key in.
const HeaderAPIKey = "X-API-Key"

type serviceKeyConfig struct {
	header       string
	onAuthorized func(r *http.Request) *http.Request
}

// ServiceKeyOption customises RequireServiceKey.
type ServiceKeyOption func(*serviceKeyConfig)

// WithKeyHeader reads the key from a header other than X-API-Key.
func WithKeyHeader(name string) ServiceKeyOption {
	return func(c *serviceKeyConfig) { c.header = name }
}

// OnServiceAuthorized runs after the key matched and returns the request to pass on. Services use it
// to attach their own service principal (claims, an "internal caller" context flag) to the request.
func OnServiceAuthorized(fn func(r *http.Request) *http.Request) ServiceKeyOption {
	return func(c *serviceKeyConfig) { c.onAuthorized = fn }
}

// ValidServiceKey reports whether the request carries the configured service key. It always
// answers false when the configured key is empty, so a service that forgot to set its key refuses
// every call instead of accepting all of them. The comparison is constant time and does not leak
// the key's length.
func ValidServiceKey(r *http.Request, key string, header ...string) bool {
	if key == "" {
		return false
	}
	name := HeaderAPIKey
	if len(header) > 0 && header[0] != "" {
		name = header[0]
	}
	got := r.Header.Get(name)
	if got == "" {
		return false
	}
	want := sha256.Sum256([]byte(key))
	have := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(want[:], have[:]) == 1
}

// RequireServiceKey admits only requests that carry the shared service key (X-API-Key by default).
// It is the one implementation every service uses for its /s2s routes: constant-time comparison and
// fail closed, meaning an empty configured key refuses everything (401), never allows everything.
func RequireServiceKey(key string, opts ...ServiceKeyOption) func(http.Handler) http.Handler {
	cfg := serviceKeyConfig{header: HeaderAPIKey}
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !ValidServiceKey(r, key, cfg.header) {
				writeJSONError(w, http.StatusUnauthorized, "invalid or missing service key")
				return
			}
			if cfg.onAuthorized != nil {
				r = cfg.onAuthorized(r)
			}
			next.ServeHTTP(w, r)
		})
	}
}
