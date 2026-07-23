package accountsecurity

import (
	"context"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(t.TempDir(), nil) // nil notify.Service: alerts are recorded but not broadcast
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// A user's very first-ever sensitive action is, by definition, also the
// first-ever sighting of whatever IP it came from — so Rule 2
// ("new_ip_sensitive_action") correctly fires on it. This mirrors the ported
// ato.go semantics exactly (see management/pkg/security/ato.go's
// checkATOAnomaly: firstFromIP<=1 after the just-recorded row is "new").
func TestRecordAndCheck_FirstActionTripsNewIPRule(t *testing.T) {
	svc := newTestService(t)
	alert, err := svc.RecordAndCheck(context.Background(), "u1", ActionPasswordChange, "10.0.0.1", "ua")
	if err != nil {
		t.Fatalf("RecordAndCheck: %v", err)
	}
	if alert == nil || alert.Reason != "new_ip_sensitive_action" {
		t.Fatalf("alert = %+v, want a new_ip_sensitive_action alert", alert)
	}
}

// A second sensitive action from an IP already seen inside the last hour
// (and below the multiple-actions threshold) raises nothing.
func TestRecordAndCheck_NoAnomalyOnRepeatFamiliarIP(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ip := "10.0.0.1"
	if _, err := svc.RecordAndCheck(ctx, "u1", ActionPasswordChange, ip, "ua"); err != nil {
		t.Fatalf("seed action: %v", err)
	}
	alert, err := svc.RecordAndCheck(ctx, "u1", ActionPasswordChange, ip, "ua")
	if err != nil {
		t.Fatalf("RecordAndCheck: %v", err)
	}
	if alert != nil {
		t.Fatalf("expected no alert for a 2nd action from an already-seen IP, below the multi-action threshold, got %+v", alert)
	}
}

func TestRecordAndCheck_MultipleActionsTripsAlert(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ip := "10.0.0.1"
	// First action seeds countFromIPSince=1 so rule 2 does not also fire and
	// mask what we are testing; use the SAME ip throughout to isolate rule 1.
	if _, err := svc.RecordAndCheck(ctx, "u1", ActionPasswordChange, ip, "ua"); err != nil {
		t.Fatalf("action 1: %v", err)
	}
	if _, err := svc.RecordAndCheck(ctx, "u1", ActionPasswordChange, ip, "ua"); err != nil {
		t.Fatalf("action 2: %v", err)
	}
	alert, err := svc.RecordAndCheck(ctx, "u1", ActionPasswordChange, ip, "ua")
	if err != nil {
		t.Fatalf("action 3: %v", err)
	}
	if alert == nil {
		t.Fatal("expected an alert on the 3rd sensitive action within 30 minutes")
	}
	if alert.Reason != "multiple_sensitive_actions" {
		t.Errorf("Reason = %q, want multiple_sensitive_actions", alert.Reason)
	}
	if alert.Status != "pending" {
		t.Errorf("Status = %q, want pending", alert.Status)
	}
}

func TestRecordAndCheck_NewIPTripsAlert(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	alert, err := svc.RecordAndCheck(ctx, "u1", ActionRecoveryUsed, "203.0.113.9", "ua")
	if err != nil {
		t.Fatalf("RecordAndCheck: %v", err)
	}
	if alert == nil {
		t.Fatal("expected an alert: sensitive action from a never-seen-before IP")
	}
	if alert.Reason != "new_ip_sensitive_action" {
		t.Errorf("Reason = %q, want new_ip_sensitive_action", alert.Reason)
	}
}

func TestRecordAndCheck_EmptyUserIDRejected(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.RecordAndCheck(context.Background(), "", ActionPasswordChange, "1.2.3.4", "ua"); err == nil {
		t.Fatal("expected error for empty user id")
	}
}

func TestDismissAndResolveLocked_OwnershipEnforced(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	alert, err := svc.RecordAndCheck(ctx, "u1", ActionRecoveryUsed, "203.0.113.9", "ua")
	if err != nil || alert == nil {
		t.Fatalf("setup RecordAndCheck: alert=%+v err=%v", alert, err)
	}

	// A different user may not touch u1's alert.
	if err := svc.Dismiss(ctx, "someone-else", alert.ID); err != ErrNotOwner {
		t.Errorf("Dismiss by non-owner: err = %v, want ErrNotOwner", err)
	}
	if err := svc.ResolveLocked(ctx, "someone-else", alert.ID); err != ErrNotOwner {
		t.Errorf("ResolveLocked by non-owner: err = %v, want ErrNotOwner", err)
	}

	// The owner can dismiss.
	if err := svc.Dismiss(ctx, "u1", alert.ID); err != nil {
		t.Fatalf("Dismiss by owner: %v", err)
	}

	feed, err := svc.Feed(ctx, "u1")
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(feed.Alerts) != 1 || feed.Alerts[0].Status != "dismissed" {
		t.Fatalf("feed alerts = %+v, want 1 dismissed alert", feed.Alerts)
	}
	if len(feed.Actions) != 1 {
		t.Fatalf("feed actions = %+v, want 1 recorded sensitive action", feed.Actions)
	}
}

func TestResolveLocked_UnknownAlert(t *testing.T) {
	svc := newTestService(t)
	if err := svc.ResolveLocked(context.Background(), "u1", 999); err != ErrNotOwner {
		t.Errorf("ResolveLocked unknown id: err = %v, want ErrNotOwner", err)
	}
}
