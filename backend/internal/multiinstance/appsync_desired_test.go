package multiinstance_test

// SYNC-APPS-01 tests: the fleet DESIRED set and per-instance REALISED set.
//
// The audit these come from found a replicator that worked perfectly and moved
// nothing: app_registry had no local producer, so every box converged an empty
// table and reported it as healthy. Every component's own tests passed. So the
// bar for these tests is not "the code runs" — it is that each one FAILS when
// the property it names is broken, and each has been shown to.
//
// The properties, in the order the design has to get them right:
//
//	1. removal is a tombstone, not an absence   → no resurrection, ever
//	2. realisation is never authoritative over desire → one broken box cannot
//	   uninstall the fleet
//	3. the desired set is writable only through the door (verified, rostered,
//	   not revoked) — there is no quorum to fall back on
//	4. a box that cannot realise an app REPORTS WHY, and that report replicates

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// ── the sentinel ─────────────────────────────────────────────────────────────

// TestDesiredSetULIDCannotCollideWithARealInstance pins the one assumption the
// whole single-wire-type design rests on: a fleet-desire row is told from a
// realisation row by its InstanceULID alone, so if any real instance could ever
// carry that value, a box's local report would be merged into the fleet desired
// set by every peer.
//
// ULIDs are Crockford base32 (0-9 plus A-Z minus I, L, O, U). '@' is outside it.
func TestDesiredSetULIDCannotCollideWithARealInstance(t *testing.T) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	sentinel := multiinstance.DesiredSetULID
	if sentinel == "" {
		t.Fatal("DesiredSetULID is empty — every realisation row would be routed to the desired set")
	}
	outside := false
	for _, r := range sentinel {
		if !strings.ContainsRune(crockford, r) {
			outside = true
			break
		}
	}
	if !outside {
		t.Errorf("DesiredSetULID %q is spelled entirely in the Crockford base32 ULID alphabet, so a real "+
			"instance identifier could collide with it and have its local realisation report merged into the fleet desired set", sentinel)
	}
}

// TestRealisationReportCannotClaimTheFleetDesireKey checks the runtime refusal
// that backs the sentinel argument. A caller passing the reserved key as its own
// instance id must be refused, not silently allowed to write fleet intent.
func TestRealisationReportCannotClaimTheFleetDesireKey(t *testing.T) {
	_, as := openTempAppSync(t)

	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"LocalInstall", func() error { return as.LocalInstall(multiinstance.DesiredSetULID, "browser", "1.0.0") }},
		{"LocalUninstall", func() error { return as.LocalUninstall(multiinstance.DesiredSetULID, "browser") }},
		{"ReportRealiseFailure", func() error {
			return as.ReportRealiseFailure(multiinstance.DesiredSetULID, "browser", "1.0.0", "nope")
		}},
	} {
		if err := tc.fn(); err == nil {
			t.Errorf("%s accepted the reserved fleet-desire key as an instance id — a local report can rewrite what the user wants", tc.name)
		}
	}

	// And nothing reached the desired set by that route.
	ds, err := as.DesiredSet()
	if err != nil {
		t.Fatalf("DesiredSet: %v", err)
	}
	if len(ds) != 0 {
		t.Errorf("desired set has %d row(s) after refused realisation reports, want 0: %+v", len(ds), ds)
	}
}

// ── property 1: removal is a tombstone ───────────────────────────────────────

