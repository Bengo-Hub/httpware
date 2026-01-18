# Shared HTTP Middleware (httpware)

**Repository:** `github.com/Bengo-Hub/shared-httpware`

Standardized HTTP middleware for BengoBox microservices, eliminating code duplication across services.

## Features

- ✅ **RequestID** - Request ID generation/propagation for distributed tracing
- ✅ **Tenant** - Tenant ID extraction from headers
- ✅ **Logging** - Structured HTTP request logging with Zap
- ✅ **Recover** - Panic recovery with stack trace logging
- ✅ **CORS** - Cross-Origin Resource Sharing headers

## Installation

```bash
go get github.com/Bengo-Hub/shared-httpware@v0.1.0
```

## Usage

### With Chi Router

```go
import (
    "github.com/go-chi/chi/v5"
    httpware "github.com/Bengo-Hub/shared-httpware"
    "go.uber.org/zap"
)

func main() {
    logger, _ := zap.NewProduction()

    r := chi.NewRouter()

    // Add middleware in recommended order
    r.Use(httpware.RequestID)
    r.Use(httpware.Tenant)
    r.Use(httpware.Recover(logger))
    r.Use(httpware.Logging(logger))

    // Your routes here
    r.Get("/health", healthHandler)
}
```

### With Standard net/http

```go
import (
    "net/http"
    httpware "github.com/Bengo-Hub/shared-httpware"
    "go.uber.org/zap"
)

func main() {
    logger, _ := zap.NewProduction()

    handler := http.HandlerFunc(myHandler)

    // Chain middleware
    wrapped := httpware.RequestID(
        httpware.Tenant(
            httpware.Recover(logger)(
                httpware.Logging(logger)(handler),
            ),
        ),
    )

    http.ListenAndServe(":8080", wrapped)
}
```

### Accessing Context Values

```go
func myHandler(w http.ResponseWriter, r *http.Request) {
    requestID := httpware.GetRequestID(r.Context())
    tenantID := httpware.GetTenantID(r.Context())
    userID := httpware.GetUserID(r.Context())

    // Use in logs, database queries, etc.
}
```

### Setting Context Values

```go
// Useful in tests or when propagating context
ctx := httpware.WithRequestID(ctx, "req-123")
ctx = httpware.WithTenantID(ctx, "tenant-456")
ctx = httpware.WithUserID(ctx, "user-789")
```

### CORS Configuration

```go
// Default permissive config (for development)
r.Use(httpware.CORS(httpware.DefaultCORSConfig()))

// Custom config (for production)
r.Use(httpware.CORS(httpware.CORSConfig{
    AllowedOrigins:   []string{"https://example.com", "https://app.example.com"},
    AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
    AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID"},
    AllowCredentials: true,
    MaxAge:           86400,
}))
```

## Middleware Details

### RequestID

- Extracts `X-Request-ID` header if present
- Generates new UUID if header is missing
- Adds request ID to response headers
- Stores in context for downstream use

### Tenant

- Extracts `X-Tenant-ID` header
- Stores in context for multi-tenant queries
- Skips silently if header not present

### Logging

Logs each request with:
- HTTP method and path
- Response status code
- Request duration
- Request ID
- Tenant ID (if present)

### Recover

- Catches panics in handlers
- Logs error with stack trace
- Returns HTTP 500 to client
- Includes request ID in error log

## Migration from Local Middleware

Replace local middleware imports:

```go
// Before
import "your-service/internal/shared/middleware"

// After
import httpware "github.com/Bengo-Hub/shared-httpware"
```

Update usage:
```go
// Before
r.Use(middleware.RequestID)
r.Use(middleware.Tenant)
r.Use(middleware.Logging(logger))
r.Use(middleware.Recover(logger))

// After
r.Use(httpware.RequestID)
r.Use(httpware.Tenant)
r.Use(httpware.Logging(logger))
r.Use(httpware.Recover(logger))
```

Then remove `internal/shared/middleware/` directory.

## Header Constants

| Header | Constant | Purpose |
|--------|----------|---------|
| `X-Request-ID` | `HeaderRequestID` | Distributed tracing |
| `X-Tenant-ID` | `HeaderTenantID` | Multi-tenant isolation |

## Context Keys

| Key | Type | Purpose |
|-----|------|---------|
| `RequestIDKey` | `contextKey` | Request tracing |
| `TenantIDKey` | `contextKey` | Tenant isolation |
| `UserIDKey` | `contextKey` | User identification |

## Services Using This Library

- inventory-service
- pos-service
- ordering-service
- logistics-service
- finance-service
- notifications-service
- projects-service
- iot-service
- subscription-service
- ticketing-service
- auth-service
