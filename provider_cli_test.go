package talia

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestRunCLIProviderSelection verifies flags and environment settings select the
// correct provider protocol, credentials, and model while preserving domain records.
func TestRunCLIProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		name, envProvider, flagProvider, envModel, flagModel string
		wantPath, wantModel, wantKey                         string
	}{
		{"native OpenAI", "", "openai", "", "", "/responses", defaultOpenAIModel, "openai-key"},
		{"native Anthropic", "", "anthropic", "", "", "/messages", defaultAnthropicModel, "anthropic-key"},
		{"provider from environment", "anthropic", "", "", "", "/messages", defaultAnthropicModel, "anthropic-key"},
		{"explicit provider wins", "anthropic", "openai", "", "", "/responses", defaultOpenAIModel, "openai-key"},
		{"model from environment", "anthropic", "", "custom-claude", "", "/messages", "custom-claude", "anthropic-key"},
		{"explicit model wins", "anthropic", "", "custom-claude", "flag-model", "/messages", "flag-model", "anthropic-key"},
		{"explicit default model wins", "anthropic", "", "custom-claude", defaultAnthropicModel, "/messages", defaultAnthropicModel, "anthropic-key"},
		{"legacy custom endpoint", "", "", "local-model", "", "/chat/completions", "local-model", "openai-key"},
		{"explicit compatibility", "anthropic", "openai-compatible", "", "local-model", "/chat/completions", "local-model", "openai-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderTestEnv(t)
			t.Setenv("TALIA_PROVIDER", tc.envProvider)
			t.Setenv("TALIA_MODEL", tc.envModel)
			t.Setenv("OPENAI_API_KEY", "openai-key")
			t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
			t.Setenv("ANTHROPIC_WORKSPACE_ID", "workspace-id")
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != tc.wantPath || r.Header.Get("Authorization") != "Bearer "+tc.wantKey {
					t.Errorf("wrong provider endpoint or credentials: path=%q", r.URL.Path)
				}
				if tc.wantPath == "/messages" && r.Header.Get("anthropic-workspace-id") != "workspace-id" {
					t.Error("missing configured Anthropic workspace")
				}
				var payload struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != tc.wantModel {
					t.Errorf("model=%q, want %q; decode error=%v", payload.Model, tc.wantModel, err)
				}
				writeProviderTestResponse(w, tc.wantPath, `{"unverified":[{"domain":"NEW.COM"},{"domain":"old.com"}]}`)
			}))
			t.Cleanup(srv.Close)
			path := filepath.Join(t.TempDir(), "domains.json")
			writeTestFile(t, path, `{"available":[{"domain":"old.com","reason":"NO_MATCH","log":"retained"}]}`)
			args := []string{"--suggest=2", "--no-verify", "--api-base=" + srv.URL}
			if tc.flagProvider != "" {
				args = append(args, "--provider="+tc.flagProvider)
			}
			if tc.flagModel != "" {
				args = append(args, "--model="+tc.flagModel)
			}
			var code int
			_, stderr := captureOutput(t, func() { code = RunCLI(append(args, path)) })
			if code != 0 || requests.Load() != 1 {
				t.Fatalf("code=%d requests=%d stderr=%q", code, requests.Load(), stderr)
			}
			got := readGroupedFile(t, path)
			if len(got.Available) != 1 || got.Available[0].Domain != "old.com" || got.Available[0].Log != "retained" || len(got.Unverified) != 1 || got.Unverified[0].Domain != "new.com" {
				t.Fatalf("suggestions did not preserve and merge the domain file: %#v", got)
			}
		})
	}
}

// clearProviderTestEnv isolates provider tests from shell configuration and restores
// the previous environment when the test completes.
func clearProviderTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TALIA_PROVIDER", "TALIA_MODEL", "OPENAI_API_KEY", "OPENAI_API_BASE", "ANTHROPIC_API_KEY", "ANTHROPIC_API_BASE", "ANTHROPIC_WORKSPACE_ID", "TALIA_SUGGEST", "TALIA_SUGGEST_PARALLEL", "TALIA_LIGHTSPEED", "WHOIS_SERVER"} {
		t.Setenv(key, "")
	}
}

