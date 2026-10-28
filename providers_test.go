package talia

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// providerTextResponse wraps structured text in a native provider response,
// including reasoning content that suggestion extraction must skip.
func providerTextResponse(t *testing.T, provider, text string) string {
	t.Helper()
	var envelope any
	if provider == "openai" {
		envelope = map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{"type": "reasoning", "summary": []any{}},
				map[string]any{"type": "message", "status": "completed", "content": []any{
					map[string]any{"type": "output_text", "text": text},
				}},
			},
		}
	} else {
		envelope = map[string]any{
			"type": "message", "stop_reason": "end_turn",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "internal"},
				map[string]any{"type": "redacted_thinking", "data": "internal"},
				map[string]any{"type": "text", "text": text},
			},
		}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestNativeSuggestionProviderContracts verifies native request paths, headers,
// structured-output schemas, and normalized suggestions with exclusions applied.
func TestNativeSuggestionProviderContracts(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			const text = `{"unverified":[{"domain":" New-Name.COM.COM "},{"domain":"new-name.com"},{"domain":"already.com"},{"domain":"bad.net"},{"domain":"another.com"}]}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := "/v1/responses"
				if provider == "anthropic" {
					path = "/v1/messages"
					if r.Header.Get("anthropic-version") != anthropicVersion || r.Header.Get("anthropic-workspace-id") != "wrk_test" {
						t.Error("missing Anthropic version or workspace header")
					}
					if r.Header.Get("x-api-key") != "" {
						t.Error("unexpected legacy Anthropic authentication")
					}
				} else if r.Header.Get("anthropic-version") != "" || r.Header.Get("anthropic-workspace-id") != "" {
					t.Error("Anthropic headers sent to OpenAI")
				}
				if r.URL.Path != path || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect native provider endpoint, method, or headers")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				var model string
				_ = json.Unmarshal(body["model"], &model)
				if model != "test-model" {
					t.Errorf("model = %q", model)
				}
				for _, field := range []string{"temperature", "top_p", "tools", "tool_choice", "response_format"} {
					if _, ok := body[field]; ok {
						t.Errorf("unexpected request field %s", field)
					}
				}
				var format struct {
					Format struct {
						Type   string         `json:"type"`
						Name   string         `json:"name"`
						Strict bool           `json:"strict"`
						Schema map[string]any `json:"schema"`
					} `json:"format"`
				}
				var userContent string
				if provider == "openai" {
					if string(body["store"]) != "false" || string(body["instructions"]) != `"`+systemPrompt+`"` {
						t.Error("Responses request must disable storage and include instructions")
					}
					_ = json.Unmarshal(body["input"], &userContent)
					if err := json.Unmarshal(body["text"], &format); err != nil || !format.Format.Strict || format.Format.Name != functionName {
						t.Error("Responses request must select named strict text.format")
					}
				} else {
					if string(body["max_tokens"]) != "8192" || string(body["system"]) != `"`+systemPrompt+`"` {
						t.Error("Messages request must include max_tokens and system prompt")
					}
					var messages []struct{ Role, Content string }
					_ = json.Unmarshal(body["messages"], &messages)
					if len(messages) != 1 || messages[0].Role != "user" {
						t.Error("Messages request must contain one user message")
					} else {
						userContent = messages[0].Content
					}
					if err := json.Unmarshal(body["output_config"], &format); err != nil {
						t.Error("Messages request must use output_config.format")
					}
				}
				if !strings.Contains(userContent, "brand names") || !strings.Contains(userContent, "Return 10") || !strings.Contains(userContent, "ALREADY.COM") {
					t.Error("request omitted prompt, target count, or exclusions")
				}
				if format.Format.Type != "json_schema" {
					t.Error("request did not select json_schema")
				}
				assertSuggestionRequestSchema(t, format.Format.Schema)
				_, _ = io.WriteString(w, providerTextResponse(t, provider, text))
			}))
			t.Cleanup(srv.Close)
			config := suggestionConfig{provider: provider, apiKey: "test-key", model: "test-model", baseURL: srv.URL + "/v1/", workspaceID: "wrk_test"}
			got, err := generateProviderSuggestions(config, "brand names", 10, []string{" ALREADY.COM "}, srv.Client())
			want := []DomainRecord{{Domain: "new-name.com"}, {Domain: "another.com"}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("suggestions = %#v, error = %v; want %#v", got, err, want)
			}
		})
	}
}

// assertSuggestionRequestSchema checks required fields and closed objects at
// both levels of the suggestion schema.
func assertSuggestionRequestSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" || schema["additionalProperties"] != false || !reflect.DeepEqual(schema["required"], []any{"unverified"}) {
		t.Errorf("schema must require only the unverified array and close the root object: %#v", schema)
		return
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("missing schema properties")
	}
	array, ok := properties["unverified"].(map[string]any)
	if !ok || array["type"] != "array" {
		t.Fatal("unverified schema must be an array")
	}
	item, ok := array["items"].(map[string]any)
	if !ok || item["type"] != "object" || item["additionalProperties"] != false || !reflect.DeepEqual(item["required"], []any{"domain"}) {
		t.Fatal("each suggestion must be a closed object requiring domain")
	}
	fields, ok := item["properties"].(map[string]any)
	if !ok || !reflect.DeepEqual(fields["domain"], map[string]any{"type": "string"}) {
		t.Fatal("domain schema must be a string")
	}
}

// TestNativeSuggestionProviderRejectsUnsuccessfulResponses verifies refusals,
// incomplete responses, and malformed envelopes produce safe errors.
func TestNativeSuggestionProviderRejectsUnsuccessfulResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, provider, response, wantErr string }{
		{"openai refusal", "openai", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"sensitive-provider-detail"}]}]}`, "refused"},
		{"openai incomplete", "openai", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "incomplete"},
		{"openai failed", "openai", `{"status":"failed","error":{"message":"sensitive-provider-detail"}}`, "response error"},
		{"openai missing status", "openai", `{"output":[]}`, "did not complete"},
		{"openai unfinished message", "openai", `{"status":"completed","output":[{"type":"message","status":"incomplete","content":[]}]}`, "did not complete"},
		{"openai no text", "openai", `{"status":"completed","output":[]}`, "no suggestion text"},
		{"openai sdk convenience field only", "openai", `{"status":"completed","output_text":"sensitive-provider-detail"}`, "no suggestion text"},
		{"anthropic refusal", "anthropic", `{"type":"message","stop_reason":"refusal","content":[{"type":"text","text":"sensitive-provider-detail"}]}`, "refused"},
		{"anthropic token limit", "anthropic", `{"type":"message","stop_reason":"max_tokens","content":[]}`, "incomplete"},
		{"anthropic pause", "anthropic", `{"type":"message","stop_reason":"pause_turn","content":[]}`, "did not complete"},
		{"anthropic error", "anthropic", `{"type":"error","error":{"message":"sensitive-provider-detail"}}`, "response error"},
		{"anthropic missing stop reason", "anthropic", `{"type":"message","content":[]}`, "did not complete"},
		{"anthropic no text", "anthropic", `{"type":"message","stop_reason":"end_turn","content":[]}`, "no suggestion text"},
		{"anthropic unexpected tool", "anthropic", `{"type":"message","stop_reason":"end_turn","content":[{"type":"tool_use","name":"sensitive-provider-detail"}]}`, "unexpected suggestion content"},
		{"invalid envelope", "openai", `sensitive-provider-detail`, "decode response"},
		{"trailing envelope", "anthropic", `{"type":"message","stop_reason":"end_turn","content":[]} sensitive-provider-detail`, "decode response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.response) }))
			t.Cleanup(srv.Close)
			_, err := generateProviderSuggestions(suggestionConfig{provider: tc.provider, apiKey: "test-key", model: "test-model", baseURL: srv.URL}, "", 1, nil, srv.Client())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "sensitive-provider-detail") {
				t.Fatalf("error = %v, want safe error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestNativeSuggestionProvidersValidateStructuredOutput verifies invalid JSON
// shapes and results without usable, nonexcluded domains are rejected.
func TestNativeSuggestionProvidersValidateStructuredOutput(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			for _, text := range []string{
				`not-json`, `null`, `{}`, `[]`, `{"unverified":null}`, `{"unverified":{}}`,
				`{"unverified":[{}]}`, `{"unverified":[null]}`, `{"unverified":[{"domain":null}]}`,
				`{"unverified":[{"domain":42}]}`, `{"unverified":[{"Domain":"a.com"}]}`,
				`{"unverified":[{"domain":"a.com","available":true}]}`,
				`{"unverified":[{"domain":"a.com"}],"extra":true}`,
				`{"unverified":[]}`, `{"unverified":[{"domain":"invalid.net"}]}`,
				`{"unverified":[{"domain":"existing.com"}]}`,
				`{"unverified":[{"domain":"a.com"}]} {}`,
			} {
				t.Run(text, func(t *testing.T) {
					t.Parallel()
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.WriteString(w, providerTextResponse(t, provider, text))
					}))
					t.Cleanup(srv.Close)
					got, err := generateProviderSuggestions(suggestionConfig{provider: provider, apiKey: "test-key", model: "test-model", baseURL: srv.URL}, "", 1, []string{"existing.com"}, srv.Client())
					if err == nil || got != nil {
						t.Fatalf("invalid or unusable output accepted: %#v, %v", got, err)
					}
				})
			}
		})
	}
}

