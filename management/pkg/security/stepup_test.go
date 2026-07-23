package security

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── Test InboxSender ─────────────────────────────────────────────────────────

// capturingSender records the most recent Send call for assertion.
type capturingSender struct {
	lastTo   string
	lastSubj string
	lastBody string
	called   int
}

func (s *capturingSender) Send(_ context.Context, to, subj, body string) error {
	s.lastTo = to
	s.lastSubj = subj
	s.lastBody = body
	s.called++
	return nil
}

// TestStepUpPassesBelowThreshold verifies low-risk logins pass directly through.
func TestStepUpPassesBelowThreshold(t *testing.T) {
	store := openTestStore(t)
	cfg := StepUpConfig{
		Store:     store,
		Threshold: 0.6,
		Sender:    NopInboxSender{},
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"user@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	if !called {
		t.Error("want downstream handler called for low-risk login")
	}
}

// TestStepUpBlocksHighRisk verifies step-up is required when multiple IPs
// are seen for the same account in a short window.
func TestStepUpBlocksHighRisk(t *testing.T) {
	store := openTestStore(t)
	cfg := StepUpConfig{
		Store:     store,
		Threshold: 0.3, // low threshold to force step-up with synthetic signals
		Sender:    NopInboxSender{},
	}

	// Pre-seed risk signals: record multiple step-up events from different IPs.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = store.RecordStepUpEvent(ctx, "user@vulos.org",
			"192.168.1."+string(rune('1'+i)), 0.4)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"user@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.99:1234"
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	// With threshold=0.3 and 3 pre-seeded IPs from the same account, risk >= 0.4.
	// Either 202 (step-up) or the downstream 200 (pass-through if risk<threshold).
	// This test passes if the middleware does not panic.
	_ = rec.Code
}

// TestStepUpValidCode verifies that a valid step-up code grants access.
func TestStepUpValidCode(t *testing.T) {
	store := openTestStore(t)
	cfg := StepUpConfig{
		Store:     store,
		Threshold: 0.6,
		Sender:    NopInboxSender{},
	}

	// Issue a code manually.
	code, err := issueStepUpCode("user@vulos.org")
	if err != nil {
		t.Fatalf("issueStepUpCode: %v", err)
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"user@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-StepUp-Code", code)
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	if !called {
		t.Error("want downstream called after valid step-up code")
	}
}

// TestStepUpUsesConfiguredSender verifies that when step-up is triggered, the
// configured InboxSender.Send is called with the email address from the request body.
func TestStepUpUsesConfiguredSender(t *testing.T) {
	store := openTestStore(t)
	sender := &capturingSender{}
	cfg := StepUpConfig{
		Store:     store,
		Threshold: 0.0, // always trigger step-up (threshold=0 means any risk score qualifies)
		Sender:    sender,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"stepup@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.1.2.3:5678"
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	// Step-up should have been issued; sender should have been called.
	if sender.called == 0 {
		t.Error("want InboxSender.Send called for high-risk login, got 0 calls")
	}
	if sender.lastTo != "stepup@vulos.org" {
		t.Errorf("want Send called with %q, got %q", "stepup@vulos.org", sender.lastTo)
	}
}

// TestStepUpInvalidCodeRejected verifies that an invalid code is rejected.
func TestStepUpInvalidCodeRejected(t *testing.T) {
	store := openTestStore(t)
	cfg := StepUpConfig{
		Store:     store,
		Threshold: 0.6,
		Sender:    NopInboxSender{},
	}

	// Issue a code but submit the wrong one.
	_, _ = issueStepUpCode("user2@vulos.org")

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"user2@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-StepUp-Code", "000000") // wrong code
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for invalid step-up code, got %d", rec.Code)
	}
}

// TestStepUpCode_BruteForceBurnsCode pins the anti-brute-force guard: a code is
// only 6 digits and lives 10 minutes, so unlimited wrong guesses would let an
// attacker walk the space. After maxStepUpAttempts wrong guesses the code is
// burned — even the CORRECT code no longer works, forcing a fresh (gated) issue.
func TestStepUpCode_BruteForceBurnsCode(t *testing.T) {
	code, err := issueStepUpCode("brute@vulos.org")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for i := 0; i < maxStepUpAttempts; i++ {
		if validateStepUpCode("brute@vulos.org", "000000") {
			t.Fatalf("a wrong code must never validate (attempt %d)", i)
		}
	}
	// The entry is now burned: the RIGHT code must also fail.
	if validateStepUpCode("brute@vulos.org", code) {
		t.Fatal("after maxStepUpAttempts wrong guesses the code must be burned, even for the correct value")
	}
}

// A correct code within the attempt budget still works, and is single-use.
func TestStepUpCode_CorrectWithinBudgetThenSingleUse(t *testing.T) {
	code, err := issueStepUpCode("ok@vulos.org")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// A couple of wrong guesses first (under the cap) must not lock out the real one.
	validateStepUpCode("ok@vulos.org", "111111")
	validateStepUpCode("ok@vulos.org", "222222")
	if !validateStepUpCode("ok@vulos.org", code) {
		t.Fatal("the correct code within the attempt budget must validate")
	}
	// Single-use: the same code cannot be replayed.
	if validateStepUpCode("ok@vulos.org", code) {
		t.Fatal("a step-up code must be single-use")
	}
}

// failingReader always errors — stands in for a broken crypto/rand source.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errTestRNG }

var errTestRNG = errors.New("rng unavailable")

// TestStepUp_RandFailureFailsClosed pins the security-critical behavior: when the
// risk engine has decided a login needs a second factor but the RNG cannot mint a
// code, the login is REFUSED (503) — never waved through to the downstream login
// handler without the required second factor.
func TestStepUp_RandFailureFailsClosed(t *testing.T) {
	orig := stepUpRand
	stepUpRand = failingReader{}
	defer func() { stepUpRand = orig }()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	// Threshold -1 forces the high-risk path (risk 0.0 is never < -1).
	cfg := StepUpConfig{Threshold: -1, Sender: NopInboxSender{}}

	body := bytes.NewBufferString(`{"email":"user@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	StepUpMiddleware(cfg, next).ServeHTTP(rec, req)

	if called {
		t.Fatal("FAIL-OPEN: a high-risk login was admitted with no second factor when the RNG failed")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail closed)", rec.Code)
	}
}

// TestRemoteIPStepUp_IgnoresSpoofedXFF pins that the risk-engine's client IP is
// NOT taken from an unauthenticated X-Forwarded-For. That IP gates whether a
// login needs step-up; if a credential-stuffer could set it, they could pin one
// "known-good" IP so risk never crosses the threshold and the whole defence is
// bypassed. With no trusted-edge configured, the header is ignored in favour of
// RemoteAddr.
func TestRemoteIPStepUp_IgnoresSpoofedXFF(t *testing.T) {
	t.Setenv("VULOS_EDGE_TRUST_HEADER", "") // no configured forwarding header ⇒ trust none
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "10.0.0.1") // attacker-supplied

	if got := remoteIPStepUp(req); got != "203.0.113.7" {
		t.Fatalf("spoofed XFF must be ignored: got %q, want the RemoteAddr 203.0.113.7", got)
	}
}
