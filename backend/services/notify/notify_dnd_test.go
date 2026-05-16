package notify

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempDNDPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "dnd.json")
}

func TestDNDDefault(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	if m.Mode() != DNDModeOff {
		t.Fatalf("expected off, got %q", m.Mode())
	}
}

func TestDNDSetClear(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	m.Set(DNDModeTotal, time.Time{})
	if m.Mode() != DNDModeTotal {
		t.Fatalf("expected total, got %q", m.Mode())
	}
	m.Clear()
	if m.Mode() != DNDModeOff {
		t.Fatalf("expected off after clear, got %q", m.Mode())
	}
}

func TestDNDTimedExpiry(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	// Set to expire in the past
	m.Set(DNDModeTotal, time.Now().Add(-1*time.Second))
	if m.Mode() != DNDModeOff {
		t.Fatalf("expired DND should be off, got %q", m.Mode())
	}
}

func TestDNDPersistence(t *testing.T) {
	path := tempDNDPath(t)
	m1 := NewDNDManager(path)
	m1.Set(DNDModePriority, time.Time{})

	// New manager reads same file
	m2 := NewDNDManager(path)
	if m2.Mode() != DNDModePriority {
		t.Fatalf("expected priority after reload, got %q", m2.Mode())
	}
}

func TestDNDAtomicWrite(t *testing.T) {
	path := tempDNDPath(t)
	m := NewDNDManager(path)
	m.Set(DNDModeTotal, time.Time{})

	// No temp file left behind
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temp file %q should not exist after atomic write", tmp)
	}
	// File must be readable
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("state file not readable: %v", err)
	}
}

func TestSuppressedOff(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	if m.Suppressed(LevelInfo, PriorityNormal) {
		t.Fatal("off mode should not suppress anything")
	}
	if m.Suppressed(LevelUrgent, PriorityCritical) {
		t.Fatal("off mode should not suppress urgent/critical")
	}
}

func TestSuppressedPriority(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	m.Set(DNDModePriority, time.Time{})

	if !m.Suppressed(LevelInfo, PriorityNormal) {
		t.Fatal("priority mode should suppress info/normal")
	}
	if !m.Suppressed(LevelWarning, PriorityNormal) {
		t.Fatal("priority mode should suppress warning/normal")
	}
	if m.Suppressed(LevelUrgent, PriorityNormal) {
		t.Fatal("priority mode should NOT suppress urgent level")
	}
	if m.Suppressed(LevelInfo, PriorityUrgent) {
		t.Fatal("priority mode should NOT suppress urgent priority")
	}
	if m.Suppressed(LevelInfo, PriorityCritical) {
		t.Fatal("priority mode should NOT suppress critical priority")
	}
}

func TestSuppressedTotal(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))
	m.Set(DNDModeTotal, time.Time{})

	if !m.Suppressed(LevelInfo, PriorityNormal) {
		t.Fatal("total mode should suppress info")
	}
	if !m.Suppressed(LevelUrgent, PriorityCritical) {
		t.Fatal("total mode should suppress even urgent/critical")
	}
}

func TestScheduleWindow(t *testing.T) {
	// Monday 23:00 — test inWindow for a Mon night schedule
	monday23 := time.Date(2026, 5, 18, 23, 0, 0, 0, time.UTC)
	w := ScheduleWindow{
		Weekdays:  0b0000010, // Monday = bit 1
		StartHHMM: "22:00",
		EndHHMM:   "06:00", // wraps midnight
	}
	if !inWindow(monday23, w) {
		t.Fatal("23:00 Monday should be inside 22:00-06:00 Mon window")
	}

	tuesday3am := time.Date(2026, 5, 19, 3, 0, 0, 0, time.UTC) // Tuesday
	// Tuesday bit = 2, not set in weekdays bitmask
	if inWindow(tuesday3am, w) {
		t.Fatal("3am Tuesday should NOT be inside Monday-only window")
	}
}

func TestScheduleWindowNonWrapping(t *testing.T) {
	// Every day 12:00-13:00
	w := ScheduleWindow{
		Weekdays:  0, // 0 = any day
		StartHHMM: "12:00",
		EndHHMM:   "13:00",
	}
	lunch := time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC)
	if !inWindow(lunch, w) {
		t.Fatal("12:30 should be inside 12:00-13:00")
	}
	dinner := time.Date(2026, 5, 16, 19, 0, 0, 0, time.UTC)
	if inWindow(dinner, w) {
		t.Fatal("19:00 should NOT be inside 12:00-13:00")
	}
}

func TestDNDScheduleActivation(t *testing.T) {
	m := NewDNDManager(tempDNDPath(t))

	// Weekday 0 = any day, 00:00-23:59 = always active
	m.SetSchedule([]ScheduleWindow{
		{Weekdays: 0, StartHHMM: "00:00", EndHHMM: "23:59"},
	})

	if m.Mode() != DNDModeTotal {
		t.Fatalf("always-on schedule should yield total mode, got %q", m.Mode())
	}
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		s   string
		min int
		ok  bool
	}{
		{"00:00", 0, true},
		{"23:59", 23*60 + 59, true},
		{"12:30", 12*60 + 30, true},
		{"bad", 0, false},
		{"25:00", 0, false},
		{"12:60", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHHMM(c.s)
		if ok != c.ok {
			t.Errorf("parseHHMM(%q) ok=%v want %v", c.s, ok, c.ok)
		}
		if ok && got != c.min {
			t.Errorf("parseHHMM(%q) = %d, want %d", c.s, got, c.min)
		}
	}
}
