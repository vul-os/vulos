package multiinstance_test

// REPRO (kept): the volatile-root re-download loop.
//
// Boots a two-box fleet where box A's storage does not survive a reboot, and
// counts how many installs A performs across three boots of the SAME three apps.
// The REPRO below is the record of the measurement and is deliberately left
// passing and unchanged; everything after it is the assertion about what should
// happen instead — SYNC-APPS-02.

import (
	"context"
	"strings"
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

// ── SYNC-APPS-02: what should happen instead ─────────────────────────────────

// mountsOverlayLive is /proc/self/mounts on a live / netboot-installed box:
// squashfs lower, tmpfs upper at /run/vulos/rw, app dir inside it. Transcribed
// from scripts/initramfs/vulos-live's shape, not built from any constant in the
// code under test.
const liveOverlayReason = "overlay at / whose upper layer /run/vulos/rw/upper is tmpfs at /run/vulos/rw (RAM-backed)"

// volatileRealiser is a fakeRealiser that can also answer the question a box on
// an overlay root has to be able to answer about ITSELF. Kept as a wrapper
// rather than a field on fakeRealiser so every other test in this package keeps
// exercising the case where the Realiser cannot measure its storage at all —
// which is the darwin/stripped-container case and must stay on the same code
// path as "durable".
type volatileRealiser struct {
	*fakeRealiser
	reason string
}

func (v *volatileRealiser) StorageVolatility() (bool, string) {
	if v.reason == "" {
		return false, ""
	}
	return true, v.reason
}

func newVolatileRealiser(disk map[string]string, reason string) *volatileRealiser {
	return &volatileRealiser{fakeRealiser: newFakeRealiser(disk), reason: reason}
}

// assertStillDesired fails with msg unless the fleet still WANTS appID. The
// positive of assertNoDesire, and the one this work needs: a re-realisation
// must never be resolved by giving up on what the user asked for.
func assertStillDesired(t *testing.T, as *multiinstance.AppSync, appID, msg string) {
	t.Helper()
	all, err := as.DesiredSet()
	if err != nil {
		t.Fatalf("DesiredSet: %v", err)
	}
	for _, d := range all {
		if d.AppID == appID && d.Desired {
			return
		}
	}
	t.Fatalf("%s: the fleet no longer desires %q (desired set: %+v)", msg, appID, all)
}

// rowFor reads one of an instance's replicated realisation rows.
func rowFor(t *testing.T, b *box, instanceULID, appID string) multiinstance.AppRegistryEntry {
	t.Helper()
	rows, err := b.as.ListAppsForInstance(instanceULID, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance(%s): %v", instanceULID, err)
	}
	for _, r := range rows {
		if r.AppID == appID {
			return r
		}
	}
	t.Fatalf("no row for %s/%s in %+v", instanceULID, appID, rows)
	return multiinstance.AppRegistryEntry{}
}

// TestALostInstallIsARerealisationNotAFirstInstall is the assertion the REPRO
// above earns. Same fleet, same three apps, same reboot of a box whose storage
// is RAM: the plan must now say WHICH of the two things it is looking at.
//
// The distinction is not cosmetic. "never installed here" and "installed here
// and the bits evaporated" produce the same empty directory and the same
// download, and only one of them is worth counting, reporting, or ever holding
// back. Reading them as the same thing is what let a box re-download its entire
// app set on every boot for as long as it lived without anything noticing.
func TestALostInstallIsARerealisationNotAFirstInstall(t *testing.T) {
	const (
		ulidA = "01HWZMINST00000000000VOLA"
		ulidB = "01HWZMINST00000000000DURB"
	)
	apps := []string{"browser", "notes", "photos"}

	a := newBox(t, ulidA)
	b := newBox(t, ulidB)
	a.learn(t, b)
	b.learn(t, a)

	for _, app := range apps {
		if err := a.as.DesireInstall(ulidA, app, "1.0.0"); err != nil {
			t.Fatalf("DesireInstall %s: %v", app, err)
		}
	}

	// Boot 1. A has never had any of these: three FIRST installs, and no
	// re-realisation recorded anywhere.
	boot1 := newVolatileRealiser(nil, liveOverlayReason)
	plan1, err := a.as.PlanReconcile(boot1)
	if err != nil {
		t.Fatalf("boot 1 PlanReconcile: %v", err)
	}
	for _, act := range plan1.Actions {
		if act.Cause != "never-realised" {
			t.Fatalf("boot 1 action %+v: cause %q, want \"never-realised\" — A has no realisation row for %s, so it has genuinely never had it; "+
				"calling a first install a re-realisation would attach a storage excuse to an app that was simply never installed", act, act.Cause, act.AppID)
		}
	}
	res1, err := a.as.Reconcile(context.Background(), ulidA, boot1)
	if err != nil {
		t.Fatalf("boot 1 Reconcile: %v", err)
	}
	if len(res1.Installed) != 3 || len(res1.ReRealised) != 0 {
		t.Fatalf("boot 1: installed %v, re-realised %v; want 3 installs and 0 re-realisations", res1.Installed, res1.ReRealised)
	}
	if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("B applies A: %v", err)
	}

	// REBOOT. The tmpfs upper is gone, so A's app directory AND A's database
	// both vanish; the identity and the peer are all that come back.
	a = newBox(t, ulidA)
	a.learn(t, b)
	b.learn(t, a)
	if err := a.as.ApplyChangeset(b.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("boot 2: A applies B: %v", err)
	}

	boot2 := newVolatileRealiser(nil, liveOverlayReason)
	plan2, err := a.as.PlanReconcile(boot2)
	if err != nil {
		t.Fatalf("boot 2 PlanReconcile: %v", err)
	}
	if len(plan2.Actions) != 3 {
		t.Fatalf("boot 2 plan has %d action(s), want 3 — the apps ARE absent and the user still wants them: %+v", len(plan2.Actions), plan2.Actions)
	}
	for _, act := range plan2.Actions {
		if act.Cause != "re-realise" {
			t.Errorf("boot 2 action %+v: cause %q, want \"re-realise\" — A's own replicated row for %s says installed=true, realise_state=\"realised\", "+
				"so this is a box putting back what it had, not a box installing something new", act, act.Cause, act.AppID)
		}
		if !act.Install {
			t.Errorf("boot 2 action %+v is not an install — a re-realisation must still put the app back; the user asked for it and it is not there", act)
		}
		if act.Deferred {
			t.Errorf("boot 2 action %+v is deferred on the FIRST re-realisation — the first one must always be immediate", act)
		}
		if !strings.Contains(act.Reason, "tmpfs") || !strings.Contains(act.Reason, "/run/vulos/rw") {
			t.Errorf("boot 2 action %+v carries reason %q, which does not name the RAM-backed mount — "+
				"a box must be able to say WHY it is downloading this again, from its own measurement", act, act.Reason)
		}
	}

	res2, err := a.as.Reconcile(context.Background(), ulidA, boot2)
	if err != nil {
		t.Fatalf("boot 2 Reconcile: %v", err)
	}
	if len(res2.Installed) != 3 {
		t.Fatalf("boot 2 installed %v, want all 3 — classifying an absence must never be a reason to leave a desired app missing", res2.Installed)
	}
	if len(res2.ReRealised) != 3 {
		t.Fatalf("boot 2 re-realised %v, want all 3", res2.ReRealised)
	}
	if !strings.Contains(res2.ReRealiseReason, "tmpfs") {
		t.Errorf("boot 2 result reason %q does not name the volatile mount", res2.ReRealiseReason)
	}
	if len(res2.Failed) != 0 {
		t.Errorf("boot 2 reported failures %v — nothing failed: the install worked and the storage evaporated", res2.Failed)
	}

	// The desire is untouched and the row still says realised. Marking either
	// one would make the fleet show a working box as broken.
	for _, app := range apps {
		row := rowFor(t, a, ulidA, app)
		if !row.Installed || row.RealiseState != "realised" {
			t.Errorf("after re-realising %s the row reads installed=%v state=%q, want installed=true state=\"realised\"", app, row.Installed, row.RealiseState)
		}
		if row.ReRealiseCount != 1 {
			t.Errorf("%s row rerealise_count = %d, want 1 after exactly one re-realisation", app, row.ReRealiseCount)
		}
		if row.ReRealiseReason != liveOverlayReason {
			t.Errorf("%s row carries reason %q, want the measured mount fact %q", app, row.ReRealiseReason, liveOverlayReason)
		}
	}
	assertStillDesired(t, a.as, apps[0], "the desire must survive a re-realisation")

	// The count is the thing that must outlive the box's own database, so the
	// only test of it that means anything is one that reboots again.
	if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("boot 2: B applies A: %v", err)
	}
	a = newBox(t, ulidA)
	a.learn(t, b)
	b.learn(t, a)
	if err := a.as.ApplyChangeset(b.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("boot 3: A applies B: %v", err)
	}
	if got := rowFor(t, a, ulidA, "browser").ReRealiseCount; got != 1 {
		t.Fatalf("after a reboot that destroyed A's database, A's own re-realisation count reads %d, want 1 — "+
			"the count has to ride the replicated row, because on this box nothing local survives the boot it is counting", got)
	}

	// Boot 3 with a long-elapsed previous re-realisation: the app comes back.
	// Age the row rather than waiting; the point is the elapsed time, not the clock.
	ageReRealisation(t, a, ulidA, 2*time.Hour)
	boot3 := newVolatileRealiser(nil, liveOverlayReason)
	res3, err := a.as.Reconcile(context.Background(), ulidA, boot3)
	if err != nil {
		t.Fatalf("boot 3 Reconcile: %v", err)
	}
	if len(res3.Installed) != 3 || len(res3.Deferred) != 0 {
		t.Fatalf("boot 3 installed %v, deferred %v — an ordinary reboot interval must always be served in full: "+
			"there is no durable storage on this path, so re-downloading IS the correct behaviour and the user's apps must come back", res3.Installed, res3.Deferred)
	}
	if got := rowFor(t, a, ulidA, "browser").ReRealiseCount; got != 2 {
		t.Errorf("browser rerealise_count = %d after a second re-realisation, want 2", got)
	}

	// And an operator standing at the DURABLE box can see all of it — which is
	// the only place it can be seen, since A's own database dies each boot.
	if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("B applies A: %v", err)
	}
	report, err := b.as.ReRealisations()
	if err != nil {
		t.Fatalf("ReRealisations: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("B's re-realisation report has %d instance(s), want 1 (A): %+v", len(report), report)
	}
	if report[0].InstanceULID != ulidA || report[0].Apps != 3 || report[0].Total != 6 {
		t.Errorf("B reports %+v; want instance %s, 3 apps, 6 total re-realisations (3 apps × 2 each)", report[0], ulidA)
	}
	if !strings.Contains(report[0].Reason, "RAM-backed") {
		t.Errorf("B reports reason %q, which does not say why A keeps re-downloading", report[0].Reason)
	}
}

