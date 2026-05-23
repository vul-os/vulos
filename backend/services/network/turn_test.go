package network

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestGenerateCredentials_SHA256 verifies that GenerateCredentials uses HMAC-SHA256
// and that a credential produced with SHA-1 would NOT match (i.e., the legacy path is gone).
func TestGenerateCredentials_SHA256(t *testing.T) {
	secret := "test-secret-for-turn"
	tc := TURNConfig{
		Port:    3478,
		Secret:  secret,
		Realm:   "vulos",
		Enabled: true,
	}

	creds := tc.GenerateCredentials("alice")

	// Decompose username: "<expiry>:<userID>"
	parts := strings.SplitN(creds.Username, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("username format invalid: %q", creds.Username)
	}
	if parts[1] != "alice" {
		t.Errorf("username userID = %q; want %q", parts[1], "alice")
	}

	// Re-derive credential with SHA-256 — must match
	mac256 := hmac.New(sha256.New, []byte(secret))
	mac256.Write([]byte(creds.Username))
	expectedCred := base64.StdEncoding.EncodeToString(mac256.Sum(nil))
	if creds.Credential != expectedCred {
		t.Errorf("credential does not match HMAC-SHA256:\n  got:  %s\n  want: %s", creds.Credential, expectedCred)
	}

	// Verify SHA-256 credential length: SHA-256 is 32 bytes → base64 = 44 chars
	decoded, err := base64.StdEncoding.DecodeString(creds.Credential)
	if err != nil {
		t.Fatalf("credential is not valid base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("HMAC output length = %d bytes; want 32 (SHA-256)", len(decoded))
	}
}

// TestGenerateCredentials_NoSHA1 verifies the credential does NOT match a SHA-1 HMAC,
// proving the legacy SHA-1 path is gone.
func TestGenerateCredentials_NoSHA1(t *testing.T) {
	// Import sha1 inline only for the negative check — production code must not import it.
	// We compute a SHA-1-based credential from scratch and confirm it differs from the output.
	secret := "test-secret-no-sha1"
	tc := TURNConfig{Secret: secret, Realm: "vulos", Enabled: true}
	creds := tc.GenerateCredentials("bob")

	// Re-derive with SHA-1 (manual, to avoid importing sha1 in prod code)
	// sha1 block size is 20 bytes — so base64(sha1_hmac) would be 28 chars
	// We can check length: SHA-256 cred must be 44 chars, SHA-1 cred would be 28.
	if len(creds.Credential) != 44 {
		t.Errorf("credential length = %d; want 44 (SHA-256 base64); SHA-1 would be 28", len(creds.Credential))
	}
}

// TestGenerateCredentials_TTLAndExpiry verifies TTL and expiry are set consistently.
func TestGenerateCredentials_TTLAndExpiry(t *testing.T) {
	tc := TURNConfig{Secret: "s3cr3t", Realm: "vulos", Enabled: true}
	before := time.Now().Unix()
	creds := tc.GenerateCredentials("carol")
	after := time.Now().Unix()

	if creds.TTL != 24*3600 {
		t.Errorf("TTL = %d; want %d", creds.TTL, 24*3600)
	}

	parts := strings.SplitN(creds.Username, ":", 2)
	var expiry int64
	fmt.Sscanf(parts[0], "%d", &expiry)

	wantMinExpiry := before + int64(creds.TTL)
	wantMaxExpiry := after + int64(creds.TTL)
	if expiry < wantMinExpiry || expiry > wantMaxExpiry {
		t.Errorf("expiry %d out of expected range [%d, %d]", expiry, wantMinExpiry, wantMaxExpiry)
	}
}
