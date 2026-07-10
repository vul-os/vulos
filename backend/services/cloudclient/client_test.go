// client_test.go — UNIFIED-SIGNIN: the CP HTTP client must survive the real
// gates (CSRF Origin allowlist + PoW CaptchaGate), drive the structured login
// steps, and split the issued wire token into verifier inputs.
package cloudclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/cloudclient/cloudclienttest"
)

const testULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func newTestClient(t *testing.T, fc *cloudclienttest.FakeCP) *Client {
	t.Helper()
	// The fake CP allows the SPA dev origin, same as the real local CP.
	t.Setenv("VULOS_CLOUD_ORIGIN", fc.AllowedOrigin)
	return New(fc.URL())
}

// ─── PoW ──────────────────────────────────────────────────────────────────────

func TestSolveChallenge_ProducesValidHashcash(t *testing.T) {
	got, err := SolveChallenge(context.Background(), "deadbeef", 8)
	if err != nil {
		t.Fatalf("SolveChallenge: %v", err)
	}
	parts := strings.SplitN(got, ":", 2)
	if len(parts) != 2 || parts[0] != "deadbeef" {
		t.Fatalf("malformed header value %q", got)
	}
	sum := sha256.Sum256([]byte(parts[0] + parts[1]))
	if sum[0] != 0 { // 8 leading zero bits = first byte zero
		t.Fatalf("nonce does not satisfy 8 leading zero bits: %x", sum)
	}
}

func TestSolveChallenge_RespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Difficulty 256 is unsolvable — only cancellation can end the loop.
	if _, err := SolveChallenge(ctx, "x", 256); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// The client must actually SOLVE the PoW (verified by the fake CP's real
// hashcash check) — not just retry blindly.
func TestLogin_SolvesPoWAndSendsOrigin(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{
		Email: "ada@vulos.test", Password: "correct horse battery", EmailVerified: true,
	})

	c := newTestClient(t, fc)
	out, err := c.Login(context.Background(), "ada@vulos.test", "correct horse battery", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if out.Step != "" {
		t.Fatalf("want full session, got step %q", out.Step)
	}
	if fc.PoWAccepted() != 1 {
		t.Fatalf("fake CP accepted %d PoW headers, want 1", fc.PoWAccepted())
	}
	var user map[string]any
	if err := json.Unmarshal(out.User, &user); err != nil || user["email"] != "ada@vulos.test" {
		t.Fatalf("unexpected user payload: %s (err=%v)", out.User, err)
	}
}

// A client with a NON-allowed Origin must be refused by the CSRF gate — the
// error surfaces as a StatusError, proving the gate is actually exercised.
func TestLogin_WrongOriginRejected(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{
		Email: "ada@vulos.test", Password: "pw123456789012", EmailVerified: true,
	})

	t.Setenv("VULOS_CLOUD_ORIGIN", "https://evil.example")
	c := New(fc.URL())
	_, err := c.Login(context.Background(), "ada@vulos.test", "pw123456789012", "")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 403 {
		t.Fatalf("want 403 StatusError from CSRF gate, got %v", err)
	}
}

