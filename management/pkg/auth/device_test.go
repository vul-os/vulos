// device_test.go -- tests for AUTH-07: recognised-device token.
//
// Acceptance criteria:
//   - mintDeviceToken / verifyDeviceToken round-trip
//   - IssueDeviceToken + ValidateAndRotateDeviceToken happy path
//   - ValidateAndRotateDeviceToken: cross-account token rejected
//   - ValidateAndRotateDeviceToken: expired token rejected
//   - ValidateAndRotateDeviceToken: rotated token rejected
//   - Token for user A cannot satisfy fingerprint for user B
package auth

import (
	"context"
	"encoding/base64"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Unit: mint / verify round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestDeviceToken_MintVerifyRoundTrip(t *testing.T) {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 3)
	}
	fp := deviceFingerprint("1.2.3.4", "Mozilla/5.0", "user-123")
	token, err := mintDeviceToken(fp, kek)
	if err != nil {
		t.Fatalf("mintDeviceToken: %v", err)
	}
	if !verifyDeviceToken(token, fp, kek) {
		t.Error("verifyDeviceToken: expected true for matching fp+kek")
	}
}

func TestDeviceToken_WrongKEK(t *testing.T) {
	kek1 := make([]byte, 32)
	kek2 := make([]byte, 32)
	for i := range kek2 {
		kek2[i] = 0xFF
	}
	fp := deviceFingerprint("1.2.3.4", "ua", "uid")
	token, _ := mintDeviceToken(fp, kek1)
	if verifyDeviceToken(token, fp, kek2) {
		t.Error("verifyDeviceToken: should return false for wrong KEK")
	}
}

func TestDeviceToken_WrongFingerprint(t *testing.T) {
	kek := make([]byte, 32)
	fp1 := deviceFingerprint("1.2.3.4", "ua-A", "user-1")
	fp2 := deviceFingerprint("1.2.3.4", "ua-B", "user-2")
	token, _ := mintDeviceToken(fp1, kek)
	if verifyDeviceToken(token, fp2, kek) {
		t.Error("verifyDeviceToken: should return false for different fingerprint")
	}
}

func TestDeviceToken_BadBase64(t *testing.T) {
	kek := make([]byte, 32)
	fp := deviceFingerprint("1.2.3.4", "ua", "uid")
	if verifyDeviceToken("!!!not-base64!!!", fp, kek) {
		t.Error("verifyDeviceToken: should return false for malformed token")
	}
}

