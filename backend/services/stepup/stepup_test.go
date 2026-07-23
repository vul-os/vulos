package stepup

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withTempDataDir(t *testing.T) {
	t.Helper()
	t.Setenv("VULOS_DATA_DIR", t.TempDir())
}

func TestMintVerifyRoundTrip(t *testing.T) {
	withTempDataDir(t)

	token, expires, err := Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token == "" {
		t.Fatal("Mint returned empty token")
	}
	if !expires.After(time.Now()) {
		t.Fatal("Mint returned an already-expired token")
	}
	if !Verify(token, "user-1") {
		t.Fatal("Verify rejected a freshly minted token for its own user")
	}
}

func TestVerifyRejectsWrongUser(t *testing.T) {
	withTempDataDir(t)

	token, _, err := Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if Verify(token, "user-2") {
		t.Fatal("Verify accepted a token minted for a different user")
	}
}

func TestVerifyRejectsTamperedToken(t *testing.T) {
	withTempDataDir(t)

	token, _, err := Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	tampered := token + "x"
	if Verify(tampered, "user-1") {
		t.Fatal("Verify accepted a tampered token")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	withTempDataDir(t)

	key, err := hmacKey()
	if err != nil {
		t.Fatalf("hmacKey: %v", err)
	}
	past := time.Now().Add(-time.Minute).Unix()
	payload := "user-1|" + itoa(past) + "|deadbeef"
	sig := sign(key, payload)
	token := b64(payload) + "." + b64raw(sig)
	if Verify(token, "user-1") {
		t.Fatal("Verify accepted an expired token")
	}
}

func TestVerifyRejectsEmptyInputs(t *testing.T) {
	withTempDataDir(t)
	if Verify("", "user-1") {
		t.Fatal("Verify accepted an empty token")
	}
	if Verify("whatever", "") {
		t.Fatal("Verify accepted an empty user id")
	}
}

func TestRequireFailsClosedWithoutSession(t *testing.T) {
	withTempDataDir(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := Require(r); err == nil {
		t.Fatal("Require succeeded with no X-User-ID header")
	}
}

func TestRequireFailsClosedWithoutToken(t *testing.T) {
	withTempDataDir(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User-ID", "user-1")
	if err := Require(r); err != ErrNotElevated {
		t.Fatalf("Require = %v, want ErrNotElevated", err)
	}
}

func TestRequireSucceedsWithValidCookie(t *testing.T) {
	withTempDataDir(t)

	token, expires, err := Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	w := httptest.NewRecorder()
	mintReq := httptest.NewRequest(http.MethodPost, "/api/auth/stepup/verify", nil)
	SetCookie(w, mintReq, token, expires)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User-ID", "user-1")
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}

	if err := Require(r); err != nil {
		t.Fatalf("Require = %v, want nil", err)
	}
}

func TestRequireSucceedsWithHeaderToken(t *testing.T) {
	withTempDataDir(t)

	token, _, err := Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-User-ID", "user-1")
	r.Header.Set(headerName, token)

	if err := Require(r); err != nil {
		t.Fatalf("Require = %v, want nil", err)
	}
}

func TestClearCookieExpiresImmediately(t *testing.T) {
	withTempDataDir(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ClearCookie(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("ClearCookie did not produce an expiring cookie: %+v", cookies)
	}
}

// --- tiny local helpers to build a malformed/expired token by hand,
// mirroring Mint's own encoding without exporting internals just for tests.

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func b64(s string) string    { return b64raw([]byte(s)) }
func b64raw(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
