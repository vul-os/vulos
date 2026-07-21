// feeds_test.go — tests for the signed append-only feed log (PEER-41).
package peering

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// feedTestStore creates a FeedStore in a temporary directory with a fresh
// Ed25519 keypair and no contact store.
func feedTestStore(t *testing.T) (*FeedStore, ed25519.PrivateKey, string) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	vulaID := encodeVulaID(pub)
	fs, err := NewFeedStore(dir, priv, vulaID, nil)
	if err != nil {
		t.Fatalf("NewFeedStore: %v", err)
	}
	return fs, priv, vulaID
}

// feedTestStoreWithContacts creates a FeedStore backed by a ContactStore.
func feedTestStoreWithContacts(t *testing.T) (*FeedStore, *ContactStore, string) {
	t.Helper()
	home := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	vulaID := encodeVulaID(pub)

	cs, err := NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	peeringDir := home // FeedStore is rooted at the given dir
	fs, err := NewFeedStore(peeringDir, priv, vulaID, cs)
	if err != nil {
		t.Fatalf("NewFeedStore: %v", err)
	}
	return fs, cs, vulaID
}

// ─── FeedCreate ───────────────────────────────────────────────────────────────

func TestFeedCreate_Public(t *testing.T) {
	fs, _, _ := feedTestStore(t)

	meta, err := fs.FeedCreate("My Blog", "A test blog", FeedAccessPublic)
	if err != nil {
		t.Fatalf("FeedCreate: %v", err)
	}
	if meta.FeedID == "" {
		t.Error("FeedID should not be empty")
	}
	if meta.Access != FeedAccessPublic {
		t.Errorf("access = %q, want %q", meta.Access, FeedAccessPublic)
	}
	if meta.Title != "My Blog" {
		t.Errorf("title = %q, want %q", meta.Title, "My Blog")
	}
}

func TestFeedCreate_InvalidAccess(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	_, err := fs.FeedCreate("Test", "", FeedAccess("invalid"))
	if err == nil {
		t.Error("expected error for invalid access level")
	}
}

func TestFeedCreate_EmptyTitle(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	_, err := fs.FeedCreate("", "desc", FeedAccessPublic)
	if err == nil {
		t.Error("expected error for empty title")
	}
}

func TestFeedCreate_SlugUniqueness(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	m1, err := fs.FeedCreate("My Feed", "", FeedAccessPublic)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	m2, err := fs.FeedCreate("My Feed", "", FeedAccessPublic)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if m1.FeedID == m2.FeedID {
		t.Errorf("expected unique FeedIDs, both got %q", m1.FeedID)
	}
}

// ─── FeedList ─────────────────────────────────────────────────────────────────

func TestFeedList(t *testing.T) {
	fs, _, _ := feedTestStore(t)

	_, _ = fs.FeedCreate("Blog", "", FeedAccessPublic)
	_, _ = fs.FeedCreate("Newsletter", "", FeedAccessLink)

	feeds, err := fs.FeedList()
	if err != nil {
		t.Fatalf("FeedList: %v", err)
	}
	if len(feeds) != 2 {
		t.Errorf("FeedList returned %d feeds, want 2", len(feeds))
	}
}

// ─── FeedPublish / append-only chain ─────────────────────────────────────────

