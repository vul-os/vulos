package sqlcrdt

// osstate_test.go — the guard on the OS state inventory.
//
// The failure mode this exists to prevent is not "the inventory is wrong
// today". It is "a feature added in six months quietly introduces state that
// does not sync, and nobody notices until a user with two boxes does". A
// document cannot catch that. These tests can, and they are built so that they
// cannot pass by examining nothing — which is this repository's dominant
// defect class (see roadmap/SYNC.md's anti-regression note, and the removed
// services/sync/hotpath.go, which had passing tests, zero callers and a route
// registered on no mux).
//
// Every assertion below counts what it checked and fails if the count is zero
// or below a floor. Every entry's evidence is a real file plus a string that
// must literally appear in it, so an entry cannot be invented.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"vulos/backend/internal/crdtsync"
)

// repoRoot is the repository root relative to this package's directory, which
// is where `go test` runs. backend/internal/sqlcrdt -> ../../..
const repoRoot = "../../.."

// minInventoryEntries is the floor on inventory size.
//
// It is not decoration. Without it every assertion in this file passes
// vacuously against an empty slice, which is precisely how a guard ends up
// printing PASS while checking nothing. Raise it when the inventory grows;
// never lower it to make a deletion pass.
const minInventoryEntries = 30

// TestOSStateInventoryEvidenceIsRealAndAnchored is the assertion that makes
// the whole inventory trustworthy rather than merely plausible: for every
// entry, the file named in Evidence must exist and must literally contain
// Anchor.
//
// A claim about the system that cannot be traced to a line of code is exactly
// the kind of claim this project has had to retract roughly a dozen times. An
// anchor that has drifted fails here rather than surviving as confident
// fiction in a document.
func TestOSStateInventoryEvidenceIsRealAndAnchored(t *testing.T) {
	inv := OSStateInventory()
	if len(inv) < minInventoryEntries {
		t.Fatalf("inventory has %d entries, floor is %d — a shrinking inventory is how coverage silently disappears", len(inv), minInventoryEntries)
	}

	checked := 0
	for _, e := range inv {
		if e.Evidence == "" || e.Anchor == "" {
			t.Errorf("%q: Evidence and Anchor are both required — an entry without them is an unverifiable claim", e.Name)
			continue
		}
		path := filepath.Join(repoRoot, e.Evidence)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%q: evidence file %s cannot be read: %v", e.Name, e.Evidence, err)
			continue
		}
		if !strings.Contains(string(data), e.Anchor) {
			t.Errorf("%q: anchor %q no longer appears in %s — either the code moved and the inventory is stale, or the claim was never true",
				e.Name, e.Anchor, e.Evidence)
			continue
		}
		checked++
	}

	if checked != len(inv) {
		t.Fatalf("verified %d of %d entries — every entry must be anchored", checked, len(inv))
	}
	if checked < minInventoryEntries {
		t.Fatalf("only %d entries verified, floor is %d", checked, minInventoryEntries)
	}
	t.Logf("verified %d inventory entries against real files and anchors", checked)
}

// TestEveryReplicatedTableAppearsInTheInventoryAsSyncing ties the inventory to
// the thing that actually replicates. A table wired into the CRDT engine but
// missing from the inventory would mean the map has stopped describing the
// territory in the direction that matters most.
func TestEveryReplicatedTableAppearsInTheInventoryAsSyncing(t *testing.T) {
	byDomain := map[string]StateEntry{}
	for _, e := range OSStateInventory() {
		if e.Domain != "" {
			byDomain[e.Domain] = e
		}
	}

	tables := ReplicatedTables()
	if len(tables) == 0 {
		t.Fatal("ReplicatedTables() is empty — nothing to check against")
	}
	for _, rt := range tables {
		e, ok := byDomain[rt.Domain]
		if !ok {
			t.Errorf("%s is wired into the CRDT engine but has no inventory entry", rt.Domain)
			continue
		}
		if e.Status != StatusSyncs {
			t.Errorf("%s replicates but the inventory records it as %q", rt.Domain, e.Status)
		}
		if e.Engine != EngineCRDT {
			t.Errorf("%s replicates through the CRDT engine but the inventory names %q", rt.Domain, e.Engine)
		}
	}
	t.Logf("checked %d replicated tables against the inventory", len(tables))
}

