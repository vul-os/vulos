// e2e_peering_test.go — end-to-end in-process peering verification.
//
// These tests exercise the canonical peer-to-peer call paths documented in
// PEERING-E2E.md without requiring a real network: two Service instances are
// created in separate temp directories and communicate via in-process
// httptest.Server instances.
//
// Surfaces exercised:
//
//  1. Identity exchange: two peers generate keypairs; each can encode/decode
//     the other's Vulos ID.
//  2. 1:1 message send/receive: Peer A signs and delivers a TypeMessage
//     envelope to Peer B's InboundMiddleware; Peer B stores it in its inbox.
//  3. Signed feed publish + subscribe: Peer A publishes a signed entry and
//     Peer B can fetch and verify it; chain integrity is confirmed.
//  4. CRDT collaborative-doc lease: CollabStore creates a doc and receives an
//     inbound CRDT update; the update is persisted and can be retrieved.
//  5. Relay forwarder opacity: a RelayStore deposits an opaque ciphertext blob;
//     the relay stores it but cannot read the plaintext payload.
package peering

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── Test peer fixture ────────────────────────────────────────────────────────

// testPeer bundles all the in-process state for one Vulos peer node.
type testPeer struct {
	home     string
	svc      *Service
	contacts *ContactStore
	inbox    *InboxStore
	feeds    *FeedStore
	collab   *CollabStore
	relay    *RelayStore

	// inboundMux is wrapped with InboundMiddleware and used to receive
	// server-to-server envelopes.
	inboundMux *http.ServeMux
	server     *httptest.Server

	// msgAPI handles /api/peering/conversations and /api/peering/inbound/message
	msgAPI *MessageAPI
}

// newTestPeer constructs a fully-wired in-process Vulos peer.
func newTestPeer(t *testing.T) *testPeer {
	t.Helper()

	home := t.TempDir()
	svc := New(home)

	contacts, err := NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	inbox, err := NewInboxStore(home)
	if err != nil {
		t.Fatalf("NewInboxStore: %v", err)
	}

	peeringRoot := svc.Root()

	feeds, err := NewFeedStore(peeringRoot, svc.PrivateKey(), svc.VulosID(), contacts)
	if err != nil {
		t.Fatalf("NewFeedStore: %v", err)
	}

	collabDir := peeringRoot + "/collab"
	collab, err := NewCollabStore(collabDir)
	if err != nil {
		t.Fatalf("NewCollabStore: %v", err)
	}

	relay, err := NewRelayStore(home, contacts)
	if err != nil {
		t.Fatalf("NewRelayStore: %v", err)
	}
	// Enable relay.
	if err := relay.SetConfig(RelayConfig{Enabled: true, CapacityMB: 50, TTLHours: 24}); err != nil {
		t.Fatalf("relay SetConfig: %v", err)
	}

	// Build a PeerClient (no SSRF guard needed — we'll use httptest directly).
	peerClient := &PeerClient{http: &http.Client{}}

	msgAPI := NewMessageAPI(contacts, inbox, peerClient, nil, svc.PrivateKey(), svc.VulosID())

	// innerMux handles all inbound routes. After InboundMiddleware verifies the
	// envelope, the envelope is placed in the request context. We register
	// HandleInboundCollabUpdate (which reads from context) rather than the
	// plain handleInboundUpdate (which reads from body — body is already
	// consumed by InboundMiddleware).
	innerMux := http.NewServeMux()
	msgAPI.RegisterMessageHandlers(innerMux)

	// Register the envelope-aware collab-update handler (reads env from ctx).
	innerMux.HandleFunc("POST /api/peering/inbound/collab-update",
		collab.HandleInboundCollabUpdate)

	// inboundMux receives signed envelopes from the "remote" peer.
	inboundMux := http.NewServeMux()
	inboundMux.Handle("/api/peering/inbound/",
		InboundMiddleware(contacts, innerMux))

	// The collab-sync GET does not go through InboundMiddleware (GET-only, no
	// envelope body). Register it directly on the outer mux.
	inboundMux.HandleFunc("GET /api/peering/inbound/collab-sync",
		collab.handleInboundSync)

	srv := httptest.NewServer(inboundMux)
	t.Cleanup(srv.Close)

	return &testPeer{
		home:       home,
		svc:        svc,
		contacts:   contacts,
		inbox:      inbox,
		feeds:      feeds,
		collab:     collab,
		relay:      relay,
		inboundMux: inboundMux,
		server:     srv,
		msgAPI:     msgAPI,
	}
}

