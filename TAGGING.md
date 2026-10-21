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
