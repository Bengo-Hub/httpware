# Tagging Guide - shared-httpware

## Version Format

Follow Semantic Versioning (SemVer): `vMAJOR.MINOR.PATCH`

- **MAJOR**: Breaking changes
- **MINOR**: New features (backward compatible)
- **PATCH**: Bug fixes (backward compatible)

## Creating a New Release

### 1. Ensure All Changes Are Committed

```bash
cd shared/httpware
git status
git add .
git commit -m "feat: description of changes"
```

### 2. Create and Push Tag

```bash
# Create annotated tag
git tag -a v0.1.0 -m "Initial release with RequestID, Tenant, Logging, Recover, CORS middleware"

# Push tag to remote
git push origin v0.1.0
```

### 3. Verify on GitHub

Check that the tag appears at:
`https://github.com/Bengo-Hub/shared-httpware/releases`

## Using Specific Version in Services

```bash
# Install specific version
go get github.com/Bengo-Hub/shared-httpware@v0.1.0

# Update to latest
go get -u github.com/Bengo-Hub/shared-httpware
```

## Release Notes Template

```markdown
## v0.1.0 - Initial Release

### Features
- RequestID middleware for distributed tracing
- Tenant middleware for multi-tenant support
- Logging middleware with structured logging
- Recover middleware for panic recovery
- CORS middleware with configurable options

### Breaking Changes
- None (initial release)

### Dependencies
- github.com/google/uuid v1.6.0
- go.uber.org/zap v1.27.0
```
