# Shared HTTP Middleware (httpware)

**Repository:** `github.com/Bengo-Hub/httpware`

Standardized HTTP middleware for Codevertex microservices, eliminating code duplication across services.

## Features

- ✅ **RequestID** - Request ID generation/propagation for distributed tracing
- ✅ **Tenant** - Tenant ID extraction from headers
- ✅ **Logging** - Structured HTTP request logging with Zap
- ✅ **Recover** - Panic recovery with stack trace logging
- ✅ **CORS** - Cross-Origin Resource Sharing headers

## Installation

```bash
go get github.com/Bengo-Hub/httpware@v0.1.0
```

## Usage

### With Chi Router

```go
import (
    "github.com/go-chi/chi/v5"
    httpware "github.com/Bengo-Hub/httpware"
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
    httpware "github.com/Bengo-Hub/httpware"
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

### StaticMedia (v0.6.0)

Serves `/media/*` uploads so they cache close to the user (browser, service worker, Cloudflare):

```go
r.Handle("/media/*", http.StripPrefix("/media", httpware.StaticMedia(mediaRoot, httpware.MediaOptions{})))
```

- Fingerprinted names (UUID or long hex hash, e.g. `{uuid}_{unix}.png`) get
  `Cache-Control: public, max-age=31536000, immutable`; other names get 1 day plus
  `stale-while-revalidate=604800`. Strong ETag, so revalidation is a 304.
- No directory listings, no dotfiles, no path escape (404).
- `Access-Control-Allow-Origin: *` and `Cross-Origin-Resource-Policy: cross-origin` so service
  workers on the UI origins can store the images; `nosniff`; a sandbox CSP for SVG/HTML/XML.

Upload handlers must keep writing new, unique file names on every upload (never overwrite a
file in place), otherwise an immutable URL would serve stale bytes for a year.

### Streaming helpers (v0.6.0)

- `BypassForStreaming(mw)`: skip a middleware for WebSocket and SSE requests. Wrap chi's
  `Compress` and `Timeout` with it (both break long-lived connections). Replaces the per-service
  `bypassForWebsocket` copies.
- `StreamHeaders(w)`: SSE headers including `X-Accel-Buffering: no` (nginx buffers otherwise).
- `ExtendWriteDeadline(w)`: lift the server `WriteTimeout` for one streaming response.

### Contact details: `pii` and `contact` (v0.7.0)

Two small packages so every service treats phone numbers and email addresses the same way.

`github.com/Bengo-Hub/httpware/contact`
- `NormalizePhone(raw, region)`: E.164 for any country. A number with its country code
  (`+44 7911 123456`, `+256 772 123456`) is checked against that country's rules; `region` (ISO
  code, default `KE`) only decides how a local form such as `0712 345 678` is read, so pass the
  tenant's or contact's country when known. Invalid numbers are rejected. `ValidPhone` wraps it.
- `SubscriberDigits(phone)`: last nine digits, the platform's dedup key for one number.
- `NormalizeEmail(raw)` / `ValidEmailSyntax(raw)`: one lowercased, well formed address with a
  dotted domain. No DNS lookup; senders that need MX checks do them themselves.

`github.com/Bengo-Hub/httpware/pii`
- `MaskEmail`, `MaskPhone`, `MaskString`, `Mask` (picks the right one) for logs and list views.
- `HashAddress(address)`: SHA-256 of the normalised address, used as a lookup key (suppression
  lists, retention) so the address itself need not be stored.
- zap fields `pii.Email`, `pii.Phone`, `pii.Address` log the masked form.

Only services that import `contact` pull in `nyaruka/phonenumbers`.

### Object storage: `objectstore` (v0.8.0)

`github.com/Bengo-Hub/httpware/objectstore` is the one place every service stores files (media,
documents, exports, backups), on Cloudflare R2 or any S3 compatible store. No SDK: requests are
signed with SigV4 on net/http (checked byte for byte against the AWS SDK signer).

Configuration, the same in every namespace (one Kubernetes Secret, `object-storage`):

| Env | Meaning |
|---|---|
| `OBJECT_STORAGE_ENDPOINT` | S3 API endpoint, e.g. `https://<account>.r2.cloudflarestorage.com` |
| `OBJECT_STORAGE_ACCESS_KEY_ID`, `OBJECT_STORAGE_SECRET_ACCESS_KEY` | credentials |
| `OBJECT_STORAGE_PUBLIC_BUCKET`, `OBJECT_STORAGE_PRIVATE_BUCKET` | the two buckets |
| `OBJECT_STORAGE_PUBLIC_BASE_URL` | domain serving the public bucket (Cloudflare cached) |
| `OBJECT_STORAGE_REGION` | signing region, default `auto` (R2) |
| `OBJECT_STORAGE_PRESIGN_TTL` | private link lifetime, default `15m` |
| `OBJECT_STORAGE_AUTO_CREATE_BUCKETS` | create missing buckets (local S3 only) |
| `MEDIA_ROOT`, `MEDIA_URL_BASE` | local fallback when no endpoint is set (development) |

Keys are `<service>/<tenant_id>/<media_type>/<yyyy>/<mm>/<uuid>.<ext>`, or
`<service>/users/<user_id>/<media_type>/...` for personal media such as avatars. The media type
(`avatars`, `logos`, `item-images`, `menu-images`, `covers`, `images`, `attachments`, `documents`,
`kyc`, `patient-photos`, `signatures`, `exports`, `backups`, `ebooks`; `Register` adds more)
decides the bucket, the size limit, the accepted types and whether images are re-encoded (EXIF and
trailing bytes removed).

```go
store, _ := objectstore.New("inventory", objectstore.FromEnv())
up, err := objectstore.ReadUpload(w, r, "file", objectstore.TypeItemImages) // 400 when IsUploadError
key, _ := store.NewKey(objectstore.Owner{TenantID: tenantID}, objectstore.TypeItemImages, up.Ext)
_ = store.Put(ctx, key, up.Data, up.ContentType)
// save key; on read:
item.ImageURL = store.URL(item.ImageKey) // public URL, or a signed link for private types
```

- Store the key, never a link. `URL` gives the public URL (cached for a year: keys never change
  content) or a presigned link; full `http(s)` URLs pass through.
- Old local paths `/media/<rel>` keep working: they resolve to `<service>/legacy/<public|private>/<rel>`
  (`WithLegacyPrivate` names the private ones), where `CopyLegacyTree` copies a service's old media
  directory once. It is idempotent, so a stopped copy is simply run again.
- `WithLegacyBaseURL(oldMediaURLBase)` (v0.9.0): stored full URLs `<old base>/media/<rel>` resolve
  the same way, so rows written with an absolute media URL need no rewrite either.
- `Normalize(ref)` (v0.9.0): call it before saving a file field a client sends back. A public
  URL or presigned link of the store becomes its key, an old full media URL becomes `/media/<rel>`,
  anything else is kept. `URL`, `Get` and `Delete` accept all of these forms.
- `Check(ctx)` (v0.8.1) is a readiness probe: both buckets reachable with the credentials.
- In local mode files live under `MEDIA_ROOT/<key>` and links are `MEDIA_URL_BASE/media/<key>`.
- Mount `store.MediaHandler()` (v0.9.1) at `/media` in every service. Locally it serves the media
  root (private files only with a link signed by `WithLocalSigner`). With object storage it
  redirects public files, keys and old paths alike, to their R2 URL (301), so `<api>/media/...`
  links other services stored keep working once the media volume is gone; private files 404.
- `URL` returns values it does not understand (an icon name in the same field) unchanged.

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
import httpware "github.com/Bengo-Hub/httpware"
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
