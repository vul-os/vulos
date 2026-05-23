// handlers_test.go — unit tests for the IDENTITY-04 HTTP handlers.
package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newTestService creates an in-memory Service with a generated identity.
func newTestService(t *testing.T) *Service {
	t.Helper()
	id, err := GenerateIdentity("handler-test@vulos.org", "testpass")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	svc := New(id, &Store{}, "http://stub-relay.invalid")
	return svc
}

// newTestServiceWithDB creates a Service backed by a real SQLite DB.
func newTestServiceWithDB(t *testing.T) *Service {
	t.Helper()
	id, err := GenerateIdentity("db-test@vulos.org", "testpass")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc := New(id, store, "http://stub-relay.invalid")
	return svc
}

// session adds the required session header to a request.
func session(r *http.Request) *http.Request {
	r.Header.Set("X-OS-Session", "test-session-token")
	return r
}

// newMux registers handlers on a fresh mux and returns it.
func newMux(svc *Service, resolver KeyResolver) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, resolver)
	return mux
}

// ─── GET /api/identity/identity ─────────────────────────────────────────────────

func TestHandleGetIdentity(t *testing.T) {
	svc := newTestService(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/identity/identity", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp identityResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Address != "handler-test@vulos.org" {
		t.Errorf("address = %q", resp.Address)
	}
	if resp.PublicKeyB64 == "" {
		t.Error("public_key_b64 is empty")
	}
}

