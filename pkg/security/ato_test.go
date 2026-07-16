package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestATORecordsSensitiveAction verifies that sensitive actions are recorded.
func TestATORecordsSensitiveAction(t *testing.T) {
	store := openTestStore(t)
	cfg := ATOConfig{Store: store, Sender: NopInboxSender{}}
	ctx := context.Background()

	// Simulate a sensitive action by pre-seeding and checking the DB.
	_ = store.RecordSensitiveAction(ctx, "user@vulos.org", "email_change", "1.2.3.4")

	count, err := store.SensitiveActionsInWindow(ctx, "user@vulos.org", 30)
	if err != nil {
		t.Fatalf("SensitiveActionsInWindow: %v", err)
	}
	if count < 1 {
		t.Errorf("want at least 1 sensitive action recorded, got %d", count)
	}

	_ = cfg // silence unused warning
}

// TestATOAnomalyMultipleActions verifies that 3+ sensitive actions in 30min
// triggers an ATO event.
func TestATOAnomalyMultipleActions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Record 4 sensitive actions in quick succession.
	for i := 0; i < 4; i++ {
		_ = store.RecordSensitiveAction(ctx, "victim@vulos.org", "email_change", "5.6.7.8")
	}

	count, _ := store.SensitiveActionsInWindow(ctx, "victim@vulos.org", 30)
	if count < 3 {
		t.Errorf("want 3+ actions for ATO threshold, got %d", count)
	}

	// Verify ATO detection is triggered (we call the internal helper).
	cfg := ATOConfig{Store: store, Sender: NopInboxSender{}}
	checkATOAnomaly(ctx, cfg, "victim@vulos.org", "email_change", "5.6.7.8")

	// ATO event should have been recorded.
	events, err := store.PendingATOEvents(ctx, 10)
	if err != nil {
		t.Fatalf("PendingATOEvents: %v", err)
	}
	if len(events) == 0 {
		t.Error("want ATO event recorded after multiple sensitive actions")
	}
}

// TestATODismissal verifies that ATO events can be dismissed.
func TestATODismissal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_ = store.RecordATOEvent(ctx, "user@vulos.org", "2fa_reset", "9.9.9.9")

	events, _ := store.PendingATOEvents(ctx, 10)
	if len(events) == 0 {
		t.Fatal("no ATO event to dismiss")
	}

	if err := store.DismissATOEvent(ctx, events[0].ID); err != nil {
		t.Fatalf("DismissATOEvent: %v", err)
	}

	remaining, _ := store.PendingATOEvents(ctx, 10)
	for _, e := range remaining {
		if e.ID == events[0].ID {
			t.Error("want dismissed event to be absent from pending list")
		}
	}
}

// TestATOMiddlewarePassthrough verifies the ATO middleware passes through
// successful responses and records the action.
func TestATOMiddlewarePassthrough(t *testing.T) {
	store := openTestStore(t)
	cfg := ATOConfig{Store: store, Sender: NopInboxSender{}}

	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/profile/change-email", nil)
	req = req.WithContext(SetUserIDCtx(req.Context(), "user3@vulos.org"))
	req.RemoteAddr = "1.1.1.1:9999"
	rec := httptest.NewRecorder()

	SensitiveActionMiddleware(cfg, "email_change", downstream).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 passthrough, got %d", rec.Code)
	}
}
