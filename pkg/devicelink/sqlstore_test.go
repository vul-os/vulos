// sqlstore_test.go — WAVE30-CP-COVERAGE: white-box tests for the PRODUCTION
// cpdb-backed SQLStore device-link state machine (StartLink→Approve→Poll→
// ResolveCredential). The existing store_test.go only exercised MemStore; the
// SQLStore path (the one that actually runs in prod) was at 0% coverage.
//
// These tests drive the security-sensitive branches: pending/approved/consumed
// transitions, expiry, replay (double-consume), wrong/unknown codes, credential
// resolution + revocation, and the sha256-only-persistence guarantee.
package devicelink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openSQLStore returns a fresh in-memory-SQLite-backed SQLStore.
func openSQLStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLStore_StartApprovePoll_HappyPath(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	start, err := s.StartLink(ctx, "https://cloud.example/app/link", 0, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		t.Fatalf("empty codes: %+v", start)
	}
	if start.Interval != DefaultInterval || start.ExpiresIn != DefaultTTL {
		t.Fatalf("defaults not applied: %+v", start)
	}

	// Poll before approval → pending.
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("want ErrPending, got %v", err)
	}

	// Approve as acct-77 — a human may type the code loosely (lowercase, dashed).
	if err := s.Approve(ctx, "  "+start.UserCode+"  ", "acct-77"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Poll after approval → credential bound to acct-77.
	cred, err := s.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll after approve: %v", err)
	}
	if cred.AccountID != "acct-77" || cred.Token == "" {
		t.Fatalf("bad credential: %+v", cred)
	}

	// Credential resolves back to the account.
	acct, err := s.ResolveCredential(ctx, cred.Token)
	if err != nil || acct != "acct-77" {
		t.Fatalf("resolve credential: acct=%q err=%v", acct, err)
	}

	// Replay: a second poll must NOT mint a second credential.
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrConsumed) {
		t.Fatalf("want ErrConsumed on second poll, got %v", err)
	}
}

func TestSQLStore_Approve_ConsumedThenReApprove(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	start, _ := s.StartLink(ctx, "u", 0, 0)
	if err := s.Approve(ctx, start.UserCode, "acct-a"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Consume it.
	if _, err := s.Poll(ctx, start.DeviceCode); err != nil {
		t.Fatalf("poll: %v", err)
	}
	// Re-approving an already-approved/consumed code → ErrConsumed (not a silent
	// re-bind to a different account).
	if err := s.Approve(ctx, start.UserCode, "acct-b"); !errors.Is(err, ErrConsumed) {
		t.Fatalf("re-approve consumed: want ErrConsumed, got %v", err)
	}
}

func TestSQLStore_Approve_DoubleApproveIsConsumed(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	start, _ := s.StartLink(ctx, "u", 0, 0)
	if err := s.Approve(ctx, start.UserCode, "acct-a"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	// Approving a second time (before poll) — state is now 'approved', not
	// 'pending', so the guarded UPDATE affects 0 rows → ErrConsumed. This prevents
	// a second human re-binding a pending link out from under the first.
	if err := s.Approve(ctx, start.UserCode, "acct-b"); !errors.Is(err, ErrConsumed) {
		t.Fatalf("double approve: want ErrConsumed, got %v", err)
	}
	// The credential must still belong to the FIRST approver.
	cred, err := s.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if cred.AccountID != "acct-a" {
		t.Fatalf("account re-bound by second approve: got %q", cred.AccountID)
	}
}

func TestSQLStore_BadInputs(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	// Approve with empty account or empty code → ErrBadInput.
	if err := s.Approve(ctx, "K7QP-2M9X", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("empty account: want ErrBadInput, got %v", err)
	}
	if err := s.Approve(ctx, "   ", "acct-1"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("empty code: want ErrBadInput, got %v", err)
	}
	// Poll with empty device code → ErrNotFound.
	if _, err := s.Poll(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty device code: want ErrNotFound, got %v", err)
	}
	// Approve an unknown code → ErrNotFound.
	if err := s.Approve(ctx, "ZZZZ-ZZZZ", "acct-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: want ErrNotFound, got %v", err)
	}
	// Poll an unknown device code → ErrNotFound.
	if _, err := s.Poll(ctx, "not-a-real-device-code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device: want ErrNotFound, got %v", err)
	}
}

