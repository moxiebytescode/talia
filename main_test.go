// Package talia contains tests for the Talia CLI library.
package talia

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// TestMain sets up the test environment to prevent tests from hitting real APIs.
func TestMain(m *testing.M) {
	// Disable .env file loading for all tests
	skipEnvFile = true
	// Clear any existing OpenAI API key to prevent accidental API calls
	_ = os.Unsetenv("OPENAI_API_KEY")
	_ = os.Unsetenv("OPENAI_API_BASE")
	os.Exit(m.Run())
}

// helperClose checks and reports Close errors.
func helperClose(t *testing.T, c io.Closer, ctx string) {
	if err := c.Close(); err != nil {
		if t != nil {
			t.Fatalf("%s: %v", ctx, err)
		}
		log.Printf("%s: %v", ctx, err)
	}
}

// helperRemove / helperRemoveAll wrap os.Remove / os.RemoveAll.
func helperRemove(t *testing.T, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", path, err)
	}
}

func helperRemoveAll(t *testing.T, path string) {
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("removeAll %s: %v", path, err)
	}
}

// captureOutput captures stdout/stderr during a function call.
func captureOutput(t *testing.T, fn func()) (string, string) {
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create stdout pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create stderr pipe: %v", err)
	}

	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr

	outCh := make(chan string)
	errCh := make(chan string)

	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, rOut) // pipe closes when test ends
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, rErr)
		errCh <- buf.String()
	}()

	fn()

	helperClose(t, wOut, "close stdout pipe")
	helperClose(t, wErr, "close stderr pipe")
	os.Stdout, os.Stderr = oldStdout, oldStderr
	return <-outCh, <-errCh
}

// TestCheckDomainAvailability covers basic WHOIS availability checks
func TestCheckDomainAvailability(t *testing.T) {
	cases := []struct {
		name          string
		serverHandler func(net.Conn)
		wantAvailable bool
		wantReason    AvailabilityReason
		wantErr       bool
	}{
		{
			name: "No match => available=TRUE, reason=NO_MATCH",
			serverHandler: func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_, _ = io.WriteString(c, "No match for example.com\n")
				helperClose(nil, c, "conn close")
			},
			wantAvailable: true,
			wantReason:    ReasonNoMatch,
		},
		{
			name: "Domain found => available=FALSE, reason=TAKEN",
			serverHandler: func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_, _ = io.WriteString(c, "Domain Name: something.com\n")
				helperClose(nil, c, "conn close")
			},
			wantAvailable: false,
			wantReason:    ReasonTaken,
		},
		{
			name: "Immediate close => reason=ERROR",
			serverHandler: func(c net.Conn) {
				helperClose(nil, c, "conn close")
			},
			wantAvailable: false,
			wantReason:    ReasonError,
			wantErr:       true,
		},
		{
			name: "Empty response => reason=ERROR",
			serverHandler: func(c net.Conn) {
				// Send no data
				_, _ = io.Copy(io.Discard, c)
				helperClose(nil, c, "conn close")
			},
			wantAvailable: false,
			wantReason:    ReasonError,
			wantErr:       true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("failed to listen: %v", err)
			}
			defer helperClose(t, ln, "listener close")

			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				tt.serverHandler(conn)
			}()

			avail, reason, _, err := CheckDomainAvailability("example.com", ln.Addr().String())
			if tt.wantErr && err == nil {
				t.Errorf("expected an error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("did not expect error, got: %v", err)
			}
			if avail != tt.wantAvailable {
				t.Errorf("got available=%v, want %v", avail, tt.wantAvailable)
			}
			if reason != tt.wantReason {
				t.Errorf("got reason=%q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestArgParsing checks we fail if no arguments or if whois is missing
func TestArgParsing(t *testing.T) {

	// No args
	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{})
		if code == 0 {
			t.Error("Expected non-zero code with no args")
		}
	})
	if !strings.Contains(stderr, "Usage: talia") {
		t.Errorf("Expected usage help, got: %s", stderr)
	}

	// Arg but no --whois
	tmpPath := filepath.Join(t.TempDir(), "somefile.json")
	_, stderr = captureOutput(t, func() {
		code := RunCLI([]string{tmpPath})
		if code == 0 {
			t.Error("Expected non-zero code if whois is missing")
		}
	})
	if !strings.Contains(stderr, "Error: --whois=<server:port> is required") {
		t.Errorf("Expected missing whois error, got: %s", stderr)
	}
}

// TestInputFileReadError ensures we fail if input file can't be read
func TestInputFileReadError(t *testing.T) {

	// We'll pass a directory instead of a file
	tmpDir, err := os.MkdirTemp("", "testdir")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer helperRemoveAll(t, tmpDir)

	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{"--whois=127.0.0.1:9999", "--sleep=0s", tmpDir})
		if code == 0 {
			t.Errorf("Expected non-zero code for read error")
		}
	})
	if !strings.Contains(stderr, "Error reading") {
		t.Errorf("Expected 'Error reading' message, got: %s", stderr)
	}
}

// TestInputFileParseError ensures we fail on malformed JSON
func TestInputFileParseError(t *testing.T) {

	tmpFile, err := os.CreateTemp("", "badjson_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer helperRemove(t, tmpFile.Name())

	if _, err := tmpFile.WriteString("{\"domain\": \"test.com\", "); err != nil { // incomplete
		t.Fatalf("write malformed JSON: %v", err)
	}
	helperClose(t, tmpFile, "tmpFile close for parse error test")

	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{"--whois=127.0.0.1:9999", "--sleep=0s", tmpFile.Name()})
		if code == 0 {
			t.Errorf("Expected non-zero code for JSON parse error")
		}
	})
	if !strings.Contains(stderr, "Error parsing JSON") {
		t.Errorf("Expected parse error, got: %s", stderr)
	}
}

// TestMainNonGrouped verifies inline updates in non-grouped mode
func TestMainNonGrouped(t *testing.T) {

	tmp, err := os.CreateTemp("", "domains_*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	domains := []DomainRecord{
		{Domain: "example1.com"},
		{Domain: "example2.com"},
	}
	js, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := tmp.Write(js); err != nil {
		t.Fatalf("write temp JSON: %v", err)
	}
	helperClose(t, tmp, "tmp file close for non-grouped test")

	// WHOIS => always 'No match for' => available
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "No match for domain\n")
			helperClose(nil, c, "conn close")
		}
	}()

	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmp.Name(),
		})
		if code != 0 {
			t.Errorf("want exit code 0, got %d", code)
		}
	})

	updated, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("reading updated file: %v", err)
	}
	var updatedList []DomainRecord
	if err := json.Unmarshal(updated, &updatedList); err != nil {
		t.Fatalf("unmarshal updated list: %v", err)
	}
	if len(updatedList) != 2 {
		t.Errorf("want 2, got %d", len(updatedList))
	}
	for _, r := range updatedList {
		if !r.Available {
			t.Errorf("domain %s expected available=true", r.Domain)
		}
		if r.Reason != ReasonNoMatch {
			t.Errorf("domain %s reason: got %s want NO_MATCH", r.Domain, r.Reason)
		}
	}
}

// TestFileWriteError ensures we fail if we can't write the input file in non-grouped mode
func TestFileWriteError(t *testing.T) {

	if os.Geteuid() == 0 {
		t.Skip("running as root; file write permission errors won't occur")
	}

	tmp, err := os.CreateTemp("", "domains_write_err_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	// put some data
	domains := []DomainRecord{{Domain: "test.com"}}
	b, _ := json.Marshal(domains)
	if _, err := tmp.Write(b); err != nil {
		t.Fatalf("write temp data: %v", err)
	}
	helperClose(t, tmp, "tmp file close for write error test")

	// Make it read-only
	if err := os.Chmod(tmp.Name(), 0o400); err != nil {
		t.Fatalf("chmod temp file: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "No match for domain\n")
			helperClose(nil, c, "conn close")
		}
	}()

	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmp.Name(),
		})
		if code == 0 {
			t.Error("Expected non-zero code on file write error")
		}
	})
	if !strings.Contains(stderr, "Error writing file") {
		t.Errorf("Expected file write error, got: %s", stderr)
	}
}