// ageReRealisation moves an instance's recorded re-realisation time back by d,
// simulating elapsed time without sleeping. It writes the replicated row
// directly because there is no API that can put a re-realisation in the past —
// and there should not be, since a caller that could back-date one could evade
// the backoff.
func ageReRealisation(t *testing.T, b *box, instanceULID string, d time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-d).Format(time.RFC3339Nano)
	res, err := b.reg.DB().Exec(`UPDATE app_registry SET rerealise_at = ? WHERE instance_ulid = ? AND rerealise_count > 0`, when, instanceULID)
	if err != nil {
		t.Fatalf("age re-realisation: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Fatalf("age re-realisation: no rows for %s carried a re-realisation to age — the fixture is not testing what it says it is", instanceULID)
	}
}

// TestARepeatedRerealisationIsHeldBackAndSaysSo covers the pathological half:
// not a box that reboots, but a box whose apps evaporate again immediately —
// a reboot loop, or cmd/server's two-minute reconcile ticker re-downloading
// gigabytes because the install lands somewhere that reads back as empty.
//
// That is the "forever" in "silently repeated forever", and it is the only case
// where refusing the download costs the user nothing: the app was there a
// moment ago and downloading it again is not going to make it stay.
func TestARepeatedRerealisationIsHeldBackAndSaysSo(t *testing.T) {
	const ulidA = "01HWZMINST00000000000LOOP"
	_, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(ulidA); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := as.DesireInstall(ulidA, "browser", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}

	// Realise it, then lose it, then lose it again immediately.
	if _, err := as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason))
	if err != nil {
		t.Fatalf("first re-realisation: %v", err)
	}
	if len(first.ReRealised) != 1 {
		t.Fatalf("first re-realisation: re-realised %v, want [browser] — the first one is never held back", first.ReRealised)
	}

	// Immediately again. This one must be held back, and it must say so.
	plan, err := as.PlanReconcileFor(ulidA, newVolatileRealiser(nil, liveOverlayReason))
	if err != nil {
		t.Fatalf("PlanReconcileFor: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan has %d action(s), want 1: %+v", len(plan.Actions), plan.Actions)
	}
	act := plan.Actions[0]
	if !act.Deferred {
		t.Fatalf("a second re-realisation of %s moments after the first was not deferred (%+v) — nothing changed in between, "+
			"so the download is pure waste and repeating it on every pass is the loop this exists to stop", act.AppID, act)
	}
	if act.Cause != "re-realise" || !act.Install {
		t.Errorf("the deferred action lost its identity: %+v — it must stay in the plan as the install it is, "+
			"so that holding it back is a visible decision rather than an install that quietly did not happen", act)
	}
	if !act.NotBefore.After(time.Now().UTC()) {
		t.Errorf("deferred action %+v has NotBefore %v, which is not in the future — a deferral with no due time is a skip", act, act.NotBefore)
	}

	res, err := as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Installed) != 0 {
		t.Errorf("the deferred re-realisation was performed anyway: %v", res.Installed)
	}
	if len(res.Failed) != 0 {
		t.Errorf("a deferral was reported as a failure %v — nothing failed", res.Failed)
	}
	why, ok := res.Deferred["browser"]
	if !ok {
		t.Fatalf("the deferral is not in the result at all: %+v — an install that silently does not happen is the defect, not the fix", res)
	}
	if !strings.Contains(why, "tmpfs") {
		t.Errorf("deferral reason %q does not say why this box keeps losing the app", why)
	}
	assertStillDesired(t, as, "browser", "a deferred re-realisation must not touch the desire")

	// The desire stands and the row is untouched, so the moment the window
	// elapses the app comes back — a deferral is a delay, never a decision that
	// the user does not get their app.
	if got := countRowsWithReRealiseCount(t, as, ulidA); got != 1 {
		t.Errorf("a deferred pass wrote to the row (count now on %d rows, want 1 unchanged) — re-stamping an unchanging fact "+
			"every pass pushes it across the LWW cursor to every peer every two minutes", got)
	}
}

