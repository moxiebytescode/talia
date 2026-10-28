# Configuration Reference

All CLI flags, environment variables, and `.env` file support.

## CLI Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `--whois` | string | — | WHOIS server in `host:port` format. Required for domain checking |
| `--sleep` | duration | `2s` | Delay between sequential WHOIS checks. Ignored in parallel mode |
| `--verbose` | bool | `false` | Include raw WHOIS response in `log` field for all results |
| `--grouped-output` | bool | `false` | Output as `{available:[], unavailable:[]}` instead of array |
| `--output-file` | string | — | Separate file for grouped output (leaves input unchanged) |
| `--suggest` | int | `0` | Number of AI suggestions to generate per request |
| `--suggest-parallel` | int | `1` | Number of concurrent AI suggestion requests |
| `--prompt` | string | — | Natural language prompt to guide AI suggestions |
| `--model` | string | `gpt-5-mini` | AI model name |
| `--api-base` | string | — | Base URL for OpenAI-compatible API |
| `--fresh` | bool | `false` | Don't send existing domains as exclusions to AI |
| `--clean` | bool | `false` | Normalize/deduplicate domains in the file, then exit |
| `--no-verify` | bool | `false` | Skip WHOIS verification after generating suggestions |
| `--merge` | bool | `false` | Merge multiple domain files with deduplication |
| `-o` | string | — | Output file for `--merge` |
| `--export-available` | string | — | Export available domains to a plain text file |
| `--lightspeed` | string | — | Parallel WHOIS: `"max"`, a positive integer, or empty for sequential. Any other value exits with an error |

## Environment Variables