func TestHandleGetIdentityNoSession(t *testing.T) {
	svc := newTestService(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := httptest.NewRequest("GET", "/api/identity/identity", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ─── POST /api/identity/send ────────────────────────────────────────────────────

func TestHandleSend(t *testing.T) {
	// Stub relay.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mail/deliver" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer relay.Close()

	sender, _ := GenerateIdentity("sender@vulos.org", "pw")
	recipient, _ := GenerateIdentity("recipient@vulos.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	svc := New(sender, &Store{}, relay.URL)
	resolver := StaticKeyResolver{"recipient@vulos.org": recipientPub}
	mux := newMux(svc, resolver)

	body, _ := json.Marshal(sendRequest{To: "recipient@vulos.org", Subject: "Hi", Body: "Hello there"})
	req := session(httptest.NewRequest("POST", "/api/identity/send", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
}

func TestHandleSendMissingFields(t *testing.T) {
	svc := newTestService(t)
	mux := newMux(svc, StaticKeyResolver{})

	body := `{"to":"x@y.z"}` // missing subject + body
	req := session(httptest.NewRequest("POST", "/api/identity/send", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
}

// ─── GET /api/identity/mailbox ──────────────────────────────────────────────────

func TestHandleListMailboxEmpty(t *testing.T) {
	svc := newTestServiceWithDB(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/identity/mailbox", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp mailboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
	if resp.Messages == nil {
		t.Error("messages is nil, want empty slice")
	}
}

func TestHandleListMailboxPagination(t *testing.T) {
	svc := newTestServiceWithDB(t)

	// Seed 3 messages.
	for i := 0; i < 3; i++ {
		id, _ := newULID()
		msg := &MailMessage{
			ID:            id,
			FromAddress:   "a@vulos.org",
			Subject:       "msg",
			BodyEncrypted: []byte("encrypted"),
		}
		if err := svc.store.saveMailMessage(msg); err != nil {
			t.Fatalf("seed saveMailMessage: %v", err)
		}
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/identity/mailbox?limit=2&offset=0", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp mailboxResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
	if len(resp.Messages) != 2 {
		t.Errorf("messages len = %d, want 2", len(resp.Messages))
	}
}

// ─── GET /api/identity/mailbox/{id} ─────────────────────────────────────────────

func TestHandleGetMessageNotFound(t *testing.T) {
	svc := newTestServiceWithDB(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/identity/mailbox/no-such-id", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetMessageFound(t *testing.T) {
	svc := newTestServiceWithDB(t)

	id, _ := newULID()
	msg := &MailMessage{
		ID:            id,
		FromAddress:   "x@vulos.org",
		Subject:       "test",
		BodyEncrypted: []byte("short"), // < 40 bytes → body returned as ""
	}
	if err := svc.store.saveMailMessage(msg); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/identity/mailbox/"+id, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp messageResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != id {
		t.Errorf("id = %q, want %q", resp.ID, id)
	}
}

// ─── PATCH /api/identity/mailbox/{id} ───────────────────────────────────────────

func TestHandlePatchMessage(t *testing.T) {
	svc := newTestServiceWithDB(t)

	id, _ := newULID()
	if err := svc.store.saveMailMessage(&MailMessage{ID: id, FromAddress: "y@vulos.org", Subject: "s", BodyEncrypted: []byte("e")}); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	readTrue := true
	body, _ := json.Marshal(patchMessageRequest{Read: &readTrue})
	req := session(httptest.NewRequest("PATCH", "/api/identity/mailbox/"+id, bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}

	// Verify persisted.
	loaded, _ := svc.store.getMailMessage(id)
	if loaded == nil || !loaded.Read {
		t.Error("message not marked as read")
	}
}

func TestHandlePatchMessageNoFields(t *testing.T) {
	svc := newTestServiceWithDB(t)
	id, _ := newULID()
	if err := svc.store.saveMailMessage(&MailMessage{ID: id, FromAddress: "z@vulos.org", Subject: "s", BodyEncrypted: []byte("e")}); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("PATCH", "/api/identity/mailbox/"+id, strings.NewReader("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
}

// seed helper with body
func seedMessage(t *testing.T, store *Store, id, from, subject string) {
	t.Helper()
	if err := store.saveMailMessage(&MailMessage{ID: id, FromAddress: from, Subject: subject, BodyEncrypted: []byte("e")}); err != nil {
		t.Fatalf("seedMessage: %v", err)
	}
}

// ─── POST /api/identity/identity/rotate ────────────────────────────────────────

func TestHandleRotateIdentity(t *testing.T) {
	// Stub relay that accepts PUT /keys/:address.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	id, _ := GenerateIdentity("rotate-test@vulos.org", "pw")
	store, _ := NewStore(filepath.Join(t.TempDir(), "identity.db"))
	defer store.Close()
	svc := New(id, store, relay.URL)

	origPub := svc.identity.PublicKeyB64

	mux := newMux(svc, StaticKeyResolver{})
	body, _ := json.Marshal(rotateRequest{Passphrase: "newpass"})
	req := session(httptest.NewRequest("POST", "/api/identity/identity/rotate", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp identityResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.PublicKeyB64 == origPub {
		t.Error("public key unchanged after rotate")
	}
	if resp.Address != "rotate-test@vulos.org" {
		t.Errorf("address = %q", resp.Address)
	}
}

// ─── Rate limiter unit tests ──────────────────────────────────────────────────

// TestSendRateLimiter_MinuteBucket verifies that rapid-fire requests are
// rejected after the per-minute cap is exhausted, and that advancing the
// clock allows new requests.
func TestSendRateLimiter_MinuteBucket(t *testing.T) {
	now := time.Now()
	rl := &sendRateLimiter{
		perMin:  3,
		perDay:  1000,
		buckets: make(map[string]*sendBucket),
		now:     func() time.Time { return now },
	}

	// First 3 requests succeed.
	for i := range 3 {
		ok, _ := rl.allow("user-a")
		if !ok {
			t.Errorf("request %d should be allowed, got rejected", i+1)
		}
	}

	// 4th request must be rejected (minute bucket empty).
	ok, retryAfter := rl.allow("user-a")
	if ok {
		t.Error("4th request should be rejected (minute cap reached)")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter should be > 0, got %d", retryAfter)
	}

	// Advance clock by 1 full minute — bucket should fully refill.
	now = now.Add(time.Minute)
	ok, _ = rl.allow("user-a")
	if !ok {
		t.Error("request after clock advance (1 min) should be allowed")
	}
}

// TestSendRateLimiter_DayBucket verifies that the per-day cap is enforced.
func TestSendRateLimiter_DayBucket(t *testing.T) {
	now := time.Now()
	rl := &sendRateLimiter{
		perMin:  1000, // high minute cap so only day bucket triggers
		perDay:  2,
		buckets: make(map[string]*sendBucket),
		now:     func() time.Time { return now },
	}

	for i := range 2 {
		ok, _ := rl.allow("user-b")
		if !ok {
			t.Errorf("request %d should be allowed, got rejected", i+1)
		}
	}

	// 3rd request must fail (day bucket empty).
	ok, retryAfter := rl.allow("user-b")
	if ok {
		t.Error("3rd request should be rejected (day cap reached)")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter should be > 0, got %d", retryAfter)
	}

	// Advance clock by 1 full day — day bucket refills.
	now = now.Add(24 * time.Hour)
	ok, _ = rl.allow("user-b")
	if !ok {
		t.Error("request after clock advance (24h) should be allowed")
	}
}

// TestSendRateLimiter_Isolation verifies that different accounts have
// independent buckets.
func TestSendRateLimiter_Isolation(t *testing.T) {
	now := time.Now()
	rl := &sendRateLimiter{
		perMin:  1,
		perDay:  1000,
		buckets: make(map[string]*sendBucket),
		now:     func() time.Time { return now },
	}

	// user-x exhausts their minute bucket.
	ok, _ := rl.allow("user-x")
	if !ok {
		t.Fatal("first request for user-x should be allowed")
	}
	ok, _ = rl.allow("user-x")
	if ok {
		t.Fatal("second request for user-x should be rejected")
	}

	// user-y's bucket is unaffected.
	ok, _ = rl.allow("user-y")
	if !ok {
		t.Error("first request for user-y should be allowed (independent bucket)")
	}
}

// TestHandleSend_RateLimit_HTTP verifies that the HTTP handler returns 429
// after the per-minute cap is exhausted, and includes Retry-After.
func TestHandleSend_RateLimit_HTTP(t *testing.T) {
	// Stub relay that always accepts.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	sender, _ := GenerateIdentity("rl-sender@vulos.org", "pw")
	recipient, _ := GenerateIdentity("rl-recipient@vulos.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	svc := New(sender, &Store{}, relay.URL)
	resolver := StaticKeyResolver{"rl-recipient@vulos.org": recipientPub}

	mux := http.NewServeMux()
	h := &Handlers{
		svc:      svc,
		resolver: resolver,
		sendRL: &sendRateLimiter{
			perMin:  2,
			perDay:  1000,
			buckets: make(map[string]*sendBucket),
		},
	}
	mux.HandleFunc("POST /api/identity/send", h.handleSend)

	sendOnce := func() int {
		body, _ := json.Marshal(sendRequest{
			To:      "rl-recipient@vulos.org",
			Subject: "Hi",
			Body:    "Hello",
		})
		req := session(httptest.NewRequest("POST", "/api/identity/send", bytes.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	// First 2 requests should succeed.
	for i := range 2 {
		if code := sendOnce(); code != http.StatusOK {
			t.Errorf("request %d: got %d, want 200", i+1, code)
		}
	}

	// 3rd request must be rate-limited.
	body, _ := json.Marshal(sendRequest{
		To:      "rl-recipient@vulos.org",
		Subject: "Hi",
		Body:    "Hello",
	})
	req := session(httptest.NewRequest("POST", "/api/identity/send", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: got %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429 response")
	}
}

// TestSendRateLimiter_SweepEvictsIdleEntries verifies that the bucket sweep
// evicts entries idle for >24h and leaves recently-active entries intact.
//
// The test inserts 1000 buckets, advances the mock clock by 24h+5min so they
// are all idle, then calls sweep directly. After the sweep the map must be
// empty. A second pass inserts 1000 buckets but immediately touches one of
// them; after the sweep only that entry should survive.
func TestSendRateLimiter_SweepEvictsIdleEntries(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := base

	rl := &sendRateLimiter{
		perMin:        100,
		perDay:        10000,
		buckets:       make(map[string]*sendBucket),
		now:           func() time.Time { return mockNow },
		sweepInterval: 5 * time.Minute,
		idleThreshold: 24 * time.Hour,
	}

	// Insert 1000 buckets, all with lastAccess = base.
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("account-%04d", i)
		rl.allow(key) // creates bucket with lastAccess = mockNow (base)
	}
	if got := len(rl.buckets); got != 1000 {
		t.Fatalf("expected 1000 buckets before sweep, got %d", got)
	}

	// Advance mock clock beyond idle threshold.
	mockNow = base.Add(24*time.Hour + 5*time.Minute + 1*time.Second)

	// Run sweep directly.
	rl.sweep(mockNow)

	if got := len(rl.buckets); got != 0 {
		t.Errorf("expected 0 buckets after sweep (all idle >24h), got %d", got)
	}

	// Second pass: 1000 buckets, one touched after the clock advance.
	mockNow = base
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("account2-%04d", i)
		rl.allow(key)
	}
	// Advance clock past idle threshold.
	mockNow = base.Add(24*time.Hour + 5*time.Minute + 1*time.Second)
	// Touch just one bucket at the new "now" time so it won't be evicted.
	rl.allow("account2-0000")

	rl.sweep(mockNow)

	if got := len(rl.buckets); got != 1 {
		t.Errorf("expected 1 bucket after sweep (only active entry survives), got %d", got)
	}
	if _, ok := rl.buckets["account2-0000"]; !ok {
		t.Error("expected account2-0000 to survive the sweep")
	}
}
