package talia

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// TestRunCLISuggestPreservesArrayDomains verifies that suggestions retain array
// records, metadata, and exclusions when converting to a grouped file.
func TestRunCLISuggestPreservesArrayDomains(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("WHOIS_SERVER", "")
	t.Setenv("TALIA_SUGGEST_PARALLEL", "")
	var exclusions string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) > 1 {
			exclusions = request.Messages[1].Content
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"suggest_domains","arguments":"{\"unverified\":[{\"domain\":\"new.com\"}]}"}}]}}]}`)
	}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "domains.json")
	writeTestFile(t, path, `[{"domain":"old.com","available":true,"reason":"NO_MATCH","log":"saved"},{"domain":"waiting.com"}]`)
	if code := RunCLI([]string{"--suggest=1", "--no-verify", "--api-base=" + srv.URL, path}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got := readGroupedFile(t, path)
	want := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: "old.com", Reason: ReasonNoMatch, Log: "saved"}},
		Unverified: []DomainRecord{{Domain: "waiting.com"}, {Domain: "new.com"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domain file = %#v, want %#v", got, want)
	}
	if !strings.Contains(exclusions, "old.com") || !strings.Contains(exclusions, "waiting.com") {
		t.Fatalf("exclusion list missing array domains: %q", exclusions)
	}
}

// writeTestFile creates a domain-file fixture and fails immediately on write errors.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestRunCLISuggestRejectsInvalidDomainFiles verifies that malformed or unsupported
// JSON causes neither suggestion requests nor changes to the original bytes.
func TestRunCLISuggestRejectsInvalidDomainFiles(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "key")
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(srv.Close)
	for _, content := range []string{`{"available":[`, `null`, `"hello"`, `{"unverified":42}`, `{"message":"not a domain file"}`, `[{"domain":42}]`} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "domains.json")
			writeTestFile(t, path, content)
			if code := RunCLI([]string{"--suggest=1", "--api-base=" + srv.URL, path}); code == 0 {
				t.Fatal("expected invalid domain file error")
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != content {
				t.Fatalf("domain file changed: %q, %v", raw, err)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid files triggered %d suggestion requests", got)
	}
}

// TestRunCLICleanPreservesArrayFormatAndMetadata verifies normalized first-occurrence
// deduplication without losing the array shape or retained check metadata.
func TestRunCLICleanPreservesArrayFormatAndMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domains.json")
	writeTestFile(t, path, `[{"domain":" Example.COM.com ","available":true,"reason":"NO_MATCH","log":"keep"},{"domain":"example.com"},{"domain":"bad.io"}]`)
	if code := RunCLI([]string{"--clean", path}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []DomainRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("array format was lost: %v", err)
	}
	want := []DomainRecord{{Domain: "example.com", Available: true, Reason: ReasonNoMatch, Log: "keep"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned array = %#v, want %#v", got, want)
	}
}

// TestRunCLICleanRejectsMalformedJSONWithoutWriting prevents malformed JSON from
// being misidentified as a plain text list and overwritten.
func TestRunCLICleanRejectsMalformedJSONWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domains.json")
	const content = "{\n\"unverified\": [\n"
	writeTestFile(t, path, content)
	if code := RunCLI([]string{"--clean", path}); code == 0 {
		t.Fatal("malformed JSON was treated as a text list")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatalf("malformed file changed: %q, %v", got, err)
	}
}

// TestRunCLIMergeAndExportMixedDomainFiles verifies first-write-wins across both
// JSON shapes and export of available array records in their stored spelling.
func TestRunCLIMergeAndExportMixedDomainFiles(t *testing.T) {
	dir := t.TempDir()
	array, grouped := filepath.Join(dir, "array.json"), filepath.Join(dir, "grouped.json")
	merged, exported := filepath.Join(dir, "merged.json"), filepath.Join(dir, "available.txt")
	writeTestFile(t, array, `[{"domain":" First.COM ","available":true,"reason":"NO_MATCH","log":"first wins"},{"domain":"waiting.com"},{"domain":"taken.com","reason":"TAKEN"}]`)
	writeTestFile(t, grouped, `{"unavailable":[{"domain":"first.com","reason":"TAKEN","log":"later"}],"unverified":[{"domain":"new.com"}]}`)
	if code := RunCLI([]string{"--merge", "-o", merged, array, grouped}); code != 0 {
		t.Fatalf("merge exit = %d", code)
	}
	want := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "first.com", Reason: ReasonNoMatch, Log: "first wins"}},
		Unavailable: []GroupedDomain{{Domain: "taken.com", Reason: ReasonTaken}},
		Unverified:  []DomainRecord{{Domain: "waiting.com"}, {Domain: "new.com"}},
	}
	if got := readGroupedFile(t, merged); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %#v, want %#v", got, want)
	}
	if code := RunCLI([]string{"--export-available=" + exported, array}); code != 0 {
		t.Fatalf("array export exit = %d", code)
	}
	raw, err := os.ReadFile(exported)
	if err != nil || string(raw) != " First.COM \n" {
		t.Fatalf("export did not preserve available name: %q, %v", raw, err)
	}
}

// TestRunCLIMergeArrayKeepsFirstOccurrenceAcrossBuckets verifies that a later
// available record cannot displace an earlier unavailable or unverified record.
func TestRunCLIMergeArrayKeepsFirstOccurrenceAcrossBuckets(t *testing.T) {
	for _, tc := range []struct {
		name, first string
		want        ExtendedGroupedData
	}{
		{"unavailable first", `{"domain":" SAME.COM ","reason":"TAKEN","log":"first"}`,
			ExtendedGroupedData{Unavailable: []GroupedDomain{{Domain: "same.com", Reason: ReasonTaken, Log: "first"}}}},
		{"unverified first", `{"domain":"same.com"}`,
			ExtendedGroupedData{Unverified: []DomainRecord{{Domain: "same.com"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "domains.json")
			output := filepath.Join(t.TempDir(), "merged.json")
			writeTestFile(t, path, "["+tc.first+`,{"domain":"same.com","available":true,"reason":"NO_MATCH","log":"later"}]`)
			if code := RunCLI([]string{"--merge", "-o", output, path}); code != 0 {
				t.Fatalf("merge exit = %d", code)
			}
			if got := readGroupedFile(t, output); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("first occurrence lost: %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestWriteSuggestionsFileNormalizesIdentity verifies normalized deduplication
// while preserving existing records and discarding suggestion check metadata.
func TestWriteSuggestionsFileNormalizesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domains.json")
	writeTestFile(t, path, `{"available":[{"domain":" Example.COM.com ","reason":"NO_MATCH","log":"keep"}],"unverified":[{"domain":"bad.io"}]}`)
	if err := writeSuggestionsFile(path, []DomainRecord{{Domain: "example.com"}, {Domain: "new.com", Available: true, Reason: ReasonNoMatch, Log: "discard"}, {Domain: "bad.io"}}); err != nil {
		t.Fatal(err)
	}
	want := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: " Example.COM.com ", Reason: ReasonNoMatch, Log: "keep"}},
		Unverified: []DomainRecord{{Domain: "bad.io"}, {Domain: "new.com"}},
	}
	if got := readGroupedFile(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("suggestions = %#v, want %#v", got, want)
	}
}