// TestSuggestionProviderHTTPErrorDoesNotExposeBody verifies HTTP failures expose
// typed provider/status information without credentials or provider response text.
func TestSuggestionProviderHTTPErrorDoesNotExposeBody(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"openai", "anthropic", "openai-compatible"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
				t.Run(fmt.Sprint(status), func(t *testing.T) {
					t.Parallel()
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(status)
						_, _ = io.WriteString(w, `{"error":{"message":"secret-key sensitive-provider-detail"}}`)
					}))
					t.Cleanup(srv.Close)
					_, err := generateProviderSuggestions(suggestionConfig{provider: provider, apiKey: "secret-key", model: "test-model", baseURL: srv.URL}, "", 1, nil, srv.Client())
					if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "sensitive-provider-detail") {
						t.Fatalf("unsafe or missing HTTP error: %v", err)
					}
					var statusErr *ProviderStatusError
					if !errors.As(err, &statusErr) || statusErr.Provider != provider || statusErr.StatusCode != status {
						t.Fatalf("missing typed provider/status information: %v", err)
					}
				})
			}
		})
	}
}

type providerTransportError struct{}

// Do simulates a transport failure whose error text contains sensitive data.
func (providerTransportError) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("sensitive-provider-detail secret-key")
}

// TestSuggestionProviderTransportErrorDoesNotExposeCredentials verifies transport
// error text cannot leak credentials or provider details into returned errors.
func TestSuggestionProviderTransportErrorDoesNotExposeCredentials(t *testing.T) {
	t.Parallel()
	_, err := generateProviderSuggestions(suggestionConfig{provider: "openai", apiKey: "secret-key", model: "test-model", baseURL: "https://example.com"}, "", 1, nil, providerTransportError{})
	if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "sensitive-provider-detail") {
		t.Fatalf("unsafe or missing transport error: %v", err)
	}
}

