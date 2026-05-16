package notify

import (
	"errors"
	"testing"
)

func TestActionRegistryDispatch(t *testing.T) {
	r := NewActionRegistry()

	called := false
	r.Register("open-settings", func(notifID, actionID string) error {
		called = true
		return nil
	})

	if err := r.Dispatch("notif-1", "open-settings"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestActionRegistryNotFound(t *testing.T) {
	r := NewActionRegistry()
	err := r.Dispatch("notif-1", "no-such-action")
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("expected ErrActionNotFound, got: %v", err)
	}
}

func TestActionRegistryLog(t *testing.T) {
	r := NewActionRegistry()
	r.Register("ping", func(_, _ string) error { return nil })
	r.Register("fail", func(_, _ string) error { return errors.New("boom") })

	r.Dispatch("n1", "ping")
	r.Dispatch("n2", "fail")
	r.Dispatch("n3", "unknown")

	log := r.Log(10)
	if len(log) != 3 {
		t.Fatalf("expected 3 log entries, got %d", len(log))
	}
	// Log is newest-first; last dispatched should be first
	if log[0].ActionID != "unknown" {
		t.Fatalf("expected newest entry to be 'unknown', got %q", log[0].ActionID)
	}
	if log[0].Err == "" {
		t.Fatal("log entry for unknown action should have error")
	}
	if log[1].Err == "" {
		t.Fatal("log entry for 'fail' action should have error")
	}
	if log[2].Err != "" {
		t.Fatalf("log entry for 'ping' should have no error, got %q", log[2].Err)
	}
}

func TestActionRegistryUnregister(t *testing.T) {
	r := NewActionRegistry()
	r.Register("tmp", func(_, _ string) error { return nil })
	r.Unregister("tmp")

	if err := r.Dispatch("n1", "tmp"); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("expected ErrActionNotFound after unregister, got: %v", err)
	}
}

func TestActionRegistryLogLimit(t *testing.T) {
	r := NewActionRegistry()
	r.maxLog = 3

	for i := 0; i < 5; i++ {
		r.Dispatch("n", "nope")
	}
	if got := len(r.Log(100)); got != 3 {
		t.Fatalf("expected log capped at 3, got %d", got)
	}
}