// TestMainVerbose checks if verbose logs are stored for successful checks
func TestMainVerbose(t *testing.T) {

	tmp, err := os.CreateTemp("", "domains_verbose_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	domains := []DomainRecord{{Domain: "verbose1.com"}}
	js, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := tmp.Write(js); err != nil {
		t.Fatalf("write temp JSON: %v", err)
	}
	helperClose(t, tmp, "tmp file close for verbose test")

	// WHOIS => "No match for"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "No match for domain\n")
			helperClose(nil, c, "conn close")
		}
	}()

	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{
			"--verbose",
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmp.Name(),
		})
		if code != 0 {
			t.Errorf("want exit code 0, got %d", code)
		}
	})

	updated, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read updated: %v", err)
	}
	var updatedList []DomainRecord
	if err := json.Unmarshal(updated, &updatedList); err != nil {
		t.Fatalf("unmarshal updated list: %v", err)
	}
	if len(updatedList) != 1 {
		t.Errorf("want 1 domain, got %d", len(updatedList))
	}
	if updatedList[0].Log == "" {
		t.Error("expected a WHOIS log in verbose mode, got empty string")
	}
}

// TestMainErrorCase verifies that if one domain triggers an error, we still proceed
func TestMainErrorCase(t *testing.T) {

	tmp, err := os.CreateTemp("", "domains_error_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	domains := []DomainRecord{
		{Domain: "error1.com"},
		{Domain: "ok2.com"},
	}
	js, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := tmp.Write(js); err != nil {
		t.Fatalf("write temp JSON: %v", err)
	}
	helperClose(t, tmp, "tmp file close for error case test")

	// First conn => immediate close => error, second => "No match for"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")

	go func() {
		counter := 0
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			func(conn net.Conn) {
				defer helperClose(nil, conn, "conn close")
				_, _ = io.Copy(io.Discard, conn)
				if counter == 0 {
					// Immediate close => error
				} else {
					_, _ = io.WriteString(conn, "No match for domain\n")
				}
				counter++
			}(c)
		}
	}()

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmp.Name(),
		})
		if code != 0 {
			t.Errorf("want 0, got %d", code)
		}
	})

	// With the new progress output, errors are shown inline with ⚠ symbol
	if !strings.Contains(stdout, "error1.com") || !strings.Contains(stdout, "error") {
		t.Errorf("Expected error indication for domain error1.com, got: %s", stdout)
	}

	updated, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("read updated: %v", err)
	}
	var updatedList []DomainRecord
	if err := json.Unmarshal(updated, &updatedList); err != nil {
		t.Fatalf("unmarshal updated list: %v", err)
	}
	if len(updatedList) != 2 {
		t.Errorf("want 2, got %d", len(updatedList))
	}
	// error1.com => reason=ERROR
	// ok2.com => reason=NO_MATCH
	if updatedList[0].Domain == "error1.com" {
		if updatedList[0].Reason != ReasonError {
			t.Errorf("expected reason=ERROR for error1.com, got %s", updatedList[0].Reason)
		}
	} else {
		t.Errorf("Unexpected domain ordering for the first record")
	}
	if updatedList[1].Domain == "ok2.com" {
		if updatedList[1].Reason != ReasonNoMatch {
			t.Errorf("expected reason=NO_MATCH for ok2.com, got %s", updatedList[1].Reason)
		}
	} else {
		t.Errorf("Unexpected domain ordering for the second record")
	}
}

// TestMainGroupedNoFile overwrites input JSON with a grouped object
func TestMainGroupedNoFile(t *testing.T) {

	tmp, err := os.CreateTemp("", "grouped_no_file_*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer helperRemove(t, tmp.Name())

	domains := []DomainRecord{
		{Domain: "g1.com"},
		{Domain: "g2.com"},
	}
	js, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := tmp.Write(js); err != nil {
		t.Fatalf("write temp JSON: %v", err)
	}
	helperClose(t, tmp, "tmp file close for grouped no file test")

	// WHOIS => first => no match => available, second => domain => taken
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		i := 0
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			func(conn net.Conn) {
				defer helperClose(nil, conn, "conn close")
				_, _ = io.Copy(io.Discard, conn)
				if i == 0 {
					_, _ = io.WriteString(conn, "No match for domain\n")
				} else {
					_, _ = io.WriteString(conn, "Domain Name: something.com\n")
				}
				i++
			}(c)
		}
	}()

	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{
			"--grouped-output",
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmp.Name(),
		})
		if code != 0 {
			t.Errorf("wanted exit code 0, got %d", code)
		}
	})

	groupedBytes, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("reading grouped: %v", err)
	}
	var grouped struct {
		Available   []map[string]string `json:"available"`
		Unavailable []map[string]string `json:"unavailable"`
	}
	if err := json.Unmarshal(groupedBytes, &grouped); err != nil {
		t.Fatalf("unmarshal grouped: %v", err)
	}
	if len(grouped.Available) != 1 || len(grouped.Unavailable) != 1 {
		t.Errorf("expected 1 avail + 1 unavail, got %d/%d", len(grouped.Available), len(grouped.Unavailable))
	}
}

// TestMainGroupedWithFile merges results into a separate grouped file
func TestMainGroupedWithFile(t *testing.T) {

	// Input
	inputFile, err := os.CreateTemp("", "grouped_input_*.json")
	if err != nil {
		t.Fatalf("temp input: %v", err)
	}
	defer helperRemove(t, inputFile.Name())

	domains := []DomainRecord{{Domain: "merge-test1.com"}, {Domain: "merge-test2.com"}}
	data, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := inputFile.Write(data); err != nil {
		t.Fatalf("write input JSON: %v", err)
	}
	helperClose(t, inputFile, "inputFile close for grouped with file test")

	// Existing grouped file (non-empty) with 1 domain
	groupedFile, err := os.CreateTemp("", "grouped_output_*.json")
	if err != nil {
		t.Fatalf("temp grouped: %v", err)
	}
	defer helperRemove(t, groupedFile.Name())

	existingGrouped := GroupedData{
		Available: []GroupedDomain{
			{Domain: "previous-available.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "previous-unavailable.com", Reason: ReasonTaken},
		},
	}
	egBytes, _ := json.MarshalIndent(existingGrouped, "", "  ")
	if _, err := groupedFile.Write(egBytes); err != nil {
		t.Fatalf("write grouped JSON: %v", err)
	}
	helperClose(t, groupedFile, "groupedFile close for grouped with file test")

	// WHOIS => first => no match => available, second => found => unavailable
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		i := 0
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func(c net.Conn) {
				defer helperClose(nil, c, "conn close")
				_, _ = io.Copy(io.Discard, c)
				if i == 0 {
					_, _ = io.WriteString(c, "No match for domain\n")
				} else {
					_, _ = io.WriteString(c, "Domain Name: found-something\n")
				}
				i++
			}(conn)
		}
	}()

	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{
			"--grouped-output",
			"--output-file=" + groupedFile.Name(),
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			inputFile.Name(),
		})
		if code != 0 {
			t.Errorf("wanted 0, got %d", code)
		}
	})

	// Input file should be unchanged
	inRaw, err := os.ReadFile(inputFile.Name())
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	var inCheck []DomainRecord
	if err := json.Unmarshal(inRaw, &inCheck); err != nil {
		t.Fatalf("unmarshal inCheck: %v", err)
	}
	for _, r := range inCheck {
		if r.Available || r.Reason != "" {
			t.Errorf("expected no changes in input JSON, found reason=%s", r.Reason)
		}
	}

	// Grouped file => old plus newly inserted
	gRaw, err := os.ReadFile(groupedFile.Name())
	if err != nil {
		t.Fatalf("read groupedFile: %v", err)
	}
	var merged GroupedData
	if err := json.Unmarshal(gRaw, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	// We had 1 in Available, 1 in Unavailable,
	// plus 1 new in Available, 1 new in Unavailable.
	if len(merged.Available) != 2 {
		t.Errorf("expected 2 available, got %d", len(merged.Available))
	}
	if len(merged.Unavailable) != 2 {
		t.Errorf("expected 2 unavailable, got %d", len(merged.Unavailable))
	}
}