// TestInventoryDomainsAgreeWithTheCRDTPolicy checks the other direction: an
// inventory entry carrying a crdtsync domain must agree with policy.go about
// whether that domain syncs. The two files are edited by different people at
// different times and this is the only thing that keeps them honest.
func TestInventoryDomainsAgreeWithTheCRDTPolicy(t *testing.T) {
	checked := 0
	for _, e := range OSStateInventory() {
		if e.Domain == "" {
			continue
		}
		d, ok := crdtsync.DecisionFor(e.Domain)
		if !ok {
			t.Errorf("%q names domain %s, which has no recorded policy decision", e.Name, e.Domain)
			continue
		}
		switch {
		case d.Sync && e.Status != StatusSyncs:
			t.Errorf("%s: policy approves it but the inventory says %q", e.Domain, e.Status)
		case !d.Sync && e.Status == StatusSyncs:
			t.Errorf("%s: policy refuses it but the inventory says it syncs", e.Domain)
		}
		checked++
	}
	if checked < len(ReplicatedTables()) {
		t.Fatalf("only %d inventory entries carry a domain; at least the %d replicated tables must", checked, len(ReplicatedTables()))
	}
	t.Logf("cross-checked %d domain-bearing entries against crdtsync policy", checked)
}

// TestEveryEntryIsReasonedAndGapsNameTheirConsequence enforces the shape that
// makes this inventory useful rather than a status column.
//
// The Consequence requirement on gaps is the deliberate part. "files.db is not
// in the policy" is a fact a reader can skim past; "your Drive is a different
// Drive on each box" is not. A gap that cannot be stated as something a user
// experiences is usually a gap that has not been thought through.
func TestEveryEntryIsReasonedAndGapsNameTheirConsequence(t *testing.T) {
	const minWhy = 40
	gaps, reasoned := 0, 0
	for _, e := range OSStateInventory() {
		if len(e.Why) < minWhy {
			t.Errorf("%q: Why is %d chars, needs at least %d — a one-word reason is not a decision", e.Name, len(e.Why), minWhy)
			continue
		}
		reasoned++
		if e.Status == StatusGap || e.Status == StatusPartial {
			gaps++
			if e.Consequence == "" {
				t.Errorf("%q is a %s but names no user-visible consequence", e.Name, e.Status)
			}
		}
		switch e.Status {
		case StatusSyncs, StatusPartial, StatusException, StatusGap:
		default:
			t.Errorf("%q has unknown status %q", e.Name, e.Status)
		}
		if e.Status == StatusSyncs && e.Engine == EngineNone {
			t.Errorf("%q claims to sync through no engine", e.Name)
		}
	}
	if reasoned < minInventoryEntries {
		t.Fatalf("only %d entries carry reasoning, floor is %d", reasoned, minInventoryEntries)
	}
	if gaps == 0 {
		t.Fatal("the inventory records zero gaps — either every gap was fixed (update this test and say so) or the inventory was gutted")
	}
	t.Logf("%d entries reasoned, %d gaps/partials each naming a consequence", reasoned, gaps)
}

