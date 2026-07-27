package telephony

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── fail-closed default ────────────────────────────────────────────────────────

// With no VULOS_TELEPHONY_PROVIDER env, New must install the noop provider: the
// virtual line reports unconfigured and every action returns ErrNoProvider — never
// a silent success or a default vendor dial.
func TestProvider_UnconfiguredFailsClosed(t *testing.T) {
	// Make sure no ambient provider env leaks in from the host.
	for _, k := range []string{"VULOS_TELEPHONY_PROVIDER", "VULOS_TELEPHONY_NUMBER", "VULOS_TELEPHONY_SEND_URL", "VULOS_TELEPHONY_WEBHOOK_SECRET"} {
		t.Setenv(k, "")
	}
	s := newTeleService("u1")

	if st := s.VirtualStatus(); st.Configured {
		t.Fatalf("unconfigured box must report Configured=false, got %+v", st)
	}
	if err := s.VirtualSend("+15551234567", "hi"); err != ErrNoProvider {
		t.Fatalf("VirtualSend must fail closed with ErrNoProvider, got %v", err)
	}
	if _, err := s.VirtualPlaceCall("+15551234567"); err != ErrNoProvider {
		t.Fatalf("VirtualPlaceCall must fail closed with ErrNoProvider, got %v", err)
	}
}

// A half-configured http provider (number but no endpoints) must still resolve to
// noop — never a partially-on state.
func TestProvider_IncompleteConfigFallsBackToNoop(t *testing.T) {
	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", "") // no endpoint
	t.Setenv("VULOS_TELEPHONY_CALL_URL", "")

	p := providerFromEnv()
	if p.Configured() {
		t.Fatalf("number-only config must be unconfigured (fail-closed), got %+v", p)
	}
	if p.Name() != "none" {
		t.Errorf("expected noop provider, got %q", p.Name())
	}
}

// ─── configured http provider: outbound send over the internet ──────────────────

func TestHTTPProvider_SendSMS_PostsToEndpoint(t *testing.T) {
	var gotAuth string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", srv.URL)
	t.Setenv("VULOS_TELEPHONY_PROVIDER_TOKEN", "sekret")

	s := newTeleService("u1")
	if st := s.VirtualStatus(); !st.Configured || st.Number != "+15550000000" {
		t.Fatalf("expected configured provider, got %+v", st)
	}

	if err := s.VirtualSend("+15551234567", "hello, number=+19998887777"); err != nil {
		t.Fatalf("VirtualSend: %v", err)
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("bearer not forwarded: %q", gotAuth)
	}
	// The body is JSON-encoded, so a comma/"number=" in the message stays in the
	// body field and cannot inject a second recipient.
	if gotBody["to"] != "+15551234567" || gotBody["from"] != "+15550000000" {
		t.Errorf("wrong routing fields: %v", gotBody)
	}
	if gotBody["body"] != "hello, number=+19998887777" {
		t.Errorf("body corrupted / injection leaked into a field: %v", gotBody)
	}

	// The sent message is recorded on the virtual line and shows up in threads.
	threads := s.VirtualThreads()
	if len(threads) != 1 || threads[0].Number != "+15551234567" {
		t.Fatalf("sent message not recorded on virtual line: %+v", threads)
	}
}

// A malformed recipient must be rejected before any HTTP call to the adapter.
func TestHTTPProvider_SendSMS_RejectsBadRecipient(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", srv.URL)

	s := newTeleService("u1")
	if err := s.VirtualSend("+1,text=redirected", "x"); err == nil {
		t.Fatal("malicious recipient must be rejected")
	}
	if called {
		t.Error("no request must reach the adapter for an invalid recipient")
	}
}

// A non-2xx from the adapter surfaces as an error (not a false success).
func TestHTTPProvider_SendSMS_PropagatesAdapterError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", srv.URL)

	s := newTeleService("u1")
	if err := s.VirtualSend("+15551234567", "x"); err == nil {
		t.Fatal("a 502 from the adapter must surface as an error")
	}
	if len(s.VirtualThreads()) != 0 {
		t.Error("a failed send must NOT be recorded on the virtual line")
	}
}

// ─── inbound webhook: HMAC-gated ingress ────────────────────────────────────────

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// A correctly-signed inbound SMS is recorded on the virtual line and fired as an
// owner-targeted notification.
func TestInboundWebhook_ValidSignatureIngests(t *testing.T) {
	const secret = "whsec"
	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", "http://unused.invalid")
	t.Setenv("VULOS_TELEPHONY_WEBHOOK_SECRET", secret)

	notes := &recordingNotifier{}
	s := New(notes, func() string { return "u1" })
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	body := []byte(`{"type":"sms","from":"+15551112222","body":"code 4821"}`)
	req := httptest.NewRequest("POST", "/api/telephony/provider/inbound", strings.NewReader(string(body)))
	req.Header.Set("X-Vulos-Signature", sign(secret, body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid inbound: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	th := s.VirtualThreadFor("+15551112222")
	if len(th) != 1 || th[0].Direction != "incoming" || th[0].Body != "code 4821" {
		t.Fatalf("inbound SMS not recorded correctly: %+v", th)
	}
	if len(notes.titles) != 1 || !strings.Contains(notes.titles[0], "+15551112222") {
		t.Errorf("expected an owner-targeted SMS notification, got %v", notes.titles)
	}
}

// A wrong / missing signature is rejected with 401 and nothing is ingested — the
// internet-facing endpoint can't be spoofed into injecting a fake OTP.
func TestInboundWebhook_BadSignatureRejected(t *testing.T) {
	const secret = "whsec"
	t.Setenv("VULOS_TELEPHONY_PROVIDER", "http")
	t.Setenv("VULOS_TELEPHONY_NUMBER", "+15550000000")
	t.Setenv("VULOS_TELEPHONY_SEND_URL", "http://unused.invalid")
	t.Setenv("VULOS_TELEPHONY_WEBHOOK_SECRET", secret)

	s := newTeleService("u1")
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	body := `{"type":"sms","from":"+1","body":"forged"}`
	for _, sig := range []string{"", "deadbeef", sign("wrong-secret", []byte(body))} {
		req := httptest.NewRequest("POST", "/api/telephony/provider/inbound", strings.NewReader(body))
		if sig != "" {
			req.Header.Set("X-Vulos-Signature", sig)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("sig=%q: got %d, want 401", sig, rec.Code)
		}
	}
	if len(s.VirtualThreads()) != 0 {
		t.Error("no forged inbound message may be ingested")
	}
}

// With no provider configured the webhook fails closed (401) — it is not an
// InboundVerifier, so every call is rejected regardless of signature.
func TestInboundWebhook_NoProviderFailsClosed(t *testing.T) {
	for _, k := range []string{"VULOS_TELEPHONY_PROVIDER", "VULOS_TELEPHONY_WEBHOOK_SECRET"} {
		t.Setenv(k, "")
	}
	s := newTeleService("u1")
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	req := httptest.NewRequest("POST", "/api/telephony/provider/inbound", strings.NewReader(`{"type":"sms"}`))
	req.Header.Set("X-Vulos-Signature", "anything")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unconfigured webhook must fail closed: got %d, want 401", rec.Code)
	}
}

// recordingNotifier captures fired notifications for assertions.
type recordingNotifier struct{ titles []string }

func (n *recordingNotifier) Send(title, body, source string) { n.titles = append(n.titles, title) }
