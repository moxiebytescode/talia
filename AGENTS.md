# AGENTS.md

Talia is a CLI tool for checking `.com` domain availability via WHOIS and generating domain suggestions via native OpenAI and Anthropic APIs, with best-effort OpenAI-compatible endpoints. All source lives in the root `talia` package with a thin binary wrapper at `cmd/talia/main.go`. Uses Conventional Commits (`feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `style`, `revert`).

## Quick Commands

```bash
go build -o talia ./cmd/talia/ # build
go test -v                     # test
go test -race -coverprofile=coverage.out ./...  # test with race + coverage
go tool golangci-lint run      # lint
```

## Documentation

- [`GLOSSARY.md`](GLOSSARY.md) — domain vocabulary; use these terms
- [`docs/adr/`](docs/adr/) — architecture decisions (WHOIS detection, suggestion-provider contracts, `.com` only, single-file lifecycle, merge semantics, parallelism)
- [Development Guide](docs/guides/development.md) — building, linting, CI, releases
- [Configuration Reference](docs/guides/configuration.md) — flags, env vars, `.env`, and behavior notes
- [Testing Guide](docs/guides/testing.md) — test architecture, mocking, and isolation
- Known bugs and open work live in [GitHub Issues](https://github.com/moxiebytescode/talia/issues)

## Agent skills

### Issue tracker

Issues live in GitHub Issues for `moxiebytescode/talia`, managed via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` plus ADRs in `docs/adr/`. See `docs/agents/domain.md`.