// TestTheKnownGapsAreStillRecorded pins the specific gaps by name.
//
// A count alone is not enough: someone could delete the Drive entry and add a
// trivial one and keep the total. These are the findings of the 2026-08-15
// audit, and each must either still be present as a gap/partial or be REMOVED
// FROM THIS LIST BY SOMEONE WHO FIXED IT — which is the point. Downgrading a
// gap to an exception without argument fails here too, because an exception is
// neither StatusGap nor StatusPartial.
func TestTheKnownGapsAreStillRecorded(t *testing.T) {
	want := []string{
		"installed app set",
		// "installed app set (the replicated mirror)" was here and was FIXED on
		// 2026-08-16 (SYNC-APPS-01): app_desired carries fleet intent, app_registry
		// carries per-instance realisation, and the store handlers write both — so
		// it is no longer a gap and TestInstalledAppSetHasBothProducers now scans
		// the source to keep it that way. "installed app set" itself stays pinned
		// as PARTIAL: bundled apps, un-adopted pre-existing installs and version
		// skew still leave <root>/apps differing between boxes by design.
		"per-app sandbox storage (appfs)",
		"app launcher visibility and suite selection",
		// "theme, accent, night shift (the copy the shell actually uses)" and
		// "dock pins" were here and were FIXED on 2026-08-17 (SYNC-PREFS-01).
		// What fixed them: frontend/src/core/syncedPrefs.ts moved shell
		// preferences onto Profile.Settings, and the two-theme contradiction
		// resolved in favour of the profiles.theme COLUMN, which ThemeProvider
		// now reads and writes. localStorage stayed as a pre-paint CACHE — the
		// keys are still there, which is why the counter-check in
		// TestShellArrangementHasAServerSideHome had to be rewritten to ask
		// whether the BOX holds the value rather than whether the browser does.
		//
		// The three below stay pinned as PARTIAL, and each remainder is a real
		// limit rather than unfinished wiring: an uploaded wallpaper is a
		// megabyte data: URI and the bag is one CRDT register; layout PACKS are
		// install artifacts, not preferences; per-widget STORAGE still does not
		// travel, so a synced widget arrives empty rather than absent.
		"wallpaper",
		"desktop layout, icon arrangement and dock profile",
		"widget rail layout",
		"Drive file metadata (the file tree, ACLs, shares, versions)",
		"Drive file bytes",
		"notification history and Do Not Disturb",
		"password manager vault",
		"joining a cluster from a new device",
	}

	open := map[string]bool{}
	for _, e := range GapsInInventory() {
		open[e.Name] = true
	}
	missing := 0
	for _, name := range want {
		if !open[name] {
			t.Errorf("%q is no longer recorded as a gap or partial. If it was FIXED, remove it from this list in the same commit and say what fixed it; if it was reclassified as an exception, the argument belongs in Why.", name)
			missing++
		}
	}
	if missing == 0 {
		t.Logf("all %d audited gaps still recorded", len(want))
	}
	if len(want) < 10 {
		t.Fatal("the pinned-gap list has been gutted")
	}
}

