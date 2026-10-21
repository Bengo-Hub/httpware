# Shared HTTP Middleware (httpware)

**Repository:** `github.com/Bengo-Hub/httpware`

Standardized HTTP middleware for Codevertex microservices, eliminating code duplication across services.

## Features

- **RequestID** - request ID propagation for tracing (a malformed incoming ID is replaced)
- **Tenant / TenantV2** - tenant resolution from JWT claims, headers and the URL; `TenantV2` refuses a
  header or URL that names a different tenant than the caller's token
- **Logging** - structured request logging (path only, never the query string)
- **Recover** - panic recovery with a stack trace; passes `http.ErrAbortHandler` on and never writes
  a second response
- **CORS / StandardCORS** - the one browser cross-origin policy for every service
- **RequireServiceKey / ValidServiceKey** - constant-time, fail-closed service-to-service key check
- **StaticMedia, MediaSigner, IsStreaming / BypassForStreaming** - media serving and streaming helpers
- Sub-packages: `contact`, `money`, `pii`, `objectstore`, `tenantexport`

## Installation

Always take the latest tag (`git tag --sort=-v:refname | head -1`); a service pinned to an old tag
misses security fixes.

```bash
go get github.com/Bengo-Hub/httpware@v0.16.0
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

Every service uses `StandardCORS` with its own origins; do not hand-roll another list.

```go
// origins come from the service's own config (exact origins or "https://*.example.com").
r.Use(httpware.CORS(httpware.StandardCORS(cfg.HTTP.AllowedOrigins)))

// Headers only this service's clients send (a device key, a widget mode) are added, not copied:
r.Use(httpware.CORS(httpware.StandardCORS(origins, "X-Device-Key")))
```

- Every response carries `Vary: Origin`. A request without an `Origin` header passes straight through.
- A preflight from an origin that is not allowed is answered 403 with no CORS headers; an allowed one
  gets 204 with the methods, headers and `Max-Age`.
- The `*` origin answers `Access-Control-Allow-Origin: *` and never sends credentials.

### Service-to-service key

```go
s2s := r.With(httpware.RequireServiceKey(cfg.Auth.APIKey))
s2s.Get("/api/v1/s2s/tenant-export", exportHandler)

