package smsotp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── OTP regex ────────────────────────────────────────────────────────────────

func TestExtractOTP(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		// Real-world 6-digit codes
		{"bank6digit", "Your FNB OTP is 847291. Do not share.", "847291"},
		{"capitec6digit", "Capitec: Use OTP 193742 to confirm.", "193742"},
		{"google6digit", "G-481922 is your Google verification code.", "481922"},
		// 4-digit PIN-style codes
		{"4digit", "Your PIN is 9182. Valid for 5 minutes.", "9182"},
		// 8-digit codes
		{"8digit", "Use code 12345678 to log in.", "12345678"},
		// Leading/trailing noise
		{"noise", "Hi! Code: 372819 — expires soon.", "372819"},
		// WhatsApp / Signal style — hyphenated codes like 493-813 are often
		// sent as 6-digit codes split by a dash. RE2 \b treats '-' as a
		// non-word char, so both halves are candidates; the regex returns the
		// first match with 4+ digits. Here each half is 3 digits (< 4) so no
		// OTP is extracted — callers should normalise before passing.
		{"whatsapp", "Your WhatsApp code is 493-813. Don't share.", ""},
		// Hyphenated where one part is ≥4 digits
		{"whatsapp6", "Code: 4938-1234 do not share", "4938"},
		// Multiple codes — return first
		{"multi", "Two codes: 1234 and 5678", "1234"},
		// No code
		{"nocode", "Hello from support. Please call us.", ""},
		// Should NOT match 9-digit sequence (too long)
		{"9digit", "Ref number: 123456789 is not an OTP", ""},
		// Should NOT match 3-digit sequence (too short)
		{"3digit", "Call 082 for help", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOTP(tc.body)
			if got != tc.want {
				t.Errorf("extractOTP(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// ─── Webhook ──────────────────────────────────────────────────────────────────

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	svc := &Service{
		notifier: nil, // notify is tested separately; nil is safe here
		histPath: dir + "/history.json",
		cfgPath:  dir + "/config.json",
		settings: Settings{RetentionHours: 24, Provider: "twilio"},
	}
	return svc
}

func postWebhook(t *testing.T, svc *Service, from, body string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("From", from)
	form.Set("To", "+27123456789")
	form.Set("Body", body)
	form.Set("MessageSid", "SM"+from[1:])

	req := httptest.NewRequest(http.MethodPost, "/api/auth/sms/webhook",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	svc.handleWebhook(rr, req)
	return rr
}

func TestWebhookStoresSMS(t *testing.T) {
	svc := newTestService(t)

	rr := postWebhook(t, svc, "+27831234567", "Your OTP is 382910. Valid 5 min.")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/xml" {
		t.Errorf("expected text/xml, got %s", ct)
	}
	if !strings.Contains(rr.Body.String(), "<Response/>") {
		t.Errorf("expected TwiML <Response/>, got %s", rr.Body.String())
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.history) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(svc.history))
	}
	msg := svc.history[0]
	if msg.OTP != "382910" {
		t.Errorf("OTP not extracted: got %q", msg.OTP)
	}
	if msg.From != "+27831234567" {
		t.Errorf("From mismatch: %s", msg.From)
	}
}

func TestWebhookNoBodyIsIgnored(t *testing.T) {
	svc := newTestService(t)

	form := url.Values{"From": {"+12025550100"}, "To": {"+27000000000"}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sms/webhook",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	svc.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	svc.mu.Lock()
	count := len(svc.history)
	svc.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 stored messages for delivery callback, got %d", count)
	}
}

// ─── History persistence ───────────────────────────────────────────────────────

func TestHistoryPersistsAndLoads(t *testing.T) {
	dir := t.TempDir()

	svc1 := &Service{
		notifier: nil,
		histPath: dir + "/history.json",
		cfgPath:  dir + "/config.json",
		settings: Settings{RetentionHours: 24, Provider: "twilio"},
	}

	postWebhook(t, svc1, "+27831234567", "Code 123456 here")

	// New service loads from same dir
	svc2 := &Service{
		notifier: nil,
		histPath: dir + "/history.json",
		cfgPath:  dir + "/config.json",
		settings: Settings{RetentionHours: 24},
	}
	svc2.load()

	svc2.mu.Lock()
	defer svc2.mu.Unlock()
	if len(svc2.history) != 1 {
		t.Fatalf("expected 1 message after reload, got %d", len(svc2.history))
	}
	if svc2.history[0].OTP != "123456" {
		t.Errorf("OTP not persisted: %q", svc2.history[0].OTP)
	}
}

// ─── 24 h pruning ─────────────────────────────────────────────────────────────

func TestPruneOld(t *testing.T) {
	svc := newTestService(t)

	old := SMSMessage{
		ID:         "old1",
		From:       "+1000000000",
		Body:       "old message",
		ReceivedAt: time.Now().Add(-25 * time.Hour), // 25 h ago → should be pruned
	}
	fresh := SMSMessage{
		ID:         "fresh1",
		From:       "+2000000000",
		Body:       "fresh message",
		ReceivedAt: time.Now().Add(-1 * time.Hour), // 1 h ago → keep
	}

	svc.mu.Lock()
	svc.history = []SMSMessage{old, fresh}
	svc.pruneOld()
	svc.mu.Unlock()

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.history) != 1 {
		t.Fatalf("expected 1 message after pruning, got %d", len(svc.history))
	}
	if svc.history[0].ID != "fresh1" {
		t.Errorf("wrong message kept after pruning: %s", svc.history[0].ID)
	}
}

