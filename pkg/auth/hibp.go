package auth

import (
	"bufio"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// hibpBaseURL is the HIBP Pwned Passwords range API base URL.
// Overridable for tests.
var hibpBaseURL = "https://api.pwnedpasswords.com/range"

// defaultHIBPClient is the http.Client used by CheckBreached when the caller
// passes nil. Tests inject their own client pointing at an httptest.Server.
var defaultHIBPClient = &http.Client{Timeout: 3 * time.Second}

// HIBPCheckEnabled reports whether the HIBP breach check is enabled.
// It reads the HIBP_CHECK_ENABLED environment variable (default "1").
// Set HIBP_CHECK_ENABLED=0 to disable (e.g. in tests).
func HIBPCheckEnabled() bool {
	v := os.Getenv("HIBP_CHECK_ENABLED")
	return v != "0"
}

// CheckBreached checks whether password appears in the HIBP Pwned Passwords
// corpus using the k-anonymity range API (only the first 5 hex chars of the
// SHA-1 hash are sent; the full hash never leaves the process).
//
// Returns (true, nil) when the password is in the breach corpus.
// Returns (false, nil) when the password is clean or when client is nil and
// HIBP_CHECK_ENABLED=0.
// Returns (false, err) on network / API errors so callers can fail-open.
//
// The client parameter is the http.Client to use; pass nil to use the
// package-level defaultHIBPClient (3-second timeout).
// The baseURL parameter overrides hibpBaseURL when non-empty (for tests).
func CheckBreached(ctx context.Context, client *http.Client, password string, baseURL string) (bool, error) {
	if !HIBPCheckEnabled() {
		return false, nil
	}

	if client == nil {
		client = defaultHIBPClient
	}
	if baseURL == "" {
		baseURL = hibpBaseURL
	}

	// SHA-1 hash the password; never send the raw password or full hash.
	h := sha1.Sum([]byte(password))
	full := strings.ToUpper(fmt.Sprintf("%x", h))
	prefix := full[:5]
	suffix := full[5:]

	url := baseURL + "/" + prefix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("hibp: build request: %w", err)
	}
	req.Header.Set("Add-Padding", "true") // request response padding for privacy

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("hibp: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hibp: unexpected status %d", resp.StatusCode)
	}

	return scanHIBPResponse(resp.Body, suffix)
}

// scanHIBPResponse reads the HIBP range response body and returns true if
// suffix (the remaining 35 hex chars of the SHA-1) appears in the list.
// The response is a newline-separated list of "SUFFIX:COUNT" lines.
func scanHIBPResponse(body io.Reader, suffix string) (bool, error) {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Each line is "HASH_SUFFIX:COUNT"; we only care about the suffix part.
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 0 {
			continue
		}
		if strings.EqualFold(parts[0], suffix) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("hibp: read response: %w", err)
	}
	return false, nil
}

// MaybeCheckBreached is the handler-layer helper: it runs the HIBP check and
// returns true if the caller should abort with a 422 password_breached error.
// On HIBP failure it logs and returns false (fail-open). When the check is
// disabled it returns false immediately.
func MaybeCheckBreached(ctx context.Context, client *http.Client, password, baseURL string) bool {
	if !HIBPCheckEnabled() {
		return false
	}
	breached, err := CheckBreached(ctx, client, password, baseURL)
	if err != nil {
		log.Printf("[auth] hibp: check failed (fail-open): %v", err)
		return false
	}
	return breached
}
