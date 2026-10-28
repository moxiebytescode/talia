# Testing Guide

Test architecture, mocking strategy, and CI integration.

## Running Tests

```bash
# Standard
go test -v

# With race detection and coverage
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

## Test Files

| File | Scope |
|---|---|
| `main_test.go` | Integration tests for all CLI paths |
| `checks_test.go` | CLI scheduling, actual concurrent overlap, result order, and log policy |
| `domainfile_test.go` | Domain preservation, malformed input, array/grouped workflows, and normalized identity |
| `whois_test.go` | WHOIS interpretation, TCP behavior, and stalled-response deadlines |
| `suggestions_test.go` | Compatibility tool-calling requests, CLI flag precedence, and partial batch failures |
| `providers_test.go` | Native OpenAI/Anthropic request contracts, response failures, and shared suggestion validation |
| `provider_cli_test.go` | Provider/model/base precedence, credentials, native endpoint selection, and file preservation on failure |
| `grouped_test.go` | Writing results into an existing grouped file (order, newest-wins, keeping `unverified`) |
| `progress_test.go` | Progress output coloring (terminal detection, `NO_COLOR`) |
| `cmd/talia/main_test.go` | `main()` exit code and `.env` precedence (shell env wins, quotes stripped) |

All library tests are in the `talia` package (white-box), giving access to unexported types and functions.

## Mocking Strategy

### WHOIS Mocking

Two approaches:

1. **`WhoisClient` interface** — `fakeWhoisClient` returns hardcoded responses for pure unit tests of availability logic.
2. **In-process TCP listeners** — `net.Listen("tcp", "127.0.0.1:0")` with goroutines serving predictable responses for integration tests that exercise the full TCP path.

### HTTP Mocking (AI API)

- `httptest.NewServer` provides a local HTTP server with controlled responses.
- CLI tests point `--api-base` at the local server and exercise actual HTTP requests without process-wide transport overrides.
- Native API tests also select `--provider` explicitly, so a local base does not select legacy Chat Completions compatibility mode.
- The internal `httpDoer` seam allows a failing adapter to exercise transport errors directly.

Provider tests assert the documented wire contracts, including JSON schemas, provider headers, refusal/truncation handling, and unusable suggestion data. These are local contract tests, not live API or account-access verification. Live checks require credentials for the chosen provider and incur API usage; the normal suite makes no external API calls.

## Test Isolation

`TestMain` (in `main_test.go`) runs before all tests and:

- Sets `skipEnvFile = true` to prevent `.env` file loading during tests.
- Unsets OpenAI/Anthropic credentials, base URLs, workspace selection, and provider/model settings to prevent shell configuration from selecting an external API during tests.

Individual tests that modify env vars use `t.Setenv(...)`, which restores the original value when the test ends. Older tests use `defer os.Unsetenv(...)`.

## Output Capture

The `captureOutput` helper (in `main_test.go`) uses `os.Pipe()` to redirect `os.Stdout` and `os.Stderr`, allowing test assertions on terminal output without consuming the actual terminal.

## Parallelism in Tests

- Pure unit tests are marked `t.Parallel()`.
- CLI tests that change environment variables or capture stdout/stderr are **not** parallel.
- CI runs `go test -race ./...` — the parallel WHOIS checker is designed to be race-detector clean (atomic counters, result-by-index writes, mutex for printing).

## Key Patterns

- **Flag isolation:** `RunCLI` uses `flag.NewFlagSet` (not `flag.CommandLine`), so parallel tests don't share flag state.
- **Temp files:** New tests use `t.TempDir()`, which is removed automatically. Older tests create temporary files and clean up via `defer os.Remove(...)`.
- **Exit code testing:** `cmd/talia/main_test.go` overrides the `exitFunc` variable to capture exit codes without actually calling `os.Exit`.

Prefer tests through the CLI or a module's existing interface. The scheduling tests hold the first wave of TCP responses until the requested concurrency is observed; a result-count assertion alone cannot prove lightspeed is parallel. File tests use real temporary files and assert retained domains, reasons, logs, and merge precedence. Tests that only exercise a cloned implementation or reassert struct tags add no production statement coverage and have been removed.

## Related Documentation

- [Development Guide](development.md)
- [Configuration Reference](configuration.md)
