// Package httpware provides standardized HTTP middleware for Codevertex microservices.
//
// One implementation of what every service used to copy:
//   - RequestID: generates or propagates a (validated) request ID for tracing
//   - Tenant / TenantV2: resolves the tenant from JWT claims, headers and the URL, and refuses a
//     request whose header or URL names a different tenant than the caller's token
//   - Logging: structured request logging (path only, never the query string)
//   - Recover: panic recovery with logging
//   - CORS / StandardCORS (cors.go): the one browser cross-origin policy
//   - RequireServiceKey / ValidServiceKey (servicekey.go): constant-time, fail-closed service key check
//   - StaticMedia, MediaSigner (media.go, media_sign.go), IsStreaming / BypassForStreaming (stream.go)
//
// Sub-packages: contact, money, pii, objectstore, tenantexport.
//
// Usage with Chi:
//
//	r := chi.NewRouter()
//	r.Use(httpware.RequestID)
//	r.Use(httpware.Recover(logger))
//	r.Use(httpware.Logging(logger))
//	r.Use(httpware.CORS(httpware.StandardCORS(origins)))
//	r.Use(httpware.TenantV2(httpware.TenantConfig{ /* claims extractor, URL param */ }))
//	r.With(httpware.RequireServiceKey(cfg.Auth.APIKey)).Get("/api/v1/s2s/thing", handler)
package httpware

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Context keys for storing values in request context.
type contextKey string

const (
	// RequestIDKey is the context key for request ID.
	RequestIDKey contextKey = "request_id"
	// TenantIDKey is the context key for tenant ID.
	TenantIDKey contextKey = "tenant_id"
	// TenantSlugKey is the context key for tenant slug.
	TenantSlugKey contextKey = "tenant_slug"
	// UserIDKey is the context key for user ID.
	UserIDKey contextKey = "user_id"
	// IsPlatformOwnerKey is the context key for platform owner flag.
	IsPlatformOwnerKey contextKey = "is_platform_owner"
	// OutletIDKey is the context key for the resolved outlet ID.
	OutletIDKey contextKey = "outlet_id"
)

// Header names for request/response.
const (
	HeaderRequestID  = "X-Request-ID"
	HeaderTenantID   = "X-Tenant-ID"
	HeaderTenantSlug = "X-Tenant-Slug"
	// HeaderOutletID is sent by frontends to override the active outlet/branch.
	// Tenant admins and platform owners use this to drill into a specific outlet.
	HeaderOutletID = "X-Outlet-ID"
)

// maxRequestIDLen bounds a propagated request ID.
const maxRequestIDLen = 128