| Variable | Fallback for | Notes |
|---|---|---|
| `TALIA_FILE` | positional arg | Target file path |
| `WHOIS_SERVER` | `--whois` | WHOIS server `host:port` |
| `OPENAI_API_KEY` | — | Required for `--suggest`. No flag equivalent |
| `OPENAI_API_BASE` | `--api-base` | Falls back to `https://api.openai.com/v1` |
| `TALIA_SUGGEST` | `--suggest` | Ignored if file has pending `unverified` domains |
| `TALIA_SUGGEST_PARALLEL` | `--suggest-parallel` | Number of parallel AI requests |
| `TALIA_PROMPT` | `--prompt` | Extra context for AI suggestions |
| `TALIA_MODEL` | `--model` | AI model name |
| `TALIA_LIGHTSPEED` | `--lightspeed` | Parallel WHOIS worker count; invalid values exit with an error |
| `NO_COLOR` | — | Any non-empty value disables colored output ([no-color.org](https://no-color.org)) |

## Precedence

```
explicit CLI flag  >  shell environment variable  >  .env file
```

### `.env` File

Talia loads a `.env` file from the current working directory at startup.

Example `.env` file:

```
OPENAI_API_KEY=your-api-key
OPENAI_API_BASE=https://generativelanguage.googleapis.com/v1beta/openai
WHOIS_SERVER=whois.verisign-grs.com:43
TALIA_PROMPT=short brandable startup names
TALIA_SUGGEST=10
TALIA_MODEL=gemini-3.1-flash-lite
TALIA_FILE=suggestions.json
```

Rules:

- Does **not** override existing shell environment variables.
- Supports `KEY=VALUE` format (matching quotes — both `"` or both `'` — are stripped from values).
- Lines without `=` are silently skipped.
- No inline comment stripping — `KEY=value # comment` sets the value to `value # comment` (the full right-hand side).
- A variable set to empty string in the shell (`export KEY=""`) counts as "existing" and will not be overwritten.
- Silently ignored if the file doesn't exist.

### Explicit Flags Always Win

`TALIA_MODEL`, `TALIA_SUGGEST_PARALLEL` and `TALIA_SUGGEST` apply only when their flag is not passed. Passing the flag explicitly, even with its default value (`--model=gpt-5-mini`, `--suggest-parallel=1`, `--suggest=0`), overrides the env var. String flags such as `--prompt`, `--api-base` and `--whois` fall back to their env var whenever they are empty.

### `--sleep` During Auto-Verification

The `--sleep` flag is ignored during the auto-verification step after `--suggest`. Auto-verification uses a hardcoded 100ms delay between WHOIS checks for speed. The `--sleep` value only applies to standalone WHOIS checking runs.

## Behavior Notes

### Check errors

A failed check does not abort the run: the domain is recorded as unavailable with reason `ERROR` and the error text in `log` (regardless of `--verbose`). The exit code is `0` as long as the file is written.

Each WHOIS check has a 30-second deadline covering connection setup, writing the query, and reading the response. A stalled or incomplete response becomes `ERROR`, even if a partial response contains `No match for`. Library callers can change this deadline with `NetWhoisClient.Timeout`; zero or negative values use the default. Each suggestion request has a two-minute timeout, including reading its response.

Progress lines and the summary are colored only when stdout is a terminal. Piped or redirected output, and any run with `NO_COLOR` set to a non-empty value, has no ANSI escape codes.

### Normalization

Every suggestion, cleaned domain, and merged domain is normalized:

| Rule | Example |
|---|---|
| Lowercase and trim whitespace | `" Example.COM "` → `"example.com"` |
| Strip repeated `.com` suffixes | `"foo.com.com.com"` → `"foo.com"` |
| Collapse double dots | `"foo..com"` → `"foo.com"` |
| Must end with `.com` | `"foo.io"` → rejected |
| No subdomains | `"sub.foo.com"` → rejected |
| Label is `[a-z0-9-]`, no leading/trailing hyphen | `"-foo.com"` → rejected |
| Label is at most 63 characters | A 64-character label → rejected |

Suggestions use normalized domain identity for deduplication and the exclusion list. Adding suggestions preserves existing records and their metadata; use `--clean` to normalize or remove existing records. Missing or empty domain files can be created, but malformed JSON and other read failures stop suggestion generation before any request is sent or the file is changed. Array files are converted to grouped files when suggestions are added: available records stay available, records with a check reason stay unavailable, and records without a check reason remain unverified.

### `--clean`

- Array and grouped JSON files are cleaned in their original format. Other input is treated as plain text, one domain per line. Malformed input beginning with `[` or `{` is rejected without changing the file.
- In grouped files, duplicates are resolved in bucket order available → unavailable → unverified, so a domain in both `available` and `unverified` keeps the `available` entry.
- In plain text, blank lines and lines starting with `#` are skipped, the first occurrence wins, and input order is preserved.

### Merging

`--merge` keeps the first occurrence of each domain; `--output-file` with `--grouped-output` keeps the newest result. See [ADR-0005](../adr/0005-two-merge-semantics.md).

Merge and export accept both array and grouped files. Array records are interpreted as described above when a grouped file is needed. In-place grouped checks also apply newest-wins: fresh checks replace stale records and remove checked domains from `unverified` without leaving a domain in both result buckets.

Writing to an existing `--output-file` keeps the order of its entries: a re-checked domain is updated where it is, and new domains, or domains that change bucket, are added at the end. Any `unverified` domains in the output file are kept, except those that were just checked.

Grouped updates compare valid domains by normalized identity, so spelling variants such as `" Example.COM "` and `"example.com"` refer to the same domain. Stored spelling and metadata come from the retained record; identity comparisons do not rewrite them. Invalid names keep their distinct raw identities until explicitly cleaned.

If a check run includes the same normalized domain more than once, grouped output keeps the last check in input order before assigning buckets. This applies to array and grouped input, including lightspeed runs; worker completion order does not change which duplicate is retained.

For grouped input, `--grouped-output --output-file` merges only the results of checking the input's `unverified` bucket. The input's existing `available` and `unavailable` entries are not copied to the output file, so older input results cannot replace newer destination results. The input file stays unchanged when the output path is separate. Without these flags, checks update the input file in place and keep its existing results.

## Related Documentation

- [Development Guide](development.md)
- [Glossary](../../GLOSSARY.md)
- [ADRs](../adr/)
