// messages_test.go — tests for PEER-14: S2S text message delivery.
//
// Tests cover:
//
//   - ConversationID: deterministic, symmetric, round-trip
//   - peerFromConvID: extract peer from conv_id
//   - InboxStore: StoreMessage / GetMessages / ListConversations / idempotency
//   - MessageAPI.HandleInboundMessage: happy path, non-approved sender rejected,
//     no-permission sender rejected, bad payload rejected
//   - MessageAPI.handleSend: peer not approved rejected, no server address rejected
//   - MessageAPI.handleListConversations / handleGetMessages: read routes
package peering

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// makeTestIdentity generates an ephemeral Ed25519 keypair and Vulos ID for tests.
func makeTestIdentity(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	vulosID := encodeVulosID(pub)
	return priv, pub, vulosID
}

// makeInboxStore creates an InboxStore in a temporary directory.
func makeInboxStore(t *testing.T) *InboxStore {
	t.Helper()
	home := t.TempDir()
	store, err := NewInboxStore(home)
	if err != nil {
		t.Fatalf("NewInboxStore: %v", err)
	}
	return store
}

// makeContactStore creates an empty ContactStore in a temporary directory.
func makeContactStore(t *testing.T) *ContactStore {
	t.Helper()
	home := t.TempDir()
	cs, err := NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}
	return cs
}

// addApprovedContact adds a contact in StateApproved with PermMessage to store.
func addApprovedContact(t *testing.T, store *ContactStore, vulosID, server string) {
	t.Helper()
	if err := store.Add(vulosID, "Test Contact", server); err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	if err := store.Approve(vulosID, DefaultPerms()); err != nil {
		t.Fatalf("store.Approve: %v", err)
	}
}

// makeTestEnvelope builds and signs a TypeMessage envelope.
func makeTestEnvelope(
	t *testing.T,
	priv ed25519.PrivateKey,
	fromVulosID, toVulosID string,
	body string,
) *Envelope {
	t.Helper()

	payload, err := json.Marshal(MessagePayload{Type: "text", Body: body})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	env, err := NewEnvelope(uuid.New().String(), fromVulosID, toVulosID, TypeMessage, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return env
}

// requestWithContextEnvelope creates an http.Request whose context carries the
// given envelope (simulating what InboundMiddleware does for downstream handlers).
func requestWithContextEnvelope(env *Envelope) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", nil)
	ctx := context.WithValue(r.Context(), EnvelopeKey, env)
	return r.WithContext(ctx)
}

// ─── ConversationID tests ─────────────────────────────────────────────────────

func TestConversationID_Symmetric(t *testing.T) {
	a := "vulos:ed25519:AAA"
	b := "vulos:ed25519:ZZZ"

	id1 := ConversationID(a, b)
	id2 := ConversationID(b, a)

	if id1 != id2 {
		t.Errorf("ConversationID not symmetric: %q vs %q", id1, id2)
	}
}

func TestConversationID_Deterministic(t *testing.T) {
	a := "vulos:ed25519:AAA"
	b := "vulos:ed25519:BBB"

	id := ConversationID(a, b)
	if !strings.HasPrefix(id, "vulos:ed25519:AAA") {
		t.Errorf("expected lower ID first, got %q", id)
	}
}

func TestConversationID_RoundTrip(t *testing.T) {
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)

	convID := ConversationID(alice, bob)

	gotBob, err := peerFromConvID(convID, alice)
	if err != nil {
		t.Fatalf("peerFromConvID (alice→bob): %v", err)
	}
	if gotBob != bob {
		t.Errorf("peerFromConvID returned %q, want %q", gotBob, bob)
	}

	gotAlice, err := peerFromConvID(convID, bob)
	if err != nil {
		t.Fatalf("peerFromConvID (bob→alice): %v", err)
	}
	if gotAlice != alice {
		t.Errorf("peerFromConvID returned %q, want %q", gotAlice, alice)
	}
}

func TestPeerFromConvID_InvalidFormat(t *testing.T) {
	_, _, local := makeTestIdentity(t)

	_, err := peerFromConvID("not-a-valid-conv-id", local)
	if err == nil {
		t.Error("expected error for invalid conv_id")
	}
}