// TestInstalledAppSetHasBothProducers is the sharpest guard here, and the one
// that answers the App Hub's blocking question mechanically instead of by
// assertion.
//
// It was TestInstalledAppSetHasNoLocalProducer until 2026-08-16, and it fired:
// the installed app set gained producers, so the assertion is now the mirror
// image. Inverted rather than deleted, because "this is wired" decays exactly
// the way "this is not wired" did — silently, with every other test green.
//
// It requires TWO producers, and that is the part the original could not have
// caught. An install is two different facts:
//
//	DESIRE      DesireInstall / DesireRemove   fleet-level, one row per app
//	REALISATION LocalInstall / LocalUninstall  per-instance, one row per box
//
// Realisation alone was never the missing piece in principle — it is a
// per-instance inventory, a description of what each box happens to have, and
// the directive asks for "put it everywhere", which is an intent. A guard that
// checked only for LocalInstall would report PASS on a system that had quietly
// reverted to inventory-only replication, which is the very state the audit
// found. So both are required, separately, with separate messages.
//
// A source scan rather than a comment, because a comment cannot tell a call
// that RUNS from one that merely EXISTS — the same reasoning behind
// TestCRDTSyncCallSiteIsReachable in cmd/server.
func TestInstalledAppSetHasBothProducers(t *testing.T) {
	var realisers, desirers []string
	scanned := 0

	backend := filepath.Join(repoRoot, "backend")
	err := filepath.Walk(backend, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable paths are not evidence of anything
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// appsync.go DEFINES the methods; a definition is not a caller. Nor is
		// the reconciler inside it — Reconcile calls LocalInstall, but a loop
		// that nothing outside the package ever starts produces nothing.
		if strings.HasSuffix(filepath.ToSlash(path), "internal/multiinstance/appsync.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanned++
		src := string(data)
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			// A comment mentioning the method is not a call site.
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, ".LocalInstall(") || strings.Contains(line, ".LocalUninstall(") {
				realisers = append(realisers, filepath.ToSlash(path)+": "+trimmed)
			}
			if strings.Contains(line, ".DesireInstall(") || strings.Contains(line, ".DesireRemove(") {
				desirers = append(desirers, filepath.ToSlash(path)+": "+trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", backend, err)
	}

	// Coverage floor: if the walk found almost no files, the scan proved
	// nothing and "zero callers" is an artifact of not looking.
	if scanned < 200 {
		t.Fatalf("scanned only %d non-test Go files under backend/ — the scan is not covering the tree, so its result means nothing", scanned)
	}

	inv := findEntry(t, "installed app set (the replicated mirror)")

	if len(desirers) == 0 {
		t.Errorf("NOTHING outside internal/multiinstance calls DesireInstall or DesireRemove, so the FLEET DESIRED SET has no "+
			"producer. Whatever else replicates, an install on one box is not being expressed as an intent for the others, and "+
			"app_registry alone is a per-instance inventory — a description of what each box happens to have. That is the exact state "+
			"roadmap/SYNC-INVENTORY.md §1 recorded as the largest gap against the directive. If this is now deliberate, the %q entry "+
			"must go back to %q and say why.", "installed app set (the replicated mirror)", StatusGap)
	}
	if len(realisers) == 0 {
		t.Errorf("NOTHING outside internal/multiinstance calls LocalInstall or LocalUninstall, so no box reports what it actually " +
			"managed to install. The fleet can want apps and never learn which instance has them, which is the half that lets a box " +
			"say WHY it could not install something instead of the app being silently missing.")
	}
	if len(desirers) == 0 || len(realisers) == 0 {
		if inv.Status == StatusSyncs {
			t.Errorf("...and the inventory still claims %q. A claim without a producer is what this whole audit was about.", StatusSyncs)
		}
		return
	}

	if inv.Status != StatusSyncs {
		t.Errorf("both producers are wired (%d desire, %d realisation call sites) but the inventory records %q as %q. "+
			"If the wiring is real the entry should say so; if it is unreachable in production, say THAT — an entry that "+
			"undersells working code is as misleading as one that oversells missing code.",
			len(desirers), len(realisers), inv.Name, inv.Status)
	}
	t.Logf("scanned %d non-test Go files: desire producers %v; realisation producers %v",
		scanned, desirers, realisers)
}

// TestShellArrangementIsBrowserLocal pins the second headline finding: the
// state the directive calls "theme" and "arrange" lives in browser
// localStorage, which is not merely unsynced but not even per-box.
//
// It checks the frontend sources directly rather than trusting the inventory,
// so the inventory cannot drift away from the code in either direction.
// TestShellArrangementHasAServerSideHome was TestShellArrangementIsBrowserLocal
// until 2026-08-17, and it fired: the state it pinned as browser-only moved onto
// the profile (SYNC-PREFS-01). Inverted rather than deleted, for the reason the
// installed-app guard was inverted — "this is wired" decays exactly the way
// "this is not wired" did, silently, with every other test green.
//
// The inversion is not a relaxation. The old test asked whether the frontend
// file contained a localStorage key, which is now the WRONG question twice
// over: the keys still exist (as a pre-paint cache, deliberately), and they
// moved into core/prefKeys.ts, so the literal no longer appears in the owning
// file at all. A test asking the old question would have gone green on a build
// where nothing synced, purely because ThemeProvider still says "localStorage".
//
// So each row now has to satisfy BOTH halves:
//
//	CACHE   the localStorage key still exists in prefKeys.ts, because removing
//	        it reintroduces the flash of wrong theme main.tsx exists to prevent
//	BOX     a bag key exists AND a registered group claims it, because a
//	        constant nobody registers is a preference that silently does not sync
//
// vulos-shell-state is the control: it is the one entry with no bag key, and
// the test fails if it ever acquires one without the inventory's exception
// being revisited.
func TestShellArrangementHasAServerSideHome(t *testing.T) {
	keysSrc, err := os.ReadFile(filepath.Join(repoRoot, "frontend/src/core/prefKeys.ts"))
	if err != nil {
		t.Fatalf("reading prefKeys.ts: %v", err)
	}
	groupsSrc, err := os.ReadFile(filepath.Join(repoRoot, "frontend/src/core/prefGroups.ts"))
	if err != nil {
		t.Fatalf("reading prefGroups.ts: %v", err)
	}
	keys, groups := string(keysSrc), string(groupsSrc)

	// owner: where the state is produced. lsKey: the pre-paint cache. bagKey:
	// where the BOX holds it, empty when it deliberately has no server home.
	shell := []struct{ owner, lsKey, bagKey string }{
		{"frontend/src/core/useWallpaper.tsx", "vulos-wallpaper", "shell.wallpaper"},
		{"frontend/src/shell/Dock.tsx", "vulos-dock-pins", "shell.dock.pins"},
		{"frontend/src/desktop/store.ts", "vulos.desktop.layout", "shell.desktop.preset"},
		{"frontend/src/widgets/layout.ts", "vulos.widgets.layout.v1", "shell.widgets.count"},
		{"frontend/src/core/ThemeProvider.tsx", "vulos-theme", "profile.theme"},
		// The exception, argued in the inventory: a window rectangle is a
		// statement about a particular screen, and this OS targets phones as
		// thin clients to the same box.
		{"frontend/src/providers/ShellProvider.tsx", "vulos-shell-state", ""},
	}

	confirmed := 0
	for _, sh := range shell {
		data, rerr := os.ReadFile(filepath.Join(repoRoot, sh.owner))
		if rerr != nil {
			t.Errorf("%s: %v", sh.owner, rerr)
			continue
		}
		if !strings.Contains(string(data), "localStorage") && !strings.Contains(string(data), "prefRead") {
			t.Errorf("%s no longer reads a local cache for %q — the pre-paint copy is what stops a flash of the wrong theme", sh.owner, sh.lsKey)
			continue
		}
		if sh.bagKey == "" {
			// The control. If this acquires a bag key, the exception in
			// OSStateInventory() has been overtaken and must be rewritten.
			if strings.Contains(keys, sh.lsKey) {
				t.Errorf("%q now has an entry in prefKeys.ts: window geometry is recorded as a DECIDED exception, so either the sync is wrong or the inventory is stale", sh.lsKey)
			}
			confirmed++
			continue
		}
		if !strings.Contains(keys, sh.lsKey) {
			t.Errorf("prefKeys.ts no longer names the cache key %q for %s", sh.lsKey, sh.owner)
			continue
		}
		if !strings.Contains(keys, sh.bagKey) {
			t.Errorf("prefKeys.ts no longer names the bag key %q: %s has lost its server-side home", sh.bagKey, sh.owner)
			continue
		}
		confirmed++
	}
	if confirmed != len(shell) {
		t.Fatalf("confirmed %d of %d shell-state locations", confirmed, len(shell))
	}

	// Every group named in prefKeys.ts must actually be REGISTERED. A constant
	// that nothing registers is a preference that silently does not sync, and
	// it looks identical to one that does from the owning file's side.
	registered := 0
	for _, g := range []string{
		"PREF_GROUP_THEME", "PREF_GROUP_WALLPAPER", "PREF_GROUP_DOCK", "PREF_GROUP_DENSITY",
		"PREF_GROUP_AI", "PREF_GROUP_DESKTOP", "PREF_GROUP_WIDGETS", "PREF_GROUP_NOTIFICATIONS",
	} {
		if !strings.Contains(keys, g) {
			t.Errorf("prefKeys.ts no longer defines %s", g)
			continue
		}
		if !strings.Contains(groups, "registerPrefGroup(lsGroup("+g) && !strings.Contains(groups, "name: "+g) {
			t.Errorf("%s is defined but never registered — the preference it names does not sync", g)
			continue
		}
		registered++
	}
	if registered != 8 {
		t.Fatalf("registered %d of 8 preference groups", registered)
	}

	t.Logf("confirmed %d shell states (%d with a server-side home, 1 excepted) and %d registered groups", confirmed, confirmed-1, registered)
}

// TestJoinPullInstallsNothing pins the finding in this repository's own
// joinsync package: the "join an existing cluster" flow downloads the
// snapshot, confirms it decrypts, and discards it. Both callbacks handed to
// Bootstrap are no-ops.
//
// It fails when someone gives the join path a real installer — which is the
// fix — so the inventory and roadmap get updated at the same time.
func TestJoinPullInstallsNothing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "backend/services/joinsync/backend.go"))
	if err != nil {
		t.Fatalf("reading joinsync/backend.go: %v", err)
	}
	src := string(data)

	checks := []string{"noopInstall", "noopApply", "vulossync.Bootstrap("}
	found := 0
	for _, c := range checks {
		if strings.Contains(src, c) {
			found++
		}
	}
	if found != len(checks) {
		t.Fatalf("joinsync/backend.go no longer matches the audited shape (%d of %d markers found). "+
			"If the join path now installs real data, update the %q inventory entry and roadmap/SYNC-INVENTORY.md.",
			found, len(checks), "joining a cluster from a new device")
	}

	inv := findEntry(t, "joining a cluster from a new device")
	if inv.Status != StatusGap {
		t.Errorf("the join pull still installs nothing, but the inventory records it as %q", inv.Status)
	}
	t.Logf("confirmed the join pull is still verification-only (%d markers)", found)
}

