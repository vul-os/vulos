package handlers_test

import (
	"testing"
	"time"

	"vulos-office/backend/handlers"
)

// helper — creates a CalEvent in calStore via the exported type, then
// calls fireReminders directly.

func makeEvent(id, title string, start time.Time, reminders []handlers.Reminder) *handlers.CalEvent {
	return &handlers.CalEvent{
		ID:        id,
		Title:     title,
		Start:     start,
		End:       start.Add(time.Hour),
		CalendarID: "personal",
		Reminders: reminders,
	}
}

// TestReminderFiresOnce checks that a single reminder fires exactly once.
func TestReminderFiresOnce(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	fireAt := time.Now().UTC().Add(-5 * time.Minute) // 5 min in the past
	event := makeEvent("ev-1", "Stand-up", fireAt.Add(10*time.Minute), []handlers.Reminder{
		{MinutesBefore: 10, Channel: "in-app"},
	})
	handlers.PutCalEvent(event)

	var fired int
	handlers.FireRemindersAt(time.Now().UTC(), func(e *handlers.CalEvent, r handlers.Reminder) {
		fired++
	})
	if fired != 1 {
		t.Errorf("expected 1 fire, got %d", fired)
	}

	// Second call — should NOT fire again.
	handlers.FireRemindersAt(time.Now().UTC(), func(e *handlers.CalEvent, r handlers.Reminder) {
		fired++
	})
	if fired != 1 {
		t.Errorf("expected no second fire, now got %d total", fired)
	}
}

// TestMultipleRemindersFire checks two reminders on the same event both fire.
func TestMultipleRemindersFire(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	eventStart := time.Now().UTC().Add(2 * time.Hour)
	event := makeEvent("ev-2", "Big Meeting", eventStart, []handlers.Reminder{
		{MinutesBefore: 60, Channel: "email"},
		{MinutesBefore: 10, Channel: "push"},
	})
	handlers.PutCalEvent(event)

	// Simulate "now" = eventStart − 59m (past the 60-min mark) — 60-min reminder fires.
	now59 := eventStart.Add(-59 * time.Minute)
	var fired59 int
	handlers.FireRemindersAt(now59, func(e *handlers.CalEvent, r handlers.Reminder) {
		fired59++
	})
	if fired59 != 1 {
		t.Errorf("at -59m: expected 1 fire (60-min reminder), got %d", fired59)
	}

	// Simulate "now" = eventStart − 9m (past the 10-min mark) — 10-min reminder fires.
	now9 := eventStart.Add(-9 * time.Minute)
	var fired9 int
	handlers.FireRemindersAt(now9, func(e *handlers.CalEvent, r handlers.Reminder) {
		fired9++
	})
	if fired9 != 1 {
		t.Errorf("at -9m: expected 1 fire (10-min reminder), got %d", fired9)
	}
}

// TestSnoozeClearsReminder checks that deleting an event removes future reminders.
func TestSnoozeDeleteRemovesReminders(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	eventStart := time.Now().UTC().Add(30 * time.Minute)
	event := makeEvent("ev-3", "Snooze Me", eventStart, []handlers.Reminder{
		{MinutesBefore: 15, Channel: "in-app"},
	})
	handlers.PutCalEvent(event)

	// "Delete" the event (snooze by removing from store).
	handlers.DeleteCalEvent("ev-3")

	// Fire — nothing should fire.
	var fired int
	handlers.FireRemindersAt(time.Now().UTC(), func(e *handlers.CalEvent, r handlers.Reminder) {
		fired++
	})
	if fired != 0 {
		t.Errorf("expected 0 after delete, got %d", fired)
	}
}

// TestNoFutureReminders ensures reminders don't fire before their time.
func TestNoFutureReminders(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	eventStart := time.Now().UTC().Add(2 * time.Hour)
	event := makeEvent("ev-4", "Future Event", eventStart, []handlers.Reminder{
		{MinutesBefore: 30, Channel: "in-app"},
	})
	handlers.PutCalEvent(event)

	// fireAt = eventStart − 30m; now = eventStart − 31m → not yet due
	var fired int
	handlers.FireRemindersAt(eventStart.Add(-31*time.Minute), func(_ *handlers.CalEvent, _ handlers.Reminder) {
		fired++
	})
	if fired != 0 {
		t.Errorf("expected 0 (reminder not due yet), got %d", fired)
	}
}

// TestReminderWithRRULE — reminder fires for RRULE events too.
func TestReminderWithRRULE(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	eventStart := time.Now().UTC().Add(-5 * time.Minute) // past anchor
	event := makeEvent("ev-5", "Weekly Standup", eventStart.Add(10*time.Minute), []handlers.Reminder{
		{MinutesBefore: 10, Channel: "email"},
	})
	event.Recurrence = "RRULE:FREQ=WEEKLY;COUNT=5"
	handlers.PutCalEvent(event)

	var fired int
	handlers.FireRemindersAt(time.Now().UTC(), func(_ *handlers.CalEvent, _ handlers.Reminder) {
		fired++
	})
	if fired != 1 {
		t.Errorf("expected 1 reminder fire, got %d", fired)
	}
}

// TestThreeRemindersFireIndependently tests that three reminders fire independently.
// "fire at" = event.Start - minutesBefore, so "now must be >= fireAt"
// means now <= event.Start - minutesBefore.
func TestThreeRemindersFireIndependently(t *testing.T) {
	handlers.ResetFiredReminders()
	handlers.ClearCalStore()

	eventStart := time.Now().UTC().Add(3 * time.Hour)
	event := makeEvent("ev-6", "Important Call", eventStart, []handlers.Reminder{
		{MinutesBefore: 120, Channel: "email"},
		{MinutesBefore: 30, Channel: "push"},
		{MinutesBefore: 5, Channel: "in-app"},
	})
	handlers.PutCalEvent(event)

	var totalFired int
	// now = eventStart − 119m  (>= fireAt for 120-min reminder = eventStart−120m)
	handlers.FireRemindersAt(eventStart.Add(-119*time.Minute), func(_ *handlers.CalEvent, _ handlers.Reminder) { totalFired++ })
	if totalFired != 1 {
		t.Errorf("at -119m: want 1, got %d", totalFired)
	}
	// now = eventStart − 29m  (>= fireAt for 30-min; 120-min already fired)
	handlers.FireRemindersAt(eventStart.Add(-29*time.Minute), func(_ *handlers.CalEvent, _ handlers.Reminder) { totalFired++ })
	if totalFired != 2 {
		t.Errorf("at -29m: want 2 total, got %d", totalFired)
	}
	// now = eventStart − 4m  (>= fireAt for 5-min; others already fired)
	handlers.FireRemindersAt(eventStart.Add(-4*time.Minute), func(_ *handlers.CalEvent, _ handlers.Reminder) { totalFired++ })
	if totalFired != 3 {
		t.Errorf("at -4m: want 3 total, got %d", totalFired)
	}
}