// writeProviderTestResponse writes a successful response in the requested endpoint's
// wire format, including Anthropic thinking content before the structured text.
func writeProviderTestResponse(w http.ResponseWriter, path, suggestions string) {
	w.Header().Set("Content-Type", "application/json")
	switch path {
	case "/responses":
		_, _ = fmt.Fprintf(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, suggestions)
	case "/messages":
		_, _ = fmt.Fprintf(w, `{"type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":%q}]}`, suggestions)
	default:
		_, _ = fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":%q}}]}}]}`, suggestions)
	}
}

// TestSuggestionConfigDefaultsAndBasePrecedence verifies provider defaults,
// environment fallbacks, explicit base overrides, and official endpoint detection.
func TestSuggestionConfigDefaultsAndBasePrecedence(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "openai-compatible"} {
		t.Run(provider, func(t *testing.T) {
			clearProviderTestEnv(t)
			t.Setenv("OPENAI_API_KEY", "key")
			t.Setenv("ANTHROPIC_API_KEY", "key")
			wantBase, wantModel := defaultOpenAIBase, defaultOpenAIModel
			if provider == "openai-compatible" {
				wantModel = defaultCompatibleModel
			}
			if provider == "anthropic" {
				wantBase, wantModel = defaultAnthropicBase, defaultAnthropicModel
			}
			config, err := resolveSuggestionConfig(provider, "", "", true, false)
			if err != nil || config.baseURL != wantBase || config.model != wantModel {
				t.Fatalf("wrong provider defaults: base=%q model=%q error=%v", config.baseURL, config.model, err)
			}
			t.Setenv("OPENAI_API_BASE", "https://openai-env.invalid/v1")
			t.Setenv("ANTHROPIC_API_BASE", "https://anthropic-env.invalid/v1")
			wantBase = "https://openai-env.invalid/v1"
			if provider == "anthropic" {
				wantBase = "https://anthropic-env.invalid/v1"
			}
			config, err = resolveSuggestionConfig(provider, "", "", true, false)
			if err != nil || config.baseURL != wantBase {
				t.Fatalf("wrong provider environment: base=%q error=%v", config.baseURL, err)
			}
			config, err = resolveSuggestionConfig(provider, "", "https://flag.invalid/v1", true, false)
			if err != nil || config.baseURL != "https://flag.invalid/v1" {
				t.Fatalf("explicit base did not win: base=%q error=%v", config.baseURL, err)
			}
		})
	}
	t.Run("official base retains native OpenAI", func(t *testing.T) {
		clearProviderTestEnv(t)
		t.Setenv("OPENAI_API_KEY", "key")
		t.Setenv("OPENAI_API_BASE", defaultOpenAIBase+"/")
		config, err := resolveSuggestionConfig("", "", "", false, false)
		if err != nil || config.provider != "openai" {
			t.Fatalf("official endpoint selected provider=%q, error=%v", config.provider, err)
		}
	})
}

// TestRunCLIProviderErrorsPreserveFile verifies configuration and provider failures
// return an error without changing the existing domain file.
func TestRunCLIProviderErrorsPreserveFile(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body, wantError string
		args                            []string
	}{
		{"unknown provider", "unknown", "", "unknown suggestion provider", nil},
		{"missing Anthropic key", "anthropic", "", "ANTHROPIC_API_KEY is not set", nil},
		{"empty explicit model", "openai", "", "model must not be empty", []string{"--model="}},
		{"OpenAI incomplete", "openai", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "incomplete", nil},
		{"Anthropic truncation", "anthropic", `{"type":"message","stop_reason":"max_tokens","content":[{"type":"text","text":"{}"}]}`, "incomplete", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderTestEnv(t)
			t.Setenv("OPENAI_API_KEY", "key")
			if tc.name != "missing Anthropic key" {
				t.Setenv("ANTHROPIC_API_KEY", "key")
			}
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			path := filepath.Join(t.TempDir(), "domains.json")
			const original = `{"unverified":[{"domain":"keep.com"}]}`
			writeTestFile(t, path, original)
			args := append([]string{"--suggest=1", "--no-verify", "--provider=" + tc.provider, "--api-base=" + srv.URL}, tc.args...)
			var code int
			_, stderr := captureOutput(t, func() { code = RunCLI(append(args, path)) })
			if code != 1 || !strings.Contains(stderr, tc.wantError) {
				t.Fatalf("code=%d stderr=%q; want error %q", code, stderr, tc.wantError)
			}
			if tc.body == "" && requests.Load() != 0 {
				t.Fatalf("invalid configuration sent %d requests", requests.Load())
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != original {
				t.Fatalf("failed suggestions changed file: %q, %v", raw, err)
			}
		})
	}
}