func TestPruneRespectsBoundary(t *testing.T) {
	svc := newTestService(t)

	exactBoundary := SMSMessage{
		ID:         "boundary",
		From:       "+3000000000",
		Body:       "edge case",
		ReceivedAt: time.Now().Add(-23*time.Hour - 59*time.Minute), // just inside 24 h
	}

	svc.mu.Lock()
	svc.history = []SMSMessage{exactBoundary}
	svc.pruneOld()
	count := len(svc.history)
	svc.mu.Unlock()

	if count != 1 {
		t.Errorf("message within 24 h window should not be pruned, got %d messages", count)
	}
}

// ─── GET /recent ──────────────────────────────────────────────────────────────

func TestRecentEndpointReturnNewestFirst(t *testing.T) {
	svc := newTestService(t)

	// Insert oldest → newest
	postWebhook(t, svc, "+27000000001", "First 111111")
	postWebhook(t, svc, "+27000000002", "Second 222222")
	postWebhook(t, svc, "+27000000003", "Third 333333")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/sms/recent", nil)
	rr := httptest.NewRecorder()
	svc.handleRecent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp struct {
		Messages []SMSMessage `json:"messages"`
		Count    int          `json:"count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 3 {
		t.Fatalf("expected 3 messages, got %d", resp.Count)
	}
	// Newest first → OTP from third message
	if resp.Messages[0].OTP != "333333" {
		t.Errorf("expected newest first (333333), got %s", resp.Messages[0].OTP)
	}
}

// ─── GET /number ──────────────────────────────────────────────────────────────

func TestNumberEndpoint(t *testing.T) {
	svc := newTestService(t)
	svc.settings.Number = "+27110001234"
	svc.settings.Provider = "twilio"

	req := httptest.NewRequest(http.MethodGet, "/api/auth/sms/number", nil)
	rr := httptest.NewRecorder()
	svc.handleNumber(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["number"] != "+27110001234" {
		t.Errorf("unexpected number: %s", resp["number"])
	}
	if resp["provider"] != "twilio" {
		t.Errorf("unexpected provider: %s", resp["provider"])
	}
}

// ─── PUT /settings ────────────────────────────────────────────────────────────

func TestSettingsUpdate(t *testing.T) {
	svc := newTestService(t)

	body := `{"number":"+27110009999","auto_fill":true,"retention_hours":12}`
	req := httptest.NewRequest(http.MethodPut, "/api/auth/sms/settings",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.handleSettings(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.settings.Number != "+27110009999" {
		t.Errorf("number not updated: %s", svc.settings.Number)
	}
	if !svc.settings.AutoFill {
		t.Error("auto_fill not updated")
	}
	if svc.settings.RetentionHours != 12 {
		t.Errorf("retention_hours not updated: %d", svc.settings.RetentionHours)
	}
}

func TestSettingsRejectsBadJSON(t *testing.T) {
	svc := newTestService(t)

	req := httptest.NewRequest(http.MethodPut, "/api/auth/sms/settings",
		strings.NewReader("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.handleSettings(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─── RegisterHandlers ─────────────────────────────────────────────────────────

func TestRegisterHandlers(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux) // must not panic

	routes := []struct {
		method string
		path   string
	}{
		{"POST", "/api/auth/sms/webhook"},
		{"GET", "/api/auth/sms/recent"},
		{"GET", "/api/auth/sms/number"},
		{"PUT", "/api/auth/sms/settings"},
	}

	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		// Just ensure routes are registered (not 404)
		if rr.Code == http.StatusNotFound {
			t.Errorf("%s %s not registered (got 404)", rt.method, rt.path)
		}
	}
}

// ─── History file permissions ─────────────────────────────────────────────────

func TestHistoryFilePermissions(t *testing.T) {
	svc := newTestService(t)
	postWebhook(t, svc, "+27831234567", "Code 999999")

	info, err := os.Stat(svc.histPath)
	if err != nil {
		t.Fatalf("stat history file: %v", err)
	}
	// Must be owner-only (0600)
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("expected history file mode 0600, got %04o", mode)
	}
}
