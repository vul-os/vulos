package backup_test

// drill_schedule_test.go — DR-DRILL-CADENCE-01: the periodic restore-drill
// scheduler exercises backup→restore and records a PASS outcome.

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/backup"
)

// TestDrillScheduler_RunOnce_RecordsPass: with a valid snapshot under
// BackupRoot, a single drill run records a PASS outcome carrying RTO/RPO + the
// DB/account counts, proving the snapshot actually restores.
func TestDrillScheduler_RunOnce_RecordsPass(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()

	// Make a real snapshot first (this is what the cadence drill restores).
	bk := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})
	if _, _, err := bk.Run(ctx); err != nil {
		t.Fatalf("backup Run: %v", err)
	}

	ds := backup.NewDrillScheduler(backup.DrillScheduleConfig{
		BackupRoot:     backupRoot,
		SkipInitialRun: true, // we drive RunOnce explicitly
	})
	out := ds.RunOnce(ctx)
	if out.Status != backup.StatusPass {
		t.Fatalf("drill status = %s, want PASS (err=%q)", out.Status, out.Err)
	}
	if out.DBs < 2 {
		t.Errorf("drill DBs = %d, want >= 2 (auth.db + misc.db)", out.DBs)
	}
	if out.Accounts != 5 {
		t.Errorf("drill accounts = %d, want 5", out.Accounts)
	}
	if out.RTOSeconds <= 0 {
		t.Errorf("drill RTO = %f, want > 0", out.RTOSeconds)
	}
	if out.At.IsZero() {
		t.Error("drill outcome timestamp not recorded")
	}

	// History + LastOutcome reflect the run.
	if last, ok := ds.LastOutcome(); !ok || last.Status != backup.StatusPass {
		t.Errorf("LastOutcome = (%+v, %v), want a PASS", last, ok)
	}
	if got := ds.History(); len(got) != 1 {
		t.Errorf("History len = %d, want 1", len(got))
	}
}

// TestDrillScheduler_NoSnapshot_RecordsFail: when there is no snapshot to
// restore, the drill records a FAIL outcome (with an error) rather than
// crashing — the loop must keep running.
func TestDrillScheduler_NoSnapshot_RecordsFail(t *testing.T) {
	ds := backup.NewDrillScheduler(backup.DrillScheduleConfig{
		BackupRoot:     t.TempDir(), // empty — no snapshots
		SkipInitialRun: true,
	})
	out := ds.RunOnce(context.Background())
	if out.Status != backup.StatusFail {
		t.Errorf("empty backup root: want FAIL, got %s", out.Status)
	}
	if out.Err == "" {
		t.Error("FAIL outcome must carry an error string")
	}
}

// TestDrillScheduler_PeriodicCadence: Start runs an initial drill promptly and
// the recorded history grows on the configured interval.
func TestDrillScheduler_PeriodicCadence(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	bk := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})
	if _, _, err := bk.Run(ctx); err != nil {
		t.Fatalf("backup Run: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ds := backup.NewDrillScheduler(backup.DrillScheduleConfig{
		BackupRoot: backupRoot,
		Interval:   40 * time.Millisecond, // tight cadence for the test
	})
	ds.Start(runCtx) // runs an immediate drill, then every 40ms

	// Wait for at least 2 recorded outcomes (initial + at least one tick).
	deadline := time.After(3 * time.Second)
	for {
		if len(ds.History()) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cadence: only %d outcomes recorded within timeout", len(ds.History()))
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	for _, o := range ds.History() {
		if o.Status != backup.StatusPass {
			t.Errorf("cadence drill recorded a non-PASS outcome: %+v", o)
		}
	}
}
