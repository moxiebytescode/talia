package talia

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestGenerateDomainSuggestionsValidatesToolAndEndpoint verifies request method,
// authorization, trailing-slash handling, and rejection of an unexpected tool.
func TestGenerateDomainSuggestionsValidatesToolAndEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, tool, suffix string
		wantErr            bool
	}{
		{"expected tool", "suggest_domains", "", false},
		{"unexpected tool", "other_tool", "", true},
		{"trailing slash", "suggest_domains", "/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing suggestion request method or authorization")
				}
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":%q,"arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}]}}]}`, tc.tool)
			}))
			t.Cleanup(srv.Close)
			got, err := GenerateDomainSuggestions("key", "", 1, "test-model", srv.URL+tc.suffix, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("suggestions = %#v, error = %v", got, err)
			}
			if !tc.wantErr && (len(got) != 1 || got[0].Domain != "a.com") {
				t.Fatalf("suggestions = %#v", got)
			}
		})
	}
}

// TestRunCLISuggestionBatchFailures verifies partial successes are preserved
// and deduplicated, while total failure leaves the original domain file intact.
func TestRunCLISuggestionBatchFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int64
		wantCode int
	}{
		{"all succeed", 0, 0},
		{"partial failure keeps suggestions", 1, 0},
		{"all fail preserves file", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "key")
			t.Setenv("TALIA_LIGHTSPEED", "")
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) <= tc.failures {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"new.com\"}]}"}}]}}]}`)
			}))
			t.Cleanup(srv.Close)
			path := filepath.Join(t.TempDir(), "domains.json")
			const original = `{"available":[{"domain":"old.com","reason":"NO_MATCH"}]}`
			writeTestFile(t, path, original)
			var code int
			_, stderr := captureOutput(t, func() {
				code = RunCLI([]string{"--suggest=1", "--suggest-parallel=3", "--no-verify", "--api-base=" + srv.URL, path})
			})
			if code != tc.wantCode || requests.Load() != 3 {
				t.Fatalf("exit = %d, requests = %d, stderr = %q", code, requests.Load(), stderr)
			}
			if tc.failures == 3 {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != original {
					t.Fatalf("all failures changed domain file: %q, %v", raw, err)
				}
			} else {
				got := readGroupedFile(t, path)
				if len(got.Available) != 1 || len(got.Unverified) != 1 || got.Unverified[0].Domain != "new.com" {
					t.Fatalf("partial successes not preserved or deduplicated: %#v", got)
				}
				if tc.failures > 0 && !strings.Contains(stderr, "Warning: some requests failed") {
					t.Fatalf("missing partial failure warning: %q", stderr)
				}
			}
		})
	}
}

// TestGenerateDomainSuggestionsSuccess verifies we parse suggestions correctly.
func TestGenerateDomainSuggestionsSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"a.com\"}]}"}}]}}]}`)
	}))
	defer srv.Close()

	got, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err != nil {
		t.Fatalf("generateSuggestions returned error: %v", err)
	}
	if len(got) != 1 || got[0].Domain != "a.com" {
		t.Fatalf("unexpected suggestions: %+v", got)
	}
}

// TestGenerateDomainSuggestionsHTTPError verifies non-success HTTP responses
// are returned as suggestion errors.
func TestGenerateDomainSuggestionsHTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

// TestRunCLISuggest verifies the CLI writes returned suggestions as unverified
// records through the configured local HTTP endpoint.
func TestRunCLISuggest(t *testing.T) {
	// Environment variables are process-wide, so this CLI test is not parallel.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"b.com\"}]}"}}]}}]}`)
	}))
	defer srv.Close()

	tmp, err := os.CreateTemp("", "sugg_*.json")
	if err != nil {
		t.Fatal(err)
	}
	err = tmp.Close()
	if err != nil {
		t.Fatalf("tmp.Close() error: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	t.Setenv("OPENAI_API_KEY", "key")

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{"--suggest=1", "--api-base=" + srv.URL, tmp.Name()})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	// Check for success message
	if !strings.Contains(stdout, "suggestions total") {
		t.Errorf("missing success message: %s", stdout)
	}

	raw, _ := os.ReadFile(tmp.Name())
	var out ExtendedGroupedData
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(out.Unverified) != 1 || out.Unverified[0].Domain != "b.com" {
		t.Fatalf("unexpected file contents: %+v", out)
	}
}