// addMutualContact registers peer b as an approved contact in peer a's store,
// pointing at b's httptest server.
func addMutualContact(t *testing.T, a, b *testPeer) {
	t.Helper()

	bAddr := strings.TrimPrefix(b.server.URL, "http://")

	if err := a.contacts.Add(b.svc.VulosID(), "peer-b", bAddr); err != nil {
		t.Fatalf("contacts.Add: %v", err)
	}
	if err := a.contacts.Approve(b.svc.VulosID(), DefaultPerms()); err != nil {
		t.Fatalf("contacts.Approve: %v", err)
	}
}

// ─── 1. Identity exchange ─────────────────────────────────────────────────────

// TestE2E_IdentityExchange verifies that two in-process peers each produce a
// valid, distinct Vulos ID and that each can decode the other's public key from
// that ID.
func TestE2E_IdentityExchange(t *testing.T) {
	a := newTestPeer(t)
	b := newTestPeer(t)

	idA := a.svc.VulosID()
	idB := b.svc.VulosID()

	if idA == idB {
		t.Fatal("peers generated the same Vulos ID — test fixture is broken")
	}

	for _, id := range []string{idA, idB} {
		if !strings.HasPrefix(id, "vulos:ed25519:") {
			t.Errorf("Vulos ID missing prefix: %q", id)
		}
	}

	// Each peer can decode the other's public key from the Vulos ID.
	pubA, err := decodeVulosID(idA)
	if err != nil {
		t.Fatalf("decodeVulosID(A): %v", err)
	}
	pubB, err := decodeVulosID(idB)
	if err != nil {
		t.Fatalf("decodeVulosID(B): %v", err)
	}

	if len(pubA) != ed25519.PublicKeySize {
		t.Errorf("pubA length %d, want %d", len(pubA), ed25519.PublicKeySize)
	}
	if len(pubB) != ed25519.PublicKeySize {
		t.Errorf("pubB length %d, want %d", len(pubB), ed25519.PublicKeySize)
	}

	// Verify that the decoded public keys match the service's public keys.
	if !bytes.Equal(pubA, a.svc.PublicKey()) {
		t.Error("decoded pubA does not match svc.PublicKey()")
	}
	if !bytes.Equal(pubB, b.svc.PublicKey()) {
		t.Error("decoded pubB does not match svc.PublicKey()")
	}

	t.Logf("A: %s", idA)
	t.Logf("B: %s", idB)
}

// ─── 2. 1:1 message send/receive ─────────────────────────────────────────────

// TestE2E_MessageSendReceive sends a text message from Peer A to Peer B and
// verifies that:
//   - the envelope is signed with A's private key,
//   - B's InboundMiddleware accepts the envelope (signature + contact check),
//   - B's inbox contains the message,
//   - the round-trip in the opposite direction also works.
func TestE2E_MessageSendReceive(t *testing.T) {
	a := newTestPeer(t)
	b := newTestPeer(t)

	// Wire mutual contacts so A can send to B and B can send to A.
	addMutualContact(t, a, b)
	addMutualContact(t, b, a)

	sendMsg := func(sender, recipient *testPeer, text string) {
		t.Helper()

		convID := ConversationID(sender.svc.VulosID(), recipient.svc.VulosID())
		msgID := uuid.New().String()

		payload, _ := json.Marshal(MessagePayload{Type: "text", Body: text})
		env, err := NewEnvelope(msgID, sender.svc.VulosID(), recipient.svc.VulosID(),
			TypeMessage, json.RawMessage(payload))
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		env.Timestamp = time.Now().UTC().Format(time.RFC3339)
		if err := env.Sign(sender.svc.PrivateKey()); err != nil {
			t.Fatalf("env.Sign: %v", err)
		}

		// Verify signature is sound before delivery.
		if err := env.Verify(); err != nil {
			t.Fatalf("pre-delivery env.Verify: %v", err)
		}

		// Deliver to recipient's inbound endpoint.
		body, _ := json.Marshal(env)
		req := httptest.NewRequest(http.MethodPost,
			"/api/peering/inbound/message", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		// Serve through recipient's inbound middleware.
		recipient.inboundMux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("inbound/message status %d body: %s", w.Code, w.Body)
		}

		// Confirm the message landed in the recipient's inbox.
		msgs, err := recipient.inbox.GetMessages(convID)
		if err != nil {
			t.Fatalf("GetMessages: %v", err)
		}
		found := false
		for _, m := range msgs {
			if m.ID == msgID && m.Body == text {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("message %q not found in recipient inbox (conv=%s)", text, convID)
		}
	}

	sendMsg(a, b, "hello from A to B")
	sendMsg(b, a, "hello from B to A")
}

// TestE2E_InboundMessage_RejectUnknownSender confirms that InboundMiddleware
// rejects envelopes from senders not in the contacts store.
func TestE2E_InboundMessage_RejectUnknownSender(t *testing.T) {
	b := newTestPeer(t)

	// Generate a stranger keypair (not added to b's contacts).
	_, strangerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	strangerPub := strangerPriv.Public().(ed25519.PublicKey)
	strangerID := encodeVulosID(strangerPub)

	payload, _ := json.Marshal(MessagePayload{Type: "text", Body: "attack"})
	env, _ := NewEnvelope(uuid.New().String(), strangerID, b.svc.VulosID(),
		TypeMessage, json.RawMessage(payload))
	env.Timestamp = time.Now().UTC().Format(time.RFC3339)
	_ = env.Sign(strangerPriv)

	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.inboundMux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for unknown sender, got %d", w.Code)
	}
}

