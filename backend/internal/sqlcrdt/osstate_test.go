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
		"installed app set (the replicated mirror)",
		"per-app sandbox storage (appfs)",
		"app launcher visibility and suite selection",
		"theme, accent, night shift (the copy the shell actually uses)",
		"wallpaper",
		"desktop layout, icon arrangement and dock profile",
		"dock pins",
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

// TestInstalledAppSetHasNoLocalProducer is the sharpest guard here, and the
// one that answers the App Hub's blocking question mechanically instead of by
// assertion.
//
// AppSync.LocalInstall / LocalUninstall are the ONLY way a locally installed
// app can enter app_registry, the table the fabric transport replicates. This
// test scans every non-test Go file under backend/ for a call to either. Today
// it finds none: POST /api/store/install goes to AppStore.Install, which
// creates a directory and never touches AppSync. So the replication is real
// and carries nothing.
//
// The test therefore goes RED IN BOTH DIRECTIONS:
//
//   - if someone wires the producer, this fails and the inventory entry and
//     roadmap/SYNC-INVENTORY.md must be updated to say apps now sync;
//   - if someone claims apps sync without wiring it, the inventory entry has
//     to stay StatusGap, which the assertion below enforces.
//
// A source scan rather than a comment, because a comment cannot tell a call
// that RUNS from one that merely EXISTS — the same reasoning behind
// TestCRDTSyncCallSiteIsReachable in cmd/server.
func TestInstalledAppSetHasNoLocalProducer(t *testing.T) {
	var callers []string
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
		// appsync.go DEFINES the methods; a definition is not a caller.
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
				callers = append(callers, filepath.ToSlash(path)+": "+trimmed)
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

	if len(callers) == 0 {
		if inv.Status != StatusGap {
			t.Errorf("app_registry still has no local producer, but the inventory records it as %q — it must stay %q", inv.Status, StatusGap)
		}
		t.Logf("scanned %d non-test Go files: app_registry has no local producer, so the installed app set does not sync", scanned)
		return
	}

	t.Fatalf("app_registry now HAS a local producer (%d call site(s)):\n  %s\n\n"+
		"This is good news, and it means the audit's headline finding is out of date. Update the %q and %q inventory entries, "+
		"roadmap/SYNC-INVENTORY.md and roadmap/SYNC.md in the same commit, then remove this branch of the test.",
		len(callers), strings.Join(callers, "\n  "),
		"installed app set", "installed app set (the replicated mirror)")
}

// TestShellArrangementIsBrowserLocal pins the second headline finding: the
// state the directive calls "theme" and "arrange" lives in browser
// localStorage, which is not merely unsynced but not even per-box.
//
// It checks the frontend sources directly rather than trusting the inventory,
// so the inventory cannot drift away from the code in either direction.
func TestShellArrangementIsBrowserLocal(t *testing.T) {
	// Each: the frontend file, and the localStorage key it persists to.
	//
	// The key is what the counter-check turns on. An earlier version of this
	// test looked for any "/api/" in the frontend file and flagged
	// ShellProvider.tsx, which does call the backend — to focus and minimize
	// COMPOSITOR windows, live, persisting nothing. That is a proxy measure
	// answering a different question. The question that matters is "does the
	// SERVER hold this state", so the check now asks the server.
	shell := []struct{ file, key string }{
		{"frontend/src/core/useWallpaper.tsx", "vulos-wallpaper"},
		{"frontend/src/shell/Dock.tsx", "vulos-dock-pins"},
		{"frontend/src/desktop/store.ts", "vulos.desktop.layout"},
		{"frontend/src/providers/ShellProvider.tsx", "vulos-shell-state"},
		{"frontend/src/core/ThemeProvider.tsx", "vulos-theme"},
	}

	confirmed := 0
	for _, s := range shell {
		data, err := os.ReadFile(filepath.Join(repoRoot, s.file))
		if err != nil {
			t.Errorf("%s: %v", s.file, err)
			continue
		}
		src := string(data)
		if !strings.Contains(src, s.key) {
			t.Errorf("%s no longer persists %q — if this state moved to the server, update the inventory to say so", s.file, s.key)
			continue
		}
		if !strings.Contains(src, "localStorage") {
			t.Errorf("%s no longer uses localStorage for %q", s.file, s.key)
			continue
		}
		confirmed++
	}
	if confirmed != len(shell) {
		t.Fatalf("confirmed %d of %d shell-state locations in the frontend", confirmed, len(shell))
	}

	// The counter-check, and the half with the teeth: if the SERVER ever
	// starts holding one of these keys, this state has a home that a
	// replicator could reach and the inventory's "browser only, not even
	// per-box" claim is stale.
	keys := make([]string, 0, len(shell))
	for _, s := range shell {
		keys = append(keys, s.key)
	}
	scanned := 0
	err := filepath.Walk(filepath.Join(repoRoot, "backend"), func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// This file names the keys in order to check for them.
		if strings.HasSuffix(filepath.ToSlash(path), "internal/sqlcrdt/osstate.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanned++
		for _, k := range keys {
			if strings.Contains(string(data), k) {
				t.Errorf("%s references shell-state key %q: the server now holds state the inventory records as browser-only", filepath.ToSlash(path), k)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking backend/: %v", err)
	}
	if scanned < 200 {
		t.Fatalf("scanned only %d non-test Go files — the counter-check proved nothing", scanned)
	}
	t.Logf("confirmed %d shell states are browser-local; %d backend files hold none of them", confirmed, scanned)
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