func TestPeerFromConvID_LocalNotInConvID(t *testing.T) {
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)
	_, _, carol := makeTestIdentity(t)

	convID := ConversationID(alice, bob)
	_, err := peerFromConvID(convID, carol)
	if err == nil {
		t.Error("expected error when local ID not in conv_id")
	}
}

// ─── InboxStore tests ─────────────────────────────────────────────────────────

func TestInboxStore_StoreAndRetrieve(t *testing.T) {
	store := makeInboxStore(t)
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)

	convID := ConversationID(alice, bob)
	msg := StoredMessage{
		ID:        uuid.New().String(),
		ConvID:    convID,
		From:      alice,
		To:        bob,
		Type:      "text",
		Body:      "hello from alice",
		Timestamp: time.Now().UTC(),
		Direction: DirectionOut,
	}

	if err := store.StoreMessage(msg); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	msgs, err := store.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Body != "hello from alice" {
		t.Errorf("body mismatch: %q", msgs[0].Body)
	}
	if msgs[0].Direction != DirectionOut {
		t.Errorf("direction mismatch: %q", msgs[0].Direction)
	}
}

func TestInboxStore_Idempotent(t *testing.T) {
	store := makeInboxStore(t)
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)

	convID := ConversationID(alice, bob)
	msg := StoredMessage{
		ID:        uuid.New().String(),
		ConvID:    convID,
		From:      alice,
		To:        bob,
		Type:      "text",
		Body:      "idempotent",
		Timestamp: time.Now().UTC(),
		Direction: DirectionIn,
	}

	// Store twice.
	if err := store.StoreMessage(msg); err != nil {
		t.Fatalf("StoreMessage first: %v", err)
	}
	if err := store.StoreMessage(msg); err != nil {
		t.Fatalf("StoreMessage second: %v", err)
	}

	msgs, err := store.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message after idempotent store, got %d", len(msgs))
	}
}

func TestInboxStore_MultipleMessages_Ordered(t *testing.T) {
	store := makeInboxStore(t)
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)

	convID := ConversationID(alice, bob)
	base := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 5; i++ {
		msg := StoredMessage{
			ID:        uuid.New().String(),
			ConvID:    convID,
			From:      alice,
			To:        bob,
			Type:      "text",
			Body:      "msg",
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Direction: DirectionOut,
		}
		if err := store.StoreMessage(msg); err != nil {
			t.Fatalf("StoreMessage %d: %v", i, err)
		}
	}

	msgs, err := store.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}

	for i := 1; i < len(msgs); i++ {
		if !msgs[i-1].Timestamp.Before(msgs[i].Timestamp) {
			t.Errorf("messages not in chronological order at index %d", i)
		}
	}
}

