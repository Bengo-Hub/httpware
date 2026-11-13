# Tagging Guide - httpware

The module is `github.com/Bengo-Hub/httpware` (repository `Bengo-Hub/httpware`).

## Version Format

Follow Semantic Versioning (SemVer): `vMAJOR.MINOR.PATCH`

- **MAJOR**: Breaking changes
- **MINOR**: New features, or a behaviour change a service must opt into in code
- **PATCH**: Bug fixes (backward compatible)

While the module is v0.x, a security-driven behaviour change (for example fail-closed checks) ships as a
MINOR and is listed under "Behaviour changes" in the release notes.

## Creating a New Release

### 1. Test and commit

```bash
cd shared/httpware
gofmt -l .            # only files you did not touch may be listed
go vet ./... && go test ./... -count=1
git add <the files you changed>
git commit -m "feat(scope): description of changes"
```

The tenantexport database tests skip without `TENANTEXPORT_TEST_POSTGRES_URL`; run them once against a
scratch database before tagging a change to that package.

### 2. Create and push the tag

```bash
git tag v0.16.0
git push origin HEAD v0.16.0
```

### 3. Move the services to it

Services pin httpware in `go.mod` and are not updated by the tag alone. Always use the latest tag:

```bash
git tag --sort=-v:refname | head -1      # the latest tag
go get github.com/Bengo-Hub/httpware@<latest>
go mod tidy && go build ./... && go test ./...
```

Build and commit one service at a time.

## Release notes template

```markdown
## vX.Y.Z

### Features
- ...

### Behaviour changes (services must check these when they bump)
- ...

### Fixes
- ...
```

## v0.20.0 (13 November 2026)

### Features
- `objectstore`: upload safety inside `Prepare` and `ReadUpload` (README, "Upload safety"): clamd
  `INSTREAM` malware scan with a per type policy and the result on `Upload.Scan`; images downscaled
  (photos 2048 px, logos and signatures 1200 px) and stored as WebP, PNG kept for transparent
  signatures; PDFs with scripts, launch actions or embedded files refused; allow-lists for
  `attachments` and `documents`; image types accept WebP.
- New dependencies: `github.com/gen2brain/webp` v0.6.4 (with `github.com/ebitengine/purego`), and
  `golang.org/x/image` v0.25.0. Both are pure Go; `CGO_ENABLED=0` builds are unchanged.

### Behaviour changes (services must check these when they bump)
- Images come back as `image/webp` / `.webp` (or `image/png` for a transparent signature). Use
  `up.ContentType` and `up.Ext`; tests that expect `.jpg` or `image/jpeg` after `Prepare` change.
- Inside Kubernetes uploads are scanned by default (`stalwart-clamav.email.svc.cluster.local:3310`).
  Private types fail closed: with clamd down `Prepare` returns an error wrapping
  `objectstore.ErrScanUnavailable`; answer 503 (`errors.Is`), not 500.
- `attachments` and `documents` refuse anything outside PDF, JPEG, PNG, WebP, plain text, `.docx`
  and `.xlsx`.
- Record `up.Scan.ScannedAt` and `up.Scan.Engine` with the file where the service keeps metadata.

### Before the first bump
1. Apply the devops-k8s change to `manifests/email/stalwart-clamav.yaml` (clamd listening on
   3310, Service port `clamd`, NetworkPolicy for the service namespaces) and check from a service
   pod that `printf 'zPING\0' | nc stalwart-clamav.email.svc.cluster.local 3310` answers `PONG`.
2. Tell the peers before the tag ships: every service imports httpware.
3. Tag, then bump one service at a time, each in its own commit (`go get
   github.com/Bengo-Hub/httpware@v0.20.0 && go mod tidy && go build ./... && go test ./...`).

## v0.19.0

### Features
- `tax`: the platform's one VAT calculator. `Compute` works out each line and the document totals, `Split`
  separates an amount into taxable value and VAT, `BandFor` resolves the KRA eTIMS OSCU band (A exempt,
  B standard, C zero-rated, D non-VAT, E reduced) and `PercentOf` resolves a percentage discount.
  - VAT is rounded to 2 decimal places on each line; totals and the per-band eTIMS figures are sums of the
    rounded lines.
  - A document discount is shared across the lines (largest remainder) and reduces each line before its
    VAT, taken off the taxable subtotal (`DiscountOnNet`, invoices) or off the bill (`DiscountOnGross`,
    tills and carts).
  - Rates are never built in: callers pass the rate treasury serves.
  - `tax/testdata/vectors.json` is shared with `@bengo-hub/shared-ui-lib/tax`, so the TypeScript and Go
    calculators are held to the same figures.

## v0.16.0

### Features
- `RequireServiceKey`, `ValidServiceKey`, `HeaderAPIKey`: one constant-time, fail-closed service key check.
- `StandardCORS`: the fleet's one CORS policy (`CORS` rebuilt around it).
- `tenantexport.Options.AllowUnauthenticated`.

### Behaviour changes
- `TenantV2` answers 403 when a header or URL tenant disagrees with the caller's claims (platform owners and
  requests without claims are unaffected; `TenantConfig.AllowMismatch` turns it off).
- `CORS`: `Vary: Origin` always, 403 for a preflight from a disallowed origin, `*` never sends credentials.
- `tenantexport.Handler` answers 503 unless `InternalKey` is set or `AllowUnauthenticated` is true.
- `RequestID` replaces a malformed incoming `X-Request-ID`.
- `Recover` re-panics `http.ErrAbortHandler` and does not write twice.
- `MediaSigner` built from an empty secret verifies nothing.
- `DefaultSecretColumns` matches `token` and `otp` as whole words (`tokens_used` is exported).

### Fixes
- `money.Split` with a negative total.
