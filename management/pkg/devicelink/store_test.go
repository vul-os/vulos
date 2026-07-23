package devicelink

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemStore_StartApprovePoll(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()

	start, err := st.StartLink(ctx, "https://cloud.example/app/link", 0, 0)
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
	if _, err := st.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("expected ErrPending, got %v", err)
	}

	// Approve as account acct-42 (a human types the user_code with a dash).
	if err := st.Approve(ctx, start.UserCode, "acct-42"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Poll after approval → credential bound to acct-42.
	cred, err := st.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll after approve: %v", err)
	}
	if cred.AccountID != "acct-42" {
		t.Fatalf("account mismatch: %q", cred.AccountID)
	}
	if cred.Token == "" {
		t.Fatal("empty credential token")
	}

	// The credential resolves back to the account.
	acct, err := st.ResolveCredential(ctx, cred.Token)
	if err != nil || acct != "acct-42" {
		t.Fatalf("resolve credential: acct=%q err=%v", acct, err)
	}

	// A second poll must NOT mint a second credential (consumed).
	if _, err := st.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrConsumed) {
		t.Fatalf("expected ErrConsumed on second poll, got %v", err)
	}
}

func TestMemStore_UnknownAndExpired(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()

	// Unknown device code.
	if _, err := st.Poll(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	// Unknown user code approval.
	if err := st.Approve(ctx, "ZZZZ-ZZZZ", "acct-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Expiry: freeze a clock and advance it past TTL.
	now := time.Now().UTC()
	st.SetClock(func() time.Time { return now })
	start, err := st.StartLink(ctx, "u", time.Second, time.Minute)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	now = now.Add(2 * time.Minute) // past TTL
	if _, err := st.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired ErrNotFound, got %v", err)
	}
	if err := st.Approve(ctx, start.UserCode, "acct-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired approve ErrNotFound, got %v", err)
	}
}

func TestMemStore_ForgedCredential(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	// A random/forged install credential resolves to nothing.
	if _, err := st.ResolveCredential(ctx, "deadbeef"); !errors.Is(err, ErrCredential) {
		t.Fatalf("expected ErrCredential, got %v", err)
	}
}
