package cloudhome

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestSQLRevokeFailsClosed exercises the identity + revocation + directory paths
// against a real SQLite store, and in doing so proves OpenSQLStore applies
// migrations 0001..0004 cleanly (0004 drops the removed content-prekey tables).
// It also confirms the store carries NO content-prekey surface anymore.
func TestSQLRevokeFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := OpenSQLStore(db) // applies 0001..0004 migrations
	if err != nil {
		t.Fatalf("OpenSQLStore: %v", err)
	}
	svc := mustService(t, store, kekOf(0x55), 1, nil, nil)

	id, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	// The directory advertises only the public identity — no content prekey.
	res, err := svc.Resolve(ctx, "bob@vulos.org")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.VulaID != id.VulaID {
		t.Fatalf("resolve VulaID mismatch: %q vs %q", res.VulaID, id.VulaID)
	}

	// Signing (identity/vouching) works before revocation.
	if _, err := svc.SignForVulaID(ctx, id.VulaID, []byte("federation-msg")); err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := svc.RevokeIdentity(ctx, "acct-bob", "lost-device"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Resolve(ctx, "bob@vulos.org"); !errors.Is(err, ErrNotDiscoverable) {
		t.Fatalf("post-revoke resolve: want ErrNotDiscoverable, got %v", err)
	}
	doc, err := svc.Lifecycle(ctx, id.VulaID)
	if err != nil || !doc.Revoked {
		t.Fatalf("SQL lifecycle relay: revoked=%v err=%v", doc.Revoked, err)
	}
}

// TestRevokeRejectsIdentity: after an account self-revokes its cloud-home
// identity, the directory resolve and capability redeem paths fail closed, the
// lifecycle relay publishes the verbatim cert, and the cert's signature verifies
// under the cloud-home identity key (so a box/relay peer rejects it with the same
// code path).
func TestRevokeRejectsIdentity(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	svc := mustService(t, store, kekOf(0x44), 1, nil, nil)

	id, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	// Not revoked yet: resolve succeeds.
	if _, err := svc.Resolve(ctx, "bob@vulos.org"); err != nil {
		t.Fatalf("pre-revoke resolve: %v", err)
	}

	cert, err := svc.RevokeIdentity(ctx, "acct-bob", "compromised")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if cert.VulaID != id.VulaID || cert.SignerVulaID != id.VulaID {
		t.Fatalf("revocation cert ids: %+v", cert)
	}
	if cert.Type != CertTypeRevocation {
		t.Fatalf("revocation cert type: %q", cert.Type)
	}

	// The cert verifies via our own Verify (structure + self-signature).
	if err := cert.Verify(); err != nil {
		t.Fatalf("revocation cert self-verify: %v", err)
	}
	// And verifies directly against the cloud-home identity key over canonical
	// bytes (the exact check vulos's lcVerifyCanonical performs cross-repo).
	idPub, _ := ParseVulaID(id.VulaID)
	unsigned := cert
	unsigned.Sig = ""
	payload, err := canonicalJSON(unsigned)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(cert.Sig)
	if err != nil {
		t.Fatalf("sig b64: %v", err)
	}
	if !ed25519.Verify(idPub, payload, sig) {
		t.Fatal("revocation signature does not verify against the cloud-home identity key over canonical bytes")
	}

	// Now every path fails closed.
	if _, err := svc.Resolve(ctx, "bob@vulos.org"); !errors.Is(err, ErrNotDiscoverable) {
		t.Fatalf("post-revoke resolve: want ErrNotDiscoverable, got %v", err)
	}
	if _, err := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Capability: []byte(`{"id":"c","owner_addr":"x"}`)}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("post-revoke redeem: want ErrRevoked, got %v", err)
	}

	// The lifecycle relay publishes the verbatim cert for peers to re-verify.
	doc, err := svc.Lifecycle(ctx, id.VulaID)
	if err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	if !doc.Revoked || doc.Revocation == nil || doc.Revocation.Sig != cert.Sig {
		t.Fatalf("lifecycle relay missing/altered revocation: %+v", doc)
	}

	// Revocation is idempotent: a second call returns the same cert.
	again, err := svc.RevokeIdentity(ctx, "acct-bob", "ignored-reason")
	if err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
	if again.Sig != cert.Sig {
		t.Fatal("re-revoke produced a different cert (must be idempotent)")
	}
}

// TestCanonicalJSONSortsKeys pins the canonical encoder to sorted-key, no-
// whitespace output — the property a cross-repo signature verification depends on.
func TestCanonicalJSONSortsKeys(t *testing.T) {
	in := map[string]any{"b": 1, "a": "x", "c": true}
	got, err := canonicalJSON(in)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	const want = `{"a":"x","b":1,"c":true}`
	if string(got) != want {
		t.Fatalf("canonical mismatch:\n got %s\nwant %s", got, want)
	}
}
