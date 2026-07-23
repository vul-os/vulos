// devicecert_verify_test.go — WAVE30-CP-COVERAGE: INTEG-SEC-01 device-cert +
// per-device-key verification. These are the crypto gates that bind a token
// mint to a specific owner-attested box; they were at 0% unit coverage.
package integrations

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ── VerifyDeviceCert (ed25519 CA attestation) ────────────────────────────────

func TestVerifyDeviceCert_ValidAndTampered(t *testing.T) {
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen ca: %v", err)
	}
	devPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen dev: %v", err)
	}
	const account, ulid = "acct-1", "01H000000000000000000ULID0"

	payload := DeviceCertPayload(devPub, account, ulid)
	certSig := ed25519.Sign(caPriv, []byte(payload))

	// Valid cert verifies.
	if !VerifyDeviceCert(caPub, devPub, account, ulid, certSig) {
		t.Fatal("valid cert should verify")
	}

	// Tampered account → the payload differs → verification fails (no cross-account
	// cert reuse).
	if VerifyDeviceCert(caPub, devPub, "acct-attacker", ulid, certSig) {
		t.Fatal("cert must not verify for a different account")
	}
	// Tampered ULID → fails.
	if VerifyDeviceCert(caPub, devPub, account, "01H000000000000000000ULIDX", certSig) {
		t.Fatal("cert must not verify for a different ULID")
	}
	// Different device pubkey → fails (cert is bound to devPub).
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyDeviceCert(caPub, otherPub, account, ulid, certSig) {
		t.Fatal("cert must not verify for a different device pubkey")
	}
}

func TestVerifyDeviceCert_ForgedCA(t *testing.T) {
	// A cert signed by an attacker's CA must not verify against the real CA pub.
	realCAPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, forgedCAPriv, _ := ed25519.GenerateKey(rand.Reader)
	devPub, _, _ := ed25519.GenerateKey(rand.Reader)
	const account, ulid = "acct-1", "ulid-1"

	forgedSig := ed25519.Sign(forgedCAPriv, []byte(DeviceCertPayload(devPub, account, ulid)))
	if VerifyDeviceCert(realCAPub, devPub, account, ulid, forgedSig) {
		t.Fatal("cert signed by a foreign CA must NOT verify")
	}
}

func TestVerifyDeviceCert_MalformedKeys(t *testing.T) {
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	devPub, _, _ := ed25519.GenerateKey(rand.Reader)
	sig := ed25519.Sign(caPriv, []byte(DeviceCertPayload(devPub, "a", "u")))

	// Wrong-size CA key → false (not a panic).
	if VerifyDeviceCert([]byte("short"), devPub, "a", "u", sig) {
		t.Fatal("malformed CA key must fail closed")
	}
	// Wrong-size device key → false.
	if VerifyDeviceCert(caPub, []byte("short"), "a", "u", sig) {
		t.Fatal("malformed device key must fail closed")
	}
	// Empty signature → false.
	if VerifyDeviceCert(caPub, devPub, "a", "u", nil) {
		t.Fatal("empty signature must fail closed")
	}
}

// ── VerifyEd25519Sig (per-request device signature) ──────────────────────────

func TestVerifyEd25519Sig(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := "integrations:mint:acct-1:01H..."
	sig := ed25519.Sign(priv, []byte(msg))

	if !VerifyEd25519Sig(pub, msg, sig) {
		t.Fatal("valid per-request sig should verify")
	}
	// Signature over a different message must not verify (no replay onto another
	// request).
	if VerifyEd25519Sig(pub, "integrations:mint:acct-1:OTHER", sig) {
		t.Fatal("sig must be bound to its message")
	}
	// Wrong key must not verify.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyEd25519Sig(otherPub, msg, sig) {
		t.Fatal("sig must not verify under a different key")
	}
	// Malformed key size → false, not panic.
	if VerifyEd25519Sig([]byte("nope"), msg, sig) {
		t.Fatal("malformed key must fail closed")
	}
}

// ── ECDSA P-256 device-key parsing + signature ───────────────────────────────

func newECDSAKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen ecdsa: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return priv, der
}

