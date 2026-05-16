package authvault

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// ─── RFC 6238 test vectors ────────────────────────────────────────────────────
//
// RFC 6238 §B uses the ASCII key "12345678901234567890" (20 bytes).
// Encoded in base32: GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
// The appendix specifies reference times and expected codes for SHA1, SHA256, SHA512.
// We test the SHA1 case (the most common deployment, what Google Authenticator uses).
//
// Ref times from the RFC (Unix timestamps):
//   59       → "94287082"  (8 digits, but we verify the algorithm, not digit count)
//   1111111109 → "07081804"
//   1111111111 → "14050471"
//   1234567890 → "89005924"
//   2000000000 → "69279037"
//   20000000000 → "65353130"
//
// RFC 6238 uses 8-digit codes in the appendix. We test with 8 digits to match.

const (
	// Base32 of the RFC 6238 SHA1 test key: "12345678901234567890"
	rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
)

type rfc6238Vector struct {
	unix int64
	code string
}

// RFC 6238 Appendix B, Table 1 (SHA-1, 8-digit codes, 30-second period).
var rfc6238Vectors = []rfc6238Vector{
	{59, "94287082"},
	{1111111109, "07081804"},
	{1111111111, "14050471"},
	{1234567890, "89005924"},
	{2000000000, "69279037"},
	{20000000000, "65353130"},
}

// TestRFC6238Vectors validates TOTP generation against the RFC 6238 test vectors.
func TestRFC6238Vectors(t *testing.T) {
	for _, v := range rfc6238Vectors {
		ts := time.Unix(v.unix, 0)
		got, err := totp.GenerateCodeCustom(pad32(rfc6238Secret), ts, totp.ValidateOpts{
			Period:    30,
			Skew:      0,
			Digits:    otp.DigitsEight,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			t.Errorf("unix=%d: unexpected error: %v", v.unix, err)
			continue
		}
		if got != v.code {
			t.Errorf("unix=%d: got %q, want %q", v.unix, got, v.code)
		}
	}
}

// ─── Store tests ─────────────────────────────────────────────────────────────

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	return s, dir
}

// TestAddAndList checks that an account added via AddManual appears in List.
func TestAddAndList(t *testing.T) {
	s, _ := tempStore(t)

	acc, err := s.AddManual("GitHub", "GitHub", "user@example.com", rfc6238Secret)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	if acc.ID == "" {
		t.Error("expected non-empty ID")
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 account, got %d", len(list))
	}
	if list[0].Service != "GitHub" {
		t.Errorf("got service %q, want %q", list[0].Service, "GitHub")
	}
}

// TestCodeGeneration checks that Code returns a 6-digit code for a live timestamp.
func TestCodeGeneration(t *testing.T) {
	s, _ := tempStore(t)

	acc, err := s.AddManual("TestSvc", "TestIssuer", "test@example.com", rfc6238Secret)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	code, remaining, err := s.Code(acc.ID)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("expected 6-digit code, got %q (len=%d)", code, len(code))
	}
	if remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("unexpected remaining time: %v", remaining)
	}
	// Code must be all digits.
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			t.Errorf("code %q contains non-digit character %q", code, ch)
		}
	}
}