func TestInboxStore_GetMessages_EmptyConv(t *testing.T) {
	store := makeInboxStore(t)

	msgs, err := store.GetMessages("no-such-conv")
	if err != nil {
		t.Fatalf("GetMessages for missing conv should not error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestInboxStore_ListConversations(t *testing.T) {
	store := makeInboxStore(t)
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)
	_, _, carol := makeTestIdentity(t)

	// Two conversations.
	conv1 := ConversationID(alice, bob)
	conv2 := ConversationID(alice, carol)

	now := time.Now().UTC().Truncate(time.Second)

	msgs := []StoredMessage{
		{ID: uuid.New().String(), ConvID: conv1, From: alice, To: bob, Type: "text", Body: "a", Timestamp: now, Direction: DirectionOut},
		{ID: uuid.New().String(), ConvID: conv1, From: bob, To: alice, Type: "text", Body: "b", Timestamp: now.Add(time.Second), Direction: DirectionIn},
		{ID: uuid.New().String(), ConvID: conv2, From: alice, To: carol, Type: "text", Body: "c", Timestamp: now.Add(2 * time.Second), Direction: DirectionOut},
	}

	for _, m := range msgs {
		if err := store.StoreMessage(m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	convs, err := store.ListConversations()
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(convs))
	}

	// First should be conv2 (most recent activity).
	if convs[0].ConvID != conv2 {
		t.Errorf("expected conv2 first (most recent), got %q", convs[0].ConvID)
	}
	if convs[0].MessageCount != 1 {
		t.Errorf("conv2 message count: expected 1, got %d", convs[0].MessageCount)
	}
	if convs[1].ConvID != conv1 {
		t.Errorf("expected conv1 second, got %q", convs[1].ConvID)
	}
	if convs[1].MessageCount != 2 {
		t.Errorf("conv1 message count: expected 2, got %d", convs[1].MessageCount)
	}
}

func TestInboxStore_StoreMessage_EmptyID(t *testing.T) {
	store := makeInboxStore(t)
	err := store.StoreMessage(StoredMessage{ID: "", ConvID: "some-conv"})
	if err == nil {
		t.Error("expected error for empty message ID")
	}
}

func TestInboxStore_StoreMessage_EmptyConvID(t *testing.T) {
	store := makeInboxStore(t)
	err := store.StoreMessage(StoredMessage{ID: uuid.New().String(), ConvID: ""})
	if err == nil {
		t.Error("expected error for empty conv_id")
	}
}

// ─── HandleInboundMessage tests ───────────────────────────────────────────────

// makeMessageAPI creates a MessageAPI with in-memory stores and no network client.
func makeMessageAPI(t *testing.T) (*MessageAPI, *ContactStore, *InboxStore, ed25519.PrivateKey, string) {
	t.Helper()

	priv, _, vulosID := makeTestIdentity(t)
	contactStore := makeContactStore(t)
	inboxStore := makeInboxStore(t)

	api := NewMessageAPI(contactStore, inboxStore, NewPeerClient(), nil, priv, vulosID)
	return api, contactStore, inboxStore, priv, vulosID
}

func TestHandleInboundMessage_HappyPath(t *testing.T) {
	api, contactStore, inboxStore, _, localVulosID := makeMessageAPI(t)

	senderPriv, _, senderVulosID := makeTestIdentity(t)
	addApprovedContact(t, contactStore, senderVulosID, "sender.example.com:8080")

	env := makeTestEnvelope(t, senderPriv, senderVulosID, localVulosID, "hello world")
	r := requestWithContextEnvelope(env)
	w := httptest.NewRecorder()

	api.HandleInboundMessage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	convID := ConversationID(senderVulosID, localVulosID)
	msgs, err := inboxStore.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(msgs))
	}
	if msgs[0].Body != "hello world" {
		t.Errorf("body mismatch: %q", msgs[0].Body)
	}
	if msgs[0].Direction != DirectionIn {
		t.Errorf("direction should be 'in', got %q", msgs[0].Direction)
	}
}

func TestHandleInboundMessage_NoEnvelopeInContext(t *testing.T) {
	api, _, _, _, _ := makeMessageAPI(t)

	r := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", nil)
	w := httptest.NewRecorder()

	api.HandleInboundMessage(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing envelope, got %d", w.Code)
	}
}