// TestFeedPublish_AppendsChainedSignedEntry is the core AC: publish appends
// a chained signed entry.
func TestFeedPublish_AppendsChainedSignedEntry(t *testing.T) {
	fs, _, _ := feedTestStore(t)

	meta, _ := fs.FeedCreate("Chain Test", "", FeedAccessPublic)

	body := json.RawMessage(`{"text":"hello"}`)
	e1, err := fs.FeedPublish(meta.FeedID, "post", body)
	if err != nil {
		t.Fatalf("publish entry 1: %v", err)
	}
	if e1.Sequence != 1 {
		t.Errorf("e1.Sequence = %d, want 1", e1.Sequence)
	}
	if e1.PrevHash != "" {
		t.Errorf("first entry should have empty PrevHash, got %q", e1.PrevHash)
	}
	if e1.ContentHash == "" {
		t.Error("ContentHash must not be empty")
	}
	if e1.Signature == "" {
		t.Error("Signature must not be empty")
	}

	e2, err := fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{"text":"world"}`))
	if err != nil {
		t.Fatalf("publish entry 2: %v", err)
	}
	if e2.Sequence != 2 {
		t.Errorf("e2.Sequence = %d, want 2", e2.Sequence)
	}
	// Chain link: e2.PrevHash must equal e1.ContentHash.
	if e2.PrevHash != e1.ContentHash {
		t.Errorf("e2.PrevHash = %q, want e1.ContentHash = %q", e2.PrevHash, e1.ContentHash)
	}
}

// TestFeedPublish_TamperBreaksChain is AC: tamper breaks chain verify.
func TestFeedPublish_TamperBreaksChain(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("Tamper Test", "", FeedAccessPublic)

	_, _ = fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{"v":1}`))
	_, _ = fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{"v":2}`))

	// Tamper: overwrite the first entry file.
	entriesDir := os.DirFS(fs.feedDir(meta.FeedID) + "/entries")
	_ = entriesDir // read above for path; write directly.
	firstFile := fs.feedDir(meta.FeedID) + "/entries/00000000000000000001.json"
	data, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatalf("read first entry: %v", err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(data, &m)
	m["body"] = json.RawMessage(`{"v":999}`) // tamper body
	tampered, _ := json.Marshal(m)
	_ = os.WriteFile(firstFile, tampered, 0600)

	// Chain verification must now fail.
	err = fs.FeedVerifyChain(meta.FeedID)
	if err == nil {
		t.Error("expected chain verification to fail after tamper")
	}
}

// TestFeedPublish_ArchivedFeedRejectsNewEntries verifies archived feeds are closed.
func TestFeedPublish_ArchivedFeedRejectsNewEntries(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("Archive Test", "", FeedAccessPublic)

	// Mark the feed as archived by directly writing the meta.
	meta.Archived = true
	dir := fs.feedDir(meta.FeedID)
	_ = feedWriteMeta(dir, meta)

	_, err := fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error publishing to archived feed")
	}
}

// ─── FeedGetEntries / subscriber pull by seq ──────────────────────────────────

// TestFeedGetEntries_SubscriberSinceLastSeq is AC: subscribers since last seq.
func TestFeedGetEntries_SubscriberSinceLastSeq(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("Seq Test", "", FeedAccessPublic)

	for i := 0; i < 5; i++ {
		_, _ = fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))
	}

	// Subscriber that has seen entries 1-3 asks for entries since seq 3.
	entries, err := fs.FeedGetEntries(meta.FeedID, 3)
	if err != nil {
		t.Fatalf("FeedGetEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries since seq=3, got %d", len(entries))
	}
	if entries[0].Sequence != 4 {
		t.Errorf("first returned entry seq = %d, want 4", entries[0].Sequence)
	}
	if entries[1].Sequence != 5 {
		t.Errorf("second returned entry seq = %d, want 5", entries[1].Sequence)
	}
}

func TestFeedGetEntries_Since0ReturnsAll(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("All Test", "", FeedAccessPublic)
	for i := 0; i < 3; i++ {
		_, _ = fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))
	}
	entries, err := fs.FeedGetEntries(meta.FeedID, 0)
	if err != nil {
		t.Fatalf("FeedGetEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

// TestFeedGetEntries_RejectsNonPositiveSequenceOnDisk is the ordered-domain
// invariant guard (dmtap FEEDS.md §4.3 / SYNC.md §3): FeedEntry.Sequence is
// documented as a 1-based monotonically increasing position, but
// encoding/json decodes it into a signed int64, so a corrupt or tampered
// entry file could carry a non-positive value. feedLoadEntriesSince must
// treat that the same as any other malformed entry (skip it) rather than let
// it flow into the `Sequence > since` ordering comparison as if it were a
// legitimate position.
//
// This test writes a tampered entry file directly (bypassing FeedPublish,
// which never produces a non-positive Sequence itself) to simulate what a
// corrupt/tampered on-disk entry would look like, and calls FeedGetEntries
// with a negative `since` cursor (the Go-level API accepts any int64; only
// the HTTP handler rejects a negative since — see
// TestHTTPFeedEntries_NegativeSinceRejected for that boundary). With since
// more negative than the tampered Sequence, `Sequence > since` alone is
// satisfied (e.g. -1 > -5), so this fails without the explicit
// `e.Sequence < 1` skip in feedLoadEntriesSince.
func TestFeedGetEntries_RejectsNonPositiveSequenceOnDisk(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("Negative Seq Test", "", FeedAccessPublic)

	if _, err := fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatalf("publish entry 1: %v", err)
	}

	// Tamper: overwrite entry 1's Sequence field with a negative value, the
	// way a corrupted disk or a bug elsewhere might. FeedPublish itself can
	// never produce this — the sequence is always computed locally as
	// last+1 — so this simulates an out-of-band write, not a reachable API
	// path.
	entriesDir := fs.feedDir(meta.FeedID) + "/entries"
	firstFile := entriesDir + "/00000000000000000001.json"
	data, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatalf("read first entry: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	m["sequence"] = json.RawMessage("-1")
	tampered, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal tampered entry: %v", err)
	}
	if err := os.WriteFile(firstFile, tampered, 0600); err != nil {
		t.Fatalf("write tampered entry: %v", err)
	}

	entries, err := fs.FeedGetEntries(meta.FeedID, -5)
	if err != nil {
		t.Fatalf("FeedGetEntries: %v", err)
	}
	for _, e := range entries {
		if e.Sequence < 1 {
			t.Fatalf("FeedGetEntries returned an entry with non-positive Sequence=%d; the ordered-domain guard should have skipped it", e.Sequence)
		}
	}
}

// ─── FeedVerifyChain ──────────────────────────────────────────────────────────

func TestFeedVerifyChain_Clean(t *testing.T) {
	fs, _, _ := feedTestStore(t)
	meta, _ := fs.FeedCreate("Verify Test", "", FeedAccessPublic)
	for i := 0; i < 4; i++ {
		_, _ = fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))
	}
	if err := fs.FeedVerifyChain(meta.FeedID); err != nil {
		t.Errorf("expected clean chain, got: %v", err)
	}
}

// ─── Access control ───────────────────────────────────────────────────────────

// TestFeedAccess_PublicAndLinkNoAuth is AC: public/link no-auth.
func TestFeedAccess_PublicAndLinkNoAuth(t *testing.T) {
	fs, _, _ := feedTestStore(t)

	pubMeta, _ := fs.FeedCreate("Public Feed", "", FeedAccessPublic)
	linkMeta, _ := fs.FeedCreate("Link Feed", "", FeedAccessLink)

	// Empty callerVulaID means unauthenticated.
	if err := fs.feedCheckAccess(pubMeta, ""); err != nil {
		t.Errorf("public feed should allow unauthenticated access: %v", err)
	}
	if err := fs.feedCheckAccess(linkMeta, ""); err != nil {
		t.Errorf("link feed should allow unauthenticated access: %v", err)
	}
}

// TestFeedAccess_PeersGated is AC: peers gated.
func TestFeedAccess_PeersGated(t *testing.T) {
	fs, cs, _ := feedTestStoreWithContacts(t)

	peersMeta, err := fs.FeedCreate("Peers Feed", "", FeedAccessPeers)
	if err != nil {
		t.Fatalf("FeedCreate: %v", err)
	}

	// Unknown caller: denied.
	unknownID := "vula:ed25519:unknownpeerXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	if err := fs.feedCheckAccess(peersMeta, unknownID); err == nil {
		t.Error("expected access denied for unknown caller")
	}

	// Approved contact: allowed.
	approvedPub, _, _ := ed25519.GenerateKey(nil)
	approvedID := encodeVulaID(approvedPub)
	_ = cs.Add(approvedID, "Approved Peer", "peer.local:8080")
	_ = cs.Approve(approvedID, DefaultPerms())

	if err := fs.feedCheckAccess(peersMeta, approvedID); err != nil {
		t.Errorf("expected approved contact to have access: %v", err)
	}
}

// ─── FeedNotifySubscribers ────────────────────────────────────────────────────

// TestFeedNotifySubscribers_PushToApprovedContacts is AC: push to approved subscribers.
func TestFeedNotifySubscribers_PushToApprovedContacts(t *testing.T) {
	fs, cs, _ := feedTestStoreWithContacts(t)
	meta, _ := fs.FeedCreate("Push Test", "", FeedAccessPublic)
	entry, _ := fs.FeedPublish(meta.FeedID, "post", json.RawMessage(`{"msg":"hi"}`))

	// Add two approved contacts and one pending.
	pub1, _, _ := ed25519.GenerateKey(nil)
	pub2, _, _ := ed25519.GenerateKey(nil)
	pub3, _, _ := ed25519.GenerateKey(nil)
	id1 := encodeVulaID(pub1)
	id2 := encodeVulaID(pub2)
	id3 := encodeVulaID(pub3)

	_ = cs.Add(id1, "Peer1", "")
	_ = cs.Approve(id1, DefaultPerms())
	_ = cs.Add(id2, "Peer2", "")
	_ = cs.Approve(id2, DefaultPerms())
	_ = cs.Add(id3, "Peer3", "")
	// id3 remains pending — should NOT receive notification.

	notified := map[string]bool{}
	fs.FeedNotifySubscribers(entry, func(contactVulaID string, e *FeedEntry) {
		notified[contactVulaID] = true
	})

	if !notified[id1] {
		t.Errorf("approved contact %s should have been notified", id1)
	}
	if !notified[id2] {
		t.Errorf("approved contact %s should have been notified", id2)
	}
	if notified[id3] {
		t.Errorf("pending contact %s should NOT have been notified", id3)
	}
}

// ─── HTTP handler tests ───────────────────────────────────────────────────────

func feedTestHandlerSetup(t *testing.T) (*FeedStore, *ContactStore, *http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	vulaID := encodeVulaID(pub)

	cs, err := NewContactStore(home)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	store, err := NewFeedStore(home, priv, vulaID, cs)
	if err != nil {
		t.Fatalf("NewFeedStore: %v", err)
	}

	mux := http.NewServeMux()
	// In tests, caller identity is passed via X-Test-Caller header.
	RegisterFeedHandlers(mux, store, func(r *http.Request) string {
		return r.Header.Get("X-Test-Caller")
	})

	return store, cs, mux, vulaID
}

func TestHTTPFeedCreate(t *testing.T) {
	_, _, mux, _ := feedTestHandlerSetup(t)

	body := `{"title":"Test Feed","access":"public"}`
	req := httptest.NewRequest("POST", "/api/feeds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /api/feeds: status %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp FeedMeta
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.FeedID == "" {
		t.Error("response missing feed_id")
	}
}

func TestHTTPFeedPublish(t *testing.T) {
	store, _, mux, _ := feedTestHandlerSetup(t)

	meta, _ := store.FeedCreate("HTTP Feed", "", FeedAccessPublic)
	// Use only the slug (last segment of feedID) in the URL and path value.
	slug := feedSafeID(meta.FeedID)

	body := `{"entry_type":"post","body":{"content":"Hello HTTP"}}`
	req := httptest.NewRequest("POST", "/api/feeds/"+slug+"/publish", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("feed_id", slug)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /api/feeds/{id}/publish: status %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var entry FeedEntry
	if err := json.NewDecoder(w.Body).Decode(&entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if entry.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", entry.Sequence)
	}
	if entry.Signature == "" {
		t.Error("entry Signature should not be empty")
	}
}

func TestHTTPFeedEntries_PublicNoAuth(t *testing.T) {
	store, _, mux, _ := feedTestHandlerSetup(t)
	meta, _ := store.FeedCreate("Public", "", FeedAccessPublic)
	slug := feedSafeID(meta.FeedID)
	_, _ = store.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))

	req := httptest.NewRequest("GET", "/api/feeds/"+slug+"/entries", nil)
	req.SetPathValue("feed_id", slug)
	// No X-Test-Caller header — unauthenticated.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/feeds/{id}/entries public: status %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestHTTPFeedEntries_PeersGated_Denied(t *testing.T) {
	store, _, mux, _ := feedTestHandlerSetup(t)
	meta, _ := store.FeedCreate("Peers Only", "", FeedAccessPeers)
	slug := feedSafeID(meta.FeedID)
	_, _ = store.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))

	req := httptest.NewRequest("GET", "/api/feeds/"+slug+"/entries", nil)
	req.SetPathValue("feed_id", slug)
	// Unknown caller.
	req.Header.Set("X-Test-Caller", "vula:ed25519:unknownXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("peers feed should return 403 for unapproved caller, got %d", w.Code)
	}
}

func TestHTTPFeedEntries_PeersGated_Approved(t *testing.T) {
	store, cs, mux, _ := feedTestHandlerSetup(t)
	meta, _ := store.FeedCreate("Peers Only", "", FeedAccessPeers)
	slug := feedSafeID(meta.FeedID)
	_, _ = store.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))

	// Add and approve a contact.
	peerPub, _, _ := ed25519.GenerateKey(nil)
	peerID := encodeVulaID(peerPub)
	_ = cs.Add(peerID, "Approved", "peer:8080")
	_ = cs.Approve(peerID, DefaultPerms())

	req := httptest.NewRequest("GET", "/api/feeds/"+slug+"/entries", nil)
	req.SetPathValue("feed_id", slug)
	req.Header.Set("X-Test-Caller", peerID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("approved peer should get 200 for peers feed, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestHTTPFeedEntries_SinceQuery(t *testing.T) {
	store, _, mux, _ := feedTestHandlerSetup(t)
	meta, _ := store.FeedCreate("Since Test", "", FeedAccessPublic)
	slug := feedSafeID(meta.FeedID)
	for i := 0; i < 5; i++ {
		_, _ = store.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))
	}

	req := httptest.NewRequest("GET", "/api/feeds/"+slug+"/entries?since=3", nil)
	req.SetPathValue("feed_id", slug)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []FeedEntry `json:"entries"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Entries) != 2 {
		t.Errorf("expected 2 entries since=3, got %d", len(resp.Entries))
	}
}