func TestGenerateDomainSuggestionsNoAPIKey(t *testing.T) {
	t.Parallel()
	_, err := generateSuggestions("", "", 1, "gpt-4o", http.DefaultClient, "", nil)
	if err == nil || err.Error() != "OPENAI_API_KEY is not set" {
		t.Fatalf("expected OPENAI_API_KEY error, got %v", err)
	}
}

type errClient struct{}

func (errClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("boom")
}

func TestGenerateDomainSuggestionsRequestError(t *testing.T) {
	t.Parallel()
	_, err := generateSuggestions("key", "", 1, "gpt-4o", errClient{}, "http://localhost", nil)
	if err == nil || !strings.Contains(err.Error(), "openai request") {
		t.Fatalf("expected openai request error, got %v", err)
	}
}

// TestGenerateDomainSuggestionsDecodeError verifies malformed HTTP response JSON
// is reported before tool arguments can be read.
func TestGenerateDomainSuggestionsDecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()
	_, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// TestGenerateDomainSuggestionsNoChoices verifies a response without choices
// cannot be mistaken for a successful suggestion list.
func TestGenerateDomainSuggestionsNoChoices(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	_, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("expected no choices error, got %v", err)
	}
}

// TestGenerateDomainSuggestionsNoToolCalls verifies the selected choice must
// contain a tool call rather than ordinary message text.
func TestGenerateDomainSuggestionsNoToolCalls(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[]}}]}`)
	}))
	defer srv.Close()
	_, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "no tool calls") {
		t.Fatalf("expected no tool calls error, got %v", err)
	}
}

// TestGenerateDomainSuggestionsUnmarshalError verifies malformed tool arguments
// are returned as structured-output errors.
func TestGenerateDomainSuggestionsUnmarshalError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"not-json"}}]}}]}`)
	}))
	defer srv.Close()
	_, err := GenerateDomainSuggestions("key", "", 1, "gpt-4o", srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "unmarshal structured output") {
		t.Fatalf("expected unmarshal error, got %v", err)
	}
}

func TestWriteSuggestionsFile_Error(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "dir")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemoveAll(t, dir)
	err = writeSuggestionsFile(dir, []DomainRecord{{Domain: "a.com"}})
	if err == nil {
		t.Fatal("expected error writing to directory, got nil")
	}
}

func TestCleanTextFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")

	content := "example.com\nINVALID DOMAIN.com\nexample.com\ntest123.com\n\n# comment\n-bad-.com\ntest123.com\ngood-name.com\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	removed, err := cleanTextFile(path)
	if err != nil {
		t.Fatalf("cleanTextFile error: %v", err)
	}

	// Should have removed 2 invalid domains
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %d: %v", len(removed), removed)
	}

	// Read back and verify
	result, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(result)), "\n")
	// Should have 3 unique valid domains
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

