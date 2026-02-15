// Package httpware provides standardized HTTP middleware for BengoBox microservices.
//
// This package eliminates code duplication by providing common middleware functions
// that were previously duplicated across all services:
//   - RequestID: Generates/propagates request IDs for tracing
//   - Tenant: Extracts tenant ID from JWT claims or headers
//   - Logging: Structured HTTP request logging
//   - Recover: Panic recovery with logging
//   - CORS: Cross-Origin Resource Sharing headers
//
// Usage with Chi router:
//
//	import httpware "github.com/Bengo-Hub/httpware"
//
//	r := chi.NewRouter()
//	r.Use(httpware.RequestID)
//	r.Use(httpware.Tenant)
//	r.Use(httpware.Recover(logger))
//	r.Use(httpware.Logging(logger))
package httpware

import (
	"context"
	"net/http"
	"runtime/debug"
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
)

// Header names for request/response.
const (
	HeaderRequestID  = "X-Request-ID"
	HeaderTenantID   = "X-Tenant-ID"
	HeaderTenantSlug = "X-Tenant-Slug"
)

// RequestID middleware extracts request ID from X-Request-ID header or generates a new one.
// The request ID is stored in context and added to response headers for tracing.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = uuid.New().String()
		}
		w.Header().Set(HeaderRequestID, id)
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Tenant middleware extracts tenant ID from X-Tenant-ID header and stores in context.
// If header is not present, the request continues without tenant context.
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
	// ClaimsExtractor extracts tenant_id and tenant_slug from JWT claims in context.
	// Returns empty strings if claims are not available. May be nil.
	ClaimsExtractor func(ctx context.Context) (tenantID, tenantSlug string, ok bool)

	// URLParamFunc extracts a named URL path parameter from the request.
	// Typically wired to chi.URLParam. May be nil.
	URLParamFunc func(r *http.Request, key string) string

	// URLParamName is the name of the URL path parameter for tenant (default: "tenant").
	URLParamName string

	// Required causes the middleware to return 400 if no tenant ID is resolved.
	Required bool
}

// TenantV2 middleware resolves tenant ID and slug via chained extraction:
//  1. JWT claims (via ClaimsExtractor callback)
//  2. HTTP headers (X-Tenant-ID, X-Tenant-Slug)
//  3. URL path parameter (via URLParamFunc callback)
//
// Each source can fill in missing values without overriding already-resolved ones.
// The resolved values are stored in context via TenantIDKey and TenantSlugKey.
func TenantV2(cfg TenantConfig) func(http.Handler) http.Handler {
	if cfg.URLParamName == "" {
		cfg.URLParamName = "tenant"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tenantID, tenantSlug string

			// 1. JWT claims (highest priority)
			if cfg.ClaimsExtractor != nil {
				if claimTID, claimSlug, ok := cfg.ClaimsExtractor(r.Context()); ok {
					tenantID = claimTID
					tenantSlug = claimSlug
				}
			}

			// 2. HTTP headers (fill gaps)
			if tenantID == "" {
				if hdr := r.Header.Get(HeaderTenantID); hdr != "" {
					tenantID = hdr
				}
			}
			if tenantSlug == "" {
				if hdr := r.Header.Get(HeaderTenantSlug); hdr != "" {
					tenantSlug = hdr
				}
			}

			// 3. URL path parameter (fill gaps — could be UUID or slug)
			if cfg.URLParamFunc != nil {
				if param := cfg.URLParamFunc(r, cfg.URLParamName); param != "" {
					// Determine if param is a UUID or a slug
					if _, err := uuid.Parse(param); err == nil {
						// It's a UUID → use as tenant ID
						if tenantID == "" {
							tenantID = param
						}
					} else {
						// It's a slug → use as tenant slug
						if tenantSlug == "" {
							tenantSlug = param
						}
					}
				}
			}

			// Enforce required tenant
			if cfg.Required && tenantID == "" && tenantSlug == "" {
				http.Error(w, `{"error":"tenant context required"}`, http.StatusBadRequest)
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

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Logging middleware logs HTTP requests with structured fields.
// Logs include: method, path, status code, duration, and request ID.
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

			log.Info("http request", fields...)
		})
	}
}

// Recover middleware recovers from panics and logs the error.
// Returns HTTP 500 Internal Server Error to the client.
func Recover(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error("panic recovered",
						zap.Any("error", err),
						zap.String("path", r.URL.Path),
						zap.String("method", r.Method),
						zap.String("request_id", GetRequestID(r.Context())),
						zap.String("stack", string(debug.Stack())),
					)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// CORSConfig holds configuration for the CORS middleware.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	AllowCredentials bool
	MaxAge           int // in seconds
}

// DefaultCORSConfig returns a permissive CORS configuration for development.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug"},
		AllowCredentials: true,
		MaxAge:           86400,
	}
}

// CORS middleware adds Cross-Origin Resource Sharing headers.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			allowed := false
			for _, o := range cfg.AllowedOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}

			if allowed && origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", joinStrings(cfg.AllowedMethods, ", "))
				w.Header().Set("Access-Control-Allow-Headers", joinStrings(cfg.AllowedHeaders, ", "))
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", formatInt(cfg.MaxAge))
				}
			}

			// Handle preflight requests
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
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

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// joinStrings joins strings with a separator.
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

// formatInt converts an int to string without importing strconv.
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + formatInt(-n)
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