// TestMainGroupedFileEmptyExisting ensures no crash if grouped file is empty
func TestMainGroupedFileEmptyExisting(t *testing.T) {

	// Input
	inputFile, err := os.CreateTemp("", "grouped_input_*.json")
	if err != nil {
		t.Fatalf("temp input: %v", err)
	}
	defer helperRemove(t, inputFile.Name())

	domains := []DomainRecord{{Domain: "empty-existing-1.com"}}
	data, _ := json.MarshalIndent(domains, "", "  ")
	if _, err := inputFile.Write(data); err != nil {
		t.Fatalf("write input JSON: %v", err)
	}
	helperClose(t, inputFile, "inputFile close for grouped empty existing test")

	// Create an empty grouped file
	groupedFile, err := os.CreateTemp("", "grouped_out_*.json")
	if err != nil {
		t.Fatalf("temp grouped: %v", err)
	}
	helperClose(t, groupedFile, "groupedFile close for grouped empty existing test")
	defer helperRemove(t, groupedFile.Name())

	// WHOIS => no match => available
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer helperClose(t, ln, "listener close")
	go func() {
		c, _ := ln.Accept()
		_, _ = io.Copy(io.Discard, c)
		_, _ = io.WriteString(c, "No match for domain\n")
		helperClose(nil, c, "conn close")
	}()

	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{
			"--grouped-output",
			"--output-file=" + groupedFile.Name(),
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			inputFile.Name(),
		})
		if code != 0 {
			t.Errorf("Expected exit code 0, got %d", code)
		}
	})

	if strings.Contains(stderr, "unexpected end of JSON") {
		t.Errorf("Should not fail on empty grouped file: %s", stderr)
	}

	// Confirm the grouped file now has a single available domain
	gRaw, err := os.ReadFile(groupedFile.Name())
	if err != nil {
		t.Fatalf("read grouped file: %v", err)
	}
	var merged GroupedData
	if err := json.Unmarshal(gRaw, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if len(merged.Available) != 1 {
		t.Errorf("expected 1 available, got %d", len(merged.Available))
	}
	if len(merged.Unavailable) != 0 {
		t.Errorf("expected 0 unavailable, got %d", len(merged.Unavailable))
	}
}

// TestMainGroupedFileRepeatedAppend tests that we can append multiple times to the same grouped file
func TestMainGroupedFileRepeatedAppend(t *testing.T) {

	// 1. Create input: a plain domain list
	inputFile, err := os.CreateTemp("", "repeated_input_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, inputFile.Name())

	doms := []DomainRecord{
		{Domain: "append-1.com"},
		{Domain: "append-2.com"},
	}
	b, _ := json.Marshal(doms)
	if _, err := inputFile.Write(b); err != nil {
		t.Fatalf("write temp data: %v", err)
	}
	helperClose(t, inputFile, "inputFile close for repeated append test")

	// 2. Create/initialize an output grouped file
	groupedFile, err := os.CreateTemp("", "repeated_output_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, groupedFile.Name())

	gf := GroupedData{
		Available: []GroupedDomain{},
		Unavailable: []GroupedDomain{
			{Domain: "some-old-unavailable.com", Reason: ReasonTaken},
		},
	}
	gfBytes, _ := json.MarshalIndent(gf, "", "  ")
	if _, err := groupedFile.Write(gfBytes); err != nil {
		t.Fatalf("write grouped file: %v", err)
	}
	helperClose(t, groupedFile, "groupedFile close for repeated append test")

	// 3. Start a mock WHOIS server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener close")

	go func() {
		// We'll alternate: first => no match => available
		// second => domain found => taken
		i := 0
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			if i%2 == 0 {
				_, _ = io.WriteString(c, "No match for domain\n")
			} else {
				_, _ = io.WriteString(c, "Domain Name: found-it\n")
			}
			helperClose(nil, c, "conn close")
			i++
		}
	}()

	// 4. Run Talia with --grouped-output and the same groupedFile a first time
	_, _ = captureOutput(t, func() {
		exitCodeFirst := RunCLI([]string{
			"--grouped-output",
			"--output-file=" + groupedFile.Name(),
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			inputFile.Name(),
		})
		if exitCodeFirst != 0 {
			t.Fatalf("First RunCLI failed with code=%d", exitCodeFirst)
		}
	})

	// 5. Re-run with the same EXACT input
	_, _ = captureOutput(t, func() {
		exitCodeSecond := RunCLI([]string{
			"--grouped-output",
			"--output-file=" + groupedFile.Name(),
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			inputFile.Name(),
		})
		if exitCodeSecond != 0 {
			t.Fatalf("Second RunCLI failed with code=%d", exitCodeSecond)
		}
	})

	// 6. Inspect the grouped file
	mergedBytes, _ := os.ReadFile(groupedFile.Name())
	var final GroupedData
	if err := json.Unmarshal(mergedBytes, &final); err != nil {
		t.Fatal(err)
	}

	if len(final.Available) < 1 || len(final.Unavailable) < 2 {
		t.Errorf("Expected domains in available/unavailable after repeated merges, got %d/%d",
			len(final.Available), len(final.Unavailable))
	}

	// Check if the original unavailable domain is still there
	var foundOriginal bool
	for _, d := range final.Unavailable {
		if d.Domain == "some-old-unavailable.com" {
			foundOriginal = true
			break
		}
	}
	if !foundOriginal {
		t.Error("Original unavailable domain was lost during merges")
	}
}

// TestMainGroupedFileWithUnverifiedInput tests using a grouped file with unverified domains as input
func TestMainGroupedFileWithUnverifiedInput(t *testing.T) {

	// We'll build a grouped JSON with a few unverified domains
	g := ExtendedGroupedData{
		Available: []GroupedDomain{
			{Domain: "old-avail.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "old-unavail.com", Reason: ReasonTaken},
		},
		Unverified: []DomainRecord{
			{Domain: "check-me-1.com"},
			{Domain: "check-me-2.com"},
		},
	}
	raw, _ := json.MarshalIndent(g, "", "  ")

	// Write it to a temp file
	inputFile, err := os.CreateTemp("", "grouped_unverified_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, inputFile.Name())
	if _, err := inputFile.Write(raw); err != nil {
		t.Fatalf("write raw JSON: %v", err)
	}
	helperClose(t, inputFile, "inputFile close for unverified input test")

	// Start WHOIS server that returns "No match for" for one domain
	// and "Domain Name:" for the other.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener close")

	go func() {
		i := 0
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c)
			if i%2 == 0 {
				_, _ = io.WriteString(c, "No match for domain\n")
			} else {
				_, _ = io.WriteString(c, "Domain Name: found-something\n")
			}
			helperClose(nil, c, "conn close")
			i++
		}
	}()

	_, _ = captureOutput(t, func() {
		exitCode := RunCLI([]string{
			"--grouped-output",
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			inputFile.Name(),
		})
		if exitCode != 0 {
			t.Fatalf("RunCLI failed with code=%d", exitCode)
		}
	})

	// Now read back the file, it should have no unverified, and the 2 domains
	// should be moved into available/unavailable.
	updatedBytes, _ := os.ReadFile(inputFile.Name())
	var updated ExtendedGroupedData
	if err := json.Unmarshal(updatedBytes, &updated); err != nil {
		t.Fatal(err)
	}

	if len(updated.Unverified) != 0 {
		t.Errorf("Expected unverified to be empty after checking, got %d", len(updated.Unverified))
	}

	// We had 1 domain "No match for" => available
	// plus 1 domain "Domain Name:" => unavailable
	// plus the old ones
	if len(updated.Available) != 2 {
		t.Errorf("Expected 2 in available, got %d", len(updated.Available))
	}
	if len(updated.Unavailable) != 2 {
		t.Errorf("Expected 2 in unavailable, got %d", len(updated.Unavailable))
	}

	// Check if the original domains are still there
	var foundOldAvail, foundOldUnavail bool
	for _, d := range updated.Available {
		if d.Domain == "old-avail.com" {
			foundOldAvail = true
			break
		}
	}
	for _, d := range updated.Unavailable {
		if d.Domain == "old-unavail.com" {
			foundOldUnavail = true
			break
		}
	}
	if !foundOldAvail || !foundOldUnavail {
		t.Error("Original domains were lost during processing")
	}
}

// TestCheckDomainAvailability_DialError verifies we get an error/log when net.Dial fails.
func TestCheckDomainAvailability_DialError(t *testing.T) {
	// Use a port that is presumably not open.
	addr := "127.0.0.1:1"

	available, reason, logData, err := CheckDomainAvailability("faildial.com", addr)
	if err == nil {
		t.Errorf("Expected error from net.Dial, got nil")
	}
	if reason != ReasonError {
		t.Errorf("Expected ReasonError for dial failure, got %s", reason)
	}
	if available {
		t.Error("Expected domain NOT to be available on dial failure")
	}
	if logData == "" {
		t.Error("Expected some error log data, got empty string")
	}
}

func TestWriteGroupedFile_EmptyPath(t *testing.T) {
	err := WriteGroupedFile("", GroupedData{})
	if err != nil {
		t.Errorf("Expected nil error if path==\"\", got %v", err)
	}
}

func TestWriteGroupedFile_NewFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "dummy_*")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmpFile.Name()
	helperClose(t, tmpFile, "tmpFile close for new file test")
	defer helperRemove(t, tmpPath) // Changed from os.Remove

	gData := GroupedData{
		Available: []GroupedDomain{{Domain: "newavail.com", Reason: ReasonNoMatch}},
	}

	err = WriteGroupedFile(tmpPath, gData)
	if err != nil {
		t.Fatalf("WriteGroupedFile returned error: %v", err)
	}

	raw, _ := os.ReadFile(tmpPath)
	// defer os.Remove(tmpPath) // This line was removed as defer helperRemove is above

	var out GroupedData
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	if len(out.Available) != 1 || out.Available[0].Domain != "newavail.com" {
		t.Errorf("Expected domain newavail.com in available, got %+v", out)
	}
}

