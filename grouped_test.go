package talia

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRunCLIGroupedOutputKeepsLastDuplicateCheck verifies repeated normalized
// domains retain the final input-ordered check before results enter buckets,
// across both input shapes and both in-place and separate grouped destinations.
func TestRunCLIGroupedOutputKeepsLastDuplicateCheck(t *testing.T) {
	for _, mode := range []struct {
		name              string
		grouped, separate bool
	}{
		{"array in place", false, false},
		{"array separate", false, true},
		{"grouped in place", true, false},
		{"grouped separate", true, true},
	} {
		for _, laterAvailable := range []bool{true, false} {
			outcome := "later error"
			if laterAvailable {
				outcome = "later available"
			}
			t.Run(mode.name+"/"+outcome, func(t *testing.T) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				go func() {
					for {
						conn, err := ln.Accept()
						if err != nil {
							return
						}
						go func(c net.Conn) {
							defer func() { _ = c.Close() }()
							domain, _ := bufio.NewReader(c).ReadString('\n')
							if strings.TrimSpace(domain) == "example.com" {
								_, _ = io.WriteString(c, "No match for example.com")
							}
						}(conn)
					}
				}()
				records := []DomainRecord{{Domain: "example.com"}, {Domain: "Example.com"}}
				want := ExtendedGroupedData{Unavailable: []GroupedDomain{{Domain: "Example.com", Reason: ReasonError, Log: "Error: empty WHOIS response"}}}
				if laterAvailable {
					records[0], records[1] = records[1], records[0]
					want = ExtendedGroupedData{Available: []GroupedDomain{{Domain: "example.com", Reason: ReasonNoMatch}}}
				}
				var input any = records
				if mode.grouped {
					input = ExtendedGroupedData{
						Unavailable: []GroupedDomain{{Domain: "EXAMPLE.COM", Reason: ReasonTaken, Log: "stale"}},
						Unverified:  records,
					}
				}
				path := writeGroupedFixture(t, input)
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				output := path
				args := []string{"--whois=" + ln.Addr().String(), "--sleep=0", "--suggest=0", "--lightspeed=2", "--grouped-output"}
				if mode.separate {
					output = filepath.Join(t.TempDir(), "output.json")
					args = append(args, "--output-file="+output)
				}
				if code := RunCLI(append(args, path)); code != 0 {
					t.Fatalf("exit = %d", code)
				}
				if got := readGroupedFile(t, output); !reflect.DeepEqual(got, want) {
					t.Fatalf("last check was lost: %#v, want %#v", got, want)
				}
				if mode.separate {
					raw, err := os.ReadFile(path)
					if err != nil || string(raw) != string(original) {
						t.Fatalf("separate output changed input: %q, %v", raw, err)
					}
				}
			})
		}
	}
}

// TestRunCLIGroupedRechecksReplaceStaleResults verifies that repeated unverified
// entries produce one fresh result and replace a conflicting stale bucket.
func TestRunCLIGroupedRechecksReplaceStaleResults(t *testing.T) {
	server, _ := startCountingWhois(t)
	path := writeGroupedFixture(t, ExtendedGroupedData{
		Unavailable: []GroupedDomain{{Domain: "again.com", Reason: ReasonTaken, Log: "stale"}},
		Unverified:  []DomainRecord{{Domain: "again.com"}, {Domain: "again.com"}},
	})
	if code := RunCLI([]string{"--whois=" + server, "--sleep=0", path}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got := readGroupedFile(t, path)
	want := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "again.com", Reason: ReasonNoMatch}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh checks did not replace stale results: %#v", got)
	}
}

