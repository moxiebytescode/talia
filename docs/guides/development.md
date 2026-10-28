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

# Generate with Anthropic (uses ANTHROPIC_API_KEY)
./talia --provider anthropic --suggest 10 --no-verify domains.json

# Clean a domain file
./talia --clean domains.json
```

## Project Structure

```
cmd/talia/main.go     # binary entry point
cli.go                # flag parsing and orchestration
checks.go             # ordered WHOIS checks and sequential/parallel scheduling
whois.go              # TCP WHOIS client
types.go              # all data structures
grouped.go            # merge/deduplicate logic for grouped format
domainfile.go         # array/grouped decoding, normalization, clean, merge, export
suggestions.go        # compatibility library entry point and concurrent suggestion batches
providers.go          # native OpenAI/Anthropic and compatible API contracts, suggestion validation
progress.go           # thread-safe progress output
env.go                # .env file loader
```

All domain logic lives in the root `talia` package. The `cmd/talia/` sub-package exists only to produce the binary.

The CLI defaults to native OpenAI Responses and supports native Anthropic Messages through `--provider`. Custom Chat Completions endpoints remain available through `openai-compatible`; the exported `GenerateDomainSuggestions` function also retains that contract for Go callers. Provider request and response details live in `providers.go`, while file lifecycle and WHOIS checking stay shared. See [ADR-0007](../adr/0007-explicit-suggestion-provider-contracts.md) and the [configuration reference](configuration.md).

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

Release automation authenticates with the repository Actions secret `RELEASE_PLEASE_TOKEN`, following release-please's [documented token setup](https://github.com/googleapis/release-please-action#github-credentials).

Create a fine-grained personal access token with:

- Resource owner: `moxiebytescode`.
- Repository access: **Only select repositories**, with `talia` selected.
- Repository permissions: **Contents**, **Issues**, and **Pull requests** set to **Read and write**. Metadata read access is included automatically.
- An expiration date; replace the secret before the token expires or if its owner loses repository access.

Store the token in `RELEASE_PLEASE_TOKEN` before enabling the workflow. It lets CI run automatically on release PRs without the approval prompt associated with the built-in `GITHUB_TOKEN`; see [Triggering a workflow](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow). The workflow's built-in token retains only contents read access.

## Related Documentation

- [Testing Guide](testing.md)
- [Configuration Reference](configuration.md)