// TestWriteGroupedFile_ParseArrayFallback verifies existing array results survive
// conversion when fresh grouped results are added.
func TestWriteGroupedFile_ParseArrayFallback(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "array_fallback_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, tmpFile.Name())

	arr := []DomainRecord{
		{Domain: "arrdomain1.com", Available: true, Reason: ReasonNoMatch},
		{Domain: "arrdomain2.com", Available: false, Reason: ReasonTaken},
	}
	raw, _ := json.Marshal(arr)
	if _, err := tmpFile.Write(raw); err != nil {
		t.Fatalf("write raw JSON: %v", err)
	}
	helperClose(t, tmpFile, "tmpFile close for parse array fallback test")

	newest := GroupedData{
		Available: []GroupedDomain{
			{Domain: "additionalAvail.com", Reason: ReasonNoMatch},
		},
	}

	err = WriteGroupedFile(tmpFile.Name(), newest)
	if err != nil {
		t.Fatalf("WriteGroupedFile error: %v", err)
	}

	finalRaw, _ := os.ReadFile(tmpFile.Name())
	var final GroupedData
	if err := json.Unmarshal(finalRaw, &final); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}

	if len(final.Available) != 2 {
		t.Errorf("Expected 2 available, got %d: %#v", len(final.Available), final.Available)
	}
	if len(final.Unavailable) != 1 {
		t.Errorf("Expected 1 unavailable, got %d: %#v", len(final.Unavailable), final.Unavailable)
	}
}

// NEW TEST #1: Directly test ConvertArrayToGrouped
func TestConvertArrayToGrouped(t *testing.T) {
	arr := []DomainRecord{
		{Domain: "test1.com", Available: true, Reason: ReasonNoMatch, Log: "log1"},
		{Domain: "test2.com", Available: false, Reason: ReasonTaken, Log: "log2"},
		{Domain: "test3.com", Available: false, Reason: ReasonError, Log: "log3"},
	}
	g := ConvertArrayToGrouped(arr)
	if len(g.Available) != 1 {
		t.Errorf("expected 1 in available, got %d", len(g.Available))
	}
	if len(g.Unavailable) != 2 {
		t.Errorf("expected 2 in unavailable, got %d", len(g.Unavailable))
	}

	if g.Available[0].Domain != "test1.com" || g.Available[0].Log != "log1" {
		t.Error("test1.com should be in available with correct log")
	}

	var foundTest2, foundTest3 bool
	for _, u := range g.Unavailable {
		if u.Domain == "test2.com" && u.Reason == ReasonTaken && u.Log == "log2" {
			foundTest2 = true
		}
		if u.Domain == "test3.com" && u.Reason == ReasonError && u.Log == "log3" {
			foundTest3 = true
		}
	}
	if !foundTest2 || !foundTest3 {
		t.Error("Did not find test2.com or test3.com in unavailable list with correct reason/log")
	}
}

// NEW TEST #2: Directly test WriteGroupedFile when existing file is valid grouped JSON
func TestWriteGroupedFile_ExistingGrouped(t *testing.T) {
	tmp, err := os.CreateTemp("", "existing_grouped_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, tmp.Name())

	existing := GroupedData{
		Available:   []GroupedDomain{{Domain: "oldavail.com", Reason: ReasonNoMatch}},
		Unavailable: []GroupedDomain{{Domain: "oldunavail.com", Reason: ReasonTaken}},
	}
	oldBytes, _ := json.Marshal(existing)
	if _, err := tmp.Write(oldBytes); err != nil {
		t.Fatalf("write old bytes: %v", err)
	}
	helperClose(t, tmp, "tmp file close for existing grouped test")

	newData := GroupedData{
		Available: []GroupedDomain{
			{Domain: "newavail.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "newunavail.com", Reason: ReasonError},
		},
	}
	if err := WriteGroupedFile(tmp.Name(), newData); err != nil {
		t.Fatalf("WriteGroupedFile: %v", err)
	}

	finalRaw, _ := os.ReadFile(tmp.Name())
	var final GroupedData
	if err := json.Unmarshal(finalRaw, &final); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	// Expect old + new in each group
	if len(final.Available) != 2 {
		t.Errorf("expected 2 in available, got %d", len(final.Available))
	}
	if len(final.Unavailable) != 2 {
		t.Errorf("expected 2 in unavailable, got %d", len(final.Unavailable))
	}

	var haveOldAvail, haveNewAvail, haveOldUnavail, haveNewUnavail bool
	for _, d := range final.Available {
		if d.Domain == "oldavail.com" {
			haveOldAvail = true
		}
		if d.Domain == "newavail.com" {
			haveNewAvail = true
		}
	}
	for _, d := range final.Unavailable {
		if d.Domain == "oldunavail.com" {
			haveOldUnavail = true
		}
		if d.Domain == "newunavail.com" {
			haveNewUnavail = true
		}
	}
	if !haveOldAvail || !haveNewAvail || !haveOldUnavail || !haveNewUnavail {
		t.Errorf("Merge logic failed. Final: %+v", final)
	}
}

// NEW TEST #3: Test RunCLIGroupedInput scenario WITHOUT --grouped-output (forces overwrite of the same file)
func TestRunCLIGroupedInput_Overwrite(t *testing.T) {

	// Build a grouped JSON with unverified domain
	g := ExtendedGroupedData{
		Available: []GroupedDomain{
			{Domain: "old-avail.com", Reason: ReasonNoMatch},
		},
		Unavailable: []GroupedDomain{
			{Domain: "old-unavail.com", Reason: ReasonTaken},
		},
		Unverified: []DomainRecord{
			{Domain: "check-me-3.com"},
		},
	}
	b, _ := json.MarshalIndent(g, "", "  ")

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "grouped_overwrite_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, tmpFile.Name())
	if _, err := tmpFile.Write(b); err != nil {
		t.Fatalf("write grouped JSON: %v", err)
	}
	helperClose(t, tmpFile, "tmpFile close for grouped overwrite test")

	// Start WHOIS => "Domain Name: found-something" => taken
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener close")

	go func() {
		c, _ := ln.Accept()
		if c != nil {
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "Domain Name: found-something\n")
			helperClose(nil, c, "conn close")
		}
	}()

	// call RunCLI with NO --grouped-output => RunCLI sees grouped JSON, calls RunCLIGroupedInput,
	// which sets finalOutputFile = inputPath if !groupedOutput.
	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{
			"--whois=" + ln.Addr().String(),
			"--sleep=0s",
			tmpFile.Name(),
		})
		if code != 0 {
			t.Errorf("Expected exit code 0, got %d", code)
		}
	})

	// Now the original file should be overwritten. unverified => empty, new domain => "taken"
	updated, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	var final ExtendedGroupedData
	if err := json.Unmarshal(updated, &final); err != nil {
		t.Fatalf("Unmarshal final: %v", err)
	}
	if len(final.Unverified) != 0 {
		t.Errorf("Expected no unverified, got %d", len(final.Unverified))
	}
	foundCheckMe3 := false
	for _, d := range final.Unavailable {
		if d.Domain == "check-me-3.com" {
			foundCheckMe3 = true
			if d.Reason != ReasonTaken {
				t.Errorf("Expected reason=TAKEN, got %s", d.Reason)
			}
		}
	}
	if !foundCheckMe3 {
		t.Errorf("Did not find 'check-me-3.com' in final unavailable: %+v", final.Unavailable)
	}
}

