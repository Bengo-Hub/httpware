# Setup Guide - shared-httpware

## Local Development with Go Workspace

When developing locally, use Go workspaces to avoid version conflicts.

### 1. Clone Repositories

```bash
cd BengoBox/
git clone git@github.com:Bengo-Hub/shared-httpware.git shared/httpware
```

### 2. Initialize Go Workspace

```bash
cd BengoBox/
go work init \
  ./inventory-service/inventory-api \
  ./pos-service/pos-api \
  ./ordering-service/ordering-backend \
  ./shared/httpware
```

### 3. Add Dependency in Service

```bash
cd inventory-service/inventory-api
go get github.com/Bengo-Hub/shared-httpware@v0.1.0
```

### 4. Import in Code

```go
import httpware "github.com/Bengo-Hub/shared-httpware"
```

## Production Usage

### Install via go get

```bash
go get github.com/Bengo-Hub/shared-httpware@v0.1.0
```

### Update go.mod

```go
require (
    github.com/Bengo-Hub/shared-httpware v0.1.0
)
```

## Updating the Library

### 1. Make Changes

Edit files in `shared/httpware/`

### 2. Test Changes

```bash
cd shared/httpware
go test ./...
```

### 3. Commit and Tag

```bash
git add .
git commit -m "feat: add new middleware"
git tag v0.1.1
git push origin master --tags
```

### 4. Update Services

```bash
cd inventory-service/inventory-api
go get github.com/Bengo-Hub/shared-httpware@v0.1.1
go mod tidy
```
