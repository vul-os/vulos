package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/telemetry"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func cleanSample(ulid, orgID string) telemetry.Sample {
	return telemetry.Sample{
		ULID:              ulid,
		OrgID:             orgID,
		SampledAt:         time.Now().UTC(),
		IPReputationScore: 100,
		DiskUsedPct:       20,
		SyncQueueDepth:    0,
		MailQueueDepth:    0,
		DKIMExpiryDays:    60,
		DMARCPassRatePct:  99,
		UptimeSec:         3600,
		LastBackupAgeSec:  3600,
	}
}

// runSuite runs the full Store contract test against any Store implementation.
func runSuite(t *testing.T, st telemetry.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("LatestSample_NotFound", func(t *testing.T) {
		_, err := st.LatestSample(ctx, "NOPE")
		if err != telemetry.ErrSampleNotFound {
			t.Fatalf("want ErrSampleNotFound, got %v", err)
		}
	})

	t.Run("InsertSample_CleanNoAlerts", func(t *testing.T) {
		s := cleanSample("DEV001", "ORG1")
		got, alerts, err := st.InsertSample(ctx, s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID == 0 {
			t.Fatal("expected non-zero ID")
		}
		if len(alerts) != 0 {
			t.Fatalf("expected 0 alerts for clean sample, got %d: %v", len(alerts), alerts)
		}
	})

	t.Run("LatestSample_Found", func(t *testing.T) {
		s := cleanSample("DEV002", "ORG1")
		s.DiskUsedPct = 50
		if _, _, err := st.InsertSample(ctx, s); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got, err := st.LatestSample(ctx, "DEV002")
		if err != nil {
			t.Fatalf("latest: %v", err)
		}
		if got.DiskUsedPct != 50 {
			t.Fatalf("want 50, got %d", got.DiskUsedPct)
		}
	})

	t.Run("InsertSample_BlocklistFiring", func(t *testing.T) {
		s := cleanSample("DEV003", "ORG1")
		s.BlocklistSpamhaus = true
		_, alerts, err := st.InsertSample(ctx, s)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if len(alerts) == 0 {
			t.Fatal("expected at least one alert for blocklist hit")
		}
		found := false
		for _, a := range alerts {
			if a.Signal == "blocklist_spamhaus" && a.Severity == "critical" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected blocklist_spamhaus/critical alert, got: %v", alerts)
		}
	})

	t.Run("InsertSample_MultipleSignals", func(t *testing.T) {
		s := cleanSample("DEV004", "ORG2")
		s.DiskUsedPct = 97          // critical
		s.MailQueueDepth = 1500     // critical
		s.DKIMExpiryDays = 2        // critical
		s.DMARCPassRatePct = 60     // critical
		s.IPReputationScore = 30    // critical
		s.BlocklistBarracuda = true // critical
		s.UptimeSec = telemetry.BackupWarnAgeSec + 1
		s.LastBackupAgeSec = telemetry.BackupCriticalAgeSec + 1 // critical

		_, alerts, err := st.InsertSample(ctx, s)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if len(alerts) < 7 {
			t.Fatalf("expected >=7 critical alerts, got %d: %v", len(alerts), alerts)
		}
		for _, a := range alerts {
			if a.Severity != "critical" {
				t.Errorf("expected critical, got %q for signal %q", a.Severity, a.Signal)
			}
		}
	})

	t.Run("RecentSamples", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			s := cleanSample("DEV005", "ORG1")
			if _, _, err := st.InsertSample(ctx, s); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		all, err := st.RecentSamples(ctx, "DEV005", 3)
		if err != nil {
			t.Fatalf("recent: %v", err)
		}
		if len(all) != 3 {
			t.Fatalf("want 3, got %d", len(all))
		}
	})

	t.Run("OpenAlerts", func(t *testing.T) {
		s := cleanSample("DEV006", "ORG3")
		s.BlocklistSORBS = true
		_, fired, err := st.InsertSample(ctx, s)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if len(fired) == 0 {
			t.Fatal("expected fired alert")
		}

		open, err := st.OpenAlerts(ctx, "DEV006")
		if err != nil {
			t.Fatalf("open alerts: %v", err)
		}
		if len(open) == 0 {
			t.Fatal("expected open alerts")
		}
	})

	t.Run("OrgOpenAlerts", func(t *testing.T) {
		orgID := "ORG_ORGALERT"
		s := cleanSample("DEV007", orgID)
		s.DiskUsedPct = 99
		if _, _, err := st.InsertSample(ctx, s); err != nil {
			t.Fatalf("insert: %v", err)
		}
		alerts, err := st.OrgOpenAlerts(ctx, orgID)
		if err != nil {
			t.Fatalf("org open alerts: %v", err)
		}
		if len(alerts) == 0 {
			t.Fatal("expected alerts for org")
		}
	})

	t.Run("ResolveAlert", func(t *testing.T) {
		s := cleanSample("DEV008", "ORG4")
		s.BlocklistSpamhaus = true
		_, fired, err := st.InsertSample(ctx, s)
		if err != nil || len(fired) == 0 {
			t.Fatalf("insert/fire: err=%v fires=%d", err, len(fired))
		}
		aid := fired[0].ID
		if err := st.ResolveAlert(ctx, aid, time.Now().UTC()); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		// Should no longer appear in OpenAlerts.
		open, _ := st.OpenAlerts(ctx, "DEV008")
		for _, a := range open {
			if a.ID == aid {
				t.Fatal("resolved alert still appears in OpenAlerts")
			}
		}
	})

	t.Run("ResolveAlert_NotFound", func(t *testing.T) {
		err := st.ResolveAlert(ctx, 999999999, time.Now().UTC())
		if err != telemetry.ErrAlertNotFound {
			t.Fatalf("want ErrAlertNotFound, got %v", err)
		}
	})

	t.Run("AllOpenAlerts", func(t *testing.T) {
		s := cleanSample("DEV009", "ORG5")
		s.SyncQueueDepth = 3000
		if _, _, err := st.InsertSample(ctx, s); err != nil {
			t.Fatalf("insert: %v", err)
		}
		all, err := st.AllOpenAlerts(ctx)
		if err != nil {
			t.Fatalf("all open alerts: %v", err)
		}
		if len(all) == 0 {
			t.Fatal("expected at least one open alert")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore(t *testing.T) {
	st := telemetry.NewMemStore()
	defer st.Close()
	runSuite(t, st)
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore tests
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := telemetry.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("telemetry.Open: %v", err)
	}
	defer st.Close()
	runSuite(t, st)
}

// ─────────────────────────────────────────────────────────────────────────────
// Threshold unit tests (pure function)
// ─────────────────────────────────────────────────────────────────────────────

func TestThreshold_CleanSample(t *testing.T) {
	s := cleanSample("X", "")
	firings := telemetry.Threshold(s)
	if len(firings) != 0 {
		t.Fatalf("expected 0 firings for clean sample, got %d: %v", len(firings), firings)
	}
}

func TestThreshold_WarnSignals(t *testing.T) {
	s := cleanSample("X", "")
	s.IPReputationScore = 60 // warn (< 70, >= 40)
	s.DiskUsedPct = 85       // warn (> 80, <= 95)
	s.SyncQueueDepth = 600   // warn (> 500, <= 2000)
	s.MailQueueDepth = 300   // warn (> 200, <= 1000)
	s.DKIMExpiryDays = 10    // warn (<= 14, > 3)
	s.DMARCPassRatePct = 85  // warn (< 90, >= 75)
	s.UptimeSec = telemetry.BackupWarnAgeSec + 1
	s.LastBackupAgeSec = telemetry.BackupWarnAgeSec + 1 // warn (> 2d, <= 7d)

	firings := telemetry.Threshold(s)
	if len(firings) != 7 {
		t.Fatalf("expected 7 warn firings, got %d: %v", len(firings), firings)
	}
	for _, f := range firings {
		if f.Severity != "warn" {
			t.Errorf("expected warn, got %q for %q", f.Severity, f.Signal)
		}
	}
}

func TestThreshold_BlocklistsCritical(t *testing.T) {
	s := cleanSample("X", "")
	s.BlocklistSpamhaus = true
	s.BlocklistBarracuda = true
	s.BlocklistSORBS = true

	firings := telemetry.Threshold(s)
	if len(firings) != 3 {
		t.Fatalf("expected 3 critical firings, got %d", len(firings))
	}
	for _, f := range firings {
		if f.Severity != "critical" {
			t.Errorf("expected critical, got %q", f.Severity)
		}
	}
}

func TestThreshold_BackupNotCheckedWhenInstanceNew(t *testing.T) {
	s := cleanSample("X", "")
	// Uptime below warn threshold → backup not evaluated.
	s.UptimeSec = 60
	s.LastBackupAgeSec = telemetry.BackupCriticalAgeSec + 1 // would fire if checked

	firings := telemetry.Threshold(s)
	for _, f := range firings {
		if f.Signal == "last_backup_age" {
			t.Fatal("backup_age should not fire when uptime < BackupWarnAgeSec")
		}
	}
}