func TestDeviceToken_TooShort(t *testing.T) {
	kek := make([]byte, 32)
	fp := deviceFingerprint("1.2.3.4", "ua", "uid")
	// 10 bytes — too short (need 48).
	short := base64.RawURLEncoding.EncodeToString(make([]byte, 10))
	if verifyDeviceToken(short, fp, kek) {
		t.Error("verifyDeviceToken: should return false for short token")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unit: deviceFingerprint encodes user_id
// ─────────────────────────────────────────────────────────────────────────────

func TestDeviceFingerprint_UserBound(t *testing.T) {
	fp1 := deviceFingerprint("1.2.3.4", "ua", "user-A")
	fp2 := deviceFingerprint("1.2.3.4", "ua", "user-B")
	for i := range fp1 {
		if fp1[i] != fp2[i] {
			return // they differ — good
		}
	}
	t.Error("fingerprints for different users should differ")
}

// ─────────────────────────────────────────────────────────────────────────────
// Store: IssueDeviceToken + ValidateAndRotateDeviceToken happy path
// ─────────────────────────────────────────────────────────────────────────────

func TestIssueAndValidateDeviceToken(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := openTestStore(t)
	ctx := context.Background()

	u, _, _ := st.Signup(ctx, "device@example.com", "securePass1234!", "1.2.3.4", "ua-test")

	// Issue a device token.
	token, err := st.IssueDeviceToken(ctx, u.ID, "1.2.3.4", "ua-test")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Validate + rotate: should succeed and return a new token.
	newToken, err := st.ValidateAndRotateDeviceToken(ctx, u.ID, "1.2.3.4", "ua-test", token)
	if err != nil {
		t.Fatalf("ValidateAndRotateDeviceToken: %v", err)
	}
	if newToken == "" {
		t.Fatal("expected non-empty new token")
	}
	if newToken == token {
		t.Error("new token should differ from old token (rotation)")
	}

	// Old token should be invalidated (rotated).
	_, err2 := st.ValidateAndRotateDeviceToken(ctx, u.ID, "1.2.3.4", "ua-test", token)
	if err2 == nil {
		t.Error("old (rotated) token should be rejected")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Store: cross-account rejection
// ─────────────────────────────────────────────────────────────────────────────

func TestDeviceToken_CrossAccountRejected(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := openTestStore(t)
	ctx := context.Background()

	uA, _, _ := st.Signup(ctx, "deviceA@example.com", "securePass1234!", "1.2.3.4", "ua-test")
	uB, _, _ := st.Signup(ctx, "deviceB@example.com", "securePass1234!", "1.2.3.4", "ua-test")

	// Issue token for user A.
	tokenA, err := st.IssueDeviceToken(ctx, uA.ID, "1.2.3.4", "ua-test")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	// Attempt to validate token A as user B — must be rejected.
	_, err = st.ValidateAndRotateDeviceToken(ctx, uB.ID, "1.2.3.4", "ua-test", tokenA)
	if err == nil {
		t.Error("token for user A must be rejected when validated as user B")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Store: expired token rejected
// ─────────────────────────────────────────────────────────────────────────────

func TestDeviceToken_ExpiredRejected(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := openTestStore(t)
	ctx := context.Background()

	u, _, _ := st.Signup(ctx, "deviceexp@example.com", "securePass1234!", "1.2.3.4", "ua-test")

	token, _ := st.IssueDeviceToken(ctx, u.ID, "1.2.3.4", "ua-test")

	// Force-expire the token in the DB.
	past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	st.mu.Lock()
	_, _ = st.db.ExecContext(ctx,
		`UPDATE device_tokens SET expires_at = ? WHERE token_hash = ?`,
		past, tokenHash(token),
	)
	st.mu.Unlock()

	_, err := st.ValidateAndRotateDeviceToken(ctx, u.ID, "1.2.3.4", "ua-test", token)
	if err == nil {
		t.Error("expired device token should be rejected")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Store: empty cookie value returns ErrNoDeviceToken
// ─────────────────────────────────────────────────────────────────────────────

func TestDeviceToken_EmptyValueReturnsErrNoDeviceToken(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := openTestStore(t)
	ctx := context.Background()

	u, _, _ := st.Signup(ctx, "deviceempty@example.com", "securePass1234!", "1.2.3.4", "ua")

	_, err := st.ValidateAndRotateDeviceToken(ctx, u.ID, "1.2.3.4", "ua", "")
	if err != ErrNoDeviceToken {
		t.Errorf("expected ErrNoDeviceToken for empty cookie, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckDeviceSkipTOTP: returns skip=false when no cookie
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckDeviceSkipTOTP_NoCookie(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	st := openTestStore(t)
	ctx := context.Background()

	u, _, _ := st.Signup(ctx, "skipc@example.com", "securePass1234!", "1.2.3.4", "ua")

	_, skip, err := st.CheckDeviceSkipTOTP(ctx, u.ID, "1.2.3.4", "ua", "")
	if err != nil {
		t.Fatalf("CheckDeviceSkipTOTP: unexpected error: %v", err)
	}
	if skip {
		t.Error("skip should be false when no cookie present")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadDeviceKEK: dev fallback
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadDeviceKEK_DevFallback(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "true")

	kek, err := LoadDeviceKEK()
	if err != nil {
		t.Fatalf("LoadDeviceKEK dev fallback: %v", err)
	}
	if len(kek) != 32 {
		t.Errorf("expected 32-byte kek, got %d", len(kek))
	}
}

func TestLoadDeviceKEK_MissingProd(t *testing.T) {
	t.Setenv("AUTH_DEVICE_KEK", "")
	t.Setenv("VULOS_DEV", "false")

	_, err := LoadDeviceKEK()
	if err == nil {
		t.Error("expected error when AUTH_DEVICE_KEK unset in prod mode")
	}
}