func TestRunCLI_GroupedInputSeparateOutputMergesOnlyFreshChecks(t *testing.T) {
	t.Setenv("TALIA_LIGHTSPEED", "")
	for _, workers := range []string{"", "max"} {
		t.Run("lightspeed="+workers, func(t *testing.T) {
			input := writeGroupedFixture(t, ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "input-only-available.com", Reason: ReasonNoMatch},
					{Domain: "held.com", Reason: ReasonNoMatch, Log: "stale input result"},
				},
				Unavailable: []GroupedDomain{
					{Domain: "input-only-unavailable.com", Reason: ReasonTaken},
					{Domain: "kept.com", Reason: ReasonTaken, Log: "stale input result"},
				},
				Unverified: []DomainRecord{
					{Domain: "new-available.com"},
					{Domain: "new-unavailable.com"},
					{Domain: "released.com"},
					{Domain: "registered.com"},
					{Domain: "checked.com"},
					{Domain: "rechecked.com"},
				},
			})
			original, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			output := writeGroupedFixture(t, ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "kept.com", Reason: ReasonNoMatch, Log: "keep available log"},
					{Domain: "registered.com", Reason: ReasonNoMatch},
				},
				Unavailable: []GroupedDomain{
					{Domain: "held.com", Reason: ReasonTaken, Log: "keep unavailable log"},
					{Domain: "released.com", Reason: ReasonTaken},
					{Domain: "rechecked.com", Reason: ReasonError, Log: "Error: timeout"},
				},
				Unverified: []DomainRecord{
					{Domain: "waiting1.com"},
					{Domain: "checked.com"},
					{Domain: "waiting2.com", Log: "suggested"},
				},
			})
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
					query, _ := bufio.NewReader(conn).ReadString('\n')
					domain := strings.TrimSpace(query)
					if domain == "new-available.com" || domain == "released.com" || domain == "checked.com" {
						_, _ = io.WriteString(conn, "No match for "+domain+"\n")
					} else {
						_, _ = io.WriteString(conn, "Domain Name: "+domain+"\n")
					}
					_ = conn.Close()
				}
			}()

			want := ExtendedGroupedData{
				Available: []GroupedDomain{
					{Domain: "kept.com", Reason: ReasonNoMatch, Log: "keep available log"},
					{Domain: "new-available.com", Reason: ReasonNoMatch},
					{Domain: "released.com", Reason: ReasonNoMatch},
					{Domain: "checked.com", Reason: ReasonNoMatch},
				},
				Unavailable: []GroupedDomain{
					{Domain: "held.com", Reason: ReasonTaken, Log: "keep unavailable log"},
					{Domain: "rechecked.com", Reason: ReasonTaken},
					{Domain: "new-unavailable.com", Reason: ReasonTaken},
					{Domain: "registered.com", Reason: ReasonTaken},
				},
				Unverified: []DomainRecord{{Domain: "waiting1.com"}, {Domain: "waiting2.com", Log: "suggested"}},
			}
			var previous []byte
			for run := range 2 {
				var code int
				_, stderr := captureOutput(t, func() {
					code = RunCLI([]string{
						"--whois=" + ln.Addr().String(), "--sleep=0", "--suggest=0",
						"--lightspeed=" + workers, "--grouped-output", "--output-file=" + output, input,
					})
				})
				if code != 0 {
					t.Fatalf("run %d: exit code = %d, stderr = %s", run, code, stderr)
				}
				if got := readGroupedFile(t, output); !reflect.DeepEqual(got, want) {
					t.Errorf("run %d: grouped output = %+v, want %+v", run, got, want)
				}
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				if previous != nil && !bytes.Equal(data, previous) {
					t.Errorf("run %d changed output despite identical checks", run)
				}
				previous = data
			}
			if unchanged, err := os.ReadFile(input); err != nil || !bytes.Equal(unchanged, original) {
				t.Errorf("input changed: read error = %v, data = %s", err, unchanged)
			}
		})
	}
}

