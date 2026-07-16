package oauthprovider

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// testKEK32 returns a deterministic non-zero 32-byte key, base64-encoded.
func testKEK32(seed byte) (raw []byte, b64 string) {
	raw = make([]byte, 32)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return raw, base64.StdEncoding.EncodeToString(raw)
}

// TestSigningKeyEncryptedAtRest verifies the id_token signing private key is
// stored as AES-256-GCM ciphertext (never plaintext PEM) and round-trips on
// reload (audit M6).
func TestSigningKeyEncryptedAtRest(t *testing.T) {
	raw, b64 := testKEK32(1)
	t.Setenv("INTEGRATIONS_KEK", b64)
	t.Setenv("VULOS_DEV", "")

	st := newTestStore(t)
	ctx := context.Background()

	k, err := st.LoadOrCreateSigningKey(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateSigningKey: %v", err)
	}

	var stored string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT private_pem FROM oauth_signing_keys WHERE kid = ?`, k.KID).Scan(&stored); err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if strings.Contains(stored, "BEGIN") {
		t.Fatalf("private key stored in plaintext at rest: %q", stored[:min(40, len(stored))])
	}
	// The stored ciphertext must decrypt back to a valid PKCS#8 PEM under the KEK.
	plain, err := decryptKEK(stored, raw)
	if err != nil {
		t.Fatalf("decryptKEK: %v", err)
	}
	if !strings.Contains(plain, "PRIVATE KEY") {
		t.Fatalf("decrypted value is not a PEM private key")
	}

	// Reload returns the same key (same KID) so previously-issued id_tokens verify.
	k2, err := st.LoadOrCreateSigningKey(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if k2.KID != k.KID {
		t.Fatalf("KID changed across reload: %s != %s", k2.KID, k.KID)
	}
}

// TestOpenStore_FailsClosedWithoutKEK verifies that in production mode
// (VULOS_DEV unset) an unset INTEGRATIONS_KEK makes OpenStore refuse to boot,
// rather than silently persisting the id_token signing key in plaintext.
func TestOpenStore_FailsClosedWithoutKEK(t *testing.T) {
	t.Setenv("INTEGRATIONS_KEK", "")
	t.Setenv("VULOS_DEV", "")

	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	defer db.Close() //nolint:errcheck
	st, err := Open(db)
	if err == nil {
		if st != nil {
			st.Close() //nolint:errcheck
		}
		t.Fatal("Open must fail closed without INTEGRATIONS_KEK outside dev, but it succeeded")
	}
	if !strings.Contains(err.Error(), "INTEGRATIONS_KEK") {
		t.Fatalf("expected a KEK-required error, got: %v", err)
	}
}

// TestOpenStore_DevModeNoKEK verifies the dev path still boots without a KEK
// (all-zeros dev key) so local development needs no secret.
func TestOpenStore_DevModeNoKEK(t *testing.T) {
	t.Setenv("INTEGRATIONS_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := newTestStore(t)
	if _, err := st.LoadOrCreateSigningKey(context.Background()); err != nil {
		t.Fatalf("LoadOrCreateSigningKey in dev: %v", err)
	}
}

// TestLegacyPlaintextKeyRewrapped verifies the re-wrap shim: a pre-existing
// plaintext key row is encrypted in place on first load once a KEK is set.
func TestLegacyPlaintextKeyRewrapped(t *testing.T) {
	raw, b64 := testKEK32(7)
	t.Setenv("INTEGRATIONS_KEK", b64)
	t.Setenv("VULOS_DEV", "")

	st := newTestStore(t)
	ctx := context.Background()

	// Simulate a legacy plaintext row written before M6.
	k, err := generateSigningKey()
	if err != nil {
		t.Fatalf("generateSigningKey: %v", err)
	}
	priv, pub, err := k.encodePEM()
	if err != nil {
		t.Fatalf("encodePEM: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO oauth_signing_keys (kid, private_pem, public_pem, active, created_at) VALUES (?, ?, ?, 1, ?)`,
		k.KID, priv, pub, time.Now().Unix()); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// Load → should return the same key AND re-wrap the row.
	got, err := st.LoadOrCreateSigningKey(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateSigningKey: %v", err)
	}
	if got.KID != k.KID {
		t.Fatalf("loaded wrong key: %s != %s", got.KID, k.KID)
	}

	var stored string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT private_pem FROM oauth_signing_keys WHERE kid = ?`, k.KID).Scan(&stored); err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	if strings.Contains(stored, "BEGIN") {
		t.Fatalf("legacy plaintext key was not re-wrapped at rest")
	}
	if plain, derr := decryptKEK(stored, raw); derr != nil || !strings.Contains(plain, "PRIVATE KEY") {
		t.Fatalf("re-wrapped key does not decrypt: err=%v", derr)
	}
}
