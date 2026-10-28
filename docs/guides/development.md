# Development Guide

Building, running, and contributing to Talia.

## Prerequisites

- Go 1.27.3 (as specified in `go.mod`; uses `tool` directive for golangci-lint)

## Build

```bash
go build -o talia ./cmd/talia/
```

The binary is built from `cmd/talia/main.go`, which is a thin wrapper around `talia.RunCLI()`.

## Run

```bash
# Basic WHOIS check
./talia --whois whois.verisign-grs.com:43 domains.json

# Generate AI suggestions
export OPENAI_API_KEY=sk-...
./talia --suggest 10 --prompt "short tech names" domains.json

# Clean a domain file
./talia --clean domains.json
```

## Project Structure

```
cmd/talia/main.go     # binary entry point
cli.go                # flag parsing and orchestration
whois.go              # TCP WHOIS client
types.go              # all data structures
grouped.go            # merge/deduplicate logic for grouped format
suggestions.go        # OpenAI API, normalization, file utilities
progress.go           # thread-safe progress output
env.go                # .env file loader
```

All domain logic lives in the root `talia` package. The `cmd/talia/` sub-package exists only to produce the binary.

## Lint

```bash
go tool golangci-lint run
```

Uses golangci-lint v2 (pinned to v2.15.0 in `go.mod`) via Go's tool directive — auto-downloads on first run. Config in `.golangci.yml` explicitly enables: `govet`, `staticcheck`, `errcheck`.

Note: CI uses `golangci/golangci-lint-action@v9.3.0` (GitHub Action) rather than `go tool golangci-lint run`, pinned to the same version, and both read the same `.golangci.yml` config. CI reads the Go version from `go.mod`.

## Commit Convention

Enforced by commitlint in CI. Allowed types:

`feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `style`, `revert`

Example: `feat: add new flag for TLD filtering`

See `.commitlintrc.json` for the full config.

## CI Pipeline

Defined in `.github/workflows/ci.yml`. Runs on every push and PR:

1. Commit message lint (commitlint)
2. `go vet ./...`
3. `go test -race -coverprofile=coverage.out ./...`
4. golangci-lint
5. Coverage artifact upload

## Release Process

Defined in `.github/workflows/release.yml`. Runs on pushes to `main`:

- Uses `release-please-action` v5 with `release-type: simple`
- Auto-generates changelog from conventional commits
- Creates release PRs and tags with `v` prefix

## Related Documentation

- [Testing Guide](testing.md)
- [Configuration Reference](configuration.md)
