package auth

import (
	"context"
	"testing"
)

// TestSharedLoginLimitOffByDefault: with the flag unset, the shared cap is inert
// and login behaves exactly as before (the login_attempts table stays dormant).
func TestSharedLoginLimitOffByDefault(t *testing.T) {
	t.Setenv("AUTH_SHARED_LOGIN_LIMIT", "")
	st := openTestStore(t)
	ctx := context.Background()
	// Many "attempts" should never throttle while off.
	for i := 0; i < loginMaxFailPerEmail+loginMaxFailPerIP+10; i++ {
		st.recordLoginAttempt(ctx, "a@b.com", "1.2.3.4", false)
	}
	if err := st.checkLoginThrottle(ctx, "a@b.com", "1.2.3.4"); err != nil {
		t.Fatalf("throttle must be inert when flag off, got %v", err)
	}
	// And nothing was written (table dormant).
	ef, ipf := st.loginThrottleStatus(ctx, "a@b.com", "1.2.3.4")
	if ef != 0 || ipf != 0 {
		t.Fatalf("expected no recorded attempts when off, got email=%d ip=%d", ef, ipf)
	}
}

// TestSharedLoginLimitPerEmail: exceeding the per-email failure cap trips
// ErrLoginThrottled.
func TestSharedLoginLimitPerEmail(t *testing.T) {
	t.Setenv("AUTH_SHARED_LOGIN_LIMIT", "1")
	st := openTestStore(t)
	ctx := context.Background()

	// Spread across DIFFERENT IPs so the IP cap is not the one that trips —
	// isolating the per-account (credential-stuffing) throttle.
	for i := 0; i < loginMaxFailPerEmail; i++ {
		ip := "10.0." + itoa(i/250) + "." + itoa(i%250)
		st.recordLoginAttempt(ctx, "victim@vulos.to", ip, false)
	}
	if err := st.checkLoginThrottle(ctx, "victim@vulos.to", "10.9.9.9"); err == nil {
		t.Fatal("expected per-email throttle to trip after cap of failures")
	}
	// A DIFFERENT account from a fresh IP is unaffected.
	if err := st.checkLoginThrottle(ctx, "innocent@vulos.to", "10.9.9.9"); err != nil {
		t.Fatalf("unrelated account must not be throttled, got %v", err)
	}
}

// TestSharedLoginLimitPerIP: exceeding the per-IP failure cap trips
// ErrLoginThrottled even across many different target emails (botnet spray).
func TestSharedLoginLimitPerIP(t *testing.T) {
	t.Setenv("AUTH_SHARED_LOGIN_LIMIT", "1")
	st := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < loginMaxFailPerIP; i++ {
		st.recordLoginAttempt(ctx, "u"+itoa(i)+"@vulos.to", "203.0.113.66", false)
	}
	if err := st.checkLoginThrottle(ctx, "fresh@vulos.to", "203.0.113.66"); err == nil {
		t.Fatal("expected per-IP throttle to trip after cap of failures from one IP")
	}
	// A different IP is unaffected.
	if err := st.checkLoginThrottle(ctx, "fresh@vulos.to", "203.0.113.99"); err != nil {
		t.Fatalf("unrelated IP must not be throttled, got %v", err)
	}
}

// TestSharedLoginLimitSuccessNotCounted: successful logins (ok=1) do not count
// toward the failure cap, so a heavily-used account is never throttled by its
// own successes.
func TestSharedLoginLimitSuccessNotCounted(t *testing.T) {
	t.Setenv("AUTH_SHARED_LOGIN_LIMIT", "1")
	st := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < loginMaxFailPerEmail*2; i++ {
		st.recordLoginAttempt(ctx, "busy@vulos.to", "192.0.2.10", true) // successes
	}
	if err := st.checkLoginThrottle(ctx, "busy@vulos.to", "192.0.2.10"); err != nil {
		t.Fatalf("successes must not throttle, got %v", err)
	}
}

// TestLoginRecordsFailureUnderFlag: a real Login with a wrong password records a
// FAILED attempt when the flag is on; and repeated failures eventually throttle
// the account through the public Login entrypoint.
func TestLoginRecordsFailureUnderFlag(t *testing.T) {
	t.Setenv("AUTH_SHARED_LOGIN_LIMIT", "1")
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	st := openTestStore(t)
	ctx := context.Background()
	if _, _, err := st.Signup(ctx, "throttle@vulos.to", "correct-horse-battery-staple", "127.0.0.1", "ua"); err != nil {
		t.Fatalf("signup: %v", err)
	}

	// Drive failures from varying IPs so the per-email cap is what trips.
	var lastErr error
	for i := 0; i < loginMaxFailPerEmail+2; i++ {
		ip := "172.16." + itoa(i/250) + "." + itoa(i%250)
		_, lastErr = st.Login(ctx, "throttle@vulos.to", "wrong", ip, "ua")
	}
	if lastErr != ErrLoginThrottled {
		t.Fatalf("expected ErrLoginThrottled after repeated failures, got %v", lastErr)
	}
	// Even the CORRECT password is now refused (throttle is checked before creds).
	if _, err := st.Login(ctx, "throttle@vulos.to", "correct-horse-battery-staple", "172.31.1.1", "ua"); err != ErrLoginThrottled {
		t.Fatalf("expected throttle to gate even correct password, got %v", err)
	}
}

// itoa is a tiny int->string helper avoiding an strconv import churn in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
