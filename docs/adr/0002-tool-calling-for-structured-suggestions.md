# Suggestions use forced tool calling over any OpenAI-compatible API

Superseded by [ADR-0007](0007-explicit-suggestion-provider-contracts.md). This records the original rationale; forced tool calling alone does not guarantee schema conformance or support across providers and models.

Suggestions come from the chat completions API with a single `suggest_domains` tool forced via `tool_choice`, so the reply is always structured JSON. Talia talks only to OpenAI-compatible endpoints (configurable base URL), which covers OpenAI, Gemini, and local models without per-provider code.

## Considered Options

- **`response_format` structured output**: less consistently supported across providers than tool calling.
- **Provider-specific SDKs**: more features, but each provider becomes its own code path.