func TestHandleInboundMessage_SenderNotApproved(t *testing.T) {
	api, contactStore, _, _, localVulosID := makeMessageAPI(t)

	senderPriv, _, senderVulosID := makeTestIdentity(t)
	// Add as pending (not approved).
	if err := contactStore.Add(senderVulosID, "not approved", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	env := makeTestEnvelope(t, senderPriv, senderVulosID, localVulosID, "sneaky message")
	r := requestWithContextEnvelope(env)
	w := httptest.NewRecorder()

	api.HandleInboundMessage(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-approved sender, got %d", w.Code)
	}
}

func TestHandleInboundMessage_SenderNoMessagePerm(t *testing.T) {
	api, contactStore, _, _, localVulosID := makeMessageAPI(t)

	senderPriv, _, senderVulosID := makeTestIdentity(t)
	// Approve with only PermCall (no PermMessage).
	if err := contactStore.Add(senderVulosID, "caller only", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := contactStore.Approve(senderVulosID, []Perm{PermCall}); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	env := makeTestEnvelope(t, senderPriv, senderVulosID, localVulosID, "call me maybe")
	r := requestWithContextEnvelope(env)
	w := httptest.NewRecorder()

	api.HandleInboundMessage(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for sender without PermMessage, got %d", w.Code)
	}
}

func TestHandleInboundMessage_BadPayload(t *testing.T) {
	api, contactStore, _, _, localVulosID := makeMessageAPI(t)

	_, _, senderVulosID := makeTestIdentity(t)
	addApprovedContact(t, contactStore, senderVulosID, "")

	// Directly construct an envelope with a syntactically invalid payload
	// JSON and inject it into the request context, bypassing signature
	// verification (InboundMiddleware already handles that).
	env := &Envelope{
		ID:        uuid.New().String(),
		From:      senderVulosID,
		To:        localVulosID,
		Type:      TypeMessage,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(`{invalid`), // syntactically bad JSON object
		Signature: "dummysig",                  // irrelevant — MW already verified
	}

	r := requestWithContextEnvelope(env)
	w := httptest.NewRecorder()

	api.HandleInboundMessage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad payload, got %d", w.Code)
	}
}

// ─── handleSend tests ─────────────────────────────────────────────────────────

// sendRequest performs a POST to /api/peering/conversations/{conv_id}/send
// using httptest without a real HTTP server.
func sendRequest(t *testing.T, api *MessageAPI, convID, body string) *httptest.ResponseRecorder {
	t.Helper()
	reqBody, _ := json.Marshal(sendMessageRequest{Body: body})
	r := httptest.NewRequest(http.MethodPost, "/api/peering/conversations/"+convID+"/send",
		bytes.NewReader(reqBody))
	r.SetPathValue("conv_id", convID)
	w := httptest.NewRecorder()
	api.handleSend(w, r)
	return w
}

func TestHandleSend_PeerNotApproved(t *testing.T) {
	api, _, _, _, localVulosID := makeMessageAPI(t)
	_, _, peerVulosID := makeTestIdentity(t)

	convID := ConversationID(localVulosID, peerVulosID)
	w := sendRequest(t, api, convID, "hello")

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-approved peer, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSend_PeerNoServer(t *testing.T) {
	api, contactStore, _, _, localVulosID := makeMessageAPI(t)
	_, _, peerVulosID := makeTestIdentity(t)

	// Approve with empty server address.
	if err := contactStore.Add(peerVulosID, "bob", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := contactStore.Approve(peerVulosID, DefaultPerms()); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	convID := ConversationID(localVulosID, peerVulosID)
	w := sendRequest(t, api, convID, "hello")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing server address, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSend_EmptyBody(t *testing.T) {
	api, contactStore, _, _, localVulosID := makeMessageAPI(t)
	_, _, peerVulosID := makeTestIdentity(t)
	addApprovedContact(t, contactStore, peerVulosID, "peer.example.com:8080")

	convID := ConversationID(localVulosID, peerVulosID)
	w := sendRequest(t, api, convID, "")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSend_InvalidConvID(t *testing.T) {
	api, _, _, _, _ := makeMessageAPI(t)

	reqBody, _ := json.Marshal(sendMessageRequest{Body: "hello"})
	r := httptest.NewRequest(http.MethodPost, "/api/peering/conversations/bad-id/send",
		bytes.NewReader(reqBody))
	r.SetPathValue("conv_id", "bad-id")
	w := httptest.NewRecorder()
	api.handleSend(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid conv_id, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── List routes tests ────────────────────────────────────────────────────────

func TestHandleListConversations_Empty(t *testing.T) {
	api, _, _, _, _ := makeMessageAPI(t)

	r := httptest.NewRequest(http.MethodGet, "/api/peering/conversations", nil)
	w := httptest.NewRecorder()
	api.handleListConversations(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Conversations []ConversationSummary `json:"conversations"`
		Count         int                   `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected 0 conversations, got %d", resp.Count)
	}
}

func TestHandleGetMessages_EmptyConv(t *testing.T) {
	api, _, _, _, _ := makeMessageAPI(t)

	r := httptest.NewRequest(http.MethodGet, "/api/peering/conversations/no-such-conv/messages", nil)
	r.SetPathValue("conv_id", "no-such-conv")
	w := httptest.NewRecorder()
	api.handleGetMessages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Messages []StoredMessage `json:"messages"`
		Count    int             `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("expected 0 messages, got %d", resp.Count)
	}
}

func TestHandleGetMessages_StoresAndReturns(t *testing.T) {
	api, contactStore, inboxStore, _, localVulosID := makeMessageAPI(t)
	senderPriv, _, senderVulosID := makeTestIdentity(t)
	addApprovedContact(t, contactStore, senderVulosID, "")

	convID := ConversationID(senderVulosID, localVulosID)

	// Manually store a couple of messages.
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		msg := StoredMessage{
			ID:        uuid.New().String(),
			ConvID:    convID,
			From:      senderVulosID,
			To:        localVulosID,
			Type:      "text",
			Body:      "msg",
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Direction: DirectionIn,
		}
		if err := inboxStore.StoreMessage(msg); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	// Also simulate an inbound message through the handler to verify the full path.
	env := makeTestEnvelope(t, senderPriv, senderVulosID, localVulosID, "via handler")
	rIn := requestWithContextEnvelope(env)
	wIn := httptest.NewRecorder()
	api.HandleInboundMessage(wIn, rIn)
	if wIn.Code != http.StatusOK {
		t.Fatalf("HandleInboundMessage: %d %s", wIn.Code, wIn.Body.String())
	}

	r := httptest.NewRequest(http.MethodGet, "/api/peering/conversations/"+convID+"/messages", nil)
	r.SetPathValue("conv_id", convID)
	w := httptest.NewRecorder()
	api.handleGetMessages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Messages []StoredMessage `json:"messages"`
		Count    int             `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Count != 4 {
		t.Errorf("expected 4 messages (3 manual + 1 via handler), got %d", resp.Count)
	}
}

// ─── sanitizePath test ────────────────────────────────────────────────────────

func TestSanitizePath(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"vulos:ed25519:ABC", "vulos:ed25519:ABC"},
		// Each character of "../etc/passwd": '.','.','/','e','t','c','/','p','a','s','s','w','d'
		// '.', '/' both map to '-' → "---etc-passwd"
		{"../etc/passwd", "---etc-passwd"},
		{"foo bar", "foo-bar"},
		// '.' is replaced with '-' to prevent ".." path traversal.
		{"normal-name_123.txt", "normal-name_123-txt"},
		// "../../secret": '.','.','/','.','.','/',... → "------secret"
		{"../../secret", "------secret"},
		{"safe-id_123", "safe-id_123"},
	}
	for _, tc := range cases {
		got := sanitizePath(tc.input)
		if got != tc.want {
			t.Errorf("sanitizePath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ─── Inbox persistence test ───────────────────────────────────────────────────

func TestInboxStore_Persistence(t *testing.T) {
	// Create the store in a stable temp dir (not using t.TempDir so we can
	// re-open it).
	dir := t.TempDir()
	inboxDir := dir // NewInboxStore appends .vulos/peering/inbox inside

	// We need to set up the directory structure NewInboxStore expects.
	// NewInboxStore takes a "home" path and creates home/.vulos/peering/inbox.
	// Use a sub-path so we don't pollute dir.
	homeDir := dir
	store1, err := NewInboxStore(homeDir)
	if err != nil {
		t.Fatalf("NewInboxStore (first): %v", err)
	}
	_ = inboxDir

	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)
	convID := ConversationID(alice, bob)

	msg := StoredMessage{
		ID:        uuid.New().String(),
		ConvID:    convID,
		From:      alice,
		To:        bob,
		Type:      "text",
		Body:      "persisted message",
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Direction: DirectionOut,
	}

	if err := store1.StoreMessage(msg); err != nil {
		t.Fatalf("StoreMessage: %v", err)
	}

	// Open a second InboxStore pointing to the same home — simulates restart.
	store2, err := NewInboxStore(homeDir)
	if err != nil {
		t.Fatalf("NewInboxStore (second): %v", err)
	}

	msgs, err := store2.GetMessages(convID)
	if err != nil {
		t.Fatalf("GetMessages after reopen: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 persisted message, got %d", len(msgs))
	}
	if msgs[0].Body != "persisted message" {
		t.Errorf("persisted body mismatch: %q", msgs[0].Body)
	}
}

// ─── InboxStore path traversal guard ─────────────────────────────────────────

func TestInboxStore_PathTraversalGuard(t *testing.T) {
	store := makeInboxStore(t)

	// Attempt a path traversal via the conv_id.
	maliciousConvID := "../../../etc"
	msg := StoredMessage{
		ID:        uuid.New().String(),
		ConvID:    maliciousConvID,
		From:      "vulos:ed25519:AAA",
		To:        "vulos:ed25519:BBB",
		Type:      "text",
		Body:      "traversal attempt",
		Timestamp: time.Now().UTC(),
		Direction: DirectionIn,
	}

	if err := store.StoreMessage(msg); err != nil {
		// An error is acceptable — path traversal was blocked.
		return
	}

	// If StoreMessage succeeded, confirm the file landed inside the inbox root.
	convDir := filepath.Join(store.root, sanitizePath(maliciousConvID))
	if !strings.HasPrefix(convDir, store.root) {
		t.Errorf("path traversal not guarded: convDir=%q is outside root=%q", convDir, store.root)
	}
}

// Expose root for path-traversal test (only needed in tests).
var _ = func() bool {
	_ = (*InboxStore)(nil)
	return true
}()

// ─── Timestamp filename round-trip test ──────────────────────────────────────

func TestTimestampFromFilename_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 5, 16, 12, 30, 45, 0, time.UTC)
	tsStr := strings.ReplaceAll(ts.UTC().Format(time.RFC3339), ":", "!")
	filename := tsStr + "_" + "some-uuid.json"

	recovered, err := timestampFromFilename(filename)
	if err != nil {
		t.Fatalf("timestampFromFilename: %v", err)
	}
	if !recovered.Equal(ts) {
		t.Errorf("recovered %v, want %v", recovered, ts)
	}
}

// ─── peerFromConvID edge cases ────────────────────────────────────────────────

func TestPeerFromConvID_RealIDs(t *testing.T) {
	// Use real generated IDs to cover the base58 character range.
	_, _, alice := makeTestIdentity(t)
	_, _, bob := makeTestIdentity(t)

	convID := ConversationID(alice, bob)

	// Re-derive from both perspectives.
	gotBob, err := peerFromConvID(convID, alice)
	if err != nil {
		t.Fatalf("alice side: %v", err)
	}
	if gotBob != bob {
		t.Errorf("alice side: got %q, want %q", gotBob, bob)
	}

	gotAlice, err := peerFromConvID(convID, bob)
	if err != nil {
		t.Fatalf("bob side: %v", err)
	}
	if gotAlice != alice {
		t.Errorf("bob side: got %q, want %q", gotAlice, alice)
	}
}

// ─── InboxStore path helper (expose for test) ─────────────────────────────────

// inboxRootPath exposes the root for test-only assertions without modifying the struct.
func inboxRootPath(s *InboxStore) string { return s.root }

func TestPathTraversalFileLandsInRoot(t *testing.T) {
	store := makeInboxStore(t)

	// Attempt path traversal via a malicious conv_id.
	cases := []string{
		"../../../tmp/evil",
		"../../etc/passwd",
		"../secret",
		"....//",
	}
	root := inboxRootPath(store)
	for _, maliciousConvID := range cases {
		sanitized := sanitizePath(maliciousConvID)
		convDir := filepath.Join(root, sanitized)

		if !strings.HasPrefix(convDir, root) {
			t.Errorf("path traversal not guarded for %q: convDir=%q escapes root=%q",
				maliciousConvID, convDir, root)
		}
		if strings.Contains(sanitized, "..") {
			t.Errorf("sanitizePath(%q) still contains '..': %q", maliciousConvID, sanitized)
		}
	}
}