// TestCompatibleSuggestionsRejectRefusalAndTruncation verifies a tool result is
// rejected when its response also reports refusal or truncation.
func TestCompatibleSuggestionsRejectRefusalAndTruncation(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"choices":[{"finish_reason":"length","message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}]}}]}`,
		`{"choices":[{"message":{"refusal":"sensitive-provider-detail","tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}]}}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) }))
			t.Cleanup(srv.Close)
			got, err := GenerateDomainSuggestions("test-key", "", 1, "test-model", srv.URL, nil)
			if err == nil || got != nil || strings.Contains(err.Error(), "sensitive-provider-detail") {
				t.Fatalf("unsafe response accepted: %#v, %v", got, err)
			}
		})
	}
}

// TestCompatibleSuggestionModelToolRequirements verifies Luna disables reasoning
// for tool calls while custom models receive no reasoning override.
func TestCompatibleSuggestionModelToolRequirements(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model, reasoning string
	}{
		{"gpt-6-luna", "none"},
		{defaultOpenAIModel, ""},
		{"custom-model", ""},
	} {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ReasoningEffort string `json:"reasoning_effort"`
					ToolChoice      struct {
						Type     string `json:"type"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ReasoningEffort != tc.reasoning || body.ToolChoice.Type != "function" || body.ToolChoice.Function.Name != functionName {
					t.Errorf("incorrect compatibility tool request for %s: %#v, %v", tc.model, body, err)
				}
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}]}}]}`)
			}))
			t.Cleanup(srv.Close)
			got, err := GenerateDomainSuggestions("test-key", "", 1, tc.model, srv.URL, nil)
			if err != nil || len(got) != 1 || got[0].Domain != "a.com" {
				t.Fatalf("suggestions = %#v, error = %v", got, err)
			}
		})
	}
}

// TestCompatibleSuggestionFinishReasons accepts complete compatibility responses
// while requiring one valid suggestion tool call, including for the stop reason.
func TestCompatibleSuggestionFinishReasons(t *testing.T) {
	t.Parallel()
	const validTool = `{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}`
	for _, tc := range []struct {
		name, reason, toolCalls, wantError string
	}{
		{"stop with valid tool", "stop", "[" + validTool + "]", ""},
		{"tool_calls with valid tool", "tool_calls", "[" + validTool + "]", ""},
		{"omitted reason with valid tool", "", "[" + validTool + "]", ""},
		{"stop without tool", "stop", "[]", "no tool calls"},
		{"stop with wrong tool", "stop", `[{"function":{"name":"other_tool","arguments":"{}"}}]`, "unexpected suggestion tool"},
		{"stop with multiple tools", "stop", "[" + validTool + "," + validTool + "]", "exactly one"},
		{"stop with malformed arguments", "stop", `[{"function":{"name":"suggest_domains","arguments":"not-json"}}]`, "unmarshal structured output"},
		{"truncated tool", "length", "[" + validTool + "]", "did not complete"},
		{"filtered tool", "content_filter", "[" + validTool + "]", "did not complete"},
		{"unknown finish reason", "unknown", "[" + validTool + "]", "did not complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"choices":[{"finish_reason":%q,"message":{"tool_calls":%s}}]}`, tc.reason, tc.toolCalls)
			}))
			t.Cleanup(srv.Close)
			got, err := GenerateDomainSuggestions("test-key", "", 1, "test-model", srv.URL, nil)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || got != nil {
					t.Fatalf("suggestions=%#v error=%v; want %q", got, err, tc.wantError)
				}
			} else if err != nil || len(got) != 1 || got[0].Domain != "a.com" {
				t.Fatalf("complete tool call rejected: suggestions=%#v error=%v", got, err)
			}
		})
	}
}