func countRowsWithReRealiseCount(t *testing.T, as *multiinstance.AppSync, ulid string) int {
	t.Helper()
	rows, err := as.ListAppsForInstance(ulid, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r.ReRealiseCount > 0 {
			n++
		}
	}
	return n
}

// TestAFailedInstallIsNotALostInstall is the boundary between the two reports.
// A box that TRIED and could not leaves a row too — installed=false,
// realise_state='failed'. Reading that as "this box had it and lost it" would
// hide an arm64 box that cannot run an amd64 app behind an excuse about
// storage, and would start counting re-realisations of an app that has never
// once been on that disk.
func TestAFailedInstallIsNotALostInstall(t *testing.T) {
	const ulidA = "01HWZMINST00000000000FAIL"
	_, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(ulidA); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := as.DesireInstall(ulidA, "steam", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.ReportRealiseFailure(ulidA, "steam", "1.0.0", "requires amd64; this box is arm64"); err != nil {
		t.Fatalf("ReportRealiseFailure: %v", err)
	}

	plan, err := as.PlanReconcileFor(ulidA, newVolatileRealiser(nil, liveOverlayReason))
	if err != nil {
		t.Fatalf("PlanReconcileFor: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan has %d action(s), want 1: %+v", len(plan.Actions), plan.Actions)
	}
	if plan.Actions[0].Cause != "never-realised" {
		t.Fatalf("a previously FAILED install was planned as %+v — a row that says installed=false/'failed' is a box that never had the app, "+
			"and dressing it as a lost install would replace the real reason (%q) with a story about tmpfs",
			plan.Actions[0], "requires amd64; this box is arm64")
	}
}

// TestARealiserThatCannotMeasureItsStorageStillRerealises pins the case that is
// every developer machine and every stripped container: no mount table, so no
// durability answer. The classification does not depend on the measurement —
// the replicated row alone says the box had the app — and the reason is left
// EMPTY rather than invented, because it is read by a user standing at another
// box.
func TestARealiserThatCannotMeasureItsStorageStillRerealises(t *testing.T) {
	const ulidA = "01HWZMINST00000000000UNKN"
	_, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(ulidA); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := as.DesireInstall(ulidA, "notes", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	// A plain fakeRealiser does not implement DurabilityReporter at all.
	if _, err := as.Reconcile(context.Background(), ulidA, newFakeRealiser(nil)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	plan, err := as.PlanReconcileFor(ulidA, newFakeRealiser(nil))
	if err != nil {
		t.Fatalf("PlanReconcileFor: %v", err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Cause != "re-realise" {
		t.Fatalf("plan %+v: a box that cannot measure its storage must still know it had the app — the row says so", plan.Actions)
	}
	if plan.Actions[0].Reason != "" {
		t.Errorf("reason %q was invented by a box that measured nothing; an unmeasured cause must be empty", plan.Actions[0].Reason)
	}
}

// TestAStalePeerCannotTalkTheRerealisationCountDown is the property that makes
// the count usable at all on the box it describes.
//
// The count lives on a replicated row and the rest of that row is
// last-write-wins. If the count went LWW too, any peer still holding a
// pre-reboot copy would reset it the moment its write won on some other field —
// and on the box this is about, the fleet's copy is the ONLY copy: A's own
// database died with the tmpfs the count is measuring. So the counter is
// grow-only, merged as a max, and a stale copy of the row can lose every other
// field and still not lower it.
func TestAStalePeerCannotTalkTheRerealisationCountDown(t *testing.T) {
	const (
		ulidA = "01HWZMINST00000000000MONA"
		ulidB = "01HWZMINST00000000000MONB"
	)
	a := newBox(t, ulidA)
	b := newBox(t, ulidB)
	a.learn(t, b)
	b.learn(t, a)

	if err := a.as.DesireInstall(ulidA, "browser", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	// Install, lose, re-realise — twice, so A's row carries a count of 2.
	for i := 0; i < 3; i++ {
		if _, err := a.as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason)); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		ageReRealisationIfAny(t, a, ulidA, 2*time.Hour)
	}
	if got := rowFor(t, a, ulidA, "browser").ReRealiseCount; got != 2 {
		t.Fatalf("fixture: A's count is %d, want 2 — the rest of this test proves nothing otherwise", got)
	}
	if err := b.as.ApplyChangeset(a.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("B applies A: %v", err)
	}

	// A STALE copy of A's row: an older timestamp and a count of zero, exactly
	// what a peer that has been away since before any of this would still hold.
	stale := multiinstance.AppRegistryEntry{
		InstanceULID:   ulidA,
		AppID:          "browser",
		AppVersion:     "1.0.0",
		Installed:      true,
		InstalledBy:    ulidA,
		UpdatedAt:      time.Now().UTC().Add(-24 * time.Hour),
		RealiseState:   "realised",
		ReRealiseCount: 0,
	}
	cs, err := a.as.EmitChangeset(ulidA, []multiinstance.AppRegistryEntry{stale})
	if err != nil {
		t.Fatalf("EmitChangeset: %v", err)
	}
	if err := b.as.ApplyChangeset(cs); err != nil {
		t.Fatalf("B applies the stale row: %v", err)
	}
	if got := rowFor(t, b, ulidA, "browser").ReRealiseCount; got != 2 {
		t.Fatalf("a stale copy of A's row reset the re-realisation count to %d (want 2) — "+
			"the fleet is the only place this count survives, and a peer that was asleep must not be able to erase it", got)
	}

	// The other direction: a count that arrives on a row the LWW merge DISCARDS
	// must still be taken. Nothing else on that row is wanted; the counter is
	// not LWW state, and the merge paths that drop a row entirely are exactly
	// where a counter carried only inside the row write would vanish.
	old := stale
	old.ReRealiseCount = 9
	old.ReRealisedAt = time.Now().UTC().Add(-24 * time.Hour)
	old.ReRealiseReason = liveOverlayReason
	cs2, err := a.as.EmitChangeset(ulidA, []multiinstance.AppRegistryEntry{old})
	if err != nil {
		t.Fatalf("EmitChangeset: %v", err)
	}
	if err := b.as.ApplyChangeset(cs2); err != nil {
		t.Fatalf("B applies the older-but-higher-count row: %v", err)
	}
	if got := rowFor(t, b, ulidA, "browser").ReRealiseCount; got != 9 {
		t.Fatalf("a re-realisation count of 9 arrived on a row that lost LWW and was dropped: B still reads %d — "+
			"the count converges independently of who wins the row", got)
	}
}

// ageReRealisationIfAny is ageReRealisation without the fixture assertion, for
// loops where the first pass legitimately records nothing.
func ageReRealisationIfAny(t *testing.T, b *box, instanceULID string, d time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-d).Format(time.RFC3339Nano)
	if _, err := b.reg.DB().Exec(`UPDATE app_registry SET rerealise_at = ? WHERE instance_ulid = ? AND rerealise_count > 0`, when, instanceULID); err != nil {
		t.Fatalf("age re-realisation: %v", err)
	}
}

// TestAnOrdinaryLocalWriteDoesNotEraseTheRerealisationCount pins the claim that
// makes the counter safe to have added to a row with a dozen existing writers.
//
// LocalInstall, LocalUninstall, ReportRealiseFailure and the three partial rows
// mergeEntry builds for its LWW tie-breaks all pass the zero value for the
// count, because none of them knows it exists. Under last-write-wins each of
// them is NEWER than the re-realisation that set it, so each would erase it —
// and the erasure would be invisible, because every one of those writes is
// correct in every other respect.
func TestAnOrdinaryLocalWriteDoesNotEraseTheRerealisationCount(t *testing.T) {
	const ulidA = "01HWZMINST00000000000ERAS"
	_, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(ulidA); err != nil {
		t.Fatalf("identity: %v", err)
	}
	if err := as.DesireInstall(ulidA, "browser", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if _, err := as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := as.Reconcile(context.Background(), ulidA, newVolatileRealiser(nil, liveOverlayReason)); err != nil {
		t.Fatalf("re-realise: %v", err)
	}

	for _, step := range []struct {
		name string
		do   func() error
	}{
		{"LocalInstall", func() error { return as.LocalInstall(ulidA, "browser", "1.0.0") }},
		{"ReportRealiseFailure", func() error {
			return as.ReportRealiseFailure(ulidA, "browser", "1.0.0", "download timed out")
		}},
		{"LocalUninstall", func() error { return as.LocalUninstall(ulidA, "browser") }},
	} {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		rows, err := as.ListAppsForInstance(ulidA, true)
		if err != nil {
			t.Fatalf("list after %s: %v", step.name, err)
		}
		var count int
		for _, r := range rows {
			if r.AppID == "browser" {
				count = r.ReRealiseCount
			}
		}
		if count != 1 {
			t.Fatalf("%s reset the re-realisation count to %d (want 1) — it is a grow-only counter and that write knows nothing about it; "+
				"the box would forget it had ever re-downloaded anything the next time it reported an ordinary install", step.name, count)
		}
	}
}