// TestDesireRemoveWritesATombstoneNotAnAbsence is the shape check. A removal
// that deletes the row cannot be distinguished from "never wanted", and every
// peer still holding a copy re-seeds it.
func TestDesireRemoveWritesATombstoneNotAnAbsence(t *testing.T) {
	const ulidA = "01HWZMINST000000000000001A"
	_, as := openTempAppSync(t)

	if err := as.DesireInstall(ulidA, "steam", "1.2.3"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireRemove(ulidA, "steam"); err != nil {
		t.Fatalf("DesireRemove: %v", err)
	}

	all, err := as.DesiredSet()
	if err != nil {
		t.Fatalf("DesiredSet: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("after removal the desired set has %d rows, want 1 (the tombstone) — a deleted row is indistinguishable from 'never wanted' and will be resurrected", len(all))
	}
	if all[0].Desired {
		t.Errorf("tombstone for %q still reads Desired=true", all[0].AppID)
	}
	if all[0].UpdatedAt.IsZero() {
		t.Error("tombstone has a zero timestamp — LWW cannot order it against a stale re-send, so the removal can lose")
	}

	// The user-facing read excludes it.
	live, err := as.DesiredApps()
	if err != nil {
		t.Fatalf("DesiredApps: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("DesiredApps returned %d app(s) after removal, want 0: %+v", len(live), live)
	}
}

// TestARemovedAppIsNotResurrectedByAStalePeer is the property this design exists
// for, run as the exchange that actually happens.
//
// Box B was offline when the user removed the app on box A. B still holds
// desired=1 at the older timestamp. When they meet, B re-sends its copy — and
// keeps re-sending it on every sync, forever, which is what makes a
// delete-the-row design resurrect apps permanently rather than once.
func TestARemovedAppIsNotResurrectedByAStalePeer(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		ulidB = "01HWZMINST000000000000001B"
		app   = "steam"
	)
	boxA := newBox(t, ulidA)
	boxB := newBox(t, ulidB)
	boxA.learn(t, boxB)
	boxB.learn(t, boxA)

	// Both boxes agree the app is wanted.
	if err := boxB.as.DesireInstall(ulidB, app, "1.0.0"); err != nil {
		t.Fatalf("B DesireInstall: %v", err)
	}
	staleCS := boxB.changesetSince(t, time.Time{})
	if err := boxA.as.ApplyChangeset(staleCS); err != nil {
		t.Fatalf("A apply B's desire: %v", err)
	}

	// The user removes it on A. B never hears about it (it is offline).
	if err := boxA.as.DesireRemove(ulidA, app); err != nil {
		t.Fatalf("A DesireRemove: %v", err)
	}

	// B comes back and re-sends its pre-removal copy. Three times, because a
	// design that survives one exchange and loses on the next is the bug.
	for i := 0; i < 3; i++ {
		if err := boxA.as.ApplyChangeset(staleCS); err != nil {
			t.Fatalf("A apply B's stale re-send #%d: %v", i+1, err)
		}
		assertNoDesire(t, boxA.as, app,
			"a stale peer's re-send RESURRECTED the app on box A: the user removed it and it came back")
	}

	// And the removal reaches B rather than the two disagreeing forever.
	if err := boxB.as.ApplyChangeset(boxA.changesetSince(t, time.Time{})); err != nil {
		t.Fatalf("B apply A's removal: %v", err)
	}
	assertNoDesire(t, boxB.as, app, "box B still wants the app after merging A's removal — the removal does not propagate")
}

// TestAnExactTieDoesNotResurrect covers the case the realisation set's OR-set
// default gets deliberately wrong for desire.
//
// app_registry resolves an install/uninstall tie as "install wins", which is
// reasonable for a straggler observation. Applying that rule to INTENT is a
// resurrection bug: two boxes acting on the same user action stamp the same
// removal instant, and install-wins means the removal never lands. The desire
// tie-break is the actor id alone.
//
// The test constructs a tie the removal must win under actor tie-break and would
// LOSE under install-wins, then applies both orders and requires the same answer.
func TestAnExactTieDoesNotResurrect(t *testing.T) {
	const (
		ulidLow  = "01HWZMINST00000000000000AA" // smaller actor: expresses "wanted"
		ulidHigh = "01HWZMINST00000000000000ZZ" // larger actor: expresses "removed"
		app      = "steam"
	)
	tie := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	want := multiinstance.DesiredEntry{AppID: app, Version: "1.0.0", Desired: true, ActorULID: ulidLow, UpdatedAt: tie}
	gone := multiinstance.DesiredEntry{AppID: app, Desired: false, ActorULID: ulidHigh, UpdatedAt: tie}

	for _, order := range [][]multiinstance.DesiredEntry{{want, gone}, {gone, want}} {
		reg, as := openTempAppSync(t)
		peer := newSignedOrigin(t, "01HWZMINST00000000000000PP")
		peer.publishInto(t, reg)

		for _, d := range order {
			cs, err := peer.as.EmitChangeset(peer.ulid, []multiinstance.AppRegistryEntry{desireWire(d)})
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if err := as.ApplyChangeset(cs); err != nil {
				t.Fatalf("apply: %v", err)
			}
		}

		all, err := as.DesiredSet()
		if err != nil {
			t.Fatalf("DesiredSet: %v", err)
		}
		if len(all) != 1 {
			t.Fatalf("desired set has %d rows, want 1", len(all))
		}
		if all[0].Desired {
			t.Errorf("order %v: an exact-timestamp tie resurrected %q — the removal by the larger actor must win, "+
				"and 'install wins' is the realisation set's rule, not desire's", orderNames(order), app)
		}
	}
}

// TestLocalIntentWinsOverALaggingClock pins the stamp bump in desireMutate.
//
// Physical clocks between a user's own boxes routinely disagree by more than the
// seconds between two of their actions. With a bare time.Now(), a lagging box's
// "remove this" loses the LWW comparison against a remote row it had just
// merged: the removal appears to work, the UI shows it gone, and the next sync
// brings the app back with no error anywhere.
func TestLocalIntentWinsOverALaggingClock(t *testing.T) {
	const (
		ulidLocal  = "01HWZMINST000000000000001A"
		ulidRemote = "01HWZMINST000000000000001B"
		app        = "steam"
	)
	reg, as := openTempAppSync(t)
	peer := newSignedOrigin(t, ulidRemote)
	peer.publishInto(t, reg)

	// A peer's desire lands stamped an hour in this box's FUTURE.
	future := time.Now().UTC().Add(time.Hour)
	cs, err := peer.as.EmitChangeset(ulidRemote, []multiinstance.AppRegistryEntry{
		desireWire(multiinstance.DesiredEntry{AppID: app, Version: "1.0.0", Desired: true, ActorULID: ulidRemote, UpdatedAt: future}),
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The user removes it HERE, on the lagging box.
	if err := as.DesireRemove(ulidLocal, app); err != nil {
		t.Fatalf("DesireRemove: %v", err)
	}

	// Reading local state HERE proves nothing, and an earlier version of this
	// test did exactly that and passed with the stamp bump removed: a local
	// desireMutate writes unconditionally, so the removal is always visible on
	// the box that made it. The defect is not that the removal fails to apply —
	// it is that it fails to SURVIVE. The measurement has to be taken after the
	// peer re-sends the row the removal was supposed to supersede, which is what
	// the very next sync does.
	if live, lerr := as.DesiredApps(); lerr != nil {
		t.Fatalf("DesiredApps: %v", lerr)
	} else if len(live) != 0 {
		t.Fatalf("the removal did not even apply locally: %+v", live)
	}

	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("re-apply peer row: %v", err)
	}
	assertNoDesire(t, as, app, "the user's removal on a lagging box was undone by the very next sync — "+
		"it lost LWW to a peer row stamped in this box's future ("+future.Format(time.RFC3339Nano)+"), "+
		"so the app came back with no error anywhere. The local stamp must bump past the highest timestamp already seen.")
}

// ── property 2: realisation is never authoritative over desire ───────────────

// TestABrokenInstanceCannotUninstallTheFleet is the failure mode that makes the
// two sets worth having.
//
// One box cannot install a desired app — wrong architecture. It says so, loudly,
// and that report replicates. What it must NOT do is get its "I do not have
// this" treated as "the user does not want this", because then a single broken
// instance uninstalls the app everywhere.
func TestABrokenInstanceCannotUninstallTheFleet(t *testing.T) {
	const (
		ulidA  = "01HWZMINST000000000000001A" // healthy box, holds the desire
		ulidB  = "01HWZMINST000000000000001B" // arm64 box that cannot install
		app    = "steam"
		reason = "registry entry \"steam\" cannot be installed on this box: requires amd64; this box is arm64"
	)
	regA, asA := openTempAppSync(t)
	originB := newSignedOrigin(t, ulidB)
	originB.publishInto(t, regA)

	if err := asA.DesireInstall(ulidA, app, "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}

	// B reports, repeatedly, that it failed. Repeatedly, because a design that
	// holds for one report and erodes over many is the same bug arriving slower.
	for i := 0; i < 5; i++ {
		if err := originB.as.ReportRealiseFailure(ulidB, app, "1.0.0", reason); err != nil {
			t.Fatalf("B ReportRealiseFailure: %v", err)
		}
		rows, err := originB.as.ChangesetSince(time.Time{})
		if err != nil {
			t.Fatalf("B ChangesetSince: %v", err)
		}
		cs, err := originB.as.EmitChangeset(ulidB, rows)
		if err != nil {
			t.Fatalf("B EmitChangeset: %v", err)
		}
		if err := asA.ApplyChangeset(cs); err != nil {
			t.Fatalf("A apply B's failure report #%d: %v", i+1, err)
		}
	}

	live, err := asA.DesiredApps()
	if err != nil {
		t.Fatalf("DesiredApps: %v", err)
	}
	found := false
	for _, d := range live {
		if d.AppID == app {
			found = true
		}
	}
	if !found {
		t.Fatalf("box B's failure to install %q removed it from the FLEET desired set — one broken instance uninstalled the fleet", app)
	}

	// And A can see WHY B does not have it.
	rows, err := asA.ListAppsForInstance(ulidB, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance(B): %v", err)
	}
	var got multiinstance.AppRegistryEntry
	for _, r := range rows {
		if r.AppID == app {
			got = r
		}
	}
	if got.RealiseState != multiinstance.RealiseFailed {
		t.Errorf("A sees B's realise_state for %q as %q, want %q — the box's report did not replicate", app, got.RealiseState, multiinstance.RealiseFailed)
	}
	if !strings.Contains(got.RealiseDetail, "requires amd64") {
		t.Errorf("A sees B's realise_detail as %q — the REASON did not replicate, so the app is merely missing with no explanation", got.RealiseDetail)
	}
}

// TestNoRealisationRowEverReachesTheDesiredSet is the structural half of the
// same property, checked across every polarity a realisation row can have. It
// exists because the previous test proves the OUTCOME for one case, and this one
// proves there is no route at all.
func TestNoRealisationRowEverReachesTheDesiredSet(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		ulidB = "01HWZMINST000000000000001B"
	)
	regA, asA := openTempAppSync(t)
	originB := newSignedOrigin(t, ulidB)
	originB.publishInto(t, regA)

	now := time.Now().UTC()
	for i, e := range []multiinstance.AppRegistryEntry{
		{InstanceULID: ulidB, AppID: "a1", AppVersion: "1", Installed: true, InstalledBy: ulidB, UpdatedAt: now, RealiseState: multiinstance.RealiseRealised},
		{InstanceULID: ulidB, AppID: "a2", Installed: false, InstalledBy: ulidB, UpdatedAt: now, RealiseState: multiinstance.RealiseRemoved},
		{InstanceULID: ulidB, AppID: "a3", Installed: false, InstalledBy: ulidB, UpdatedAt: now, RealiseState: multiinstance.RealiseFailed, RealiseDetail: "boom"},
		{InstanceULID: ulidB, AppID: "a4", Installed: true, InstalledBy: ulidB, UpdatedAt: now},
	} {
		cs, err := originB.as.EmitChangeset(ulidB, []multiinstance.AppRegistryEntry{e})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		if err := asA.ApplyChangeset(cs); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}

	ds, err := asA.DesiredSet()
	if err != nil {
		t.Fatalf("DesiredSet: %v", err)
	}
	if len(ds) != 0 {
		t.Errorf("merging %d realisation rows wrote %d row(s) into the FLEET desired set: %+v — "+
			"a box's report of its own state must never become a statement about what the user wants", 4, len(ds), ds)
	}
}

// ── property 3: the desired set is writable only through the door ────────────

// TestUnverifiedOriginCannotWriteTheDesiredSet covers the three ways an origin
// fails verification. Desire has no quorum to fall back on, so an unverified
// row must be DROPPED — "recorded but not counted" would mean "applied".
func TestUnverifiedOriginCannotWriteTheDesiredSet(t *testing.T) {
	const app = "steam"
	now := time.Now().UTC()
	row := desireWire(multiinstance.DesiredEntry{
		AppID: app, Version: "1.0.0", Desired: true,
		ActorULID: "01HWZMINST00000000000000XX", UpdatedAt: now,
	})

	t.Run("unrostered origin", func(t *testing.T) {
		_, as := openTempAppSync(t)
		stranger := newSignedOrigin(t, "01HWZMINST00000000000000QQ")
		cs, err := stranger.as.EmitChangeset(stranger.ulid, []multiinstance.AppRegistryEntry{row})
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		// Deliberately NOT published into the receiver's roster.
		if err := as.ApplyChangeset(cs); err != nil {
			t.Fatalf("apply: %v", err)
		}
		assertNoDesire(t, as, app, "an origin that is not in the roster wrote the fleet desired set")
	})

	t.Run("unsigned changeset", func(t *testing.T) {
		reg, as := openTempAppSync(t)
		peer := newSignedOrigin(t, "01HWZMINST00000000000000QQ")
		peer.publishInto(t, reg)
		cs, err := peer.as.EmitChangeset(peer.ulid, []multiinstance.AppRegistryEntry{row})
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		cs.Signature = "" // strip it
		if err := as.ApplyChangeset(cs); err != nil {
			t.Fatalf("apply: %v", err)
		}
		assertNoDesire(t, as, app, "an unsigned changeset wrote the fleet desired set")
	})

	t.Run("tampered desire row", func(t *testing.T) {
		reg, as := openTempAppSync(t)
		peer := newSignedOrigin(t, "01HWZMINST00000000000000QQ")
		peer.publishInto(t, reg)
		cs, err := peer.as.EmitChangeset(peer.ulid, []multiinstance.AppRegistryEntry{row})
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		if cs.Signature == "" {
			t.Fatal("a desire row was emitted UNSIGNED — a desired=true row is an unauthenticated remote-INSTALL primitive")
		}
		// Swap the app for a different one, keeping the signature.
		cs.Entries[0].AppID = "keylogger"
		if err := as.ApplyChangeset(cs); err != nil {
			t.Fatalf("apply: %v", err)
		}
		assertNoDesire(t, as, "keylogger", "a tampered desire row was accepted — desired=true rows are not covered by the signature")
	})
}

// TestRevokedPeerCannotWriteTheDesiredSet checks that the desired set inherits
// the eviction work rather than routing around it: a peer that is refused at the
// door for revocation must be refused here too, through the SAME check.
func TestRevokedPeerCannotWriteTheDesiredSet(t *testing.T) {
	const app = "steam"
	reg, as := openTempAppSync(t)
	peer := newSignedOrigin(t, "01HWZMINST00000000000000QQ")
	peer.publishInto(t, reg)

	cs, err := peer.as.EmitChangeset(peer.ulid, []multiinstance.AppRegistryEntry{
		desireWire(multiinstance.DesiredEntry{AppID: app, Desired: true, ActorULID: peer.ulid, UpdatedAt: time.Now().UTC()}),
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	// Revoke it in the receiver's roster.
	inst, ok := reg.Get(peer.ulid)
	if !ok {
		t.Fatal("peer not in roster")
	}
	inst.Revoked = true
	if err := reg.Upsert(inst); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertNoDesire(t, as, app, "a REVOKED peer wrote the fleet desired set")
}

// TestDesireRidesTheExistingChangesetTransport proves the union in
// ChangesetSince, through the real wire encoding. If desire rows do not appear
// in a changeset the transport would carry, the whole feature is the previous
// defect again: a set with a producer and no pipe.
func TestDesireRidesTheExistingChangesetTransport(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		ulidB = "01HWZMINST000000000000001B"
	)
	regB, asB := openTempAppSync(t)
	originA := newSignedOrigin(t, ulidA)
	originA.publishInto(t, regB)

	if err := originA.as.DesireInstall(ulidA, "steam", "1.2.3"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := originA.as.LocalInstall(ulidA, "browser", "2.0.0"); err != nil {
		t.Fatalf("LocalInstall: %v", err)
	}

	rows, err := originA.as.ChangesetSince(time.Time{})
	if err != nil {
		t.Fatalf("ChangesetSince: %v", err)
	}
	sawDesire, sawRealised := false, false
	for _, r := range rows {
		if r.InstanceULID == multiinstance.DesiredSetULID && r.AppID == "steam" {
			sawDesire = true
		}
		if r.InstanceULID == ulidA && r.AppID == "browser" {
			sawRealised = true
		}
	}
	if !sawDesire {
		t.Fatal("ChangesetSince did not include the fleet desire row — the desired set does not ride the transport, so nothing replicates")
	}
	if !sawRealised {
		t.Fatal("ChangesetSince stopped including realisation rows")
	}

	cs, err := originA.as.EmitChangeset(ulidA, rows)
	if err != nil {
		t.Fatalf("EmitChangeset: %v", err)
	}
	blob, err := multiinstance.MarshalChangeset(cs)
	if err != nil {
		t.Fatalf("MarshalChangeset: %v", err)
	}
	got, err := multiinstance.UnmarshalChangeset(blob)
	if err != nil {
		t.Fatalf("UnmarshalChangeset: %v", err)
	}
	if err := asB.ApplyChangeset(got); err != nil {
		t.Fatalf("B ApplyChangeset: %v", err)
	}

	live, err := asB.DesiredApps()
	if err != nil {
		t.Fatalf("B DesiredApps: %v", err)
	}
	if len(live) != 1 || live[0].AppID != "steam" || live[0].Version != "1.2.3" {
		t.Fatalf("box B's desired set after sync is %+v, want exactly steam@1.2.3 — "+
			"install on one instance did not reach the other, which is the gap this work exists to close", live)
	}
}

// TestSigningMessageUnchangedForPreDesireChangesets guards the mixed-version
// fleet. Extending the signed message must not invalidate signatures a box that
// predates the desired set produced, or the first upgraded box refuses every
// other box's uninstall observations.
func TestLegacyShapedSignedUninstallStillVerifiesEndToEnd(t *testing.T) {
	const ulidP = "01HWZMINST00000000000000PP"
	reg, as := openTempAppSync(t)
	peer := newSignedOrigin(t, ulidP)
	peer.publishInto(t, reg)

	// Establish a local installed row so the uninstall has something to act on.
	if err := as.LocalInstall("01HWZMINST000000000000001A", "browser", "1.0.0"); err != nil {
		t.Fatalf("LocalInstall: %v", err)
	}

	// A changeset containing ONLY the rows a pre-SYNC-APPS-01 box could produce.
	cs := peer.emitUninstall(t, "01HWZMINST000000000000001A", "browser", "1.0.0", time.Now().UTC().Add(time.Minute))
	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rows, err := as.ListAppsForInstance("01HWZMINST000000000000001A", true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Installed {
		t.Fatal("the legacy-shaped signed uninstall did not verify/apply — extending the signing message broke mixed-version verification")
	}
}

// ── property 4: a box that cannot realise an app reports why ─────────────────

// fakeRealiser is a scripted box. installErr[appID] makes that install fail with
// a named reason, which is how an architecture mismatch arrives — as an ordinary
// error, with no special handling anywhere.
type fakeRealiser struct {
	disk       map[string]string
	installErr map[string]error
	removeErr  map[string]error
	installed  []string
	removed    []string
}

func newFakeRealiser(disk map[string]string) *fakeRealiser {
	if disk == nil {
		disk = map[string]string{}
	}
	return &fakeRealiser{disk: disk, installErr: map[string]error{}, removeErr: map[string]error{}}
}

func (f *fakeRealiser) RealisedVersions() (map[string]string, error) {
	out := make(map[string]string, len(f.disk))
	for k, v := range f.disk {
		out[k] = v
	}
	return out, nil
}

func (f *fakeRealiser) Realise(_ context.Context, appID, version string) error {
	if err, ok := f.installErr[appID]; ok {
		return err
	}
	f.disk[appID] = version
	f.installed = append(f.installed, appID)
	return nil
}

func (f *fakeRealiser) Unrealise(_ context.Context, appID string) error {
	if err, ok := f.removeErr[appID]; ok {
		return err
	}
	delete(f.disk, appID)
	f.removed = append(f.removed, appID)
	return nil
}

// TestPlanReconcileComputesTheRightDifferences covers all four cases at once,
// including the two that are decisions rather than arithmetic: an app on disk
// the desired set has never heard of is LEFT ALONE (un-adopted, not undesired),
// and a version difference is not an action.
func TestPlanReconcileComputesTheRightDifferences(t *testing.T) {
	const ulidA = "01HWZMINST000000000000001A"
	_, as := openTempAppSync(t)

	if err := as.DesireInstall(ulidA, "wanted-missing", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireInstall(ulidA, "wanted-present", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireInstall(ulidA, "removed-present", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireRemove(ulidA, "removed-present"); err != nil {
		t.Fatalf("DesireRemove: %v", err)
	}
	if err := as.DesireInstall(ulidA, "version-skew", "2.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}

	r := newFakeRealiser(map[string]string{
		"wanted-present":  "1.0.0",
		"removed-present": "1.0.0",
		"version-skew":    "1.0.0", // older than desired — deliberately NOT an action
		"never-heard-of":  "1.0.0", // pre-existing local install — must be left alone
	})

	plan, err := as.PlanReconcile(r)
	if err != nil {
		t.Fatalf("PlanReconcile: %v", err)
	}

	// SYNC-APPS-02: every action now carries WHY it is in the plan. The cause
	// strings are transcribed literally rather than taken from the constants
	// under test — a test that builds its expectation out of the symbol it is
	// checking proves only that the code agrees with itself. "never-realised" is
	// the one that matters here: this box has no realisation row for
	// wanted-missing, so it has genuinely never had it, which is a different
	// action from re-installing something it had and lost.
	want := []multiinstance.ReconcileAction{
		{AppID: "removed-present", Install: false, Cause: "undesired"},
		{AppID: "wanted-missing", Version: "1.0.0", Install: true, Cause: "never-realised"},
	}
	if len(plan.Actions) != len(want) {
		t.Fatalf("plan has %d action(s), want %d: %+v", len(plan.Actions), len(want), plan.Actions)
	}
	for i := range want {
		if plan.Actions[i] != want[i] {
			t.Errorf("action %d = %+v, want %+v", i, plan.Actions[i], want[i])
		}
	}
}

// TestReconcileReportsWhyThisBoxCannotRealiseAnApp is the end-to-end shape of
// the directive's requirement: an app the user wants that an instance cannot run
// is the instance REPORTING WHY, not the app quietly missing.
//
// The error is the one services/appnet/registry.go actually produces for an arch
// mismatch, and it reaches the replicated row without passing through any
// arch-aware code in this package.
func TestReconcileReportsWhyThisBoxCannotRealiseAnApp(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		app   = "steam"
	)
	_, as := openTempAppSync(t)
	if err := as.DesireInstall(ulidA, app, "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireInstall(ulidA, "notes", "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}

	archErr := errors.New(`registry entry "steam" cannot be installed on this box: requires amd64; this box is arm64`)
	r := newFakeRealiser(nil)
	r.installErr[app] = archErr

	res, err := as.Reconcile(context.Background(), ulidA, r)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := res.Failed[app]; got != archErr.Error() {
		t.Errorf("ReconcileResult.Failed[%q] = %q, want the installer's own reason %q", app, got, archErr)
	}
	if len(res.Installed) != 1 || res.Installed[0] != "notes" {
		t.Errorf("Installed = %v, want [notes] — one app failing must not stop the rest", res.Installed)
	}

	rows, err := as.ListAppsForInstance(ulidA, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance: %v", err)
	}
	byID := map[string]multiinstance.AppRegistryEntry{}
	for _, row := range rows {
		byID[row.AppID] = row
	}
	if got := byID[app]; got.RealiseState != multiinstance.RealiseFailed || !strings.Contains(got.RealiseDetail, "requires amd64") {
		t.Errorf("row for %q is state=%q detail=%q, want a %q state carrying the reason — "+
			"without it the app is merely absent and the user is never told why", app, got.RealiseState, got.RealiseDetail, multiinstance.RealiseFailed)
	}
	if got := byID["notes"]; got.RealiseState != multiinstance.RealiseRealised {
		t.Errorf("row for notes is state=%q, want %q", got.RealiseState, multiinstance.RealiseRealised)
	}

	// The desire is untouched by the failure. This is property 2 again, on the
	// path that is most tempting to get wrong: the box has just proved it cannot
	// have the app, and it still does not get to decide the app is not wanted.
	live, err := as.DesiredApps()
	if err != nil {
		t.Fatalf("DesiredApps: %v", err)
	}
	if len(live) != 2 {
		t.Errorf("desired set has %d apps after a realisation failure, want 2 — a failure rewrote the user's intent", len(live))
	}
}

// TestAPermanentFailureIsNotRestampedForever pins the quiet one.
//
// Reconcile runs on a timer, and a failure report stamps UpdatedAt = now. An
// arm64 box that can never install an amd64-only app therefore fails on every
// pass, and each report would cross the LWW cursor and be pushed to every peer —
// forever, for as long as the app stays desired. Nothing would be WRONG, which
// is exactly why it would not be noticed: the state converges, the reason is
// correct, and the fleet gossips an unchanging fact until someone reads a
// bandwidth graph.
//
// A CHANGED reason must still be written, so the check is not "have we reported
// a failure" but "have we reported THIS failure".
func TestAPermanentFailureIsNotRestampedForever(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		app   = "steam"
	)
	_, as := openTempAppSync(t)
	if err := as.DesireInstall(ulidA, app, "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	r := newFakeRealiser(nil)
	r.installErr[app] = errors.New(`registry entry "steam" cannot be installed on this box: requires amd64; this box is arm64`)

	stampAfterPass := func(pass int) time.Time {
		t.Helper()
		if _, err := as.Reconcile(context.Background(), ulidA, r); err != nil {
			t.Fatalf("Reconcile pass %d: %v", pass, err)
		}
		rows, err := as.ListAppsForInstance(ulidA, true)
		if err != nil {
			t.Fatalf("ListAppsForInstance: %v", err)
		}
		for _, row := range rows {
			if row.AppID == app {
				return row.UpdatedAt
			}
		}
		t.Fatalf("no realisation row for %q after pass %d", app, pass)
		return time.Time{}
	}

	first := stampAfterPass(1)
	for pass := 2; pass <= 4; pass++ {
		if got := stampAfterPass(pass); !got.Equal(first) {
			t.Fatalf("reconcile pass %d restamped an UNCHANGED failure (%v → %v) — the row crosses the sync cursor on every "+
				"timer tick and is pushed to every peer for as long as the app stays desired", pass, first, got)
		}
	}

	// A different reason IS news and must be written.
	r.installErr[app] = errors.New("download timed out")
	if got := stampAfterPass(5); got.Equal(first) {
		t.Error("the failure reason changed from an arch mismatch to a timeout and the row was not updated — " +
			"that is the difference between a box that can never have the app and one that might on the next try")
	}
}

// TestReconcileRemovesWhatTheFleetNoLongerWants closes the loop: a box that was
// holding an app the user removed elsewhere actually loses it, and says so.
func TestReconcileRemovesWhatTheFleetNoLongerWants(t *testing.T) {
	const (
		ulidA = "01HWZMINST000000000000001A"
		app   = "steam"
	)
	_, as := openTempAppSync(t)
	if err := as.DesireInstall(ulidA, app, "1.0.0"); err != nil {
		t.Fatalf("DesireInstall: %v", err)
	}
	if err := as.DesireRemove(ulidA, app); err != nil {
		t.Fatalf("DesireRemove: %v", err)
	}

	r := newFakeRealiser(map[string]string{app: "1.0.0"})
	res, err := as.Reconcile(context.Background(), ulidA, r)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != app {
		t.Fatalf("Removed = %v, want [%s] — a tombstoned app was left installed on this box", res.Removed, app)
	}
	if _, still := r.disk[app]; still {
		t.Fatalf("%q is still on disk after reconciling against its tombstone", app)
	}
	rows, err := as.ListAppsForInstance(ulidA, true)
	if err != nil {
		t.Fatalf("ListAppsForInstance: %v", err)
	}
	for _, row := range rows {
		if row.AppID == app && row.RealiseState != multiinstance.RealiseRemoved {
			t.Errorf("row for %q is state=%q, want %q", app, row.RealiseState, multiinstance.RealiseRemoved)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// desireWire renders a DesiredEntry as the wire row a peer would send. It
// deliberately reimplements the mapping rather than exporting the production
// one, so a change to the wire shape is caught here instead of being mirrored
// silently by a shared helper.
func desireWire(d multiinstance.DesiredEntry) multiinstance.AppRegistryEntry {
	return multiinstance.AppRegistryEntry{
		InstanceULID: multiinstance.DesiredSetULID,
		AppID:        d.AppID,
		AppVersion:   d.Version,
		Installed:    d.Desired,
		InstalledBy:  d.ActorULID,
		UpdatedAt:    d.UpdatedAt,
	}
}

// assertNoDesire fails with msg if the fleet currently WANTS appID. A tombstone
// (Desired=false) is not a failure — it is the correct end state of a removal,
// and requiring the row to be absent would assert the very bug the tombstone
// prevents.
func assertNoDesire(t *testing.T, as *multiinstance.AppSync, appID, msg string) {
	t.Helper()
	all, err := as.DesiredSet()
	if err != nil {
		t.Fatalf("DesiredSet: %v", err)
	}
	for _, d := range all {
		if d.AppID == appID && d.Desired {
			t.Fatalf("%s (row: %+v)", msg, d)
		}
	}
}

// box is a whole instance: its own registry, its own AppSync, its own signing
// identity. Two boxes that have learned each other are the smallest setting in
// which "install on one, appears on the other" means anything.
type box struct {
	ulid string
	reg  *multiinstance.Registry
	as   *multiinstance.AppSync
}

func newBox(t *testing.T, ulid string) *box {
	t.Helper()
	reg, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(ulid); err != nil {
		t.Fatalf("GenerateAndSetIdentity(%s): %v", ulid, err)
	}
	return &box{ulid: ulid, reg: reg, as: as}
}

// learn publishes other's instance row (carrying its public key) into b's
// roster, which is what lets b verify other's signatures.
func (b *box) learn(t *testing.T, other *box) {
	t.Helper()
	inst, ok := other.reg.Get(other.ulid)
	if !ok || inst.Ed25519PublicKey == "" {
		t.Fatalf("box %s has no published pubkey to learn", other.ulid)
	}
	if err := b.reg.Upsert(inst); err != nil {
		t.Fatalf("publish %s into %s's roster: %v", other.ulid, b.ulid, err)
	}
}

// changesetSince builds the signed changeset this box would push to a peer.
func (b *box) changesetSince(t *testing.T, since time.Time) *multiinstance.AppChangeset {
	t.Helper()
	rows, err := b.as.ChangesetSince(since)
	if err != nil {
		t.Fatalf("%s ChangesetSince: %v", b.ulid, err)
	}
	cs, err := b.as.EmitChangeset(b.ulid, rows)
	if err != nil {
		t.Fatalf("%s EmitChangeset: %v", b.ulid, err)
	}
	return cs
}

// orderNames renders an application order for a failure message.
func orderNames(order []multiinstance.DesiredEntry) []string {
	out := make([]string, 0, len(order))
	for _, d := range order {
		if d.Desired {
			out = append(out, "want("+d.ActorULID+")")
		} else {
			out = append(out, "remove("+d.ActorULID+")")
		}
	}
	return out
}