// validRequestID accepts only short IDs made of letters, digits and "-_.:", so a caller cannot put
// newlines or markup into logs and response headers through X-Request-ID.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// RequestID middleware takes the request ID from X-Request-ID when it is well formed, otherwise
// generates one. The ID is stored in context and added to the response headers for tracing.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validRequestID(id) {
			id = uuid.New().String()
		}
		w.Header().Set(HeaderRequestID, id)
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Tenant middleware extracts tenant ID from X-Tenant-ID header and stores in context.
// If header is not present, the request continues without tenant context. It trusts the header,
// so use it only where authentication and tenant checks run afterwards; TenantV2 is the safer form.
func Tenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get(HeaderTenantID)
		if tenantID != "" {
			ctx := context.WithValue(r.Context(), TenantIDKey, tenantID)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// TenantConfig configures the TenantV2 middleware.
// Uses interface callbacks to avoid hard-coupling to auth-client or chi.
type TenantConfig struct {
	// ClaimsExtractor extracts tenant_id, tenant_slug and is_platform_owner from JWT claims in context.
	// Returns empty strings and false if claims are not available. May be nil.
	ClaimsExtractor func(ctx context.Context) (tenantID, tenantSlug string, isPlatformOwner bool, ok bool)

	// URLParamFunc extracts a named URL path parameter from the request.
	// Typically wired to chi.URLParam. May be nil.
	URLParamFunc func(r *http.Request, key string) string

	// URLParamName is the name of the URL path parameter for tenant (default: "tenant").
	URLParamName string

	// Required causes the middleware to return 400 if no tenant ID is resolved.
	Required bool

	// AllowMismatch turns off the mismatch check below. Leave it false; it exists only so a service
	// with a known legacy client can be migrated in steps.
	AllowMismatch bool
}

// TenantV2 middleware resolves tenant ID and slug via chained extraction:
//  1. JWT claims (via ClaimsExtractor callback)
//  2. HTTP headers (X-Tenant-ID, X-Tenant-Slug)
//  3. URL path parameter (via URLParamFunc callback)
//
// Each source can fill in missing values without overriding already-resolved ones.
// The resolved values are stored in context via TenantIDKey and TenantSlugKey.
//
// When the caller has claims and is not a platform owner, a header or URL parameter that names a
// different tenant than the claims is refused with 403: the token decides which tenant a request is
// for, and a client-supplied header or path must never point it elsewhere. Platform owners may scope
// a request to any tenant through the URL, and requests without claims (service-to-service calls,
// public routes) keep the header and URL values as before.
func TenantV2(cfg TenantConfig) func(http.Handler) http.Handler {
	if cfg.URLParamName == "" {
		cfg.URLParamName = "tenant"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tenantID, tenantSlug string
			var claimTenantID, claimTenantSlug string
			var isPlatformOwner, haveClaims bool

			// 1. JWT claims (highest priority)
			if cfg.ClaimsExtractor != nil {
				if claimTID, claimSlug, claimIsPO, ok := cfg.ClaimsExtractor(r.Context()); ok {
					tenantID = claimTID
					tenantSlug = claimSlug
					isPlatformOwner = claimIsPO
					claimTenantID, claimTenantSlug = claimTID, claimSlug
					haveClaims = true
				}
			}
			enforce := haveClaims && !isPlatformOwner && !cfg.AllowMismatch
			mismatch := func(supplied, claimed string) bool {
				return enforce && supplied != "" && claimed != "" && !strings.EqualFold(supplied, claimed)
			}

			// 2. HTTP headers (fill gaps)
			hdrID := r.Header.Get(HeaderTenantID)
			hdrSlug := r.Header.Get(HeaderTenantSlug)
			if mismatch(hdrID, claimTenantID) || mismatch(hdrSlug, claimTenantSlug) {
				writeJSONError(w, http.StatusForbidden, "tenant does not match the signed-in organization")
				return
			}
			if tenantID == "" && hdrID != "" {
				tenantID = hdrID
			}
			if tenantSlug == "" && hdrSlug != "" {
				tenantSlug = hdrSlug
			}

			// 3. URL path parameter (fill gaps — could be UUID or slug)
			// For platform owners, URL param always wins over claims so they can scope
			// requests to any tenant (e.g. GET /v1/urban-loft/inventory/... as superuser).
			if cfg.URLParamFunc != nil {
				if param := cfg.URLParamFunc(r, cfg.URLParamName); param != "" {
					if _, err := uuid.Parse(param); err == nil {
						// It's a UUID → use as tenant ID (override for platform owners)
						if mismatch(param, claimTenantID) {
							writeJSONError(w, http.StatusForbidden, "tenant does not match the signed-in organization")
							return
						}
						if tenantID == "" || isPlatformOwner {
							tenantID = param
						}
					} else {
						// It's a slug → use as tenant slug (override for platform owners)
						if mismatch(param, claimTenantSlug) {
							writeJSONError(w, http.StatusForbidden, "tenant does not match the signed-in organization")
							return
						}
						if tenantSlug == "" || isPlatformOwner {
							tenantSlug = param
							if isPlatformOwner {
								tenantID = "" // reset ID so syncer re-resolves from slug
							}
						}
					}
				}
			}

			// Enforce required tenant
			if cfg.Required && tenantID == "" && tenantSlug == "" {
				writeJSONError(w, http.StatusBadRequest, "tenant context required")
				return
			}

			// Store in context
			ctx := r.Context()
			if tenantID != "" {
				ctx = context.WithValue(ctx, TenantIDKey, tenantID)
			}
			if tenantSlug != "" {
				ctx = context.WithValue(ctx, TenantSlugKey, tenantSlug)
			}
			if isPlatformOwner {
				ctx = context.WithValue(ctx, IsPlatformOwnerKey, true)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeJSONError answers with a small JSON error body and the right content type.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Logging middleware logs HTTP requests with structured fields.
// Logs include: method, path (never the query string), status code, duration, and request ID.
func Logging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(ww, r)

			fields := []zap.Field{
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.statusCode),
				zap.Duration("duration", time.Since(start)),
				zap.String("request_id", GetRequestID(r.Context())),
			}

			if tenantID := GetTenantID(r.Context()); tenantID != "" {
				fields = append(fields, zap.String("tenant_id", tenantID))
			}
			if tenantSlug := GetTenantSlug(r.Context()); tenantSlug != "" {
				fields = append(fields, zap.String("tenant_slug", tenantSlug))
			}
			if outletID := GetOutletID(r.Context()); outletID != "" {
				fields = append(fields, zap.String("outlet_id", outletID))
			}

			log.Info("http request", fields...)
		})
	}
}

