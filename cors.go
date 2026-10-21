package httpware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	// AllowedOrigins are exact origins ("https://pos.example.com"), single-wildcard patterns
	// ("https://*.example.com", the * never crosses a "/" or "@") or "*" for any origin.
	AllowedOrigins []string
	// AllowedMethods defaults to GET, POST, PUT, PATCH, DELETE, OPTIONS.
	AllowedMethods []string
	// AllowedHeaders are the request headers a browser may send.
	AllowedHeaders []string
	// ExposedHeaders are the response headers a browser script may read.
	ExposedHeaders []string
	// AllowCredentials lets the browser send cookies and authorization with the request. It is never
	// combined with the "*" origin: that pair would let any site act as the signed-in user, so for "*"
	// the response carries no credentials header.
	AllowCredentials bool
	// MaxAge is the preflight cache time in seconds.
	MaxAge int
}

// standardAllowedHeaders is the union of what the fleet's browser clients send. Service-specific
// extras (a device key, a widget mode) are passed to StandardCORS.
var standardAllowedHeaders = []string{
	"Accept", "Authorization", "Content-Type", "Origin",
	HeaderRequestID, HeaderTenantID, HeaderTenantSlug, HeaderOutletID, HeaderAPIKey,
	"Idempotency-Key", "X-Idempotency-Key", "X-Requested-With", "X-CSRF-Token",
}

// standardExposedHeaders are the response headers the fleet's clients read.
var standardExposedHeaders = []string{
	"Link", "Retry-After", "Content-Disposition", HeaderRequestID,
	"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
	"X-Idempotency-Replayed", "Idempotent-Replayed", "X-Sub-Grace-Days-Left",
}

// StandardCORS is the one CORS policy every service uses: the fleet's common request and response
// headers, credentials on, one-day preflight cache. extraAllowedHeaders adds headers only that
// service's clients send (for example X-Device-Key). origins come from the service's own config.
func StandardCORS(origins []string, extraAllowedHeaders ...string) CORSConfig {
	allowed := append(append([]string(nil), standardAllowedHeaders...), extraAllowedHeaders...)
	return CORSConfig{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   allowed,
		ExposedHeaders:   append([]string(nil), standardExposedHeaders...),
		AllowCredentials: true,
		MaxAge:           86400,
	}
}

// DefaultCORSConfig returns StandardCORS for the three original storefront origins.
//
// Deprecated: pass the service's own origins to StandardCORS.
func DefaultCORSConfig() CORSConfig {
	return StandardCORS([]string{"https://accounts.codevertexafrica.com", "https://ordering.codevertexafrica.com", "https://pos.codevertexafrica.com"})
}

// originAllowed reports whether origin matches one of the patterns, and whether it matched only
// because of the "*" any-origin pattern.
func originAllowed(patterns []string, origin string) (allowed, anyOrigin bool) {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			continue
		case p == "*":
			return true, true
		case strings.EqualFold(p, origin):
			return true, false
		case strings.Contains(p, "*"):
			i := strings.Index(p, "*")
			prefix, suffix := strings.ToLower(p[:i]), strings.ToLower(p[i+1:])
			o := strings.ToLower(origin)
			if len(o) >= len(prefix)+len(suffix) && strings.HasPrefix(o, prefix) && strings.HasSuffix(o, suffix) {
				middle := o[len(prefix) : len(o)-len(suffix)]
				if middle != "" && !strings.ContainsAny(middle, "/@\\") {
					return true, false
				}
			}
		}
	}
	return false, false
}

// CORS adds Cross-Origin Resource Sharing headers.
//
// Every response carries Vary: Origin so a shared cache never serves one origin's answer to another.
// A preflight (OPTIONS with Access-Control-Request-Method) from an origin that is not allowed is
// refused with 403 and no CORS headers; an allowed one is answered with 204. Requests without an
// Origin header are not cross-origin and pass straight through.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	}
	allowMethods := strings.Join(methods, ", ")
	allowHeaders := strings.Join(cfg.AllowedHeaders, ", ")
	exposeHeaders := strings.Join(cfg.ExposedHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed, anyOrigin := originAllowed(cfg.AllowedOrigins, origin)
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			if preflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				if !allowed {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}

			if allowed {
				if anyOrigin {
					h.Set("Access-Control-Allow-Origin", "*")
				} else {
					h.Set("Access-Control-Allow-Origin", origin)
					if cfg.AllowCredentials {
						h.Set("Access-Control-Allow-Credentials", "true")
					}
				}
				if exposeHeaders != "" && !preflight {
					h.Set("Access-Control-Expose-Headers", exposeHeaders)
				}
			}

			if preflight {
				h.Set("Access-Control-Allow-Methods", allowMethods)
				if allowHeaders != "" {
					h.Set("Access-Control-Allow-Headers", allowHeaders)
				}
				if cfg.MaxAge > 0 {
					h.Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
