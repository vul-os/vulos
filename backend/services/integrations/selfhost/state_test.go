package selfhost

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestState_RoundTripAndBinding(t *testing.T) {
	secret := []byte("state-secret-key")
	now := time.Now()

	rec := httptest.NewRecorder()
	state, challenge, err := GenerateState(rec, secret, "user-1", ProviderGoogle, now, false)
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	if challenge == "" {
		t.Fatalf("empty PKCE challenge")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no state cookie")
	}

	// A callback carrying the cookie + matching state + same user verifies.
	req := httptest.NewRequest(http.MethodGet, "/api/integrations/google/callback", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	verifier, err := VerifyState(req, state, string(secret), "user-1", ProviderGoogle, now)
	if err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
	if verifier == "" {
		t.Fatalf("empty verifier")
	}
	// The challenge must be the S256 of the verifier (PKCE binding).
	if pkceChallengeS256(verifier) != challenge {
		t.Fatalf("challenge is not S256(verifier)")
	}
}

func TestState_RejectsCrossUserAndTamper(t *testing.T) {
	secret := []byte("state-secret-key")
	now := time.Now()
	rec := httptest.NewRecorder()
	state, _, _ := GenerateState(rec, secret, "user-1", ProviderGoogle, now, false)
	cookies := rec.Result().Cookies()

	mk := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/cb", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		return req
	}

	// Different session user → reject (a callback for user-2 cannot complete
	// user-1's flow).
	if _, err := VerifyState(mk(), state, string(secret), "user-2", ProviderGoogle, now); err != ErrStateMismatch {
		t.Fatalf("cross-user: got %v want ErrStateMismatch", err)
	}
	// Different provider → reject.
	if _, err := VerifyState(mk(), state, string(secret), "user-1", ProviderDropbox, now); err != ErrStateMismatch {
		t.Fatalf("cross-provider: got %v want ErrStateMismatch", err)
	}
	// Wrong secret → reject (HMAC integrity).
	if _, err := VerifyState(mk(), state, "wrong-secret", "user-1", ProviderGoogle, now); err != ErrStateMismatch {
		t.Fatalf("wrong secret: got %v want ErrStateMismatch", err)
	}
	// Tampered state value → reject.
	if _, err := VerifyState(mk(), state+"x", string(secret), "user-1", ProviderGoogle, now); err != ErrStateMismatch {
		t.Fatalf("tampered state: got %v want ErrStateMismatch", err)
	}
	// Expired (11 minutes later) → reject.
	if _, err := VerifyState(mk(), state, string(secret), "user-1", ProviderGoogle, now.Add(11*time.Minute)); err != ErrStateMismatch {
		t.Fatalf("expired: got %v want ErrStateMismatch", err)
	}
	// No cookie → reject.
	bare := httptest.NewRequest(http.MethodGet, "/cb", nil)
	if _, err := VerifyState(bare, state, string(secret), "user-1", ProviderGoogle, now); err != ErrStateMismatch {
		t.Fatalf("no cookie: got %v want ErrStateMismatch", err)
	}
}

func TestBuildRedirectURL(t *testing.T) {
	t.Setenv("OAUTH_REDIRECT_BASE", "https://box.example.com/")
	got := buildRedirectURL(ProviderGoogle)
	want := "https://box.example.com/api/integrations/google/callback"
	if got != want {
		t.Fatalf("redirect=%q want %q", got, want)
	}
}
