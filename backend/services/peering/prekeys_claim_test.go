package peering

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClaimOneTimePreKey_Depletion pins Contract A: two claims return two
// DIFFERENT one-time prekeys, then the pool is exhausted and a claim returns a
// nil OPK (signed-prekey-only fallback). A claimed OPK is gone from the store.
func TestClaimOneTimePreKey_Depletion(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	store, err := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 2)
	if err != nil {
		t.Fatalf("NewPreKeyStore: %v", err)
	}

	c1, err := store.ClaimOneTimePreKey()
	if err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	c2, err := store.ClaimOneTimePreKey()
	if err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if c1.OneTimePreKey == nil || c2.OneTimePreKey == nil {
		t.Fatal("first two claims must each yield an OPK")
	}
	if c1.OneTimePreKey.ID == c2.OneTimePreKey.ID {
		t.Fatal("two claims returned the SAME OPK — no per-sender forward secrecy")
	}
	// Both claims carry the signed prekey.
	if c1.SignedPreKey.ID == "" || len(c1.SignedPreKey.Sig) == 0 {
		t.Fatal("claim must include the signed prekey")
	}

	// Pool now exhausted → nil OPK (never reuse, never block).
	c3, err := store.ClaimOneTimePreKey()
	if err != nil {
		t.Fatalf("claim 3: %v", err)
	}
	if c3.OneTimePreKey != nil {
		t.Fatal("exhausted pool must return a nil one_time_prekey")
	}

	// A claimed OPK is truly gone from the store (single-use): its private scalar
	// is no longer resolvable, so an X3DHRespond referencing it must fail closed.
	if _, ok := store.oneTimePreKeyPriv(c1.OneTimePreKey.ID); ok {
		t.Fatal("claimed OPK private key still present in store")
	}
	hdr := X3DHHeader{
		Version:         ContentCryptoV2X3DH,
		SenderVulosID:   bobID, // sender id irrelevant; the OPK lookup fails first
		EphemeralPub:    make([]byte, 32),
		SignedPreKeyID:  c1.SignedPreKey.ID,
		OneTimePreKeyID: c1.OneTimePreKey.ID,
	}
	if _, err := X3DHRespond(store, bobPriv, bobID, hdr); err == nil {
		t.Fatal("X3DHRespond against a consumed/claimed OPK must fail closed")
	}
}

// TestClaimHandler_HTTP exercises the wire endpoint: correct identity returns a
// claimable bundle; a foreign identity 404s.
func TestClaimHandler_HTTP(t *testing.T) {
	bobPriv, bobID := pkIdentity(t)
	store, err := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 2)
	if err != nil {
		t.Fatalf("NewPreKeyStore: %v", err)
	}
	mux := http.NewServeMux()
	RegisterPreKeyHandlers(mux, store, NewPublishedBundleStore(0, 0))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	claim := func(id string) (*ClaimedBundle, int) {
		body, _ := json.Marshal(map[string]string{"identity_vulos_id": id})
		resp, err := http.Post(srv.URL+"/api/peering/prekeys/claim", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, resp.StatusCode
		}
		var out ClaimedBundle
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return &out, resp.StatusCode
	}

	c1, code := claim(bobID)
	if code != http.StatusOK || c1 == nil || c1.OneTimePreKey == nil {
		t.Fatalf("valid claim: code=%d bundle=%+v", code, c1)
	}
	if c1.IdentityVulosID != bobID {
		t.Fatalf("claim identity = %q, want %q", c1.IdentityVulosID, bobID)
	}
	// Verify the returned signed prekey actually verifies against the identity.
	b := &PreKeyBundlePublic{IdentityVulosID: bobID, SignedPreKey: c1.SignedPreKey}
	if err := b.VerifySignedPreKey(); err != nil {
		t.Fatalf("claimed signed prekey does not verify: %v", err)
	}

	c2, _ := claim(bobID)
	if c2.OneTimePreKey == nil || c2.OneTimePreKey.ID == c1.OneTimePreKey.ID {
		t.Fatal("second HTTP claim must return a different OPK")
	}

	// Foreign identity → 404.
	_, otherID := pkIdentity(t)
	if _, code := claim(otherID); code != http.StatusNotFound {
		t.Fatalf("foreign identity claim: code=%d, want 404", code)
	}
}
