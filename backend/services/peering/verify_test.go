package peering

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStderr redirects os.Stderr to a buffer for the duration of f, then
// restores it.  Returns the bytes written.
func captureStderr(f func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		panic(fmt.Sprintf("captureStderr: pipe: %v", err))
	}
	old := os.Stderr
	os.Stderr = w
	f()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	r.Close()
	return string(out)
}

// newTestSvc creates an EmailVerifyService backed by a temp directory.
func newTestSvc(t *testing.T) *EmailVerifyService {
	t.Helper()
	return NewEmailVerifyService(t.TempDir())
}

// ─── Start ─────────────────────────────────────────────────────────────────────

func TestEmailVerifyStart_LogsToken(t *testing.T) {
	// Non-prod: the token is printed verbatim so operators can complete the flow
	// without a real mail sender.
	t.Setenv("VULOS_ENV", "local")
	svc := newTestSvc(t)
	var tok string
	out := captureStderr(func() {
		var err error
		tok, err = svc.Start("alice@vulos.org")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	})
	if tok == "" {
		t.Fatal("Start returned empty token")
	}
	want := fmt.Sprintf("[peering] email verification token for alice@vulos.org: %s", tok)
	if !strings.Contains(out, want) {
		t.Fatalf("stderr did not contain %q; got: %q", want, out)
	}
}

// TestEmailVerifyStart_RedactsTokenInProd is the wave-34 fail-open regression:
// the verification token is a live credential (replaying it to
// /verify/email/confirm hijacks the email→identity binding). In prod it must
// NOT appear in logs.
func TestEmailVerifyStart_RedactsTokenInProd(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	svc := newTestSvc(t)
	var tok string
	out := captureStderr(func() {
		var err error
		tok, err = svc.Start("alice@vulos.org")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	})
	if tok == "" {
		t.Fatal("Start returned empty token")
	}
	if strings.Contains(out, tok) {
		t.Fatalf("prod: token leaked to stderr; out=%q", out)
	}
	// It should still note that a token was issued (redacted).
	if !strings.Contains(out, "redacted") {
		t.Errorf("prod: expected a redacted issuance line; got: %q", out)
	}
}