func TestRunCLI_GroupedInputSeparateOutputDestinationFormats(t *testing.T) {
	for _, format := range []string{"missing", "empty", "array"} {
		t.Run(format, func(t *testing.T) {
			input := writeGroupedFixture(t, ExtendedGroupedData{
				Available:   []GroupedDomain{{Domain: "input-only-available.com", Reason: ReasonNoMatch}},
				Unavailable: []GroupedDomain{{Domain: "input-only-unavailable.com", Reason: ReasonTaken}},
				Unverified:  []DomainRecord{{Domain: "checked.com"}},
			})
			output := filepath.Join(t.TempDir(), "out.json")
			want := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "checked.com", Reason: ReasonNoMatch}}}
			if format != "missing" {
				raw := ""
				if format == "array" {
					raw = `[{"domain":"kept.com","available":true,"reason":"NO_MATCH"}]`
					want.Available = []GroupedDomain{
						{Domain: "kept.com", Reason: ReasonNoMatch},
						{Domain: "checked.com", Reason: ReasonNoMatch},
					}
				}
				if err := os.WriteFile(output, []byte(raw), 0644); err != nil {
					t.Fatal(err)
				}
			}
			addr, _ := startCountingWhois(t)
			var code int
			_, stderr := captureOutput(t, func() {
				code = RunCLI([]string{
					"--whois=" + addr, "--sleep=0", "--suggest=0", "--grouped-output", "--output-file=" + output, input,
				})
			})
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %s", code, stderr)
			}
			if got := readGroupedFile(t, output); !reflect.DeepEqual(got, want) {
				t.Errorf("grouped output = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRunCLI_GroupedInputSeparateOutputWithNoFreshChecks(t *testing.T) {
	input := writeGroupedFixture(t, ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "input-only.com", Reason: ReasonNoMatch}},
		Unavailable: []GroupedDomain{{Domain: "kept.com", Reason: ReasonTaken}},
	})
	want := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: "kept.com", Reason: ReasonNoMatch}},
		Unverified: []DomainRecord{{Domain: "waiting.com"}},
	}
	output := writeGroupedFixture(t, want)
	addr, _ := startCountingWhois(t)
	var code int
	_, stderr := captureOutput(t, func() {
		code = RunCLI([]string{
			"--whois=" + addr, "--suggest=0", "--grouped-output", "--output-file=" + output, input,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if got := readGroupedFile(t, output); !reflect.DeepEqual(got, want) {
		t.Errorf("grouped output = %+v, want %+v", got, want)
	}
}

func TestRunCLI_GroupedInputSeparateOutputRejectsInvalidDestination(t *testing.T) {
	input := writeGroupedFixture(t, ExtendedGroupedData{Unverified: []DomainRecord{{Domain: "checked.com"}}})
	output := filepath.Join(t.TempDir(), "out.json")
	original := []byte(`{"available":`)
	if err := os.WriteFile(output, original, 0644); err != nil {
		t.Fatal(err)
	}
	addr, _ := startCountingWhois(t)
	var code int
	_, stderr := captureOutput(t, func() {
		code = RunCLI([]string{
			"--whois=" + addr, "--sleep=0", "--suggest=0", "--grouped-output", "--output-file=" + output, input,
		})
	})
	if code != 1 || !strings.Contains(stderr, "parse grouped file") {
		t.Errorf("exit code = %d, stderr = %q; want 1 and a parse error", code, stderr)
	}
	if data, err := os.ReadFile(output); err != nil || !bytes.Equal(data, original) {
		t.Errorf("invalid destination changed: read error = %v, data = %s", err, data)
	}
}

func TestWriteGroupedFile_CorruptExisting(t *testing.T) {
	tmp, err := os.CreateTemp("", "corrupt_grouped_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, tmp.Name()) // Changed from os.Remove

	// Write invalid JSON that fails parse as GroupedData & DomainRecord array
	if _, err := tmp.WriteString(`{"not_valid": `); err != nil {
		t.Fatalf("write corrupt JSON: %v", err)
	}
	helperClose(t, tmp, "tmp file close for corrupt existing test")

	newest := GroupedData{
		Available: []GroupedDomain{{Domain: "wontexist.com", Reason: ReasonNoMatch}},
	}

	// Should fail with parse grouped file
	err = WriteGroupedFile(tmp.Name(), newest)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse grouped file") {
		t.Errorf("Expected parse grouped file error, got %v", err)
	}
}

func TestRunCLIGroupedInput_WriteError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "No match for domain")
			helperClose(nil, conn, "conn")
		}
	}()

	dir := t.TempDir()
	ext := ExtendedGroupedData{Unverified: []DomainRecord{{Domain: "a.com"}}}
	_, stderr := captureOutput(t, func() {
		code := RunCLIGroupedInput(ln.Addr().String(), "input.json", ext, 0, false, true, dir, 0)
		if code == 0 {
			t.Error("expected non-zero exit")
		}
	})
	if !strings.Contains(stderr, "Error writing grouped JSON") {
		t.Errorf("missing write error, got %s", stderr)
	}
}

func TestRunCLIDomainArray_GroupedSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "No match for domain")
			helperClose(nil, c, "conn")
		}
	}()

	outFile := filepath.Join(t.TempDir(), "out.json")
	domains := []DomainRecord{{Domain: "a.com"}}
	_, stderr := captureOutput(t, func() {
		code := RunCLIDomainArray(ln.Addr().String(), "in.json", domains, 0, false, true, outFile, 0)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read outFile: %v", err)
	}
	if !strings.Contains(string(data), "a.com") {
		t.Errorf("output file missing domain: %s", string(data))
	}
}

func TestRunCLIDomainArray_GroupedOverwrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "No match for domain")
			helperClose(nil, conn, "conn")
		}
	}()

	inputFile, err := os.CreateTemp("", "input_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, inputFile.Name())
	if err := inputFile.Close(); err != nil {
		t.Fatal(err)
	}

	domains := []DomainRecord{{Domain: "a.com"}}
	stdout, stderr := captureOutput(t, func() {
		code := RunCLIDomainArray(ln.Addr().String(), inputFile.Name(), domains, 0, false, true, "", 0)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "overwrote input") {
		t.Errorf("missing overwrite message: %s", stdout)
	}
	data, err := os.ReadFile(inputFile.Name())
	if err != nil {
		t.Fatalf("read input file: %v", err)
	}
	if !strings.Contains(string(data), "available") {
		t.Errorf("expected grouped JSON in file, got %s", string(data))
	}
}

func TestRunCLIDomainArray_WriteGroupedError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "No match for domain")
			helperClose(nil, conn, "conn")
		}
	}()

	dir := t.TempDir()
	domains := []DomainRecord{{Domain: "a.com"}}
	_, stderr := captureOutput(t, func() {
		code := RunCLIDomainArray(ln.Addr().String(), "in.json", domains, 0, false, true, dir, 0)
		if code == 0 {
			t.Error("expected non-zero exit")
		}
	})
	if !strings.Contains(stderr, "Error writing grouped file") {
		t.Errorf("expected grouped file error, got %s", stderr)
	}
}

func TestRunCLIDomainArray_ErrorHandling(t *testing.T) {
	domains := []DomainRecord{{Domain: "err.com"}}
	tmp, err := os.CreateTemp("", "err_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer helperRemove(t, tmp.Name())
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}

	stdout, _ := captureOutput(t, func() {
		code := RunCLIDomainArray("127.0.0.1:1", tmp.Name(), domains, 0, true, false, "", 0)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "err.com") {
		t.Errorf("missing check output")
	}
	data, _ := os.ReadFile(tmp.Name())
	var out []DomainRecord
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out[0].Reason != ReasonError || out[0].Available {
		t.Errorf("unexpected record: %+v", out[0])
	}
	if out[0].Log == "" {
		t.Error("expected log for error case")
	}
}

func TestRunCLIDomainArray_WriteInputDirError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "No match for domain")
			helperClose(nil, conn, "conn")
		}
	}()

	dir := t.TempDir()
	code := RunCLIDomainArray(ln.Addr().String(), dir, []DomainRecord{{Domain: "a.com"}}, 0, false, false, "", 0)
	if code == 0 {
		t.Error("expected non-zero code")
	}
}

func TestRunCLIDomainArray_GroupedOverwriteWriteError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			_, _ = io.Copy(io.Discard, c)
			_, _ = io.WriteString(c, "No match for domain")
			helperClose(nil, c, "conn")
		}
	}()

	dir := t.TempDir()
	code := RunCLIDomainArray(ln.Addr().String(), dir, []DomainRecord{{Domain: "a.com"}}, 0, false, true, "", 0)
	if code == 0 {
		t.Error("expected non-zero code")
	}
}

// TestRunCLIGroupedInput_Verbose verifies verbose grouped checks retain WHOIS text.
func TestRunCLIGroupedInput_Verbose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "No match for domain")
			helperClose(nil, conn, "conn")
		}
	}()

	tmpFile := filepath.Join(t.TempDir(), "out.json")
	ext := ExtendedGroupedData{Unverified: []DomainRecord{{Domain: "a.com"}}}
	_, _ = captureOutput(t, func() {
		code := RunCLIGroupedInput(ln.Addr().String(), tmpFile, ext, 0, true, true, tmpFile, 0)
		if code != 0 {
			t.Fatalf("expected 0, got %d", code)
		}
	})
	data, _ := os.ReadFile(tmpFile)
	var out ExtendedGroupedData
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Available) != 1 || out.Available[0].Log == "" {
		t.Errorf("expected log stored, got %+v", out)
	}
}

// TestWriteGroupedFile_ReadError verifies an unreadable destination is reported
// as a read error before grouped results can be written.
func TestWriteGroupedFile_ReadError(t *testing.T) {
	dir := t.TempDir()
	err := WriteGroupedFile(dir, GroupedData{Available: []GroupedDomain{{Domain: "x.com"}}})
	if err == nil || !strings.Contains(err.Error(), "read grouped file") {
		t.Fatalf("expected read error, got %v", err)
	}
}

