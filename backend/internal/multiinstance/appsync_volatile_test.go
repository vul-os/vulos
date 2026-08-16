package multiinstance_test

// REPRO (temporary): the volatile-root re-download loop.
//
// Boots a two-box fleet where box A's storage does not survive a reboot, and
// counts how many installs A performs across three boots of the SAME three apps.

import (
	"context"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

func TestREPROVolatileBoxReinstallsEveryBoot(t *testing.T) {
	const (
		ulidA = "01HWZMINST00000000000VOLA"
		ulidB = "01HWZMINST00000000000DURB"
	)
	apps := []string{"browser", "notes", "photos"}

	// Boot 1: A is a fresh box; the user desires three apps on the fleet.
	a := newBox(t, ulidA)
	b := newBox(t, ulidB)
	a.learn(t, b)
	b.learn(t, a)

	for _, app := range apps {
		if err := a.as.DesireInstall(ulidA, app, "1.0.0"); err != nil {
			t.Fatalf("DesireInstall %s: %v", app, err)
		}
	}
	disk := newFakeRealiser(nil)
	res, err := a.as.Reconcile(context.Background(), ulidA, disk)
	if err != nil {
		t.Fatalf("boot 1 Reconcile: %v", err)
	}
	t.Logf("boot 1: installed %v", res.Installed)
	if len(res.Installed) != 3 {
		t.Fatalf("boot 1 installed %d, want 3", len(res.Installed))
	}

	// A pushes everything it knows to B: the fleet desire AND A's own
	// realisation rows. This is the fleet's memory of A.
	if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("B applies A: %v", err)
	}

	totalInstalls := len(res.Installed)
	for boot := 2; boot <= 3; boot++ {
		// REBOOT of a box on an overlay root: the tmpfs upper is gone, so
		// $HOME/.vulos/apps AND $HOME/.vulos/db/multiinstance.db both vanish.
		a = newBox(t, ulidA) // brand-new empty DB, same instance identity
		a.learn(t, b)
		b.learn(t, a)
		disk = newFakeRealiser(nil) // empty app dir

		// A rejoins and pulls everything B holds — including B's copy of A's
		// own realisation rows.
		if err := a.as.ApplyChangeset(b.changesetSince(t, time.Time{})); err != nil {
			t.Fatalf("boot %d: A applies B: %v", boot, err)
		}
		rows, err := a.as.ListAppsForInstance(ulidA, true)
		if err != nil {
			t.Fatalf("boot %d: ListAppsForInstance: %v", boot, err)
		}
		t.Logf("boot %d: fleet remembers %d realisation row(s) for A", boot, len(rows))
		for _, r := range rows {
			t.Logf("           %s installed=%v state=%q", r.AppID, r.Installed, r.RealiseState)
		}

		plan, err := a.as.PlanReconcile(disk)
		if err != nil {
			t.Fatalf("boot %d: PlanReconcile: %v", boot, err)
		}
		t.Logf("boot %d: plan = %d action(s)", boot, len(plan.Actions))
		for _, act := range plan.Actions {
			t.Logf("           %+v", act)
		}

		res, err := a.as.Reconcile(context.Background(), ulidA, disk)
		if err != nil {
			t.Fatalf("boot %d: Reconcile: %v", boot, err)
		}
		t.Logf("boot %d: installed %v", boot, res.Installed)
		totalInstalls += len(res.Installed)

		if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
			t.Fatalf("boot %d: B applies A: %v", boot, err)
		}
	}
	t.Logf("TOTAL installs across 3 boots of 3 apps: %d", totalInstalls)
	var _ = multiinstance.RealiseRealised
}
