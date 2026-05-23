package airouter

// phishing_test.go — 4 tests for POST /api/airouter/mail/phishing-classify.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newPhishingTestSetup creates a PhishingHandler wired to a stub LLM provider
// that returns stubJSON as the LLM completion.
func newPhishingTestSetup(t *testing.T, stubJSON string) (*PhishingHandler, *Store, *httptest.Server) {
	t.Helper()
	stub := stubMailSSE(t, stubJSON)
	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID:        "stub",
		BaseURL:   stub.URL,
		APIKeyEnc: "k",
		Models:    []string{"test-model"},
	})
	_ = s.SetConfig(Config{Mode: ModeBYO, ActiveModel: "test-model"})
	return NewPhishingHandler(NewRouter(s), s), s, stub
}

// doPhishingPost fires a POST to /api/airouter/mail/phishing-classify.
func doPhishingPost(t *testing.T, h *PhishingHandler, body any, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterPhishingHandlers(mux, h.router, h.store)
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/airouter/mail/phishing-classify", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestPhishingOptInGate verifies that the endpoint returns 403 when
// ai_mail_enabled is false for the account.
func TestPhishingOptInGate(t *testing.T) {
	h, _, _ := newPhishingTestSetup(t, `{"verdict":"clean","confidence":0.1,"reasons":[],"suspicious_elements":[]}`)

	// Do NOT enable AI for this account.
	w := doPhishingPost(t, h, phishingClassifyRequest{
		MessageHeaders: "From: a@b.example",
		MessageBody:    "hello",
	}, "gated_user")

	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "ai_mail_disabled" {
		t.Errorf("want error=ai_mail_disabled, got %q", resp["error"])
	}
}

// TestPhishingJSONShape verifies that a successful response has all required
// fields with correct types: verdict, confidence, reasons, suspicious_elements.
func TestPhishingJSONShape(t *testing.T) {
	verdictJSON := `{"verdict":"phishing","confidence":0.92,"reasons":["spoof_domain"],"suspicious_elements":["urgency_language"]}`
	h, store, _ := newPhishingTestSetup(t, verdictJSON)

	// Enable AI for this account.
	enableAIForAccount(t, store, "shape_user")

	w := doPhishingPost(t, h, phishingClassifyRequest{
		MessageHeaders: "From: paypal-secure@evil.example\r\nSubject: Account Suspended",
		MessageBody:    "Click here to verify your account",
		URLs:           []string{"http://evil.example/login"},
	}, "shape_user")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp phishingClassifyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if resp.Verdict != "phishing" {
		t.Errorf("verdict: want phishing, got %q", resp.Verdict)
	}
	if resp.Confidence <= 0 || resp.Confidence > 1 {
		t.Errorf("confidence out of [0,1]: %f", resp.Confidence)
	}
	if resp.Reasons == nil {
		t.Error("reasons must not be nil")
	}
	if resp.SuspiciousElements == nil {
		t.Error("suspicious_elements must not be nil")
	}
}

// TestPhishingBYORouting verifies that the phishing endpoint routes through
// the BYO provider (mock LLM endpoint receives the request).
func TestPhishingBYORouting(t *testing.T) {
	verdictJSON := `{"verdict":"clean","confidence":0.05,"reasons":[],"suspicious_elements":[]}`
	var byoReceived bool

	// Intercept the BYO call.
	byoStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			byoReceived = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", jsonStringLit(verdictJSON))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer byoStub.Close()

	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID:        "byo-stub",
		BaseURL:   byoStub.URL,
		APIKeyEnc: "k",
		Models:    []string{"test-model"},
	})
	_ = s.SetConfig(Config{Mode: ModeBYO, ActiveModel: "test-model"})
	h := NewPhishingHandler(NewRouter(s), s)

	enableAIForAccount(t, s, "byo_user")
	w := doPhishingPost(t, h, phishingClassifyRequest{
		MessageHeaders: "From: newsletter@legit.example",
		MessageBody:    "Your monthly digest is here",
	}, "byo_user")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !byoReceived {
		t.Error("BYO endpoint must have received the LLM request")
	}
}

// TestPhishingTokenLogging verifies that a successful call writes a record to
// ai_mail_token_log with endpoint="phishing-classify".
func TestPhishingTokenLogging(t *testing.T) {
	verdictJSON := `{"verdict":"suspicious","confidence":0.55,"reasons":["unusual_sender"],"suspicious_elements":[]}`
	h, store, _ := newPhishingTestSetup(t, verdictJSON)

	enableAIForAccount(t, store, "log_user")
	w := doPhishingPost(t, h, phishingClassifyRequest{
		MessageHeaders: "From: promo@maybe-spam.example",
		MessageBody:    "Special offer just for you",
	}, "log_user")

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// Wait briefly for the goroutine-based log write.
	// In tests the stub is synchronous so a tight poll is enough.
	var count int
	for i := 0; i < 50; i++ {
		store.db.QueryRow(
			`SELECT COUNT(*) FROM ai_mail_token_log WHERE account_id=? AND endpoint=?`,
			"log_user", "phishing-classify",
		).Scan(&count)
		if count > 0 {
			break
		}
	}
	if count == 0 {
		t.Error("expected token log entry with endpoint=phishing-classify")
	}
}

// enableAIForAccount is a test helper that enables ai_mail_enabled for an account.
func enableAIForAccount(t *testing.T, s *Store, accountID string) {
	t.Helper()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO accounts(account_id, ai_mail_enabled, monthly_token_usage, updated_at)
		 VALUES(?,0,0,datetime('now'))`, accountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`UPDATE accounts SET ai_mail_enabled=1 WHERE account_id=?`, accountID)
	if err != nil {
		t.Fatal(err)
	}
}

// TestParsePhishingVerdict verifies the JSON parser handles edge cases.
func TestParsePhishingVerdict(t *testing.T) {
	// Markdown-wrapped JSON (some LLMs emit this).
	wrapped := "```json\n{\"verdict\":\"clean\",\"confidence\":0.1,\"reasons\":[],\"suspicious_elements\":[]}\n```"
	// The parser should strip markdown fences via { ... } extraction.
	v, err := parsePhishingVerdict(wrapped)
	if err != nil {
		t.Fatalf("wrapped JSON: %v", err)
	}
	if v.Verdict != "clean" {
		t.Errorf("want clean, got %q", v.Verdict)
	}

	// Unknown verdict should fail.
	_, err = parsePhishingVerdict(`{"verdict":"banana","confidence":0.5,"reasons":[]}`)
	if err == nil {
		t.Error("unknown verdict must return error")
	}

	_ = strings.Contains("x", "y") // prevent unused import
}
