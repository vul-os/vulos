package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// admitCode runs an envelope (signed by priv, From=fromID) through
// InboundMiddleware against contacts and returns the HTTP status. 200 = admitted.
func admitCode(t *testing.T, contacts *ContactStore, priv ed25519.PrivateKey, fromID, toID string) int {
	t.Helper()
	env, err := NewEnvelope("m-"+fromID, fromID, toID, TypeMessage, nil)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("sign env: %v", err)
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	h := InboundMiddleware(contacts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", bytesReader(body))
	h.ServeHTTP(rr, req)
	return rr.Code
}

// TestIngest_RevocationThroughFetchGatesAdmission is the Item-1 END-TO-END proof:
// a contact that publishes a valid self-revocation in its well-known bundle is,
// after the REAL fetch→ingest path (FetchPeerProfile → wkIngestLifecycle →
// IngestPeerLifecycle → RecordRevocation), rejected at admission and by
// VerifyVulosSignatureChecked — IsRevoked is genuinely populated, not poked
// directly.
func TestIngest_RevocationThroughFetchGatesAdmission(t *testing.T) {
	dir := t.TempDir()
	aliceRootPriv, aliceRootID := lcKeypair(t)
	_ = aliceRootPriv
	store, err := NewLifecycleStore(dir, aliceRootID, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}

	// Wire the global hooks exactly as the live server does.
	SetRevocationChecker(store.IsRevoked)
	SetLifecycleIngestor(func(lc *WKLifecycle) { _, _ = store.IngestPeerLifecycle(lc) })
	SetIdentityRootResolver(store.RootForResolvedKey)
	t.Cleanup(func() {
		SetRevocationChecker(nil)
		SetLifecycleIngestor(nil)
		SetIdentityRootResolver(nil)
	})

	// Bob's ENVELOPE identity (base58) and his self-revocation of it.
	bobPriv, bobID := lcKeypair(t)
	revCert, err := NewRevocationCert(bobPriv, bobID, bobID, "compromised")
	if err != nil {
		t.Fatalf("NewRevocationCert: %v", err)
	}

	// Bob's well-known OUTER identity (base64url) + signer for the wk response.
	bobWkID, sign := wkTestSignedPeer(t)
	fakeBob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := WKIdentityResponse{
			VulosID:     bobWkID,
			DisplayName: "Bob",
			Lifecycle: &WKLifecycle{
				RootVulosID: bobID,
				Revocations: []*RevocationCert{revCert},
			},
		}
		sign(&resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer fakeBob.Close()

	aliceContacts := makeContactStore(t)
	addApprovedContact(t, aliceContacts, bobID, "host:1")

	// Before the fetch: Bob is an approved, non-revoked contact → admitted.
	if code := admitCode(t, aliceContacts, bobPriv, bobID, aliceRootID); code != http.StatusOK {
		t.Fatalf("pre-revocation admission = %d, want 200", code)
	}

	// Real fetch → ingests the self-revocation carried in the well-known bundle.
	wkCacheMu.Lock()
	delete(wkCache, bobWkID)
	wkCacheMu.Unlock()
	if _, ferr := FetchPeerProfile(context.Background(), bobWkID, fakeBob.URL); ferr != nil {
		t.Fatalf("FetchPeerProfile: %v", ferr)
	}
	t.Cleanup(func() {
		wkCacheMu.Lock()
		delete(wkCache, bobWkID)
		wkCacheMu.Unlock()
	})

	// IsRevoked is now populated via the fetch path (not a direct RecordRevocation).
	if !store.IsRevoked(bobID) {
		t.Fatal("fetch→ingest did not populate IsRevoked for the self-revoked key")
	}

	// After the fetch: admission of Bob's now-revoked key is rejected.
	if code := admitCode(t, aliceContacts, bobPriv, bobID, aliceRootID); code != http.StatusForbidden {
		t.Fatalf("post-revocation admission = %d, want 403", code)
	}

	// And the cross-box capability verifier rejects it too.
	msg := []byte("capability")
	sig := ed25519.Sign(bobPriv, msg)
	if err := VerifyVulosSignatureChecked(bobID, msg, sig); err == nil {
		t.Fatal("VerifyVulosSignatureChecked must reject the revoked key")
	}
}

// TestIngest_RotationFollowedAtAdmission is the Item-2 proof that a rotated peer is
// accepted on its NEW key: ingesting a valid rotation chain records an alias so an
// envelope from the new key is admitted even though only the ROOT is an approved
// contact.
func TestIngest_RotationFollowedAtAdmission(t *testing.T) {
	dir := t.TempDir()
	_, aliceRootID := lcKeypair(t)
	store, err := NewLifecycleStore(dir, aliceRootID, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	SetIdentityRootResolver(store.RootForResolvedKey)
	t.Cleanup(func() { SetIdentityRootResolver(nil) })

	bobRootPriv, bobRootID := lcKeypair(t)
	bobNewPriv, bobNewID := lcKeypair(t)
	rot, err := NewRotationCert(bobRootPriv, bobRootID, bobNewID)
	if err != nil {
		t.Fatalf("NewRotationCert: %v", err)
	}
	lc := &WKLifecycle{RootVulosID: bobRootID, Chain: []LifecycleLink{{Rotation: rot}}}
	current, err := store.IngestPeerLifecycle(lc)
	if err != nil {
		t.Fatalf("IngestPeerLifecycle: %v", err)
	}
	if current != bobNewID {
		t.Fatalf("resolved current = %q, want %q", current, bobNewID)
	}

	contacts := makeContactStore(t)
	addApprovedContact(t, contacts, bobRootID, "host:1") // approve the ROOT only

	// Envelope from the NEW (rotated) key is admitted by following the rotation.
	if code := admitCode(t, contacts, bobNewPriv, bobNewID, aliceRootID); code != http.StatusOK {
		t.Fatalf("rotated-key admission = %d, want 200", code)
	}
}

// TestIngest_ForgedRotationRejected is the Item-2 fail-closed proof: a forged
// rotation chain records NO alias, so an envelope from the attacker key is rejected
// at admission.
func TestIngest_ForgedRotationRejected(t *testing.T) {
	dir := t.TempDir()
	_, aliceRootID := lcKeypair(t)
	store, err := NewLifecycleStore(dir, aliceRootID, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	SetIdentityRootResolver(store.RootForResolvedKey)
	t.Cleanup(func() { SetIdentityRootResolver(nil) })

	bobRootPriv, bobRootID := lcKeypair(t)
	_, bobNewID := lcKeypair(t)
	attackerPriv, attackerID := lcKeypair(t)

	rot, err := NewRotationCert(bobRootPriv, bobRootID, bobNewID)
	if err != nil {
		t.Fatalf("NewRotationCert: %v", err)
	}
	// Forge: redirect the rotation to the attacker's key (invalidates the signature).
	rot.NewVulosID = attackerID
	lc := &WKLifecycle{RootVulosID: bobRootID, Chain: []LifecycleLink{{Rotation: rot}}}
	if _, err := store.IngestPeerLifecycle(lc); err == nil {
		t.Fatal("forged rotation chain must fail closed (return error)")
	}
	if _, ok := store.RootForResolvedKey(attackerID); ok {
		t.Fatal("forged rotation must NOT record an alias")
	}

	contacts := makeContactStore(t)
	addApprovedContact(t, contacts, bobRootID, "host:1")

	// Envelope from the attacker key is rejected — the forged chain bought nothing.
	if code := admitCode(t, contacts, attackerPriv, attackerID, aliceRootID); code != http.StatusForbidden {
		t.Fatalf("forged-rotation admission = %d, want 403", code)
	}
}

// TestSelfRevokeHandler proves the session-authed self-revoke endpoint signs and
// records a self-revocation of the node's head (and rejects unauthenticated calls).
func TestSelfRevokeHandler(t *testing.T) {
	dir := t.TempDir()
	priv, id := lcKeypair(t)
	store, err := NewLifecycleStore(dir, id, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	mux := http.NewServeMux()
	RegisterIdentityLifecycleHandlers(mux, store, priv)

	// Unauthenticated → 401, nothing revoked.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peering/identity/revoke", nil)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated self-revoke = %d, want 401", rr.Code)
	}
	if store.IsRevoked(id) {
		t.Fatal("unauthenticated call must not revoke")
	}

	// Authenticated (session header set by the auth middleware) → revokes head.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/peering/identity/revoke", strings.NewReader(`{"reason":"retiring"}`))
	req2.Header.Set("X-User-ID", "u1")
	req2.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("authenticated self-revoke = %d, want 200: %s", rr2.Code, rr2.Body)
	}
	if !store.IsRevoked(id) {
		t.Fatal("self-revoke did not record the revocation")
	}
	// Published for peers to ingest.
	if got := store.RevocationList(); len(got) != 1 || got[0].VulosID != id {
		t.Fatalf("self-revocation not published in RevocationList: %+v", got)
	}
}
