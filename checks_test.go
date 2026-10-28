package talia

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCLICheckScheduling(t *testing.T) {
	for _, tc := range []struct {
		name, workers string
		overlap       int
		verbose       bool
	}{
		{"sequential", "", 1, false},
		{"limited", "2", 2, false},
		{"oversized", "20", 3, true},
		{"max", "max", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			arrived := make(chan struct{}, 3)
			release := make(chan struct{})
			var active, peak atomic.Int64
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer func() { _ = conn.Close() }()
						domain, _ := bufio.NewReader(conn).ReadString('\n')
						current := active.Add(1)
						for old := peak.Load(); current > old; old = peak.Load() {
							if peak.CompareAndSwap(old, current) {
								break
							}
						}
						arrived <- struct{}{}
						<-release
						active.Add(-1)
						switch strings.TrimSpace(domain) {
						case "a.com":
							_, _ = io.WriteString(conn, "No match for a.com")
						case "b.com":
							_, _ = io.WriteString(conn, "Domain Name: B.COM")
						}
					}()
				}
			}()
			observed := make(chan bool, 1)
			go func() {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				defer close(release)
				for range tc.overlap {
					select {
					case <-arrived:
					case <-timer.C:
						observed <- false
						return
					}
				}
				observed <- true
			}()
			path := filepath.Join(t.TempDir(), "domains.json")
			writeTestFile(t, path, `[{"domain":"a.com"},{"domain":"b.com"},{"domain":"c.com"}]`)
			args := []string{"--whois=" + ln.Addr().String(), "--sleep=0", "--lightspeed=" + tc.workers}
			if tc.verbose {
				args = append(args, "--verbose")
			}
			if code := RunCLI(append(args, path)); code != 0 {
				t.Fatalf("exit = %d", code)
			}
			if !<-observed || peak.Load() != int64(tc.overlap) {
				t.Errorf("concurrent checks = %d, want %d", peak.Load(), tc.overlap)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var records []DomainRecord
			if err := json.Unmarshal(raw, &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 3 {
				t.Fatalf("records = %#v", records)
			}
			for i, reason := range []AvailabilityReason{ReasonNoMatch, ReasonTaken, ReasonError} {
				rec := records[i]
				if rec.Domain != []string{"a.com", "b.com", "c.com"}[i] || rec.Reason != reason || rec.Available != (i == 0) {
					t.Errorf("record %d = %#v", i, rec)
				}
				if (rec.Log != "") != (tc.verbose || reason == ReasonError) {
					t.Errorf("unexpected log policy for %#v", rec)
				}
			}
		})
	}
}