func TestResolveOrigin(t *testing.T) {
	t.Setenv("VULOS_CLOUD_ORIGIN", "")
	cases := []struct{ base, want string }{
		{"http://localhost:8099", "http://localhost:5173"},
		{"http://127.0.0.1:8081", "http://localhost:5173"},
		{"https://api.vulos.org", "https://api.vulos.org"},
	}
	for _, tc := range cases {
		if got := resolveOrigin(tc.base); got != tc.want {
			t.Errorf("resolveOrigin(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
	t.Setenv("VULOS_CLOUD_ORIGIN", "https://cloud.example")
	if got := resolveOrigin("http://localhost:9"); got != "https://cloud.example" {
		t.Errorf("env override: got %q", got)
	}
}

// ─── Login steps ─────────────────────────────────────────────────────────────

func TestLogin_InvalidCredentials(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "right", EmailVerified: true})

	c := newTestClient(t, fc)
	_, err := c.Login(context.Background(), "ada@vulos.test", "wrong", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_EmailVerificationRequiredStep(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{Email: "new@vulos.test", Password: "pw", EmailVerified: false})

	c := newTestClient(t, fc)
	out, err := c.Login(context.Background(), "new@vulos.test", "pw", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if out.Step != StepEmailVerificationRequired {
		t.Fatalf("want %q step, got %q", StepEmailVerificationRequired, out.Step)
	}
}

func TestLogin_TOTPRequiredStep_ThenVerify(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{
		Email: "sec@vulos.test", Password: "pw", EmailVerified: true, TOTPCode: "424242",
	})

	// Without a code: surface the step so the UI can prompt.
	c := newTestClient(t, fc)
	out, err := c.Login(context.Background(), "sec@vulos.test", "pw", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if out.Step != StepTOTPRequired {
		t.Fatalf("want totp_required step, got %q", out.Step)
	}

	// With the right code: full session (fresh client = fresh jar).
	c2 := newTestClient(t, fc)
	out2, err := c2.Login(context.Background(), "sec@vulos.test", "pw", "424242")
	if err != nil {
		t.Fatalf("Login with TOTP: %v", err)
	}
	if out2.Step != "" {
		t.Fatalf("want full session, got step %q", out2.Step)
	}

	// With a wrong code: ErrInvalidTOTP.
	c3 := newTestClient(t, fc)
	if _, err := c3.Login(context.Background(), "sec@vulos.test", "pw", "000000"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("want ErrInvalidTOTP, got %v", err)
	}
}

// ─── Broker pubkey + issue ───────────────────────────────────────────────────

func TestBrokerPubkey(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()

	c := newTestClient(t, fc)
	pub, err := c.BrokerPubkey(context.Background())
	if err != nil {
		t.Fatalf("BrokerPubkey: %v", err)
	}
	if !pub.Equal(fc.BrokerPub) {
		t.Fatal("returned pubkey does not match the fake CP broker key")
	}
}

func TestIssueLoginToken_HappyPath_SignatureVerifies(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	acc := fc.AddAccount(cloudclienttest.Account{
		Email: "ada@vulos.test", Password: "pw", EmailVerified: true,
	})
	fc.BindULID(testULID, acc.ID)

	c := newTestClient(t, fc)
	if _, err := c.Login(context.Background(), "ada@vulos.test", "pw", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}
	payload, sigB64, err := c.IssueLoginToken(context.Background(), testULID, "ada")
	if err != nil {
		t.Fatalf("IssueLoginToken: %v", err)
	}

	// The raw payload must verify against the broker key with the returned sig.
	sig, err := decodeB64(sigB64)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if !ed25519.Verify(fc.BrokerPub, payload, sig) {
		t.Fatal("payload signature does not verify against broker pubkey")
	}

	// And carry the claims the OS verifier requires.
	var tok struct {
		AccountID string    `json:"account_id"`
		ULID      string    `json:"ulid"`
		Email     string    `json:"email"`
		Name      string    `json:"name"`
		JTI       string    `json:"jti"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(payload, &tok); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if tok.AccountID != acc.ID || tok.ULID != testULID || tok.Email != "ada@vulos.test" ||
		tok.Name != "ada" || tok.JTI == "" || !tok.ExpiresAt.After(time.Now()) {
		t.Fatalf("token claims wrong: %+v", tok)
	}
}

func TestIssueLoginToken_NotOwnedULID(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()
	fc.AddAccount(cloudclienttest.Account{Email: "ada@vulos.test", Password: "pw", EmailVerified: true})
	other := fc.AddAccount(cloudclienttest.Account{Email: "other@vulos.test", Password: "pw", EmailVerified: true})
	fc.BindULID(testULID, other.ID) // owned by someone else

	c := newTestClient(t, fc)
	if _, err := c.Login(context.Background(), "ada@vulos.test", "pw", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, _, err := c.IssueLoginToken(context.Background(), testULID, "ada")
	if !errors.Is(err, ErrIssueForbidden) {
		t.Fatalf("want ErrIssueForbidden, got %v", err)
	}
}

func TestIssueLoginToken_WithoutSession(t *testing.T) {
	fc := cloudclienttest.NewFakeCP()
	defer fc.Server.Close()

	c := newTestClient(t, fc)
	_, _, err := c.IssueLoginToken(context.Background(), testULID, "ada")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("want 401 StatusError, got %v", err)
	}
}

func decodeB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