// TestGapsAreNotQuietlyReclassified guards the cheapest way to make this
// inventory lie: flip a StatusGap to StatusException and leave the Why alone.
// An exception has to argue that syncing would be WRONG, so it must say
// something about why. This checks the exceptions carry a substantive
// argument, and that they are not the majority of the table — an inventory
// where everything is an exception has stopped applying the directive.
func TestGapsAreNotQuietlyReclassified(t *testing.T) {
	inv := OSStateInventory()
	exceptions := 0
	for _, e := range inv {
		if e.Status != StatusException {
			continue
		}
		exceptions++
		if len(e.Why) < 80 {
			t.Errorf("%q is an exception with only %d chars of argument — the bar for an exception is higher than for a gap", e.Name, len(e.Why))
		}
	}
	if exceptions == 0 {
		t.Fatal("no exceptions recorded — the directive allows for a few, and naming them is part of the deliverable")
	}
	if exceptions*2 > len(inv) {
		t.Errorf("%d of %d entries are exceptions: when most state is excepted, the directive is no longer being applied", exceptions, len(inv))
	}
	t.Logf("%d exceptions of %d entries, each with a stated argument", exceptions, len(inv))
}

// TestInventoryNamesAreUnique — a duplicate name would let the pinned-gap
// test above pass while pointing at the wrong entry.
func TestInventoryNamesAreUnique(t *testing.T) {
	seen := map[string]int{}
	for _, e := range OSStateInventory() {
		seen[e.Name]++
	}
	var dupes []string
	for n, c := range seen {
		if c > 1 {
			dupes = append(dupes, n)
		}
	}
	sort.Strings(dupes)
	if len(dupes) > 0 {
		t.Fatalf("duplicate inventory names: %v", dupes)
	}
	if len(seen) < minInventoryEntries {
		t.Fatalf("%d unique names, floor is %d", len(seen), minInventoryEntries)
	}
}

// findEntry returns the named entry or fails the test.
func findEntry(t *testing.T, name string) StateEntry {
	t.Helper()
	for _, e := range OSStateInventory() {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("inventory has no entry named %q", name)
	return StateEntry{}
}