// TestMergeFiles tests merging multiple files
func TestMergeFiles(t *testing.T) {
	dir := t.TempDir()

	// Create source files
	file1 := filepath.Join(dir, "file1.json")
	file2 := filepath.Join(dir, "file2.json")
	output := filepath.Join(dir, "output.json")

	data1 := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "a.com"}},
		Unavailable: []GroupedDomain{{Domain: "b.com"}},
	}
	data2 := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: "c.com"}},
		Unverified: []DomainRecord{{Domain: "d.com"}},
	}

	b1, _ := json.Marshal(data1)
	b2, _ := json.Marshal(data2)
	_ = os.WriteFile(file1, b1, 0644)
	_ = os.WriteFile(file2, b2, 0644)

	count, err := mergeFiles(output, []string{file1, file2})
	if err != nil {
		t.Fatalf("mergeFiles error: %v", err)
	}
	if count != 4 {
		t.Errorf("expected 4 domains, got %d", count)
	}

	// Verify output
	raw, _ := os.ReadFile(output)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)

	if len(result.Available) != 2 {
		t.Errorf("expected 2 available, got %d", len(result.Available))
	}
	if len(result.Unavailable) != 1 {
		t.Errorf("expected 1 unavailable, got %d", len(result.Unavailable))
	}
	if len(result.Unverified) != 1 {
		t.Errorf("expected 1 unverified, got %d", len(result.Unverified))
	}
}

// TestMergeFiles_Deduplication tests that duplicates are removed
func TestMergeFiles_Deduplication(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "file1.json")
	file2 := filepath.Join(dir, "file2.json")
	output := filepath.Join(dir, "output.json")

	data1 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}}}
	data2 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}, {Domain: "b.com"}}}

	b1, _ := json.Marshal(data1)
	b2, _ := json.Marshal(data2)
	_ = os.WriteFile(file1, b1, 0644)
	_ = os.WriteFile(file2, b2, 0644)

	count, err := mergeFiles(output, []string{file1, file2})
	if err != nil {
		t.Fatalf("mergeFiles error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 unique domains, got %d", count)
	}
}

// TestMergeFiles_ReadError tests error handling for missing file
func TestMergeFiles_ReadError(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output.json")

	_, err := mergeFiles(output, []string{"/nonexistent/file.json"})
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// TestMergeFiles_ParseError tests error handling for invalid JSON
func TestMergeFiles_ParseError(t *testing.T) {
	dir := t.TempDir()
	badFile := filepath.Join(dir, "bad.json")
	output := filepath.Join(dir, "output.json")

	_ = os.WriteFile(badFile, []byte("not json"), 0644)

	_, err := mergeFiles(output, []string{badFile})
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestExportAvailableDomains tests exporting available domains
func TestExportAvailableDomains(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "output.txt")

	data := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "a.com"}, {Domain: "b.com"}, {Domain: "c.com"}},
		Unavailable: []GroupedDomain{{Domain: "taken.com"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(input, b, 0644)

	count, err := exportAvailableDomains(input, output)
	if err != nil {
		t.Fatalf("exportAvailableDomains error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 domains exported, got %d", count)
	}

	// Verify output
	raw, _ := os.ReadFile(output)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	expected := map[string]bool{"a.com": true, "b.com": true, "c.com": true}
	for _, line := range lines {
		if !expected[line] {
			t.Errorf("unexpected domain: %s", line)
		}
	}
}

// TestExportAvailableDomains_Empty tests exporting when no available domains
func TestExportAvailableDomains_Empty(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "output.txt")

	data := ExtendedGroupedData{Unavailable: []GroupedDomain{{Domain: "taken.com"}}}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(input, b, 0644)

	count, err := exportAvailableDomains(input, output)
	if err != nil {
		t.Fatalf("exportAvailableDomains error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 domains, got %d", count)
	}

	raw, _ := os.ReadFile(output)
	if string(raw) != "" {
		t.Errorf("expected empty file, got %q", string(raw))
	}
}

// TestExportAvailableDomains_ReadError tests error handling
func TestExportAvailableDomains_ReadError(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output.txt")

	_, err := exportAvailableDomains("/nonexistent/file.json", output)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// TestExportAvailableDomains_ParseError tests error handling for invalid JSON
func TestExportAvailableDomains_ParseError(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "output.txt")

	_ = os.WriteFile(input, []byte("not json"), 0644)

	_, err := exportAvailableDomains(input, output)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestExportAvailableDomains_WriteError tests error handling for write failure
func TestExportAvailableDomains_WriteError(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")

	data := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}}}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(input, b, 0644)

	_, err := exportAvailableDomains(input, "/nonexistent/dir/output.txt")
	if err == nil {
		t.Error("expected error for write failure")
	}
}

// TestCleanSuggestionsFile tests cleaning domain files
func TestCleanSuggestionsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.json")

	data := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "good.com"}, {Domain: "UPPERCASE.COM"}, {Domain: "bad-avail"}},
		Unavailable: []GroupedDomain{{Domain: "also-good.com"}},
		Unverified:  []DomainRecord{{Domain: "valid.com"}, {Domain: "invalid"}, {Domain: "double..com"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	removed, err := cleanSuggestionsFile(file)
	if err != nil {
		t.Fatalf("cleanSuggestionsFile error: %v", err)
	}

	// "invalid" and "bad-avail" should be removed (no .com), "double..com" normalizes to "double.com"
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %v", removed)
	}

	// Verify file was cleaned
	raw, _ := os.ReadFile(file)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)

	// Check domains are lowercase
	for _, d := range result.Available {
		if d.Domain != strings.ToLower(d.Domain) {
			t.Errorf("expected lowercase, got %s", d.Domain)
		}
	}
}

// TestCleanSuggestionsFile_Deduplication tests that duplicates are removed
func TestCleanSuggestionsFile_Deduplication(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.json")

	data := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: "a.com"}, {Domain: "A.COM"}},
		Unverified: []DomainRecord{{Domain: "a.com"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	_, err := cleanSuggestionsFile(file)
	if err != nil {
		t.Fatalf("cleanSuggestionsFile error: %v", err)
	}

	raw, _ := os.ReadFile(file)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)

	total := len(result.Available) + len(result.Unavailable) + len(result.Unverified)
	if total != 1 {
		t.Errorf("expected 1 unique domain after dedup, got %d", total)
	}
}

// TestCleanSuggestionsFile_ReadError tests error handling
func TestCleanSuggestionsFile_ReadError(t *testing.T) {
	_, err := cleanSuggestionsFile("/nonexistent/file.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// TestCleanSuggestionsFile_ParseError tests error handling for invalid JSON
func TestCleanSuggestionsFile_ParseError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(file, []byte("not json"), 0644)

	_, err := cleanSuggestionsFile(file)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestRunCLI_MergeMultipleFiles tests the --merge flag with multiple files
func TestRunCLI_MergeMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "file1.json")
	file2 := filepath.Join(dir, "file2.json")
	output := filepath.Join(dir, "output.json")

	data1 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}}}
	data2 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "b.com"}}}
	b1, _ := json.Marshal(data1)
	b2, _ := json.Marshal(data2)
	_ = os.WriteFile(file1, b1, 0644)
	_ = os.WriteFile(file2, b2, 0644)

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{"--merge", "-o", output, file1, file2})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	if !strings.Contains(stdout, "Merged") {
		t.Errorf("expected merge message, got %s", stdout)
	}

	raw, _ := os.ReadFile(output)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)
	if len(result.Available) != 2 {
		t.Errorf("expected 2 available domains, got %d", len(result.Available))
	}
}