// TestHTTPFeedEntries_NegativeSinceRejected is the HTTP-boundary half of the
// ordered-domain invariant guard: since is documented as a 1-based sequence
// cursor, so a negative value is not a value any legitimate caller would
// send. Without the `v < 0` check in handleFeedEntries this previously fell
// through to `since = v` and was silently treated the same as since=0 instead
// of being rejected as a bad request.
func TestHTTPFeedEntries_NegativeSinceRejected(t *testing.T) {
	store, _, mux, _ := feedTestHandlerSetup(t)
	meta, _ := store.FeedCreate("Negative Since Test", "", FeedAccessPublic)
	slug := feedSafeID(meta.FeedID)
	_, _ = store.FeedPublish(meta.FeedID, "post", json.RawMessage(`{}`))

	req := httptest.NewRequest("GET", "/api/feeds/"+slug+"/entries?since=-1", nil)
	req.SetPathValue("feed_id", slug)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for since=-1; body: %s", w.Code, w.Body.String())
	}
}

// ─── feedSlugify ──────────────────────────────────────────────────────────────

func TestFeedSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"My Blog Post!", "my-blog-post"},
		{"  spaces  ", "spaces"},
		{"ALL CAPS", "all-caps"},
		{"", "feed"},
		{"123", "123"},
	}
	for _, tc := range cases {
		got := feedSlugify(tc.in)
		if got != tc.want {
			t.Errorf("feedSlugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─── feedSafeID round-trip ────────────────────────────────────────────────────

func TestFeedSafeID(t *testing.T) {
	id := "vula:ed25519:abc123/my-blog"
	safe := feedSafeID(id)
	if strings.Contains(safe, "/") {
		t.Errorf("feedSafeID result should not contain '/': %q", safe)
	}
}