// ─── 3. Signed feed publish + subscribe ──────────────────────────────────────

// TestE2E_FeedPublishSubscribe creates a feed on Peer A, publishes several
// entries, and verifies that:
//   - entries are Ed25519-signed with A's private key,
//   - the content hash chain is intact (FeedVerifyChain),
//   - subscribers (Peer B) can fetch entries since a given sequence number.
func TestE2E_FeedPublishSubscribe(t *testing.T) {
	a := newTestPeer(t)
	b := newTestPeer(t)

	// Peer B is an approved contact so it may access peers-access feeds.
	addMutualContact(t, a, b)

	// Create a public feed on A.
	meta, err := a.feeds.FeedCreate("E2E Test Feed", "integration test feed", FeedAccessPublic)
	if err != nil {
		t.Fatalf("FeedCreate: %v", err)
	}

	// Publish three entries.
	entries := []string{"entry-alpha", "entry-beta", "entry-gamma"}
	for i, text := range entries {
		body, _ := json.Marshal(map[string]string{"text": text})
		entry, err := a.feeds.FeedPublish(meta.FeedID, "post", json.RawMessage(body))
		if err != nil {
			t.Fatalf("FeedPublish %d: %v", i, err)
		}
		if entry.Sequence != int64(i+1) {
			t.Errorf("entry %d sequence %d, want %d", i, entry.Sequence, i+1)
		}
		if entry.Signature == "" {
			t.Errorf("entry %d has no signature", i)
		}
	}

	// Verify the hash chain integrity.
	if err := a.feeds.FeedVerifyChain(meta.FeedID); err != nil {
		t.Fatalf("FeedVerifyChain: %v", err)
	}

	// Subscriber (B) can fetch all entries from seq 0 (public feed — no auth).
	all, err := a.feeds.FeedGetEntries(meta.FeedID, 0)
	if err != nil {
		t.Fatalf("FeedGetEntries(since=0): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}

	// Subscriber fetches entries since seq 1 (only the last two).
	since1, err := a.feeds.FeedGetEntries(meta.FeedID, 1)
	if err != nil {
		t.Fatalf("FeedGetEntries(since=1): %v", err)
	}
	if len(since1) != 2 {
		t.Fatalf("expected 2 entries since seq 1, got %d", len(since1))
	}

	// Each entry's signature can be independently verified.
	for _, e := range all {
		if err := feedVerifyEntry(e); err != nil {
			t.Errorf("feedVerifyEntry seq=%d: %v", e.Sequence, err)
		}
	}

	// Notification: simulate pushing to approved contacts (b is approved on a).
	pushed := map[string]*FeedEntry{}
	a.feeds.FeedNotifySubscribers(all[len(all)-1], func(contactID string, e *FeedEntry) {
		pushed[contactID] = e
	})
	if _, ok := pushed[b.svc.VulosID()]; !ok {
		t.Error("FeedNotifySubscribers did not push to peer B")
	}

	// _ to suppress unused b warning (b is used above for contact setup)
	_ = b
}

// ─── 4. CRDT collaborative-doc lease ─────────────────────────────────────────

// TestE2E_CollabDocLease verifies the CRDT collaboration path:
//   - a document can be created with metadata (simulating a lease acquisition),
//   - an inbound S2S CRDT update envelope is accepted and persisted,
//   - the state is retrievable for catch-up sync.
func TestE2E_CollabDocLease(t *testing.T) {
	a := newTestPeer(t)
	b := newTestPeer(t)

	// A acquires a collaborative-doc "lease" by creating a document.
	docID := "e2e-collab-" + uuid.New().String()
	owner := a.svc.VulosID()

	meta := DocMeta{
		DocID:      docID,
		Title:      "E2E Collaboration Test",
		DocType:    "doc",
		Owner:      owner,
		SharedWith: []string{b.svc.VulosID()},
		CreatedAt:  time.Now().UTC(),
	}
	if err := a.collab.UpsertMeta(meta); err != nil {
		t.Fatalf("UpsertMeta: %v", err)
	}

	// Verify doc is listed.
	docs, err := a.collab.ListDocs()
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	found := false
	for _, d := range docs {
		if d.DocID == docID {
			found = true
			break
		}
	}
	if !found {
		t.Error("newly created doc not in ListDocs")
	}

	// Wire b as an approved contact of a so InboundMiddleware passes.
	addMutualContact(t, a, b)

	// Simulate B sending a signed collab-update envelope to A.
	opsPayload := []byte("fake-yjs-binary-update-blob")
	opsB64 := base64.StdEncoding.EncodeToString(opsPayload)

	collabPayload, _ := json.Marshal(map[string]string{
		"doc_id": docID,
		"ops":    opsB64,
	})

	envID := uuid.New().String()
	env, err := NewEnvelope(envID, b.svc.VulosID(), a.svc.VulosID(),
		"collab-update", json.RawMessage(collabPayload))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	env.Timestamp = time.Now().UTC().Format(time.RFC3339)
	if err := env.Sign(b.svc.PrivateKey()); err != nil {
		t.Fatalf("env.Sign: %v", err)
	}

	// Deliver to A's inbound endpoint.
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost,
		"/api/peering/inbound/collab-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.inboundMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("inbound/collab-update status %d body: %s", w.Code, w.Body)
	}

	// Confirm A persisted the Yjs blob (catch-up sync round-trip). Full-state read
	// is ACL-gated (authorizeRoom); this private/untracked doc needs an
	// authenticated session but no per-doc grant.
	syncReq := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/peering/inbound/collab-sync?doc_id=%s", docID), nil)
	syncReq.Header.Set("X-User-ID", "os-user")
	sw := httptest.NewRecorder()
	a.inboundMux.ServeHTTP(sw, syncReq)

	if sw.Code != http.StatusOK {
		t.Fatalf("collab-sync status %d body: %s", sw.Code, sw.Body)
	}

	var syncResp collabMsg
	if err := json.Unmarshal(sw.Body.Bytes(), &syncResp); err != nil {
		t.Fatalf("decode collab-sync response: %v", err)
	}
	if syncResp.Type != msgTypeSync {
		t.Errorf("expected collab:sync, got %q", syncResp.Type)
	}
	if !bytes.Equal(syncResp.Payload, opsPayload) {
		t.Errorf("sync payload mismatch: got %q want %q", syncResp.Payload, opsPayload)
	}

	t.Logf("CRDT update for doc %s persisted and retrievable via sync", docID)
}

// ─── 5. Relay forwarder opacity ───────────────────────────────────────────────

// TestE2E_RelayOpacity deposits an opaque ciphertext blob at a relay and verifies:
//   - the relay accepts and stores the blob,
//   - the relay cannot read the plaintext (the blob is stored as-is, opaque),
//   - the recipient can pick up the blob,
//   - the relay never has access to the underlying plaintext.
func TestE2E_RelayOpacity(t *testing.T) {
	// Three parties: sender (alice), recipient (bob), relay (relay).
	alice := newTestPeer(t)
	bob := newTestPeer(t)
	relay := newTestPeer(t)

	// Alice and Bob must be approved in the relay's contacts store.
	addMutualContact(t, relay, alice)
	addMutualContact(t, relay, bob)

	// The "plaintext" that should never leave Alice's device.
	plaintext := []byte("SUPER SECRET MESSAGE — relay must not see this")

	// Alice "encrypts" for Bob (for the test, we just base64-encode as a proxy
	// for the real encryption; the relay opacity property is that the relay
	// receives only the ciphertext blob and never calls any decryption).
	ciphertext := []byte(base64.StdEncoding.EncodeToString(plaintext))
	blobB64 := base64.StdEncoding.EncodeToString(ciphertext)
	blobID := "e2e-relay-blob-" + uuid.New().String()

	// Sign the deposit request with Alice's key.
	req := relayDepositRequest{
		To:            bob.svc.VulosID(),
		BlobID:        blobID,
		BlobB64:       blobB64,
		TTLHours:      1,
		SenderVulosID: alice.svc.VulosID(),
		Nonce:         fmt.Sprintf("%d", time.Now().UnixNano()),
	}

	canonical, err := relayDepositCanonical(req)
	if err != nil {
		t.Fatalf("relayDepositCanonical: %v", err)
	}
	sig := ed25519.Sign(alice.svc.PrivateKey(), canonical)
	req.Signature = base64.RawURLEncoding.EncodeToString(sig)

	// Deposit at relay.
	if err := relay.relay.Deposit(req); err != nil {
		t.Fatalf("relay.Deposit: %v", err)
	}

	// Opacity assertion: scan the relay's store directory and confirm that the
	// raw plaintext bytes are NOT present in any stored file.
	relayStoreDir := relay.home + "/peering/relay/store"
	err = walkAndCheck(relayStoreDir, plaintext)
	if err != nil {
		t.Fatalf("relay opacity violation: plaintext found in relay store: %v", err)
	}

	// The ciphertext blob SHOULD be present (relay stored it).
	found, err := walkAndFind(relayStoreDir, []byte(blobB64))
	if err != nil {
		t.Fatalf("walkAndFind: %v", err)
	}
	if !found {
		t.Error("ciphertext blob not found in relay store — deposit may have failed silently")
	}

	// Bob picks up the blob using a valid signed auth token.
	tsUnix := fmt.Sprintf("%d", time.Now().Unix())
	pickupMsg := bob.svc.VulosID() + "." + tsUnix
	pickupSig := ed25519.Sign(bob.svc.PrivateKey(), []byte(pickupMsg))
	pickupSigB64 := base64.RawURLEncoding.EncodeToString(pickupSig)

	blobs, err := relay.relay.Pickup(bob.svc.VulosID(), tsUnix, pickupSigB64)
	if err != nil {
		t.Fatalf("relay.Pickup: %v", err)
	}
	if len(blobs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(blobs))
	}
	if blobs[0].ID != blobID {
		t.Errorf("blob ID mismatch: got %q want %q", blobs[0].ID, blobID)
	}
	if blobs[0].BlobB64 != blobB64 {
		t.Error("blob content mismatch after pickup")
	}

	// Bob acknowledges receipt; relay deletes the blob.
	if err := relay.relay.Ack(bob.svc.VulosID(), tsUnix, pickupSigB64, []string{blobID}); err != nil {
		t.Fatalf("relay.Ack: %v", err)
	}

	// After ACK, pickup returns no blobs for Bob.
	tsUnix2 := fmt.Sprintf("%d", time.Now().Unix())
	pickupMsg2 := bob.svc.VulosID() + "." + tsUnix2
	pickupSig2 := ed25519.Sign(bob.svc.PrivateKey(), []byte(pickupMsg2))
	pickupSigB64_2 := base64.RawURLEncoding.EncodeToString(pickupSig2)

	blobsAfterAck, err := relay.relay.Pickup(bob.svc.VulosID(), tsUnix2, pickupSigB64_2)
	if err != nil {
		t.Fatalf("relay.Pickup after Ack: %v", err)
	}
	if len(blobsAfterAck) != 0 {
		t.Errorf("expected 0 blobs after Ack, got %d", len(blobsAfterAck))
	}

	t.Logf("relay opacity confirmed: plaintext not in store; blob retrieved then acked by Bob")
}

