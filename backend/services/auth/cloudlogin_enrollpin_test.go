// cloudlogin_enrollpin_test.go — UNIFIED-SIGNIN: pinning the cloud login-broker
// pubkey at owner-approved ENROLLMENT closes the first-login TOFU window.
//
// The guarantee under test: after PinBrokerPubkeyAtEnrollment succeeds, the very
// next login goes through ensureBrokerPubkey's MISMATCH-CHECKED (fail-closed)
// branch — NOT trust-on-first-use. A cloud that later serves a different broker
// key is refused (ErrBrokerKeyMismatch) instead of silently re-pinned.
package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
)

// After enrollment pins the key, a subsequent login with a DIFFERENT cloud key
// must hit the mismatch-checked branch (not TOFU).
func TestPinBrokerPubkeyAtEnrollment_ClosesTOFUWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", filepath.Join(dir, "broker.pub"))

	brokerPub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Sanity: nothing pinned yet.
	if _, err := LoadBrokerPubkey(); !errors.Is(err, ErrNoBrokerPubkey) {
		t.Fatalf("expected ErrNoBrokerPubkey before enrollment, got %v", err)
	}

	// Enrollment-time pin (fetch returns the CP's active key).
	fetch := func(context.Context) (ed25519.PublicKey, error) { return brokerPub, nil }
	if err := PinBrokerPubkeyAtEnrollment(context.Background(), fetch); err != nil {
		t.Fatalf("PinBrokerPubkeyAtEnrollment: %v", err)
	}

	// It is now pinned to disk.
	got, err := LoadBrokerPubkey()
	if err != nil {
		t.Fatalf("LoadBrokerPubkey after pin: %v", err)
	}
	if !got.Equal(brokerPub) {
		t.Fatal("pinned key does not match the fetched broker key")
	}

	// Login #1 with a DIFFERENT served key → mismatch-checked branch REFUSES,
	// proving ensureBrokerPubkey did NOT trust-on-first-use.
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	wrongFetch := func(context.Context) (ed25519.PublicKey, error) { return wrongPub, nil }
	if _, err := ensureBrokerPubkey(context.Background(), wrongFetch); !errors.Is(err, ErrBrokerKeyMismatch) {
		t.Fatalf("expected ErrBrokerKeyMismatch (mismatch-checked branch), got %v", err)
	}

	// Login #1 with the SAME served key → accepted, returns the pinned key.
	sameFetch := func(context.Context) (ed25519.PublicKey, error) { return brokerPub, nil }
	pk, err := ensureBrokerPubkey(context.Background(), sameFetch)
	if err != nil {
		t.Fatalf("ensureBrokerPubkey with matching key: %v", err)
	}
	if !pk.Equal(brokerPub) {
		t.Fatal("ensureBrokerPubkey returned the wrong key")
	}
}

// An INLINE VULOS_CLOUD_BROKER_PUBKEY override wins: enrollment must NOT fetch
// or write anything (the override stays authoritative).
func TestPinBrokerPubkeyAtEnrollment_InlineEnvOverride_NoOp(t *testing.T) {
	inlinePub, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", base64.StdEncoding.EncodeToString(inlinePub))

	fetched := false
	fetch := func(context.Context) (ed25519.PublicKey, error) {
		fetched = true
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		return other, nil
	}
	if err := PinBrokerPubkeyAtEnrollment(context.Background(), fetch); err != nil {
		t.Fatalf("PinBrokerPubkeyAtEnrollment with inline override: %v", err)
	}
	if fetched {
		t.Fatal("inline env override must short-circuit BEFORE fetching the broker key")
	}
	// The inline override is still what LoadBrokerPubkey returns.
	got, err := LoadBrokerPubkey()
	if err != nil {
		t.Fatalf("LoadBrokerPubkey: %v", err)
	}
	if !got.Equal(inlinePub) {
		t.Fatal("inline override key changed")
	}
}

// A fetch failure at enrollment is non-fatal (returns an error the caller logs)
// and must NOT pin anything — first-login TOFU stays intact.
func TestPinBrokerPubkeyAtEnrollment_FetchFailure_LeavesUnpinned(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", filepath.Join(dir, "broker.pub"))

	fetch := func(context.Context) (ed25519.PublicKey, error) { return nil, errors.New("network down") }
	if err := PinBrokerPubkeyAtEnrollment(context.Background(), fetch); err == nil {
		t.Fatal("expected an error on fetch failure")
	}
	if _, err := LoadBrokerPubkey(); !errors.Is(err, ErrNoBrokerPubkey) {
		t.Fatalf("nothing should be pinned after a fetch failure, got %v", err)
	}
}