// Attach a service principal to the request once the key matched:
httpware.RequireServiceKey(key, httpware.OnServiceAuthorized(func(r *http.Request) *http.Request {
    return r.WithContext(withInternalCaller(r.Context()))
}))
```

The comparison is constant time, and an empty configured key refuses every request (401) instead of
accepting every request. `ValidServiceKey(r, key)` is the same check for code that cannot use
middleware.

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

#### Planned for video ads (not built, 2026-10-22)

MarketFlow's video ads need large uploads that never pass through the API, and chunked reads for
platform uploads. These are planned for a new tag. The signatures below are the design, not code:

| Planned | Purpose |
|---|---|
| `UploadURL(ctx, key, contentType, maxBytes, ttl)` | presigned PUT, so a browser uploads footage straight to the bucket (never through ingress or the 100 MB Cloudflare request limit). The key is minted by `NewKey` first; the service checks `Exists` and the size after the client reports completion |
| `CreateMultipart` / `PartURL(uploadID, part, ttl)` / `CompleteMultipart` / `AbortMultipart` | presigned multipart upload for files above about 100 MB, with resume after an interrupted part; abandoned uploads are aborted by the service's purge job |
| `OpenRange(ctx, ref, offset, length)` | ranged read, so a worker can stream a stored video to YouTube or TikTok in 8 MB chunks without buffering it |
| `PutStream` option `WithCacheControl(value)` | short-cache public objects (`public, max-age=300`) for preview videos that are deleted after review, instead of the default year-long caching |
| media type `video_draft` | generated preview videos: MP4, up to 300 MB, not re-encoded, public or private bucket per call (public previews by default) |
| media type `video_source` | customer-uploaded footage: MP4 or MOV, private bucket, up to 1 GB through multipart |

Rules that stay: store the key, never a link; presigned links are never sent through Image
Transformations; deletes go through `Owned` first.

### Tenant exports (`tenantexport`, v0.14.0)

`tenantexport.Write(ctx, db, tenantID, w, opts)` streams one tenant's rows of the service's
database (every table of `current_schema()` with a `tenant_id` column) as gzipped JSON Lines:
an `export` line, one `row` line per row, then a `summary` line with counts. Rows are read one at a
time, so memory stays flat. Columns whose names look like secrets (`DefaultSecretColumns`:
password, secret, token, api key, credential, private key, pin, otp, encrypted) and the
infrastructure tables (outbox, processed events, backups, `*_bk_*` copies) are never exported.

`tenantexport.Handler(db, opts)` serves it for auth-api's tenant backups (v0.14.1: `IsStreaming` also matches `Accept: application/x-ndjson`, which auth-api sends, so routers that wrap Timeout and Compress in `BypassForStreaming` skip both). Mount it as
`GET /api/v1/s2s/tenant-export` and set `Options.InternalKey` so the handler checks `X-API-Key` itself (since v0.16.0 it answers 503 when no key is set, unless `Options.AllowUnauthenticated` is true for a route already behind `RequireServiceKey`), and mount it outside any
request-timeout middleware; the tenant comes from `X-Tenant-ID`. A stream without its summary
line is incomplete.

`tenantexport.Probe(ctx, db, tenantID, opts)` (v0.15.0) reports which tenant tables hold at least one
row, with one indexed `EXISTS` per table, and `Handler` answers `GET ...?probe=1` with that as small
JSON (`has_data`, `tables_with_data`). auth-api probes each covered service first and skips the ones
where the tenant has no rows. A service on an older version answers the probe with the export stream,
which the caller detects from the content type, so bumping is optional but saves a full export.
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

- Keeps `X-Request-ID` when it is short and made of letters, digits and `-_.:`; otherwise generates a UUID
- Adds the request ID to response headers
- Stores in context for downstream use

### Tenant

- Extracts `X-Tenant-ID` header and stores it in context. It trusts the header, so use it only where
  authentication and a tenant check run afterwards; `TenantV2` is the safer form.

### TenantV2

Resolves the tenant from JWT claims, then `X-Tenant-ID` / `X-Tenant-Slug`, then the URL parameter.
When the caller has claims and is not a platform owner, a header or URL value that names a different
tenant than the claims is refused with 403 (JSON). Platform owners may scope a request to any tenant
through the URL, and requests without claims (service calls, public routes) keep the header and URL
values. `TenantConfig.AllowMismatch` turns the check off for a service migrating in steps.

### Logging

Logs each request with:
- HTTP method and path
- Response status code
- Request duration
- Request ID
- Tenant ID (if present)

### Recover

- Catches panics in handlers
- Logs error with stack trace and request ID
- Returns HTTP 500 unless the response had already started (no second write)
- Re-panics `http.ErrAbortHandler` so a deliberate abort reaches the server

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
| `X-Tenant-Slug` | `HeaderTenantSlug` | Tenant slug (checked against the token by `TenantV2`) |
| `X-Outlet-ID` | `HeaderOutletID` | Active outlet or branch |
| `X-API-Key` | `HeaderAPIKey` | Shared service-to-service key |

## Context Keys

| Key | Type | Purpose |
|-----|------|---------|
| `RequestIDKey` | `contextKey` | Request tracing |
| `TenantIDKey` | `contextKey` | Tenant isolation |
| `UserIDKey` | `contextKey` | User identification |
| `TenantSlugKey` | `contextKey` | Tenant slug |
| `OutletIDKey` | `contextKey` | Resolved outlet |
| `IsPlatformOwnerKey` | `contextKey` | Platform owner flag |

## Services Using This Library

pos-api, treasury-api, inventory-api, ordering-backend, logistics-api, erp-api, marketflow-api,
notifications-api, subscriptions-api, auth-api, hospital-api, library-api, maskani-api, projects-api
and ticketing-api. Keep them on the latest tag; the older ones (below v0.15.0) lack the tenant export
probe, and below v0.16.0 lack `RequireServiceKey`, the fixed CORS and the `TenantV2` mismatch check.
