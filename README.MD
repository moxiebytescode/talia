# Talia

[![CI](https://github.com/moxiebytescode/talia/actions/workflows/ci.yml/badge.svg)](https://github.com/moxiebytescode/talia/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/moxiebytescode/talia?logo=github&sort=semver)](https://github.com/moxiebytescode/talia/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/moxiebytescode/talia.svg)](https://pkg.go.dev/github.com/moxiebytescode/talia)

Talia is a command-line tool for domain name discovery. It combines **AI-powered domain suggestion generation** (via OpenAI, Anthropic, or a custom OpenAI-compatible API) with **WHOIS availability checking** to help you find available domain names fast.

**Key capabilities:**

- **AI Domain Generation** — Generate creative `.com` domain ideas using LLMs, with customizable prompts and parallel requests for high throughput
- **WHOIS Availability Checking** — Verify domain availability via WHOIS servers, with sequential or parallel (lightspeed) modes
- **End-to-End Workflow** — Generate suggestions, auto-verify them, clean/normalize results, merge files, and export available domains — all from a single CLI

## Table of Contents

- [Overview](#overview)
- [Disclaimer](#disclaimer)
- [Installation](#installation)
- [Usage](#usage)
  - [Required WHOIS Server](#required-whois-server)
  - [JSON Input Format](#json-input-format)
  - [Domain Checks and Rate Limiting](#domain-checks-and-rate-limiting)
  - [Optional Sleep Flag](#optional-sleep-flag)
  - [Generating Domain Suggestions](#generating-domain-suggestions)
  - [Merging Domain Files](#merging-domain-files)
  - [Cleaning Domain Files](#cleaning-domain-files)
  - [Verbose Mode](#verbose-mode)
  - [Grouped Output](#grouped-output)
  - [Unverified Domains in Grouped Files](#unverified-domains-in-grouped-files)
- [How It Works](#how-it-works)
- [Testing](#testing)
- [Configuration](#configuration)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

Talia can generate domain name suggestions using AI, check domain availability via WHOIS, or both. When checking domains, it reads a list from a JSON file and connects to a **user-specified** WHOIS server. After each check, Talia can:

1. **Non-grouped mode (default)**:  
   Talia updates the **original file** in an array format, for example:

   [
   {
   "domain": "filterguard.com",
   "available": false,
   "reason": "TAKEN"
   },
   {
   "domain": "vetterhub.com",
   "available": true,
   "reason": "NO_MATCH"
   }
   ]

2. **Grouped mode** (`--grouped-output`):

   - **No `--output-file`**: Talia **overwrites** the input file with a single JSON object containing `"available"` and `"unavailable"` arrays, each filled with `{ "domain": "...", "reason": "..." }`. For example:

     {
     "available": [
     {
     "domain": "vetterhub.com",
     "reason": "NO_MATCH"
     }
     ],
     "unavailable": [
     {
     "domain": "filterguard.com",
     "reason": "TAKEN"
     }
     ]
     }

   - **With `--output-file`**: Talia leaves the original file alone and writes/merges a grouped JSON file at the specified path:
     {
     "available": [
     { "domain": "vetterhub.com", "reason": "NO_MATCH" }
     ],
     "unavailable": [
     { "domain": "filterguard.com", "reason": "TAKEN" }
     ]
     }

---

## Disclaimer

- **No Default WHOIS Server**: You must specify a WHOIS server via `--whois=<host:port>`. If you use `whois.verisign-grs.com:43` or similar, confirm you comply with its TOS.
- **Accuracy**: Talia identifies availability by scanning for the substring `"No match for"`. This may not work for every TLD or WHOIS provider.

## Installation

### Prerequisites

- Go 1.27.3 or later (as declared in `go.mod`; with the default `GOTOOLCHAIN=auto`, an older Go downloads it automatically)
- golangci-lint v2.15.0 (declared as a Go tool; auto-downloads on first use)

### Steps

    # Clone the repository:
    git clone https://github.com/moxiebytescode/talia.git
    cd talia

    # Build Talia:
    go build -o talia ./cmd/talia/

---

## Usage

### Required WHOIS Server

You **must** specify a WHOIS server:

    ./talia --whois=whois.example.com:43 path/to/domains.json

If you omit this flag, Talia exits with an error.

### JSON Input Format

Your JSON file should be an array of objects, each having a `"domain"` field, e.g.:

    [
      { "domain": "filterguard.com" },
      { "domain": "vetterhub.com" }
    ]

Alternatively, you can now use a grouped file with an `"unverified"` array as input (see [Unverified Domains in Grouped Files](#unverified-domains-in-grouped-files)).

### Domain Checks and Rate Limiting

By default, Talia sleeps 2 seconds (`--sleep=2s`) between each domain WHOIS query to reduce server load. You can override this with:

    ./talia --whois=whois.example.com:43 --sleep=500ms path/to/domains.json

### Optional Sleep Flag

As shown above, `--sleep` changes how long Talia waits between domain checks.

### Generating Domain Suggestions

Choose a suggestion provider with `--provider` or `TALIA_PROVIDER`. This feature requires the selected provider's API key and internet access. Talia loads a `.env` file from the current directory without overriding shell variables.

| Provider | Default model | API key | Request contract |
|---|---|---|---|
| `openai` (default) | `gpt-6.1-terra` | `OPENAI_API_KEY` | Native Responses API with JSON structured output |
| `anthropic` | `claude-haiku-5-5` | `ANTHROPIC_API_KEY` | Native Messages API with JSON structured output |
| `openai-compatible` | `gpt-6-luna`; usually override for the endpoint | `OPENAI_API_KEY` | Chat Completions with forced tool calling; best effort |

`--model` overrides the default or `TALIA_MODEL`. `TALIA_MODEL` applies to whichever provider is selected; unset it to use provider defaults. Custom models must support the selected request contract. The native provider defaults are [GPT-6.1 Terra](https://developers.openai.com/api/docs/models/gpt-6.1-terra) and [Claude Haiku 5.5](https://platform.claude.com/docs/en/models/haiku-5-5/overview). Compatibility mode keeps `gpt-6-luna` because Terra's tool calling requires Responses; custom endpoints usually need their own model name.

Use `--api-base` to override the provider's base URL. Otherwise, OpenAI and compatibility mode read `OPENAI_API_BASE` (default `https://api.openai.com/v1`); Anthropic reads `ANTHROPIC_API_BASE` (default `https://api.anthropic.com/v1`). Set `ANTHROPIC_WORKSPACE_ID` if your Anthropic key requires workspace selection.

Example `.env` file:
```
OPENAI_API_KEY=your-api-key
TALIA_PROVIDER=openai
WHOIS_SERVER=whois.verisign-grs.com:43
TALIA_PROMPT=short brandable startup names
TALIA_SUGGEST=10
TALIA_FILE=suggestions.json
```

Use the `--suggest` flag to generate domain ideas. If you also provide `--whois`, Talia will automatically verify the suggestions via WHOIS (with a fast 100ms delay between checks). Use `--no-verify` to skip auto-verification and just write suggestions. You can change the model with `--model`.

    # Generate and auto-verify suggestions
    OPENAI_API_KEY=... ./talia --suggest=5 --whois=whois.verisign-grs.com:43 --prompt="three word tech names" suggestions.json

    # Generate suggestions only (no verification)
    OPENAI_API_KEY=... ./talia --suggest=5 --no-verify --prompt="short brandable names" suggestions.json

    # Generate with Anthropic
    ANTHROPIC_API_KEY=... ./talia --provider=anthropic --suggest=5 --no-verify --prompt="short brandable names" suggestions.json

    # Use a custom Chat Completions endpoint and its model
    OPENAI_API_KEY=... ./talia --provider=openai-compatible --api-base="http://localhost:1234/v1" --model=local-model --suggest=5 --no-verify suggestions.json

For existing configurations, a custom `--api-base` or `OPENAI_API_BASE` still selects Chat Completions compatibility mode when neither `--provider` nor `TALIA_PROVIDER` is set. An explicit provider always selects its own request contract, including when its base URL is customized. See the [configuration reference](docs/guides/configuration.md) for precedence and failure behavior.

#### Parallel Suggestion Requests

Use `--suggest-parallel` to fire multiple suggestion requests concurrently. Each request asks the AI for `--suggest` domains; the final count can be lower because invalid names, exclusions, and duplicates are removed:

    # Run 5 parallel requests, each generating 10 suggestions (up to 50 total)
    ./talia --suggest=10 --suggest-parallel=5 --no-verify suggestions.json

    # Set via environment variable
    TALIA_SUGGEST_PARALLEL=3 ./talia --suggest=10 --no-verify suggestions.json

This is useful for generating large batches of ideas quickly. Duplicates across requests are automatically removed when writing to the output file.

Talia normalizes suggestions, fixing common issues such as repeated `.com` suffixes (`example.com.com.com`) and double dots. It removes invalid names, duplicates, and excluded domains. A malformed response or one with no usable suggestions fails that request. Requested counts are best effort; Talia does not retry to fill a shortfall.

**Example output (`suggestions.json`):**

```json
{
  "unverified": [
    { "domain": "techtriplewave.com" },
    { "domain": "cloudbytefusion.com" },
    { "domain": "datastreampulse.com" },
    { "domain": "logicmeshzone.com" },
    { "domain": "pixelcorelabs.com" }
  ]
}
```

Missing credentials, refusals, incomplete responses, and other total request failures leave the domain file unchanged. If some parallel requests succeed, Talia keeps their suggestions and reports the failed requests.

**Tip:** You can add new suggestions to an existing grouped file by appending to the `"unverified"` array, then re-running Talia to check them.

### Merging Domain Files

Use `--merge` to combine multiple domain files:

    # Merge multiple files into a new output file (inputs unchanged)
    ./talia --merge -o combined.json file1.json file2.json file3.json

    # Merge files into the first file listed
    ./talia --merge file1.json file2.json file3.json

This merges all domains preserving their categories (available, unavailable, unverified). Duplicates are automatically skipped.

### Exporting Available Domains

Use `--export-available` to export just the available domains to a plain text file:

    ./talia --export-available=available.txt suggestions.json

This writes one domain per line, making it easy to use with other tools.

### Parallel WHOIS Checking (Lightspeed Mode)

Use `--lightspeed=<n>` for parallel WHOIS checking. The flag requires a value: a positive worker count or `max`. Any other value exits with an error.

    # Use 10 concurrent workers
    ./talia --whois=whois.verisign-grs.com:43 --lightspeed=10 domains.json

    # Use 50 concurrent workers
    ./talia --whois=whois.verisign-grs.com:43 --lightspeed=50 domains.json

    # Maximum parallelism (one goroutine per domain)
    ./talia --whois=whois.verisign-grs.com:43 --lightspeed=max domains.json

This dramatically speeds up checking large domain lists. Note that aggressive parallelism may trigger rate limiting on some WHOIS servers.

### Cleaning Domain Files

Use `--clean` to sanitize an existing domain file. This normalizes all domains, removes invalid entries, and deduplicates. Works with both JSON grouped files and plain text domain lists (one domain per line):

    ./talia --clean suggestions.json
    ./talia --clean domains.txt

The clean operation:
- Converts domains to lowercase
- Fixes repeated `.com` suffixes (e.g., `example.com.com.com` → `example.com`)
- Removes double dots (e.g., `example..com` → `example.com`)
- Removes domains that don't match the `name.com` format
- Removes duplicates

Invalid domains are listed in the output so you can see what was removed.

### Verbose Mode

By default, Talia only writes the WHOIS response to the `"log"` field if there's an error. If you set `--verbose`, Talia stores the full WHOIS response for every domain:

    ./talia --whois=whois.example.com:43 --verbose path/to/domains.json

### Grouped Output

Use `--grouped-output` to produce a grouped JSON. The behavior depends on `--output-file`:

1. **No `--output-file`**: The input JSON file is overwritten with:

   {
   "available": [
   { "domain": "...", "reason": "NO_MATCH" }
   ],
   "unavailable": [
   { "domain": "...", "reason": "TAKEN" }
   ]
   }

2. **With `--output-file=somefile.json`**: The input file stays intact, and Talia writes/merges grouped results into `somefile.json`.

### Unverified Domains in Grouped Files

Talia now supports using a grouped file with an `"unverified"` array as input. This is useful for adding new domains to an existing grouped file:

```json
{
  "available": [{ "domain": "existing-available.com", "reason": "NO_MATCH" }],
  "unavailable": [{ "domain": "existing-unavailable.com", "reason": "TAKEN" }],
  "unverified": [
    { "domain": "new-domain1.com" },
    { "domain": "new-domain2.com" }
  ]
}
```

When Talia processes this file in place (without a separate `--output-file`):

1. It checks all domains in the `"unverified"` array
2. Moves them into either `"available"` or `"unavailable"` based on WHOIS results
3. Clears the `"unverified"` array
4. Preserves existing domains in the `"available"` and `"unavailable"` arrays

This allows you to incrementally add and verify new domains while maintaining your existing results.

With `--grouped-output --output-file=somefile.json`, Talia leaves the input file unchanged and merges only the newly checked results into `somefile.json`. The input's existing `available` and `unavailable` entries are not copied. Existing results in the output file are kept unless a fresh check replaces them, and its `unverified` domains are kept unless they were just checked. See the [merging behavior](docs/guides/configuration.md#merging) for ordering rules.

### Static Analysis (golangci‑lint)

To lint the codebase, run:

```bash
go tool golangci-lint run
```

Go automatically builds and caches the version pinned in `go.mod` the first time you invoke the command.  
If you already have a global `golangci-lint` on your PATH, you can instead run:

```bash
golangci-lint run
```

A clean, zero‑error run is required before submitting a pull request.

---

## How It Works

1. **Parse Input**: Talia reads the JSON from disk into Go structs.
2. **Check WHOIS**: For each domain, Talia queries the WHOIS server.
   - If "No match for" → `available=true, reason=NO_MATCH`
   - Otherwise → `available=false, reason=TAKEN`
   - If error → `available=false, reason=ERROR`
3. **Write**:
   - **Non-grouped**: Talia updates `[{"domain":"x","available":true,"reason":"NO_MATCH"},...]` in the same file.
   - **Grouped w/o output-file**: Talia overwrites your file with `{ "available": [...], "unavailable": [...] }`.
   - **Grouped w/ output-file**: Leaves input alone, merges or creates grouped JSON in the separate file.
   - **Grouped with unverified**: Processes unverified domains and moves them to the appropriate lists.

---

## Testing

    go test -v

Talia's test suite covers:

- **Unit Tests**: checking domain availability with mock WHOIS responses
- **Integration Tests**: verifying inline updates, grouped mode, merges, and file rewrites
- **Error Handling**: invalid JSON, read/write failures, missing WHOIS server, etc.

---

## Configuration

- **`--whois=<host:port>`**: WHOIS server (required for checks; can also be set via `WHOIS_SERVER` env var)
- **`--sleep=<duration>`**: Wait time between queries (default 2s)
- **`--verbose`**: Store WHOIS data in `"log"` even for successful checks
- **`--grouped-output`**: Switch to grouped output
- **`--output-file=<path>`**: If provided, Talia merges/writes grouped JSON to this file and leaves the input alone
- **`--suggest=<n>`**: Generate <n> domain suggestions per request (can also be set via `TALIA_SUGGEST` env var)
- **`--suggest-parallel=<n>`**: Run <n> suggestion requests in parallel (default 1; can also be set via `TALIA_SUGGEST_PARALLEL` env var)
- **`--prompt=<text>`**: Text to influence domain suggestion themes (can also be set via `TALIA_PROMPT` env var)
- **`--provider=<name>`**: Suggestion provider: `openai` (default), `anthropic`, or `openai-compatible` (can also be set via `TALIA_PROVIDER`)
- **`--model=<name>`**: Suggestion model (default: `gpt-6.1-terra` for OpenAI, `claude-haiku-5-5` for Anthropic, `gpt-6-luna` for compatibility mode; can also be set via `TALIA_MODEL`)
- **`--api-base=<url>`**: Provider base URL; overrides `OPENAI_API_BASE` or `ANTHROPIC_API_BASE` for the selected provider
- **`--fresh`**: Don't pass existing domains to AI when generating suggestions (allows duplicates, starts fresh)
- **`--no-verify`**: Skip WHOIS verification after generating suggestions (by default, suggestions are verified if `--whois` is provided)
- **`--merge`**: Merge multiple domain files (use with `-o` to specify output file)
- **`-o=<path>`**: Output file for merge operation (if not set, merges into first file)
- **`--export-available=<path>`**: Export available domains to a plain text file (one domain per line)
- **`--lightspeed=<n>`**: Enable parallel WHOIS checking with n workers, or `max` for one worker per domain (can also be set via `TALIA_LIGHTSPEED` env var)
- **`--clean`**: Clean and normalize domains in the file (removes invalid domains, fixes malformed entries)
- **Input JSON**: The file containing an array of domain objects or a grouped object with unverified domains (can also be set via `TALIA_FILE` env var)

---

## Documentation

- [`GLOSSARY.md`](GLOSSARY.md) — domain vocabulary
- [`docs/adr/`](docs/adr/) — architecture decisions
- [`docs/guides/`](docs/guides/) — development, configuration, and testing guides

## Contributing

Pull requests are welcome. All commits must follow the Conventional Commits
specification so that automated release tooling can parse them correctly.
A commit lint check runs in CI to enforce this format. Example prefix types
include `feat`, `fix`, `chore`, and `docs`.

## License

This project is licensed under the terms of the [MIT License](LICENSE).
