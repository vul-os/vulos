package auth

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// sha1HexUpper returns the full uppercase hex SHA-1 of s.
func sha1HexUpper(s string) string {
	h := sha1.Sum([]byte(s))
	return strings.ToUpper(fmt.Sprintf("%x", h))
}

// hibpServer builds a fake HIBP range endpoint. It responds to
// GET /{5-char-prefix} with a newline-separated list of "SUFFIX:COUNT" lines.
// knownHashes maps full 40-char uppercase hex SHA-1s that should appear
// breached (count=1) in the response.
func hibpServer(t *testing.T, knownHashes []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract the 5-char prefix from the URL path (e.g. "/ABCDE" → "ABCDE").
		prefix := strings.TrimPrefix(r.URL.Path, "/")
		if len(prefix) != 5 {
			http.Error(w, "bad prefix", http.StatusBadRequest)
			return
		}
		prefix = strings.ToUpper(prefix)
		var lines []string
		for _, full := range knownHashes {
			full = strings.ToUpper(full)
			if strings.HasPrefix(full, prefix) {
				lines = append(lines, full[5:]+":1")
			}
		}
		w.WriteHeader(http.StatusOK)
		if len(lines) > 0 {
			fmt.Fprintln(w, strings.Join(lines, "\n"))
		}
	}))
}

// hibpServer5xx builds a fake HIBP endpoint that always returns 500.
func hibpServer5xx(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit tests for CheckBreached
// ─────────────────────────────────────────────────────────────────────────────

// TestHIBP_BreachedPassword verifies that a password present in the mock corpus
// is detected and CheckBreached returns (true, nil).
func TestHIBP_BreachedPassword(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	password := "correcthorsebatterystaple"
	fullHash := sha1HexUpper(password)

	srv := hibpServer(t, []string{fullHash})
	defer srv.Close()

	client := srv.Client()
	breached, err := CheckBreached(context.Background(), client, password, srv.URL)
	if err != nil {
		t.Fatalf("CheckBreached returned unexpected error: %v", err)
	}
	if !breached {
		t.Error("expected breached=true for known-breached password")
	}
}

// TestHIBP_CleanPassword verifies that a password NOT in the corpus returns
// (false, nil).
func TestHIBP_CleanPassword(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	// A clean password whose hash won't be in the known list.
	cleanPassword := "vulos-unique-test-password-xyz-9887766"
	// The server has a different password's hash, not the clean one.
	otherHash := sha1HexUpper("totally-different-password-12345")

	srv := hibpServer(t, []string{otherHash})
	defer srv.Close()

	client := srv.Client()
	breached, err := CheckBreached(context.Background(), client, cleanPassword, srv.URL)
	if err != nil {
		t.Fatalf("CheckBreached returned unexpected error: %v", err)
	}
	if breached {
		t.Error("expected breached=false for clean password")
	}
}

// TestHIBP_EndpointFailure verifies that a 5xx response from HIBP causes
// CheckBreached to return (false, non-nil error) — so callers can fail-open.
func TestHIBP_EndpointFailure(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	srv := hibpServer5xx(t)
	defer srv.Close()

	client := srv.Client()
	breached, err := CheckBreached(context.Background(), client, "somepassword1234", srv.URL)
	if err == nil {
		t.Error("expected error on 5xx response from HIBP")
	}
	if breached {
		t.Error("expected breached=false on error")
	}
}

// TestHIBP_Disabled verifies that when HIBP_CHECK_ENABLED=0, CheckBreached
// returns (false, nil) immediately without making any HTTP request.
func TestHIBP_Disabled(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "0")

	// Use a server that would return breached=true if contacted.
	password := "correcthorsebatterystaple"
	fullHash := sha1HexUpper(password)
	srv := hibpServer(t, []string{fullHash})
	defer srv.Close()

	// Even though the server would return the hash, the check is disabled.
	breached, err := CheckBreached(context.Background(), srv.Client(), password, srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if breached {
		t.Error("expected breached=false when HIBP_CHECK_ENABLED=0")
	}
}

// TestHIBP_MaybeCheckBreached_FailOpen verifies that MaybeCheckBreached
// returns false (don't block) when HIBP returns an error.
func TestHIBP_MaybeCheckBreached_FailOpen(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	srv := hibpServer5xx(t)
	defer srv.Close()

	// Should NOT block signup even though HIBP call failed.
	block := MaybeCheckBreached(context.Background(), srv.Client(), "somepassword1234", srv.URL)
	if block {
		t.Error("MaybeCheckBreached must return false (fail-open) when HIBP errors")
	}
}

// TestHIBP_MaybeCheckBreached_Disabled verifies that MaybeCheckBreached
// returns false when HIBP_CHECK_ENABLED=0.
func TestHIBP_MaybeCheckBreached_Disabled(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "0")

	password := "hunter2breachedpassword"
	fullHash := sha1HexUpper(password)
	srv := hibpServer(t, []string{fullHash})
	defer srv.Close()

	block := MaybeCheckBreached(context.Background(), srv.Client(), password, srv.URL)
	if block {
		t.Error("MaybeCheckBreached must return false when disabled")
	}
}

// TestHIBP_KAnonymity verifies that CheckBreached never sends the full hash
// by using a server that records what prefix it received and checking it is
// exactly 5 characters.
func TestHIBP_KAnonymity(t *testing.T) {
	t.Setenv("HIBP_CHECK_ENABLED", "1")

	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	password := "testpassword1234longvalue"
	_, _ = CheckBreached(context.Background(), srv.Client(), password, srv.URL)

	// Path is "/<PREFIX>"; strip leading slash.
	prefix := strings.TrimPrefix(receivedPath, "/")
	if len(prefix) != 5 {
		t.Errorf("HIBP request path should contain exactly 5-char prefix, got %q (len=%d)", prefix, len(prefix))
	}

	// Verify the 5-char prefix matches the SHA-1 of the password.
	fullHash := sha1HexUpper(password)
	if !strings.EqualFold(prefix, fullHash[:5]) {
		t.Errorf("sent prefix %q, want %q", prefix, fullHash[:5])
	}

	// Verify the full hash is NOT in the path.
	if strings.Contains(receivedPath, fullHash) {
		t.Error("full SHA-1 hash must never be sent to HIBP API")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Restore HIBP_CHECK_ENABLED between test runs using t.Setenv (auto-restored).
// ─────────────────────────────────────────────────────────────────────────────

func init() {
	// Default to disabled in the test binary so existing tests that don't set
	// the env are unaffected. Individual HIBP tests explicitly set it to "1".
	if os.Getenv("HIBP_CHECK_ENABLED") == "" {
		os.Setenv("HIBP_CHECK_ENABLED", "0")
	}
}
