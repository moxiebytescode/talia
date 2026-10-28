package talia

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// WhoisClient abstracts a WHOIS lookup mechanism.
type WhoisClient interface {
	Lookup(domain string) (string, error)
}

// NetWhoisClient performs WHOIS lookups over TCP.
type NetWhoisClient struct {
	Server string
	// Timeout bounds connection setup, query writing, and response reading.
	// Values <= 0 use the default of 30 seconds.
	Timeout time.Duration
}

// Lookup queries the configured WHOIS server for the given domain and returns
// the raw response string.
func (c NetWhoisClient) Lookup(domain string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.Dial("tcp", c.Server)
	if err != nil {
		return "", fmt.Errorf("failed to connect to WHOIS: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "connection close error: %v\n", cerr)
		}
	}()

	if err := conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("set WHOIS deadline: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "%s\r\n", domain); err != nil {
		return "", fmt.Errorf("write WHOIS query: %w", err)
	}

	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}

	data, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, io.EOF) {
		// Treat connection reset by peer and similar errors as empty WHOIS response
		errStr := err.Error()
		if strings.Contains(errStr, "connection reset by peer") || strings.Contains(errStr, "broken pipe") || strings.Contains(errStr, "connection closed") {
			return "", fmt.Errorf("empty WHOIS response")
		}
		return "", fmt.Errorf("read error: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty WHOIS response")
	}
	return string(data), nil
}

// CheckDomainAvailabilityWithClient queries the WHOIS client and interprets the
// response to determine availability.
func CheckDomainAvailabilityWithClient(domain string, client WhoisClient) (bool, AvailabilityReason, string, error) {
	resp, err := client.Lookup(domain)
	if err == nil && resp == "" {
		err = fmt.Errorf("empty WHOIS response")
	}
	if err != nil {
		return false, ReasonError, err.Error(), err
	}
	if strings.Contains(resp, "No match for") {
		return true, ReasonNoMatch, resp, nil
	}
	return false, ReasonTaken, resp, nil
}

// CheckDomainAvailability queries a WHOIS server using NetWhoisClient.
func CheckDomainAvailability(domain, server string) (bool, AvailabilityReason, string, error) {
	return CheckDomainAvailabilityWithClient(domain, NetWhoisClient{Server: server})
}