// ─── 6. Relay attestation ─────────────────────────────────────────────────────

// TestE2E_RelayAttestation exercises the relay attestation flow:
//   - a relay populates its AttestStore with a noop attestation document,
//   - a sender fetches and verifies the document via AttestFetchAndVerifyWithClient,
//   - the sender proceeds to deposit (noop provider is used in tests only).
func TestE2E_RelayAttestation(t *testing.T) {
	relay := newTestPeer(t)
	alice := newTestPeer(t)
	addMutualContact(t, relay, alice)

	// The default registry is default-deny and ships no "accept anything"
	// verifier, so register a deliberate passing verifier under a dedicated
	// test provider to exercise the end-to-end happy path.
	const e2eProvider AttestProvider = "test-e2e-provider-39"
	AttestRegisterVerifier(e2eProvider, attestVerifierFunc(func(_ AttestDoc, _ AttestPolicy) error {
		return nil
	}))

	// Set up attestation doc on relay.
	attestStore := NewAttestStore()
	doc := AttestDoc{
		Provider:     e2eProvider,
		RelayVulosID: relay.svc.VulosID(),
		IssuedAt:     time.Now().UTC(),
	}
	if err := attestStore.Set(doc); err != nil {
		t.Fatalf("AttestStore.Set: %v", err)
	}

	// Expose attest endpoint.
	attestMux := http.NewServeMux()
	RegisterAttestHandlers(attestMux, attestStore)
	attestSrv := httptest.NewServer(attestMux)
	t.Cleanup(attestSrv.Close)

	// Sender fetches and verifies.
	policy := AttestPolicy{
		Provider: e2eProvider,
		MaxAge:   5 * time.Minute,
	}
	fetchedDoc, err := AttestFetchAndVerifyWithClient(attestSrv.Client(),
		attestSrv.URL+"/api/peering/relay/attest", policy)
	if err != nil {
		t.Fatalf("AttestFetchAndVerifyWithClient: %v", err)
	}
	if fetchedDoc.RelayVulosID != relay.svc.VulosID() {
		t.Errorf("attestation RelayVulosID mismatch: got %q want %q",
			fetchedDoc.RelayVulosID, relay.svc.VulosID())
	}
	t.Logf("relay attestation verified for provider %q", fetchedDoc.Provider)
}

