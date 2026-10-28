package talia

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeWhoisClient struct {
	resp string
	err  error
}

func (f fakeWhoisClient) Lookup(domain string) (string, error) {
	return f.resp, f.err
}

// TestCheckDomainAvailabilityWithClient verifies raw responses map to the
// expected availability reason, with failed and empty responses treated as ERROR.
func TestCheckDomainAvailabilityWithClient(t *testing.T) {
	cases := []struct {
		name       string
		client     WhoisClient
		wantAvail  bool
		wantReason AvailabilityReason
		wantErr    bool
	}{
		{
			name:       "available",
			client:     fakeWhoisClient{resp: "No match for example.com"},
			wantAvail:  true,
			wantReason: ReasonNoMatch,
		},
		{
			name:       "taken",
			client:     fakeWhoisClient{resp: "Domain Name: example.com"},
			wantAvail:  false,
			wantReason: ReasonTaken,
		},
		{
			name:       "error",
			client:     fakeWhoisClient{err: errors.New("dial fail")},
			wantAvail:  false,
			wantReason: ReasonError,
			wantErr:    true,
		},
		{
			name:       "empty response",
			client:     fakeWhoisClient{},
			wantReason: ReasonError,
			wantErr:    true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			avail, reason, _, err := CheckDomainAvailabilityWithClient("example.com", tt.client)
			if avail != tt.wantAvail {
				t.Errorf("avail=%v want %v", avail, tt.wantAvail)
			}
			if reason != tt.wantReason {
				t.Errorf("reason=%s want %s", reason, tt.wantReason)
			}
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestNetWhoisClientBoundsStalledResponses verifies the deadline covers silent
// and partial responses, including partial text that otherwise looks available.
func TestNetWhoisClientBoundsStalledResponses(t *testing.T) {
	for _, response := range []string{"", "No match for example.com"} {
		t.Run(response, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			stop := make(chan struct{})
			t.Cleanup(func() { close(stop) })
			query := make(chan string, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				query <- line
				_, _ = io.WriteString(conn, response)
				<-stop // a peer that never closes its response
			}()
			client := NetWhoisClient{Server: ln.Addr().String(), Timeout: 50 * time.Millisecond}
			available, reason, log, err := CheckDomainAvailabilityWithClient("example.com", client)
			var networkError net.Error
			if available || reason != ReasonError || log == "" || !errors.As(err, &networkError) || !networkError.Timeout() {
				t.Fatalf("stalled check = %v, %s, %q, %v", available, reason, log, err)
			}
			if got := <-query; got != "example.com\r\n" {
				t.Fatalf("WHOIS query = %q", got)
			}
		})
	}
}

func TestNetWhoisClientLookupSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			_, _ = io.WriteString(conn, "Hello")
			helperClose(nil, conn, "conn")
		}
	}()

	c := NetWhoisClient{Server: ln.Addr().String()}
	resp, err := c.Lookup("example.com")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if resp != "Hello" {
		t.Fatalf("got %q want %q", resp, "Hello")
	}
}

func TestNetWhoisClientLookupDialError(t *testing.T) {
	c := NetWhoisClient{Server: "127.0.0.1:1"}
	if _, err := c.Lookup("example.com"); err == nil {
		t.Fatal("expected dial error, got nil")
	}
}

// TestNetWhoisClientLookupEmpty verifies a peer that accepts the query but sends
// no response returns an empty-response error.
func TestNetWhoisClientLookupEmpty(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer helperClose(t, ln, "listener")
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
			helperClose(nil, conn, "conn")
		}
	}()

	c := NetWhoisClient{Server: ln.Addr().String()}
	_, err = c.Lookup("example.com")
	if err == nil || !strings.Contains(err.Error(), "empty WHOIS") {
		t.Fatalf("expected empty response error, got %v", err)
	}
}