// TestRunCLI_MergeIntoFirst tests merging into first file when no -o
func TestRunCLI_MergeIntoFirst(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "file1.json")
	file2 := filepath.Join(dir, "file2.json")

	data1 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}}}
	data2 := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "b.com"}}}
	b1, _ := json.Marshal(data1)
	b2, _ := json.Marshal(data2)
	_ = os.WriteFile(file1, b1, 0644)
	_ = os.WriteFile(file2, b2, 0644)

	_, _ = captureOutput(t, func() {
		code := RunCLI([]string{"--merge", file1, file2})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	raw, _ := os.ReadFile(file1)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)
	if len(result.Available) != 2 {
		t.Errorf("expected 2 available domains in file1, got %d", len(result.Available))
	}
}

// TestRunCLI_MergeError tests merge with insufficient files
func TestRunCLI_MergeError(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "file1.json")
	_ = os.WriteFile(file1, []byte("{}"), 0644)

	_, stderr := captureOutput(t, func() {
		code := RunCLI([]string{"--merge", file1})
		if code == 0 {
			t.Error("expected non-zero exit")
		}
	})

	if !strings.Contains(stderr, "requires at least 2 files") {
		t.Errorf("expected error about 2 files, got %s", stderr)
	}
}

// TestRunCLI_ExportAvailable tests the --export-available flag
func TestRunCLI_ExportAvailable(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "output.txt")

	data := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}, {Domain: "b.com"}}}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(input, b, 0644)

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{"--export-available=" + output, input})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	if !strings.Contains(stdout, "Exported 2") {
		t.Errorf("expected export message, got %s", stdout)
	}

	raw, _ := os.ReadFile(output)
	if !strings.Contains(string(raw), "a.com") || !strings.Contains(string(raw), "b.com") {
		t.Errorf("expected domains in output, got %s", string(raw))
	}
}

// TestRunCLI_Clean tests the --clean flag
func TestRunCLI_Clean(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.json")

	data := ExtendedGroupedData{
		Available:  []GroupedDomain{{Domain: "good.com"}},
		Unverified: []DomainRecord{{Domain: "invalid"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{"--clean", file})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	if !strings.Contains(stdout, "Removed 1") {
		t.Errorf("expected removal message, got %s", stdout)
	}
}

// TestRunCLI_LightspeedFlag tests the --lightspeed flag parsing
func TestRunCLI_LightspeedFlag(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_, _ = io.WriteString(c, "No match for domain")
				helperClose(nil, c, "conn")
			}(conn)
		}
	}()

	dir := t.TempDir()
	file := filepath.Join(dir, "domains.json")
	data := ExtendedGroupedData{Unverified: []DomainRecord{{Domain: "a.com"}, {Domain: "b.com"}}}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	stdout, _ := captureOutput(t, func() {
		code := RunCLI([]string{"--whois=" + ln.Addr().String(), "--lightspeed=2", file})
		if code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	if !strings.Contains(stdout, "a.com") || !strings.Contains(stdout, "b.com") {
		t.Errorf("expected domains in output, got %s", stdout)
	}
}

// TestCleanSuggestionsFile_InvalidInUnavailable tests invalid domains in unavailable section
func TestCleanSuggestionsFile_InvalidInUnavailable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.json")

	data := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "good.com"}},
		Unavailable: []GroupedDomain{{Domain: "valid.com"}, {Domain: "invalid-no-tld"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	removed, err := cleanSuggestionsFile(file)
	if err != nil {
		t.Fatalf("cleanSuggestionsFile error: %v", err)
	}

	if len(removed) != 1 || removed[0] != "invalid-no-tld" {
		t.Errorf("expected [invalid-no-tld] removed, got %v", removed)
	}
}

// TestMergeFiles_InvalidDomains tests that invalid domains are skipped in all sections
func TestMergeFiles_InvalidDomains(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input.json")
	output := filepath.Join(dir, "output.json")

	data := ExtendedGroupedData{
		Available:   []GroupedDomain{{Domain: "valid.com"}, {Domain: "invalid1"}},
		Unavailable: []GroupedDomain{{Domain: "also-valid.com"}, {Domain: "invalid2"}},
		Unverified:  []DomainRecord{{Domain: "third.com"}, {Domain: "invalid3"}},
	}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	count, err := mergeFiles(output, []string{file})
	if err != nil {
		t.Fatalf("mergeFiles error: %v", err)
	}

	// Should only count valid domains (3 total)
	if count != 3 {
		t.Errorf("expected 3 valid domains, got %d", count)
	}

	// Verify output has only valid domains
	raw, _ := os.ReadFile(output)
	var result ExtendedGroupedData
	_ = json.Unmarshal(raw, &result)

	if len(result.Available) != 1 || result.Available[0].Domain != "valid.com" {
		t.Errorf("expected 1 valid available domain, got %v", result.Available)
	}
	if len(result.Unavailable) != 1 || result.Unavailable[0].Domain != "also-valid.com" {
		t.Errorf("expected 1 valid unavailable domain, got %v", result.Unavailable)
	}
	if len(result.Unverified) != 1 || result.Unverified[0].Domain != "third.com" {
		t.Errorf("expected 1 valid unverified domain, got %v", result.Unverified)
	}
}

// TestMergeFiles_WriteError tests write error handling
func TestMergeFiles_WriteError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input.json")

	data := ExtendedGroupedData{Available: []GroupedDomain{{Domain: "a.com"}}}
	b, _ := json.Marshal(data)
	_ = os.WriteFile(file, b, 0644)

	// Try to write to a directory (should fail)
	_, err := mergeFiles(dir, []string{file})
	if err == nil {
		t.Error("expected write error")
	}
}

// startCountingWhois starts a WHOIS server that reports every domain as available
// and returns its address and a function reporting how many connections it accepted.
func startCountingWhois(t *testing.T) (string, func() int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var conns atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				_, _ = bufio.NewReader(c).ReadString('\n')
				_, _ = io.WriteString(c, "No match for domain")
				_ = c.Close()
			}(conn)
		}
	}()
	return ln.Addr().String(), conns.Load
}

func TestRunCLI_InvalidLightspeedIsRejected(t *testing.T) {
	cases := []struct {
		name, flagValue, envValue, wantInErr string
	}{
		{"word flag", "fast", "", `"fast"`},
		{"zero flag", "0", "", `"0"`},
		{"negative flag", "-3", "", `"-3"`},
		{"word env", "", "fast", "TALIA_LIGHTSPEED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, conns := startCountingWhois(t)
			file := filepath.Join(t.TempDir(), "domains.json")
			if err := os.WriteFile(file, []byte(`[{"domain":"a.com"}]`), 0644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TALIA_LIGHTSPEED", tc.envValue)

			args := []string{"--whois=" + addr}
			if tc.flagValue != "" {
				args = append(args, "--lightspeed="+tc.flagValue)
			}
			var code int
			_, stderr := captureOutput(t, func() { code = RunCLI(append(args, file)) })

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr, tc.wantInErr) || !strings.Contains(stderr, "max") {
				t.Errorf("stderr %q should mention %s and the valid forms", stderr, tc.wantInErr)
			}
			if n := conns(); n != 0 {
				t.Errorf("made %d WHOIS connections, want 0", n)
			}
		})
	}
}

func TestRunCLI_ValidLightspeedChecksAllDomains(t *testing.T) {
	for _, value := range []string{"", "max", "4"} {
		t.Run("value="+value, func(t *testing.T) {
			addr, conns := startCountingWhois(t)
			file := filepath.Join(t.TempDir(), "domains.json")
			if err := os.WriteFile(file, []byte(`[{"domain":"a.com"},{"domain":"b.com"}]`), 0644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TALIA_LIGHTSPEED", "")

			var code int
			captureOutput(t, func() {
				code = RunCLI([]string{"--whois=" + addr, "--sleep=0", "--lightspeed=" + value, file})
			})

			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if n := conns(); n != 2 {
				t.Errorf("made %d WHOIS connections, want 2", n)
			}
		})
	}
}