// TestWriteGroupedFileUsesNormalizedIdentity verifies spellings share a result
// identity, fresh checks remove equivalent unverified entries, and unrelated
// records keep their order, spelling, and metadata.
func TestWriteGroupedFileUsesNormalizedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing ExtendedGroupedData
		newest   GroupedData
		want     ExtendedGroupedData
	}{
		{
			name: "equivalent spellings update and move buckets",
			existing: ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "KEEP.COM", Reason: ReasonNoMatch, Log: "untouched"},
					{Domain: " Example.COM.com ", Reason: ReasonNoMatch, Log: "stale"},
					{Domain: "move.com", Reason: ReasonNoMatch},
				},
				Unavailable: []GroupedDomain{
					{Domain: "b.com", Reason: ReasonTaken},
					{Domain: " RETURN.COM ", Reason: ReasonTaken},
				},
				Unverified: []DomainRecord{
					{Domain: "EXAMPLE..com"}, {Domain: " MOVE.COM "},
					{Domain: "return.com.com"}, {Domain: "waiting.com", Log: "keep"},
				},
			},
			newest: GroupedData{
				Available: []GroupedDomain{
					{Domain: "example.com", Reason: ReasonNoMatch, Log: "fresh"},
					{Domain: "Return.COM", Reason: ReasonNoMatch, Log: "returned"},
				},
				Unavailable: []GroupedDomain{{Domain: " Move.COM ", Reason: ReasonTaken, Log: "fresh taken"}},
			},
			want: ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "KEEP.COM", Reason: ReasonNoMatch, Log: "untouched"},
					{Domain: "example.com", Reason: ReasonNoMatch, Log: "fresh"},
					{Domain: "Return.COM", Reason: ReasonNoMatch, Log: "returned"},
				},
				Unavailable: []GroupedDomain{
					{Domain: "b.com", Reason: ReasonTaken},
					{Domain: " Move.COM ", Reason: ReasonTaken, Log: "fresh taken"},
				},
				Unverified: []DomainRecord{{Domain: "waiting.com", Log: "keep"}},
			},
		},
		{
			name: "invalid domains retain distinct raw identities",
			existing: ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "bad.io", Reason: ReasonNoMatch},
					{Domain: "other.io", Reason: ReasonNoMatch, Log: "keep"},
				},
				Unverified: []DomainRecord{{Domain: "bad.io"}, {Domain: "waiting.io"}},
			},
			newest: GroupedData{Unavailable: []GroupedDomain{{Domain: "bad.io", Reason: ReasonError, Log: "fresh error"}}},
			want: ExtendedGroupedData{
				Available:   []GroupedDomain{{Domain: "other.io", Reason: ReasonNoMatch, Log: "keep"}},
				Unavailable: []GroupedDomain{{Domain: "bad.io", Reason: ReasonError, Log: "fresh error"}},
				Unverified:  []DomainRecord{{Domain: "waiting.io"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeGroupedFixture(t, tc.existing)
			if err := WriteGroupedFile(path, tc.newest); err != nil {
				t.Fatal(err)
			}
			if got := readGroupedFile(t, path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("grouped file = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// writeGroupedFixture writes v as JSON to a fresh file and returns its path.
func writeGroupedFixture(t *testing.T, v any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grouped.json")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// readGroupedFile parses the grouped file at path.
func readGroupedFile(t *testing.T, path string) ExtendedGroupedData {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read grouped file: %v", err)
	}
	var gd ExtendedGroupedData
	if err := json.Unmarshal(raw, &gd); err != nil {
		t.Fatalf("parse grouped file: %v", err)
	}
	return gd
}

// readGroupedDomains returns the domain names in each bucket of a grouped file.
func readGroupedDomains(t *testing.T, path string) (available, unavailable []string) {
	t.Helper()
	gd := readGroupedFile(t, path)
	for _, d := range gd.Available {
		available = append(available, d.Domain)
	}
	for _, d := range gd.Unavailable {
		unavailable = append(unavailable, d.Domain)
	}
	return available, unavailable
}

func TestWriteGroupedFile_KeepsExistingOrderAndAppendsNew(t *testing.T) {
	path := writeGroupedFixture(t, GroupedData{
		Available: []GroupedDomain{
			{Domain: "zeta.com", Reason: ReasonNoMatch},
			{Domain: "alpha.com", Reason: ReasonNoMatch},
			{Domain: "mike.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "yankee.com", Reason: ReasonTaken},
			{Domain: "bravo.com", Reason: ReasonTaken},
		},
	})

	newest := GroupedData{
		Available: []GroupedDomain{
			{Domain: "kilo.com", Reason: ReasonNoMatch},
			{Domain: "delta.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "xray.com", Reason: ReasonTaken},
			{Domain: "charlie.com", Reason: ReasonTaken},
		},
	}
	if err := WriteGroupedFile(path, newest); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	available, unavailable := readGroupedDomains(t, path)
	wantAvailable := []string{"zeta.com", "alpha.com", "mike.com", "kilo.com", "delta.com"}
	wantUnavailable := []string{"yankee.com", "bravo.com", "xray.com", "charlie.com"}
	if !reflect.DeepEqual(available, wantAvailable) {
		t.Errorf("available = %v, want %v", available, wantAvailable)
	}
	if !reflect.DeepEqual(unavailable, wantUnavailable) {
		t.Errorf("unavailable = %v, want %v", unavailable, wantUnavailable)
	}
}

func TestWriteGroupedFile_RecheckInSameBucketUpdatesInPlace(t *testing.T) {
	path := writeGroupedFixture(t, GroupedData{
		Available: []GroupedDomain{},
		Unavailable: []GroupedDomain{
			{Domain: "first.com", Reason: ReasonTaken},
			{Domain: "flaky.com", Reason: ReasonError, Log: "Error: timeout"},
			{Domain: "last.com", Reason: ReasonTaken},
		},
	})

	newest := GroupedData{
		Unavailable: []GroupedDomain{{Domain: "flaky.com", Reason: ReasonTaken}},
	}
	if err := WriteGroupedFile(path, newest); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	got := readGroupedFile(t, path)
	want := []GroupedDomain{
		{Domain: "first.com", Reason: ReasonTaken},
		{Domain: "flaky.com", Reason: ReasonTaken},
		{Domain: "last.com", Reason: ReasonTaken},
	}
	if !reflect.DeepEqual(got.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", got.Unavailable, want)
	}
}

func TestWriteGroupedFile_DomainChangingBucketMovesToEnd(t *testing.T) {
	path := writeGroupedFixture(t, GroupedData{
		Available: []GroupedDomain{
			{Domain: "keep.com", Reason: ReasonNoMatch},
			{Domain: "lapsed.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "dropped.com", Reason: ReasonTaken},
			{Domain: "held.com", Reason: ReasonTaken},
		},
	})

	newest := GroupedData{
		Available:   []GroupedDomain{{Domain: "dropped.com", Reason: ReasonNoMatch}},
		Unavailable: []GroupedDomain{{Domain: "lapsed.com", Reason: ReasonTaken}},
	}
	if err := WriteGroupedFile(path, newest); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	available, unavailable := readGroupedDomains(t, path)
	wantAvailable := []string{"keep.com", "dropped.com"}
	wantUnavailable := []string{"held.com", "lapsed.com"}
	if !reflect.DeepEqual(available, wantAvailable) {
		t.Errorf("available = %v, want %v", available, wantAvailable)
	}
	if !reflect.DeepEqual(unavailable, wantUnavailable) {
		t.Errorf("unavailable = %v, want %v", unavailable, wantUnavailable)
	}
}

func TestWriteGroupedFile_RepeatedWritesAreByteIdentical(t *testing.T) {
	var newest GroupedData
	for _, d := range []string{"h.com", "c.com", "f.com", "a.com", "g.com", "b.com", "e.com", "d.com"} {
		newest.Available = append(newest.Available, GroupedDomain{Domain: d, Reason: ReasonNoMatch})
	}
	path := filepath.Join(t.TempDir(), "out.json")

	var previous []byte
	for i := range 5 {
		if err := WriteGroupedFile(path, newest); err != nil {
			t.Fatalf("WriteGroupedFile #%d: %v", i, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read #%d: %v", i, err)
		}
		if previous != nil && string(raw) != string(previous) {
			t.Fatalf("write #%d changed the file:\nbefore:\n%s\nafter:\n%s", i, previous, raw)
		}
		previous = raw
	}
}

func TestWriteGroupedFile_KeepsUnverifiedDomains(t *testing.T) {
	path := writeGroupedFixture(t, ExtendedGroupedData{
		Available: []GroupedDomain{{Domain: "old.com", Reason: ReasonNoMatch}},
		Unverified: []DomainRecord{
			{Domain: "waiting1.com"},
			{Domain: "checked.com"},
			{Domain: "waiting2.com", Log: "suggested"},
		},
	})

	newest := GroupedData{
		Unavailable: []GroupedDomain{{Domain: "checked.com", Reason: ReasonTaken}},
	}
	if err := WriteGroupedFile(path, newest); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	got := readGroupedFile(t, path)
	wantUnverified := []DomainRecord{{Domain: "waiting1.com"}, {Domain: "waiting2.com", Log: "suggested"}}
	if !reflect.DeepEqual(got.Unverified, wantUnverified) {
		t.Errorf("unverified = %+v, want %+v", got.Unverified, wantUnverified)
	}
	wantUnavailable := []GroupedDomain{{Domain: "checked.com", Reason: ReasonTaken}}
	if !reflect.DeepEqual(got.Unavailable, wantUnavailable) {
		t.Errorf("unavailable = %+v, want %+v", got.Unavailable, wantUnavailable)
	}
	wantAvailable := []GroupedDomain{{Domain: "old.com", Reason: ReasonNoMatch}}
	if !reflect.DeepEqual(got.Available, wantAvailable) {
		t.Errorf("available = %+v, want %+v", got.Available, wantAvailable)
	}
}

func TestWriteGroupedFile_OmitsEmptyUnverified(t *testing.T) {
	path := writeGroupedFixture(t, ExtendedGroupedData{
		Unverified: []DomainRecord{{Domain: "checked.com"}},
	})

	newest := GroupedData{
		Available: []GroupedDomain{{Domain: "checked.com", Reason: ReasonNoMatch}},
	}
	if err := WriteGroupedFile(path, newest); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read grouped file: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("parse grouped file: %v", err)
	}
	if _, ok := keys["unverified"]; ok {
		t.Errorf("expected no unverified key, got file:\n%s", raw)
	}
	for _, k := range []string{"available", "unavailable"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("expected %q key, got file:\n%s", k, raw)
		}
	}
}