func TestEmailVerifyStart_Returns204(t *testing.T) {
	svc := newTestSvc(t)
	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	body := bytes.NewBufferString(`{"email":"bob@vulos.org"}`)
	req := httptest.NewRequest("POST", "/verify/email/start", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestEmailVerifyStart_MissingEmail_400(t *testing.T) {
	svc := newTestSvc(t)
	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	req := httptest.NewRequest("POST", "/verify/email/start", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestEmailVerifyStart_RateLimit_429(t *testing.T) {
	svc := newTestSvc(t)
	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	send := func() int {
		body := bytes.NewBufferString(`{"email":"carol@vulos.org"}`)
		req := httptest.NewRequest("POST", "/verify/email/start", body)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	// First request must succeed.
	if code := send(); code != http.StatusNoContent {
		t.Fatalf("first start: expected 204, got %d", code)
	}
	// Immediate second request must be rate-limited.
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("second start (rate-limited): expected 429, got %d", code)
	}
}

// ─── Confirm ───────────────────────────────────────────────────────────────────

func TestEmailVerifyConfirm_ConsumesToken(t *testing.T) {
	svc := newTestSvc(t)
	tok, err := svc.Start("dave@vulos.org")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := svc.Confirm(tok); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	st := svc.Status()
	if !st.Bound {
		t.Fatal("expected bound=true after confirm")
	}
	if st.Email != "dave@vulos.org" {
		t.Fatalf("expected email dave@vulos.org, got %q", st.Email)
	}
}

func TestEmailVerifyConfirm_AlreadyUsed_410(t *testing.T) {
	svc := newTestSvc(t)
	tok, _ := svc.Start("eve@vulos.org")
	if err := svc.Confirm(tok); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	err := svc.Confirm(tok)
	if err == nil {
		t.Fatal("expected error on second Confirm")
	}
	if !IsVerifyGone(err) {
		t.Fatalf("expected verifyGoneError, got %T: %v", err, err)
	}
}

func TestEmailVerifyConfirm_Expired_410(t *testing.T) {
	svc := newTestSvc(t)
	tok, _ := svc.Start("frank@vulos.org")

	// Back-date the expiry.
	svc.mu.Lock()
	svc.state.Pending.ExpiresAt = time.Now().Add(-time.Second)
	svc.mu.Unlock()

	err := svc.Confirm(tok)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !IsVerifyGone(err) {
		t.Fatalf("expected verifyGoneError for expired token, got %T: %v", err, err)
	}
}

func TestEmailVerifyConfirm_WrongToken_410(t *testing.T) {
	svc := newTestSvc(t)
	_, _ = svc.Start("grace@vulos.org")
	err := svc.Confirm("00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected error for wrong token")
	}
	if !IsVerifyGone(err) {
		t.Fatalf("expected verifyGoneError for wrong token, got %T: %v", err, err)
	}
}

func TestEmailVerifyConfirm_HTTP_200(t *testing.T) {
	svc := newTestSvc(t)
	tok, _ := svc.Start("henry@vulos.org")

	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	req := httptest.NewRequest("GET", "/verify/email/confirm?token="+tok, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var st VerifyEmailState
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !st.Bound {
		t.Fatal("expected bound=true in response")
	}
}

func TestEmailVerifyConfirm_HTTP_410_ExpiredUsed(t *testing.T) {
	svc := newTestSvc(t)
	tok, _ := svc.Start("ivy@vulos.org")

	// Use the token once.
	if err := svc.Confirm(tok); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}

	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	req := httptest.NewRequest("GET", "/verify/email/confirm?token="+tok, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 for reused token, got %d", rr.Code)
	}
}

func TestEmailVerifyConfirm_MissingToken_400(t *testing.T) {
	svc := newTestSvc(t)
	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	req := httptest.NewRequest("GET", "/verify/email/confirm", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// ─── Status ────────────────────────────────────────────────────────────────────

func TestEmailVerifyStatus_Unbound(t *testing.T) {
	svc := newTestSvc(t)
	st := svc.Status()
	if st.Bound {
		t.Fatal("fresh service must not be bound")
	}
}

func TestEmailVerifyStatus_AfterStart(t *testing.T) {
	svc := newTestSvc(t)
	_, _ = svc.Start("jack@vulos.org")
	st := svc.Status()
	if st.Email != "jack@vulos.org" {
		t.Fatalf("expected email jack@vulos.org, got %q", st.Email)
	}
	if st.Bound {
		t.Fatal("must not be bound before confirm")
	}
	if st.LastAttempt == "" {
		t.Fatal("last_attempt must be set after start")
	}
}

func TestEmailVerifyStatus_HTTP(t *testing.T) {
	svc := newTestSvc(t)
	tok, _ := svc.Start("kate@vulos.org")
	if err := svc.Confirm(tok); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	mux := http.NewServeMux()
	RegisterEmailVerifyHandlers(mux, svc)

	req := httptest.NewRequest("GET", "/verify/email/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var st VerifyEmailState
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.Bound {
		t.Fatal("expected bound=true")
	}
	if st.Email != "kate@vulos.org" {
		t.Fatalf("expected kate@vulos.org, got %q", st.Email)
	}
	// Status must not expose the raw token.
	if st.Pending != nil {
		t.Fatal("status must not expose the pending token")
	}
}

func TestEmailVerifyStatus_DoesNotExposeToken(t *testing.T) {
	svc := newTestSvc(t)
	_, _ = svc.Start("liam@vulos.org")
	st := svc.Status()
	if st.Pending != nil {
		t.Fatal("Status() must not expose the pending token")
	}
}

// ─── Persistence ───────────────────────────────────────────────────────────────

func TestEmailVerifyPersistence(t *testing.T) {
	dir := t.TempDir()
	svc1 := NewEmailVerifyService(dir)
	tok, err := svc1.Start("mia@vulos.org")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Reload from disk.
	svc2 := NewEmailVerifyService(dir)
	if err := svc2.Confirm(tok); err != nil {
		t.Fatalf("Confirm after reload: %v", err)
	}
	if st := svc2.Status(); !st.Bound {
		t.Fatal("expected bound=true after reload + confirm")
	}
}
