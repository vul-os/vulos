package appsplatform

import (
	"encoding/hex"
	"strings"
	"testing"
)

// These tests harden token / secret minting (tokens.go): the vat_/vas_ prefixes,
// the CSPRNG length/entropy, hashing-at-rest, and uniqueness at scale.

// TestTokenAndSecretPrefixes locks the wire prefixes the rest of the platform
// (and clients) match on.
func TestTokenAndSecretPrefixes(t *testing.T) {
	if tok := GenerateToken(); !strings.HasPrefix(tok, "vat_") {
		t.Fatalf("app token must start with vat_, got %q", tok)
	}
	if sec := GenerateSecret(); !strings.HasPrefix(sec, "vas_") {
		t.Fatalf("signing secret must start with vas_, got %q", sec)
	}
}

// TestTokenEntropyLength asserts each secret carries 32 random bytes (64 hex
// chars) of entropy after its prefix, and ids 16 bytes (32 hex).
func TestTokenEntropyLength(t *testing.T) {
	if body := strings.TrimPrefix(GenerateToken(), "vat_"); len(body) != 64 {
		t.Fatalf("app token entropy = %d hex chars, want 64 (32 bytes)", len(body))
	}
	if body := strings.TrimPrefix(GenerateSecret(), "vas_"); len(body) != 64 {
		t.Fatalf("signing secret entropy = %d hex chars, want 64", len(body))
	}
	for _, id := range []string{GenerateWebhookID(), GenerateAppID()} {
		if len(id) != 32 {
			t.Fatalf("id entropy = %d hex chars, want 32 (16 bytes)", len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("id is not valid hex: %v", err)
		}
	}
}

// TestTokensAreHexDecodable confirms the entropy is valid hex (no truncation /
// encoding bug that would shrink the keyspace).
func TestTokensAreHexDecodable(t *testing.T) {
	for _, body := range []string{
		strings.TrimPrefix(GenerateToken(), "vat_"),
		strings.TrimPrefix(GenerateSecret(), "vas_"),
	} {
		b, err := hex.DecodeString(body)
		if err != nil {
			t.Fatalf("entropy not hex: %v", err)
		}
		if len(b) != 32 {
			t.Fatalf("decoded entropy = %d bytes, want 32", len(b))
		}
	}
}

// TestTokenUniquenessAtScale mints many tokens and asserts no collisions — a
// crude but effective CSPRNG smoke test (a stuck/seeded generator would repeat).
func TestTokenUniquenessAtScale(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		for _, v := range []string{GenerateToken(), GenerateSecret(), GenerateWebhookID(), GenerateAppID()} {
			if _, dup := seen[v]; dup {
				t.Fatalf("duplicate secret/id minted: %q", v)
			}
			seen[v] = struct{}{}
		}
	}
}

// TestHashTokenIsSHA256Hex pins the at-rest representation: sha256 hex, 64 chars,
// and never equal to the plaintext (so a DB leak does not hand out live tokens).
func TestHashTokenIsSHA256Hex(t *testing.T) {
	tok := GenerateToken()
	h := HashToken(tok)
	if len(h) != 64 {
		t.Fatalf("HashToken length = %d, want 64 (sha256 hex)", len(h))
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatalf("HashToken output is not hex: %v", err)
	}
	if h == tok || strings.Contains(h, tok) {
		t.Fatal("hash leaks the plaintext token")
	}
}

// TestHashTokenAvalanche confirms a one-char change in the token produces a
// wholly different hash (no prefix-preserving structure to exploit).
func TestHashTokenAvalanche(t *testing.T) {
	a := HashToken("vat_aaaa")
	b := HashToken("vat_aaab")
	if a == b {
		t.Fatal("distinct tokens hashed equal")
	}
	same := 0
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] == b[i] {
			same++
		}
	}
	if same > len(a)/2 {
		t.Fatalf("hashes share %d/%d chars — weak diffusion", same, len(a))
	}
}

// TestCreatedSecretsMatchStoredHash ties minting to storage: the plaintext token
// returned at create hashes to what the registry will look up, and the signing
// secret is returned verbatim (it must be reproducible to sign events).
func TestCreatedSecretsMatchStoredHash(t *testing.T) {
	r := NewMemoryRegistry()
	c, err := r.Create(CreateParams{Name: "x", Products: []string{ProductOffice}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Token, "vat_") || !strings.HasPrefix(c.SigningSecret, "vas_") {
		t.Fatalf("create returned malformed secrets: %q / %q", c.Token, c.SigningSecret)
	}
	got, err := r.GetByTokenHash(HashToken(c.Token))
	if err != nil {
		t.Fatalf("token does not hash to the stored value: %v", err)
	}
	if got.ID != c.App.ID {
		t.Fatal("token hash resolved to the wrong app")
	}
	// The plaintext token itself must NOT be queryable (only its hash).
	if _, err := r.GetByTokenHash(c.Token); err != ErrNotFound {
		t.Fatal("registry resolved a raw (unhashed) token — hashing bypassed")
	}
}