// TestNormalizeDomain specifies canonical .com spellings and rejection of
// invalid labels, subdomains, unsupported suffixes, and overlong names.
func TestNormalizeDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		// Valid domains
		{"example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"  example.com  ", "example.com"},
		{"my-domain.com", "my-domain.com"},
		{"test123.com", "test123.com"},
		{"a1b2c3.com", "a1b2c3.com"},

		// DNS label length limit
		{strings.Repeat("a", 63) + ".com", strings.Repeat("a", 63) + ".com"},
		{strings.Repeat("a", 64) + ".com", ""},

		// Repeated .com suffixes
		{"example.com.com", "example.com"},
		{"example.com.com.com", "example.com"},
		{"Lordboard..com.com.com", "lordboard.com"},

		// Double dots
		{"example..com", "example.com"},
		{"ex..am..ple.com", ""}, // becomes ex.am.ple.com which has subdomains, invalid

		// Invalid: wrong TLD
		{"example.net", ""},
		{"example.org", ""},

		// Invalid: no name
		{".com", ""},
		{"", ""},

		// Invalid: subdomains (not simple name.com)
		{"sub.example.com", ""},
		{"a.b.com", ""},

		// Invalid: spaces in domain name
		{"flora board.com", ""},
		{"hello board.com", ""},
		{"my domain.com", ""},

		// Invalid: special characters
		{"test_domain.com", ""},
		{"test@domain.com", ""},
		{"test!domain.com", ""},
		{"test#domain.com", ""},

		// Invalid: hyphen at start or end
		{"-example.com", ""},
		{"example-.com", ""},
		{"-test-.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeDomain(tt.input)
			if got != tt.want {
				t.Errorf("normalizeDomain(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// suggestAPICalls records the requests received by a fake suggestion API.
type suggestAPICalls struct {
	mu     sync.Mutex
	models []string
}

func (c *suggestAPICalls) record(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = append(c.models, model)
}

func (c *suggestAPICalls) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

// runSuggestCLI runs RunCLI on a domain file holding fileContent, against a fake suggestion
// API with the given env vars set. It returns the exit code and the model named in each
// request the API received.
func runSuggestCLI(t *testing.T, env map[string]string, fileContent string, args ...string) (int, []string) {
	t.Helper()
	calls := &suggestAPICalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		calls.record(payload.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"c.com\"}]}"}}]}}]}`)
	}))
	t.Cleanup(srv.Close)

	for _, k := range []string{"TALIA_MODEL", "TALIA_SUGGEST", "TALIA_SUGGEST_PARALLEL", "WHOIS_SERVER", "TALIA_LIGHTSPEED"} {
		t.Setenv(k, env[k])
	}
	t.Setenv("OPENAI_API_KEY", "key")

	file := filepath.Join(t.TempDir(), "suggestions.json")
	if fileContent != "" {
		if err := os.WriteFile(file, []byte(fileContent), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var code int
	captureOutput(t, func() { code = RunCLI(append(append([]string{"--api-base=" + srv.URL}, args...), file)) })
	return code, calls.snapshot()
}

// TestRunCLI_ExplicitFlagsBeatEnvFallbacks verifies explicit values, including
// defaults and zero, take precedence over suggestion environment fallbacks.
func TestRunCLI_ExplicitFlagsBeatEnvFallbacks(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		fileContent  string
		args         []string
		wantCode     int
		wantRequests int
		wantModel    string
	}{
		{"explicit custom model", map[string]string{"TALIA_MODEL": "env-model"}, "",
			[]string{"--suggest=1", "--model=my-model"}, 0, 1, "my-model"},
		{"explicit default model", map[string]string{"TALIA_MODEL": "env-model"}, "",
			[]string{"--suggest=1", "--model=" + defaultCompatibleModel}, 0, 1, defaultCompatibleModel},
		{"model from env", map[string]string{"TALIA_MODEL": "env-model"}, "",
			[]string{"--suggest=1"}, 0, 1, "env-model"},
		{"explicit default parallel", map[string]string{"TALIA_SUGGEST_PARALLEL": "4"}, "",
			[]string{"--suggest=1", "--suggest-parallel=1"}, 0, 1, defaultCompatibleModel},
		{"parallel from env", map[string]string{"TALIA_SUGGEST_PARALLEL": "4"}, "",
			[]string{"--suggest=1"}, 0, 4, defaultCompatibleModel},
		// With no suggestions requested, Talia goes on to check domains and fails
		// because no WHOIS server is configured.
		{"explicit zero suggest", map[string]string{"TALIA_SUGGEST": "10"}, "",
			[]string{"--suggest=0"}, 1, 0, ""},
		{"suggest from env", map[string]string{"TALIA_SUGGEST": "1"}, "",
			nil, 0, 1, defaultCompatibleModel},
		{"env suggest ignored with unverified domains", map[string]string{"TALIA_SUGGEST": "1"},
			`{"unverified":[{"domain":"waiting.com"}]}`, nil, 1, 0, ""},
		{"invalid lightspeed stops before suggesting", map[string]string{"TALIA_LIGHTSPEED": "fast"}, "",
			[]string{"--suggest=1"}, 1, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, models := runSuggestCLI(t, tc.env, tc.fileContent, tc.args...)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if len(models) != tc.wantRequests {
				t.Fatalf("API received %d requests, want %d", len(models), tc.wantRequests)
			}
			for _, m := range models {
				if m != tc.wantModel {
					t.Errorf("request used model %q, want %q", m, tc.wantModel)
				}
			}
		})
	}
}