// Recover middleware recovers from panics and logs the error. It answers 500 unless the handler had
// already started the response (a second write would only corrupt it). A panic with
// http.ErrAbortHandler is the standard way to abort a response on purpose, so it is passed on to the
// server instead of being logged as a crash.
func Recover(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			defer func() {
				if err := recover(); err != nil {
					if err == http.ErrAbortHandler {
						panic(err)
					}
					log.Error("panic recovered",
						zap.Any("error", err),
						zap.String("path", r.URL.Path),
						zap.String("method", r.Method),
						zap.String("request_id", GetRequestID(r.Context())),
						zap.String("stack", string(debug.Stack())),
					)
					if !rw.wrote {
						http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					}
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// GetRequestID returns the request ID from context, or empty string if not found.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

// GetTenantID returns the tenant ID from context, or empty string if not found.
func GetTenantID(ctx context.Context) string {
	if id, ok := ctx.Value(TenantIDKey).(string); ok {
		return id
	}
	return ""
}

// GetTenantSlug returns the tenant slug from context, or empty string if not found.
func GetTenantSlug(ctx context.Context) string {
	if slug, ok := ctx.Value(TenantSlugKey).(string); ok {
		return slug
	}
	return ""
}

// IsPlatformOwner returns true if the user is a platform owner.
func IsPlatformOwner(ctx context.Context) bool {
	if isPO, ok := ctx.Value(IsPlatformOwnerKey).(bool); ok {
		return isPO
	}
	return false
}

// GetUserID returns the user ID from context, or empty string if not found.
func GetUserID(ctx context.Context) string {
	if id, ok := ctx.Value(UserIDKey).(string); ok {
		return id
	}
	return ""
}

// WithRequestID adds a request ID to the context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}

// WithTenantID adds a tenant ID to the context.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, TenantIDKey, tenantID)
}

// WithTenantSlug adds a tenant slug to the context.
func WithTenantSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, TenantSlugKey, slug)
}

// WithUserID adds a user ID to the context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}

// GetOutletID returns the outlet ID from context, or empty string if not set.
func GetOutletID(ctx context.Context) string {
	if id, ok := ctx.Value(OutletIDKey).(string); ok {
		return id
	}
	return ""
}

// WithOutletID stores an outlet ID in the context.
func WithOutletID(ctx context.Context, outletID string) context.Context {
	return context.WithValue(ctx, OutletIDKey, outletID)
}

// OutletHeader extracts the X-Outlet-ID header value from the request, or returns empty string.
// Services that need full outlet resolution (DB lookup, HQ bypass) should use their own
// outlet context middleware. This helper is for lightweight header-only extraction.
func OutletHeader(r *http.Request) string {
	return r.Header.Get(HeaderOutletID)
}

// responseWriter wraps http.ResponseWriter to capture the status code and whether the response has
// started.
//
// Embedding http.ResponseWriter as an INTERFACE field only promotes the three methods
// declared on that interface (Header/Write/WriteHeader) — Go does not promote any other
// interface the concrete value underneath happens to implement. Every caller that wraps a
// request in Logging therefore silently lost http.Hijacker (WebSocket upgrades) and
// http.Flusher (SSE streaming) capability, regardless of what the real underlying writer
// supported. Hijack/Unwrap/Flush below close that gap by delegating explicitly.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	wrote      bool
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.wrote = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.wrote = true
	return rw.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker by delegating to the wrapped writer. Required by
// WebSocket libraries (nhooyr.io/websocket, gorilla/websocket) that hijack the raw
// connection directly via a `w.(http.Hijacker)` type assertion.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("httpware: underlying %T does not implement http.Hijacker", rw.ResponseWriter)
	}
	rw.wrote = true
	return hj.Hijack()
}

// Unwrap exposes the wrapped writer for Go's http.ResponseController (Go 1.20+), the
// std-library-idiomatic alternative to a raw type assertion for Hijack/Flush/deadlines.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Flush implements http.Flusher by delegating to the wrapped writer. Required for SSE
// (Server-Sent Events) streaming, which must flush after every event to reach the client
// immediately instead of sitting in a buffer — the same promotion gap as Hijack above.
func (rw *responseWriter) Flush() {
	rw.wrote = true
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
