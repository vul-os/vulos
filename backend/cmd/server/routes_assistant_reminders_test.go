package main

// routes_assistant_reminders_test.go — the wave-62 REMINDERS wire-seam +
// HTTP-round-trip contract (crosses two service boundaries):
//
//   1. assistant scheduler → notify.Service (the Notifier seam). A fired reminder
//      must produce a real notify.Notification whose wire JSON the shell's
//      NotificationCenter reads (title/body/type/priority), targeted at the user.
//   2. HTTP round-trip: set_reminder via /agent → ledger → /execute creates the
//      reminder; GET /api/assistant/reminders lists ONLY the caller's own; POST
//      /api/assistant/reminders/cancel cancels it — all session-scoped by X-User-ID.

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
	"vulos/backend/services/notify"
)

// TestReminderNotifierWireSeam guards the scheduler↔notify boundary: the adapter
// must emit a notification the shell can consume, carrying the reminder text as
// plain-text Body (rendered escaped by the client) and targeting the user.
func TestReminderNotifierWireSeam(t *testing.T) {
	svc := notify.New()
	n := reminderNotifier{svc: svc}

	n.NotifyReminder("alice", "rem_123", "call the dentist")

	list := svc.List(10)
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 notification from a fired reminder, got %d", len(list))
	}
	got := list[0]
	// Serialize to the SAME JSON the shell's /api/notifications endpoint returns,
	// and assert the fields the NotificationCenter reads.
	raw, _ := json.Marshal(got)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("notification did not serialize: %v", err)
	}
	if wire["title"] != "Reminder" {
		t.Fatalf("reminder notification title wrong on the wire: %v", wire["title"])
	}
	if wire["body"] != "call the dentist" {
		t.Fatalf("reminder text did not survive to the notification body: %v", wire["body"])
	}
	// Per-user targeting: the subtype carries the owner so a per-user client routes it.
	if sub, _ := wire["subtype"].(string); sub != "reminder:alice" {
		t.Fatalf("reminder notification not targeted at the user: subtype=%v", sub)
	}
	// It is an alert delivered at high priority (a reminder is time-sensitive).
	if wire["type"] != string(notify.TypeAlert) {
		t.Fatalf("reminder should be an alert, got type=%v", wire["type"])
	}
}

// remindersHTTPMux wires the assistant routes with a scripted model and a REAL
// on-disk reminders store (the production seam), so the HTTP round-trip exercises
// the store exactly as production does.
func remindersHTTPMux(t *testing.T, replies []string) *http.ServeMux {
	t.Helper()
	st, err := assistant.OpenRemindersStore(filepath.Join(t.TempDir(), "reminders.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	deps := assistantDeps{
		model:     &scriptedModel{replies: replies},
		cfg:       ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"},
		source:    assistant.NewFixtureSource(),
		ledger:    assistant.NewProposalLedger(),
		reminders: st,
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)
	return mux
}

func TestRemindersHTTPRoundTrip(t *testing.T) {
	remindAt := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	mux := remindersHTTPMux(t, []string{
		`{"tool":"set_reminder","args":{"text":"call the dentist","remind_at":"` + remindAt + `"}}`,
	})

	// 1) Propose via /agent — a ledger-gated proposal, nothing created yet.
	rec, out := doAssistantJSON(t, mux, "POST", "/api/assistant/agent", "alice",
		`{"message":"remind me to call the dentist tomorrow at 3pm"}`)
	if rec.Code != 200 {
		t.Fatalf("/agent status=%d", rec.Code)
	}
	prop, _ := out["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("expected a set_reminder proposal, got %v", out)
	}
	id, _ := prop["id"].(string)
	if id == "" || prop["tool"] != "set_reminder" {
		t.Fatalf("bad proposal: %v", prop)
	}

	// Nothing listed yet (not created until approval).
	_, before := doAssistantJSON(t, mux, "GET", "/api/assistant/reminders", "alice", "")
	if rl, _ := before["reminders"].([]any); len(rl) != 0 {
		t.Fatalf("reminder created before approval — ledger bypass: %v", rl)
	}

	// 2) Approve via /execute (id only).
	rec, _ = doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "alice", `{"id":"`+id+`"}`)
	if rec.Code != 200 {
		t.Fatalf("/execute status=%d", rec.Code)
	}

	// 3) It now lists for alice — and ONLY alice.
	_, aOut := doAssistantJSON(t, mux, "GET", "/api/assistant/reminders", "alice", "")
	aList, _ := aOut["reminders"].([]any)
	if len(aList) != 1 {
		t.Fatalf("alice should have 1 reminder, got %v", aList)
	}
	_, bOut := doAssistantJSON(t, mux, "GET", "/api/assistant/reminders", "bob", "")
	if bList, _ := bOut["reminders"].([]any); len(bList) != 0 {
		t.Fatalf("bob must not see alice's reminder — isolation broken: %v", bList)
	}

	// The reminder id (for cancel).
	first, _ := aList[0].(map[string]any)
	remID, _ := first["id"].(string)

	// 4) Bob cannot cancel alice's reminder (session-scoped, 404).
	recBob, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/reminders/cancel", "bob", `{"id":"`+remID+`"}`)
	if recBob.Code == 200 {
		t.Fatal("bob cancelled alice's reminder — cross-user cancel allowed")
	}
	_, stillThere := doAssistantJSON(t, mux, "GET", "/api/assistant/reminders", "alice", "")
	if rl, _ := stillThere["reminders"].([]any); len(rl) != 1 {
		t.Fatalf("alice's reminder vanished after bob's failed cancel: %v", rl)
	}

	// 5) Alice cancels her own — gone.
	recA, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/reminders/cancel", "alice", `{"id":"`+remID+`"}`)
	if recA.Code != 200 {
		t.Fatalf("alice's own cancel failed: %d", recA.Code)
	}
	_, done := doAssistantJSON(t, mux, "GET", "/api/assistant/reminders", "alice", "")
	if rl, _ := done["reminders"].([]any); len(rl) != 0 {
		t.Fatalf("cancelled reminder still listed: %v", rl)
	}
}
