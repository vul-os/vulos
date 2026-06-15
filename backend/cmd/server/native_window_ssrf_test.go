package main

// native_window_ssrf_test.go — SSRF regression tests for the
// POST /api/shell/native-window URL validator (VULN-07).

import (
	"testing"
)

func TestValidateNativeWindowURL_Blocked(t *testing.T) {
	blocked := []struct {
		url  string
		desc string
	}{
		{"file:///etc/passwd", "file scheme"},
		{"ftp://example.com/file", "ftp scheme"},
		{"http://localhost/", "localhost"},
		{"http://127.0.0.1/", "loopback IPv4"},
		{"http://0.0.0.0/", "unspecified"},
		{"http://169.254.169.254/latest/meta-data/", "AWS metadata"},
		{"http://192.168.1.1/", "private network"},
		{"http://10.0.0.1/", "private network 10.x"},
		{"not-a-url", "not a URL"},
	}
	for _, tc := range blocked {
		err := validateNativeWindowURL(tc.url)
		if err == nil {
			t.Errorf("validateNativeWindowURL(%q) [%s]: expected error, got nil", tc.url, tc.desc)
		}
	}
}

func TestValidateNativeWindowURL_Allowed(t *testing.T) {
	// Only exercise URL-structure validation (scheme + parse); DNS resolution
	// is network-dependent so we avoid URLs whose hosts may not resolve in CI.
	// The happy-path DNS path is covered by integration tests.
	// Use example.com which is guaranteed to resolve per RFC 2606.
	allowed := []string{
		"https://example.com",
		"http://example.com/path?q=1",
	}
	for _, u := range allowed {
		err := validateNativeWindowURL(u)
		if err != nil {
			t.Errorf("validateNativeWindowURL(%q): unexpected error: %v", u, err)
		}
	}
}