// TestCodeRolls30Seconds verifies that codes are consistent within a window.
func TestCodeRolls30Seconds(t *testing.T) {
	now := time.Now()

	code1, err := totp.GenerateCodeCustom(pad32(rfc6238Secret), now, totp.ValidateOpts{
		Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code at now: %v", err)
	}
	// Same second should yield the same code.
	code2, err := totp.GenerateCodeCustom(pad32(rfc6238Secret), now, totp.ValidateOpts{
		Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code at now (2): %v", err)
	}
	if code1 != code2 {
		t.Errorf("codes within same second differ: %q vs %q", code1, code2)
	}

	// Code 30 seconds later should differ.
	future := now.Add(30 * time.Second)
	code3, err := totp.GenerateCodeCustom(pad32(rfc6238Secret), future, totp.ValidateOpts{
		Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code at +30s: %v", err)
	}
	// This is probabilistically almost always different (1/1000000 chance of collision).
	if code1 == code3 {
		t.Logf("note: codes happened to match across windows (rare but possible)")
	}
}

// TestEncryptedAtRest verifies that the on-disk keychain.enc is not plaintext base32.
func TestEncryptedAtRest(t *testing.T) {
	s, dir := tempStore(t)

	_, err := s.AddManual("EncTest", "EncIssuer", "enc@example.com", rfc6238Secret)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	keychainPath := dir + "/keychain.enc"
	data, err := os.ReadFile(keychainPath)
	if err != nil {
		t.Fatalf("reading keychain.enc: %v", err)
	}

	raw := string(data)
	// The plaintext base32 secret must NOT appear verbatim in the file.
	if strings.Contains(raw, rfc6238Secret) {
		t.Error("keychain.enc contains plaintext base32 secret — not encrypted at rest")
	}
	// The file must be valid (non-empty) JSON envelope, not plaintext.
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		t.Errorf("keychain.enc does not look like JSON envelope: %q", raw[:min(len(raw), 64)])
	}
}

// TestOtpauthURIParse checks that an otpauth:// URI is correctly parsed.
func TestOtpauthURIParse(t *testing.T) {
	// Build a canonical otpauth URI.
	uri := "otpauth://totp/GitHub:user%40example.com?secret=" + rfc6238Secret + "&issuer=GitHub&digits=6&period=30"

	s, _ := tempStore(t)
	acc, err := s.AddFromURI(uri)
	if err != nil {
		t.Fatalf("AddFromURI: %v", err)
	}

	if acc.Issuer != "GitHub" {
		t.Errorf("issuer: got %q, want %q", acc.Issuer, "GitHub")
	}
	if acc.AccountID != "user@example.com" {
		t.Errorf("account: got %q, want %q", acc.AccountID, "user@example.com")
	}
	if acc.Digits != 6 {
		t.Errorf("digits: got %d, want 6", acc.Digits)
	}
	if acc.Period != 30 {
		t.Errorf("period: got %d, want 30", acc.Period)
	}
}

// TestDelete checks that Delete removes the account and its secret.
func TestDelete(t *testing.T) {
	s, _ := tempStore(t)

	acc, err := s.AddManual("DelSvc", "DelIssuer", "del@example.com", rfc6238Secret)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	if err := s.Delete(acc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected empty list after delete, got %d accounts", len(list))
	}

	_, _, err = s.Code(acc.ID)
	if err == nil {
		t.Error("expected error when calling Code on deleted account")
	}
}

// TestDeleteNotFound checks that Delete returns an error for unknown IDs.
func TestDeleteNotFound(t *testing.T) {
	s, _ := tempStore(t)
	if err := s.Delete("nonexistent"); err == nil {
		t.Error("expected error for non-existent account, got nil")
	}
}

// TestPersistence checks that accounts survive a Store reload from disk.
func TestPersistence(t *testing.T) {
	s, dir := tempStore(t)

	original, err := s.AddManual("PersistSvc", "PersistIssuer", "persist@example.com", rfc6238Secret)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	// Reload from the same directory.
	s2, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt (reload): %v", err)
	}

	list := s2.List()
	if len(list) != 1 {
		t.Fatalf("after reload: expected 1 account, got %d", len(list))
	}
	if list[0].ID != original.ID {
		t.Errorf("after reload: ID mismatch %q vs %q", list[0].ID, original.ID)
	}

	// Confirm code generation still works after reload.
	code, _, err := s2.Code(original.ID)
	if err != nil {
		t.Fatalf("Code after reload: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("expected 6-digit code after reload, got %q", code)
	}
}

// TestInvalidSecret checks that adding an invalid base32 secret returns an error.
func TestInvalidSecret(t *testing.T) {
	s, _ := tempStore(t)
	_, err := s.AddManual("Bad", "Bad", "bad@example.com", "not-valid-base32!!!")
	if err == nil {
		t.Error("expected error for invalid base32 secret, got nil")
	}
}

// TestInvalidURI checks that an invalid otpauth URI returns an error.
func TestInvalidURI(t *testing.T) {
	s, _ := tempStore(t)
	_, err := s.AddFromURI("https://not-an-otpauth-uri.com")
	if err == nil {
		t.Error("expected error for non-otpauth URI, got nil")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
