// handlers_test.go — unit tests for the VUMAIL-04 HTTP handlers.
package vumail

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newTestService creates an in-memory Service with a generated identity.
func newTestService(t *testing.T) *Service {
	t.Helper()
	id, err := GenerateIdentity("handler-test@vumail.org", "testpass")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	svc := New(id, &Store{}, "http://stub-relay.invalid")
	return svc
}

// newTestServiceWithDB creates a Service backed by a real SQLite DB.
func newTestServiceWithDB(t *testing.T) *Service {
	t.Helper()
	id, err := GenerateIdentity("db-test@vumail.org", "testpass")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "vumail.db"))
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

// ─── GET /api/vumail/identity ─────────────────────────────────────────────────

func TestHandleGetIdentity(t *testing.T) {
	svc := newTestService(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/vumail/identity", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body)
	}
	var resp identityResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Address != "handler-test@vumail.org" {
		t.Errorf("address = %q", resp.Address)
	}
	if resp.PublicKeyB64 == "" {
		t.Error("public_key_b64 is empty")
	}
}

func TestHandleGetIdentityNoSession(t *testing.T) {
	svc := newTestService(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := httptest.NewRequest("GET", "/api/vumail/identity", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ─── POST /api/vumail/send ────────────────────────────────────────────────────

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

	sender, _ := GenerateIdentity("sender@vumail.org", "pw")
	recipient, _ := GenerateIdentity("recipient@vumail.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	svc := New(sender, &Store{}, relay.URL)
	resolver := StaticKeyResolver{"recipient@vumail.org": recipientPub}
	mux := newMux(svc, resolver)

	body, _ := json.Marshal(sendRequest{To: "recipient@vumail.org", Subject: "Hi", Body: "Hello there"})
	req := session(httptest.NewRequest("POST", "/api/vumail/send", bytes.NewReader(body)))
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
	req := session(httptest.NewRequest("POST", "/api/vumail/send", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
}

// ─── GET /api/vumail/mailbox ──────────────────────────────────────────────────

func TestHandleListMailboxEmpty(t *testing.T) {
	svc := newTestServiceWithDB(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/vumail/mailbox", nil))
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
			FromAddress:   "a@vumail.org",
			Subject:       "msg",
			BodyEncrypted: []byte("encrypted"),
		}
		if err := svc.store.saveMailMessage(msg); err != nil {
			t.Fatalf("seed saveMailMessage: %v", err)
		}
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/vumail/mailbox?limit=2&offset=0", nil))
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

// ─── GET /api/vumail/mailbox/{id} ─────────────────────────────────────────────

func TestHandleGetMessageNotFound(t *testing.T) {
	svc := newTestServiceWithDB(t)
	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/vumail/mailbox/no-such-id", nil))
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
		FromAddress:   "x@vumail.org",
		Subject:       "test",
		BodyEncrypted: []byte("short"), // < 40 bytes → body returned as ""
	}
	if err := svc.store.saveMailMessage(msg); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("GET", "/api/vumail/mailbox/"+id, nil))
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

// ─── PATCH /api/vumail/mailbox/{id} ───────────────────────────────────────────

func TestHandlePatchMessage(t *testing.T) {
	svc := newTestServiceWithDB(t)

	id, _ := newULID()
	if err := svc.store.saveMailMessage(&MailMessage{ID: id, FromAddress: "y@vumail.org", Subject: "s", BodyEncrypted: []byte("e")}); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	readTrue := true
	body, _ := json.Marshal(patchMessageRequest{Read: &readTrue})
	req := session(httptest.NewRequest("PATCH", "/api/vumail/mailbox/"+id, bytes.NewReader(body)))
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
	if err := svc.store.saveMailMessage(&MailMessage{ID: id, FromAddress: "z@vumail.org", Subject: "s", BodyEncrypted: []byte("e")}); err != nil {
		t.Fatalf("seed saveMailMessage: %v", err)
	}

	mux := newMux(svc, StaticKeyResolver{})

	req := session(httptest.NewRequest("PATCH", "/api/vumail/mailbox/"+id, strings.NewReader("{}")))
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

// ─── POST /api/vumail/identity/rotate ────────────────────────────────────────

func TestHandleRotateIdentity(t *testing.T) {
	// Stub relay that accepts PUT /keys/:address.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	id, _ := GenerateIdentity("rotate-test@vumail.org", "pw")
	store, _ := NewStore(filepath.Join(t.TempDir(), "vumail.db"))
	defer store.Close()
	svc := New(id, store, relay.URL)

	origPub := svc.identity.PublicKeyB64

	mux := newMux(svc, StaticKeyResolver{})
	body, _ := json.Marshal(rotateRequest{Passphrase: "newpass"})
	req := session(httptest.NewRequest("POST", "/api/vumail/identity/rotate", bytes.NewReader(body)))
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
	if resp.Address != "rotate-test@vumail.org" {
		t.Errorf("address = %q", resp.Address)
	}
}
