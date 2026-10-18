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
- `URL` returns values it does not understand (an icon name in the same field) unchanged.
- `Normalize(ref)`: call it before saving a file field a client sends back. A public URL or
  presigned link of the store becomes its key, a full media URL of the service (`MEDIA_URL_BASE`,
  or other old bases given with `WithLegacyBaseURL`) becomes its key or `/media/<rel>` path,
  anything else is kept. `URL`, `Get`, `Exists` and `Delete` accept all of these forms.
- `Owned(ref)`: true only for this service's own keys. Check it before deleting a record's file;
  old volume files and external URLs may be shared and are left alone.
- `Check(ctx)` is a readiness probe: both buckets reachable with the credentials.
- `Open(ctx, ref)` (v0.12.0) streams a stored file (reader, size, content type) without holding it
  in memory.
- `Serve(w, r, ref, contentType)` (v0.13.0) writes a stored file to the response with range
  support, so in-browser PDF, EPUB and audio readers can seek: the reader's Range header goes to
  the bucket and a 206 comes back as is; local files use `http.ServeContent`. It returns
  `ErrNotFound` before writing anything. Use it to serve private files (e-books, documents)
  through the API after the access check.
- `PutStream(ctx, key, r, size, contentType)` (v0.14.0) uploads without holding the file in
  memory (backup archives, large exports): one PUT signed with `UNSIGNED-PAYLOAD`.

### Tenant exports (`tenantexport`, v0.14.0)

`tenantexport.Write(ctx, db, tenantID, w, opts)` streams one tenant's rows of the service's
database (every table of `current_schema()` with a `tenant_id` column) as gzipped JSON Lines:
an `export` line, one `row` line per row, then a `summary` line with counts. Rows are read one at a
time, so memory stays flat. Columns whose names look like secrets (`DefaultSecretColumns`:
password, secret, token, api key, credential, private key, pin, otp, encrypted) and the
infrastructure tables (outbox, processed events, backups, `*_bk_*` copies) are never exported.

`tenantexport.Handler(db, opts)` serves it for auth-api's tenant backups (v0.14.1: `IsStreaming` also matches `Accept: application/x-ndjson`, which auth-api sends, so routers that wrap Timeout and Compress in `BypassForStreaming` skip both). Mount it as
`GET /api/v1/s2s/tenant-export` behind the service's API-key middleware (or set `Options.InternalKey`, v0.14.2, to have the handler check `X-API-Key` itself) and outside any
request-timeout middleware; the tenant comes from `X-Tenant-ID`. A stream without its summary
line is incomplete.
- Mount `store.MediaHandler()` at `/media` in every service. It serves the media volume (new files
  in local mode, and old `/media/<rel>` paths not yet relinked; private ones, named by
  `WithLegacyPrivate`, only with a link signed by `WithLocalSigner`). With object storage a public
  key redirects (301) to its R2 URL and a private key answers 404.

Moving existing media (v0.10.0). Old values (`/media/<rel>` or `<MEDIA_URL_BASE>/media/<rel>`) keep
working from the volume until each row is relinked:

```go
rl := store.NewRelinker()
key, changed, err := rl.Relink(ctx, brand.LogoURL, objectstore.Owner{TenantID: brand.TenantID.String()}, objectstore.TypeLogos)
// changed: save key on the row. errors.Is(err, objectstore.ErrNotFound): the file is gone, report it.
```

Each referenced file is uploaded under its owner with a content hash name, so one file used by many
rows is stored once and a rerun uploads nothing twice. Files no row references are never moved and
go away with the volume, which is released once every row is relinked.

### Money rounding: `money` (v0.11.0)

The single place services round amounts, so ledgers, balance sheets, tax submissions and FX
conversions agree to the cent. Do not write a private `round2` in a service.

```go
import "github.com/Bengo-Hub/httpware/money"

money.Round2(74.24000000000001)         // 74.24
money.Round(v, money.Places("UGX"))     // currency aware, UGX/RWF/JPY have 0 places
money.Convert(100, rate, "UGX")         // converts and rounds at the target currency places
money.Fields2(&tax, &total)             // round fields in place before sending to a regulator
money.Sum(2, a, b, c)                   // total rounded once
money.Split(100, 2, 3)                  // [33.34 33.33 33.33], parts add back to the total
money.Split(discount, 2, n, weights...) // prorate a discount without losing a cent
```

Rounding is half away from zero and strips float noise (1.005 gives 1.01, negatives mirror).
The currency table mirrors `CURRENCY_META` in shared-ui-lib; keep the two in step.

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
