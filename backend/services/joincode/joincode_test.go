package joincode

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// setupHome creates a temporary home directory for a test and returns its path.
// It also writes a minimal storage.json so Issue can find credentials.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write a minimal storage.json (storageprov-managed shape)
	cfg := storageConfig{
		Bucket:    "test-bucket",
		Region:    "us-east-1",
		AccessKey: "AKIATEST",
		SecretKey: "supersecret",
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dbDir, "storage.json"), data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return home
}

// shortCodeRe matches VULOS-XXXX-XXXX-XXXX where X is from the Crockford alphabet.
var shortCodeRe = regexp.MustCompile(`^VULOS-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// TestIssue_ShortCodeFormat verifies the VULOS-XXXX-XXXX-XXXX format.
func TestIssue_ShortCodeFormat(t *testing.T) {
	home := setupHome(t)
	jc, code, err := Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if jc == nil {
		t.Fatal("Issue returned nil JoinCode")
	}
	if !shortCodeRe.MatchString(code) {
		t.Errorf("short code %q does not match VULOS-XXXX-XXXX-XXXX format", code)
	}
}

// TestIssue_CredentialsFromStorage checks that the JoinCode carries the S3 creds from storage.json.
func TestIssue_CredentialsFromStorage(t *testing.T) {
	home := setupHome(t)
	jc, _, err := Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if jc.Bucket != "test-bucket" {
		t.Errorf("Bucket: got %q, want %q", jc.Bucket, "test-bucket")
	}
	if jc.Region != "us-east-1" {
		t.Errorf("Region: got %q, want %q", jc.Region, "us-east-1")
	}
	if jc.AccessKey != "AKIATEST" {
		t.Errorf("AccessKey: got %q, want %q", jc.AccessKey, "AKIATEST")
	}
	if jc.SecretKey != "supersecret" {
		t.Errorf("SecretKey: got %q, want %q", jc.SecretKey, "supersecret")
	}
}

// TestIssue_StorageJsonAbsent verifies graceful handling when storage.json is missing.
func TestIssue_StorageJsonAbsent(t *testing.T) {
	home := t.TempDir()
	// No storage.json — Issue should succeed with empty creds, not error.
	jc, code, err := Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("Issue with absent storage.json: %v", err)
	}
	if jc == nil {
		t.Fatal("Issue returned nil JoinCode")
	}
	if !shortCodeRe.MatchString(code) {
		t.Errorf("short code %q does not match pattern", code)
	}
	// Creds are empty strings — that's acceptable (cluster may be unconfigured).
	if jc.Bucket != "" || jc.AccessKey != "" {
		t.Errorf("expected empty creds when storage.json absent, got bucket=%q access=%q", jc.Bucket, jc.AccessKey)
	}
}

// TestDecode_Valid verifies that a valid code can be redeemed once.
func TestDecode_Valid(t *testing.T) {
	home := setupHome(t)
	_, code, err := Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	jc, err := Decode(home, code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if jc.Bucket != "test-bucket" {
		t.Errorf("Bucket: got %q, want %q", jc.Bucket, "test-bucket")
	}
}

// TestDecode_OneTimeUse verifies that a code cannot be decoded twice.
func TestDecode_OneTimeUse(t *testing.T) {
	home := setupHome(t)
	_, code, err := Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := Decode(home, code); err != nil {
		t.Fatalf("first Decode: %v", err)
	}

	_, err = Decode(home, code)
	if err != ErrInvalid {
		t.Errorf("second Decode: got %v, want ErrInvalid", err)
	}
}

// TestDecode_Expired verifies that expired codes are rejected.
func TestDecode_Expired(t *testing.T) {
	home := setupHome(t)
	_, code, err := Issue(home, -1*time.Second) // already expired
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = Decode(home, code)
	if err != ErrExpired {
		t.Errorf("Decode expired: got %v, want ErrExpired", err)
	}
}

// TestDecode_Invalid verifies that non-existent codes are rejected.
func TestDecode_Invalid(t *testing.T) {
	home := setupHome(t)
	_, err := Decode(home, "VULA-0000-0000-0000")
	if err != ErrInvalid {
		t.Errorf("Decode invalid: got %v, want ErrInvalid", err)
	}
}

// TestQRPayload verifies the vulos://join/v1 URL format and field presence.
func TestQRPayload(t *testing.T) {
	exp := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	jc := &JoinCode{
		Bucket:    "my-bucket",
		Region:    "eu-west-1",
		AccessKey: "AKID",
		SecretKey: "SK",
		ExpiresAt: exp,
	}
	payload := QRPayload(jc)

	const prefix = "vulos://join/v1?"
	if !strings.HasPrefix(payload, prefix) {
		t.Errorf("QRPayload prefix wrong: %q", payload)
	}

	// Parse the query string to check values properly (url.Values.Encode URL-encodes colons).
	qs, err := url.ParseQuery(strings.TrimPrefix(payload, prefix))
	if err != nil {
		t.Fatalf("QRPayload parse: %v", err)
	}
	for _, tc := range []struct{ key, want string }{
		{"bucket", "my-bucket"},
		{"region", "eu-west-1"},
		{"access", "AKID"},
		{"secret", "SK"},
		{"exp", exp.UTC().Format(time.RFC3339)},
	} {
		if got := qs.Get(tc.key); got != tc.want {
			t.Errorf("QRPayload[%s]: got %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestShortCodeUniqueness issues multiple codes and verifies no duplicates.
func TestShortCodeUniqueness(t *testing.T) {
	home := setupHome(t)
	seen := make(map[string]bool)
	const n = 20
	for i := 0; i < n; i++ {
		_, code, err := Issue(home, time.Hour)
		if err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
		if seen[code] {
			t.Errorf("duplicate short code: %q", code)
		}
		seen[code] = true
	}
}

// TestCrockfordAlphabet verifies that I, L, O are absent from the alphabet (anti-confusion).
func TestCrockfordAlphabet(t *testing.T) {
	for _, ch := range []byte{'I', 'L', 'O'} {
		if strings.ContainsRune(crockfordAlphabet, rune(ch)) {
			t.Errorf("crockfordAlphabet must not contain %q", ch)
		}
	}
}
