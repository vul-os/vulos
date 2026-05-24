package calendar_rrule_test

import (
	"testing"
	"time"

	"vulos-office/backend/services/calendar_rrule"
)

var anchor = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC) // Monday 2026-01-05

// parse_valid tests that a well-formed RRULE round-trips without error.
func TestParse_Valid(t *testing.T) {
	_, err := calendar_rrule.Parse(anchor, "RRULE:FREQ=DAILY;COUNT=5")
	if err != nil {
		t.Fatalf("Parse returned unexpected error: %v", err)
	}
}

// parse_empty tests that an empty string produces an error.
func TestParse_Empty(t *testing.T) {
	_, err := calendar_rrule.Parse(anchor, "")
	if err == nil {
		t.Fatal("expected error for empty RRULE string, got nil")
	}
}

// expand_daily_count tests FREQ=DAILY;COUNT=3 returns exactly 3 dates.
func TestExpand_DailyCount(t *testing.T) {
	from := anchor.Add(-time.Hour)
	to := anchor.Add(5 * 24 * time.Hour)

	dates, err := calendar_rrule.Expand(anchor, "RRULE:FREQ=DAILY;COUNT=3", from, to)
	if err != nil {
		t.Fatalf("Expand error: %v", err)
	}
	if len(dates) != 3 {
		t.Errorf("expected 3 occurrences, got %d", len(dates))
	}
}

// expand_weekly_until tests FREQ=WEEKLY with an UNTIL clause.
func TestExpand_WeeklyUntil(t *testing.T) {
	until := anchor.Add(4 * 7 * 24 * time.Hour) // 4 weeks
	rruleStr := "RRULE:FREQ=WEEKLY;UNTIL=" + until.UTC().Format("20060102T150405Z")

	from := anchor.Add(-time.Hour)
	to := anchor.Add(6 * 7 * 24 * time.Hour)

	dates, err := calendar_rrule.Expand(anchor, rruleStr, from, to)
	if err != nil {
		t.Fatalf("Expand error: %v", err)
	}
	// Should be 5 occurrences: weeks 0,1,2,3,4 (anchor is week 0)
	if len(dates) < 4 || len(dates) > 5 {
		t.Errorf("expected 4 or 5 occurrences, got %d", len(dates))
	}
}

// next_occurrence_daily tests that NextOccurrence correctly skips the first event.
func TestNextOccurrence_Daily(t *testing.T) {
	next, err := calendar_rrule.NextOccurrence(anchor, "RRULE:FREQ=DAILY;COUNT=5", anchor)
	if err != nil {
		t.Fatalf("NextOccurrence error: %v", err)
	}
	expected := anchor.Add(24 * time.Hour)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, next)
	}
}

// next_occurrence_exhausted tests that exhausted rules return zero time.
func TestNextOccurrence_Exhausted(t *testing.T) {
	// COUNT=1 means only one occurrence, so "after" the anchor there's nothing.
	next, err := calendar_rrule.NextOccurrence(anchor, "RRULE:FREQ=DAILY;COUNT=1", anchor)
	if err != nil {
		t.Fatalf("NextOccurrence error: %v", err)
	}
	if !next.IsZero() {
		t.Errorf("expected zero time for exhausted rule, got %v", next)
	}
}

// expand_bi_weekly_mon_wed tests "every 2 weeks on Mon/Wed"
func TestExpand_BiWeeklyMonWed(t *testing.T) {
	from := anchor.Add(-time.Hour) // before 2026-01-05 (Mon)
	to := anchor.Add(15 * 24 * time.Hour)

	dates, err := calendar_rrule.Expand(anchor, "RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE", from, to)
	if err != nil {
		t.Fatalf("Expand error: %v", err)
	}
	// In first two weeks from anchor (only 2 weeks visible = 1 biweekly pass):
	// Mon Jan 5 and Wed Jan 7 in week 0; next occurrence is week 2 (Jan 19 Mon, Jan 21 Wed)
	// within 15 days that gives Mon Jan 5, Wed Jan 7 (and possibly Mon Jan 19 if to > Jan 19)
	if len(dates) < 2 {
		t.Errorf("expected at least 2 occurrences for bi-weekly Mon/Wed, got %d", len(dates))
	}
}