// ─── Envelope sign/verify round-trip ─────────────────────────────────────────

// TestE2E_EnvelopeSignVerify is a focused test confirming that the envelope
// round-trip (sign → marshal → unmarshal → verify) works correctly.
func TestE2E_EnvelopeSignVerify(t *testing.T) {
	a := newTestPeer(t)

	env, err := NewEnvelope(
		uuid.New().String(),
		a.svc.VulosID(),
		"vulos:ed25519:target",
		TypeMessage,
		json.RawMessage(`{"type":"text","body":"hello"}`),
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	if err := env.Sign(a.svc.PrivateKey()); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Marshal and unmarshal (simulate wire transport).
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := decoded.Verify(); err != nil {
		t.Fatalf("Verify after wire round-trip: %v", err)
	}

	// Tamper: change the body; verification must fail.
	decoded.Payload = json.RawMessage(`{"type":"text","body":"TAMPERED"}`)
	if err := decoded.Verify(); err == nil {
		t.Fatal("expected Verify to fail after tampering with payload")
	}
}

// ─── File-system helpers ──────────────────────────────────────────────────────

// walkAndCheck walks dir and returns an error if needle bytes appear verbatim
// in any file.
func walkAndCheck(dir string, needle []byte) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		path := dir + "/" + e.Name()
		if e.IsDir() {
			if err := walkAndCheck(path, needle); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(data, needle) {
			return fmt.Errorf("plaintext found in %s", path)
		}
	}
	return nil
}

// walkAndFind walks dir and returns true if needle bytes appear in any file.
func walkAndFind(dir string, needle []byte) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		path := dir + "/" + e.Name()
		if e.IsDir() {
			if found, err := walkAndFind(path, needle); err != nil || found {
				return found, err
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(data, needle) {
			return true, nil
		}
	}
	return false, nil
}

// ─── Unused-import guard ──────────────────────────────────────────────────────

var _ = context.Background // ensure context import stays (used by future tests)
var _ = rand.Reader        // ensure crypto/rand stays