func TestSQLStore_Expiry(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	// Freeze the clock (white-box: we can set the unexported now func).
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	start, err := s.StartLink(ctx, "u", time.Second, time.Minute)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Advance past the TTL.
	now = now.Add(2 * time.Minute)

	// Poll of an expired code → ErrNotFound (indistinguishable from unknown; no
	// leak of "existed but expired").
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired poll: want ErrNotFound, got %v", err)
	}
	// Approving an expired code also fails closed.
	if err := s.Approve(ctx, start.UserCode, "acct-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired approve: want ErrNotFound, got %v", err)
	}
}

func TestSQLStore_ApproveThenExpire_PollDenied(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	start, _ := s.StartLink(ctx, "u", time.Second, time.Minute)
	if err := s.Approve(ctx, start.UserCode, "acct-1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Expire AFTER approval but BEFORE the install polls — the credential must not
	// be mintable from an expired (even if approved) link.
	now = now.Add(2 * time.Minute)
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired-after-approve poll: want ErrNotFound, got %v", err)
	}
}

func TestSQLStore_ResolveCredential_ForgedAndEmpty(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	// Empty token → ErrCredential.
	if _, err := s.ResolveCredential(ctx, ""); !errors.Is(err, ErrCredential) {
		t.Fatalf("empty token: want ErrCredential, got %v", err)
	}
	// A random/forged token resolves to nothing.
	if _, err := s.ResolveCredential(ctx, "deadbeefdeadbeef"); !errors.Is(err, ErrCredential) {
		t.Fatalf("forged token: want ErrCredential, got %v", err)
	}
}

func TestSQLStore_RevokedCredential_NoLongerResolves(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	start, _ := s.StartLink(ctx, "u", 0, 0)
	_ = s.Approve(ctx, start.UserCode, "acct-rev")
	cred, err := s.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	// Sanity: resolves while active.
	if acct, err := s.ResolveCredential(ctx, cred.Token); err != nil || acct != "acct-rev" {
		t.Fatalf("pre-revoke resolve: acct=%q err=%v", acct, err)
	}

	// Soft-revoke it directly (the store persists only the hash, so we revoke by
	// hash the same way an admin revoke path would).
	if _, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE install_credentials SET revoked = 1 WHERE token_hash = ?`), hash(cred.Token)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// A revoked credential must fail closed (ResolveCredential filters revoked=0).
	if _, err := s.ResolveCredential(ctx, cred.Token); !errors.Is(err, ErrCredential) {
		t.Fatalf("revoked resolve: want ErrCredential, got %v", err)
	}
}

// TestSQLStore_RawSecretsNeverPersisted asserts the security posture from the
// package doc: neither the raw device_code nor the raw install credential is
// stored — only their sha256 hashes.
func TestSQLStore_RawSecretsNeverPersisted(t *testing.T) {
	ctx := context.Background()
	s := openSQLStore(t)

	start, _ := s.StartLink(ctx, "u", 0, 0)
	_ = s.Approve(ctx, start.UserCode, "acct-x")
	cred, err := s.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	// The raw device_code must not appear in device_links; the stored hash must.
	var storedDeviceHash string
	if err := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT device_code_hash FROM device_links WHERE user_code = ?`),
		NormalizeUserCode(start.UserCode)).Scan(&storedDeviceHash); err != nil {
		t.Fatalf("read device hash: %v", err)
	}
	if storedDeviceHash == start.DeviceCode {
		t.Fatal("raw device_code was persisted (must be hashed)")
	}
	if storedDeviceHash != hash(start.DeviceCode) {
		t.Fatalf("device_code_hash mismatch: %q", storedDeviceHash)
	}

	// The raw install credential must not appear in install_credentials.
	var storedTokenHash string
	if err := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT token_hash FROM install_credentials WHERE account_id = ?`),
		"acct-x").Scan(&storedTokenHash); err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	if storedTokenHash == cred.Token {
		t.Fatal("raw install credential was persisted (must be hashed)")
	}
	if storedTokenHash != hash(cred.Token) {
		t.Fatalf("token_hash mismatch: %q", storedTokenHash)
	}
}