func TestParseECDSAPublicKey_RejectsNonP256(t *testing.T) {
	// A P-384 key must be rejected — the device identity key is always P-256.
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("gen p384: %v", err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if _, err := ParseECDSAPublicKey(der); !errors.Is(err, ErrBadDeviceKey) {
		t.Fatalf("P-384 key: want ErrBadDeviceKey, got %v", err)
	}
	// Garbage DER → ErrBadDeviceKey.
	if _, err := ParseECDSAPublicKey([]byte("not-der")); !errors.Is(err, ErrBadDeviceKey) {
		t.Fatalf("garbage der: want ErrBadDeviceKey, got %v", err)
	}
}

func TestVerifyDeviceSig_ECDSA(t *testing.T) {
	priv, der := newECDSAKey(t)
	msg := RegisterSigMessage("01H000000000000000000ULID0")
	digest := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if !VerifyDeviceSig(der, msg, sig) {
		t.Fatal("valid device sig should verify")
	}
	// Different message → fail.
	if VerifyDeviceSig(der, RegisterSigMessage("OTHER-ULID"), sig) {
		t.Fatal("sig must be bound to its message")
	}
	// Different key → fail.
	_, otherDER := newECDSAKey(t)
	if VerifyDeviceSig(otherDER, msg, sig) {
		t.Fatal("sig must not verify under a different key")
	}
	// Malformed pubkey DER → false.
	if VerifyDeviceSig([]byte("bad"), msg, sig) {
		t.Fatal("malformed pubkey must fail closed")
	}
}

// ── Device-key registry: trust-on-first-use pinning ──────────────────────────

func TestDeviceKeyStore_PinTOFU_MemAndSQL(t *testing.T) {
	eachDeviceKeyStore(t, func(t *testing.T, ks DeviceKeyStore) {
		ctx := context.Background()
		_, der := newECDSAKey(t)
		const ulid = "01H000000000000000000ULID0"

		// First pin wins.
		pinned, err := ks.Pin(ctx, ulid, der, AlgoECDSAP256)
		if err != nil || !pinned {
			t.Fatalf("first pin: pinned=%v err=%v", pinned, err)
		}
		// Re-pin identical bytes → idempotent, pinned=false, no error.
		pinned, err = ks.Pin(ctx, ulid, der, AlgoECDSAP256)
		if err != nil || pinned {
			t.Fatalf("idempotent re-pin: pinned=%v err=%v", pinned, err)
		}
		// A DIFFERENT key for the same ULID → conflict (tamper signal), original
		// key unchanged.
		_, otherDER := newECDSAKey(t)
		if _, err := ks.Pin(ctx, ulid, otherDER, AlgoECDSAP256); !errors.Is(err, ErrDeviceKeyConflict) {
			t.Fatalf("conflicting pin: want ErrDeviceKeyConflict, got %v", err)
		}
		got, err := ks.Get(ctx, ulid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got.PubKeyDER) != string(der) {
			t.Fatal("conflicting pin must not overwrite the original key")
		}

		// Empty ulid / empty key are rejected.
		if _, err := ks.Pin(ctx, "", der, AlgoECDSAP256); !errors.Is(err, ErrBadDeviceKey) {
			t.Fatalf("empty ulid: want ErrBadDeviceKey, got %v", err)
		}
		if _, err := ks.Pin(ctx, ulid, nil, AlgoECDSAP256); !errors.Is(err, ErrBadDeviceKey) {
			t.Fatalf("empty key: want ErrBadDeviceKey, got %v", err)
		}

		// Delete un-pins → Get returns ErrNoDeviceKey; a fresh key can be pinned.
		if err := ks.Delete(ctx, ulid); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := ks.Get(ctx, ulid); !errors.Is(err, ErrNoDeviceKey) {
			t.Fatalf("get after delete: want ErrNoDeviceKey, got %v", err)
		}
		if pinned, err := ks.Pin(ctx, ulid, otherDER, AlgoECDSAP256); err != nil || !pinned {
			t.Fatalf("re-pin after delete: pinned=%v err=%v", pinned, err)
		}
		// Delete is idempotent (no error on an absent ulid).
		if err := ks.Delete(ctx, "absent-ulid"); err != nil {
			t.Fatalf("idempotent delete: %v", err)
		}
	})
}

func TestDeviceKeyStore_GetUnpinned(t *testing.T) {
	eachDeviceKeyStore(t, func(t *testing.T, ks DeviceKeyStore) {
		if _, err := ks.Get(context.Background(), "never-pinned"); !errors.Is(err, ErrNoDeviceKey) {
			t.Fatalf("get unpinned: want ErrNoDeviceKey, got %v", err)
		}
	})
}

// eachDeviceKeyStore runs fn against both the Mem and SQL device-key stores.
func eachDeviceKeyStore(t *testing.T, fn func(t *testing.T, ks DeviceKeyStore)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		fn(t, NewMemDeviceKeyStore())
	})
	t.Run("sql", func(t *testing.T) {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		s, err := Open(db)
		if err != nil {
			db.Close()
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s.DeviceKeys())
	})
}
