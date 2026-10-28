# Support explicit suggestion-provider contracts

The CLI supports native OpenAI Responses and Anthropic Messages APIs for suggestions, using each provider's JSON structured output contract. Custom OpenAI-compatible endpoints retain Chat Completions with forced tool calling as a best-effort option. We accept provider-specific request and response handling because matching the OpenAI request format does not establish equivalent behavior or guarantees across providers and models.

This supersedes [ADR-0002](0002-tool-calling-for-structured-suggestions.md), which required forced tool calling through a single OpenAI-compatible contract. OpenAI Chat Completions is non-strict by default; Anthropic's compatibility layer ignores function `strict` and `response_format`, and some Claude models reject forced tool calls. Suggestions require a fixed data shape rather than executing a tool, so native JSON output fits the operation and removes the forced-call dependency for supported providers.

OpenAI uses strict `text.format` JSON Schema with `store: false`; Anthropic uses `output_config.format` JSON Schema and API version `2026-10-21`. The native provider defaults are `gpt-6.1-terra` and `claude-haiku-5-5`, with explicit model overrides for compatible models. Chat Completions compatibility mode keeps `gpt-6-luna` because Terra's tool calling requires Responses. Shared validation handles missing data, normalization, duplicates, and exclusions; refusals, incomplete responses, and requests with no usable suggestions fail. Counts remain best effort without automatic retries.

Existing custom-base CLI configurations select Chat Completions unless a provider is selected explicitly. The exported Go `GenerateDomainSuggestions` function keeps its existing Chat Completions contract. MCP support is deferred; this change only updates suggestion generation in the existing CLI.

## Sources

- [OpenAI function calling: strict mode](https://developers.openai.com/api/docs/guides/function-calling#strict-mode)
- [OpenAI JSON structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [GPT-6.1 Terra](https://developers.openai.com/api/docs/models/gpt-6.1-terra)
- [Anthropic OpenAI compatibility limitations](https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk)
- [Anthropic JSON structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)
- [Claude Haiku 5.5](https://platform.claude.com/docs/en/models/haiku-5-5/overview)
- [Anthropic API versions](https://platform.claude.com/docs/en/api/versioning)
