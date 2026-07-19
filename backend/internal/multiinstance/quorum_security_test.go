package multiinstance_test

// quorum_security_test.go — PENTEST-01 adversarial regressions for CRDT quorum
// and multi-tenancy isolation in the app-sync layer.
//
// Attack categories covered:
//
//	QUORUM-SEC-01  A single forged-origin (one peer with N fake OriginULIDs) cannot
//	               force a destructive uninstall — each inflated origin contributes
//	               at most ONE verified observation because EACH origin is bound to
//	               a DISTINCT rostered Ed25519 key. An unsigned/unknown origin is
//	               not counted at all.
//	QUORUM-SEC-02  PeerCount inflation is ignored — the quorum threshold is derived
//	               from the LOCAL registry's peer count, never the self-reported
//	               AppChangeset.PeerCount field.
//	QUORUM-SEC-03  An uninstall from a revoked key is rejected — even if the
//	               signature is technically valid the revocation flag blocks it.
//	QUORUM-SEC-04  Cross-tenant instance isolation — listing apps for instance A
//	               does not return apps installed on instance B.
//	QUORUM-SEC-05  Observation-set GC — a re-install bumps the generation so stale
//	               uninstall observations from an earlier generation cannot be
//	               replayed to reach quorum on the new generation.

import (
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// ─── helpers (shared with appsync_test.go in the same package) ────────────────

// openSec opens a fresh Registry + AppSync in a temp dir for security tests.
// Identical to openTempAppSync; duplicated here for readability of standalone
// security-test runs.
func openSec(t *testing.T) (*multiinstance.Registry, *multiinstance.AppSync) {
	t.Helper()
	dir := t.TempDir()
	reg, err := multiinstance.Open(filepath.Join(dir, "instances.db"))
	if err != nil {
		t.Fatalf("openSec Open: %v", err)
	}
	as, err := multiinstance.OpenAppSync(reg)
	if err != nil {
		t.Fatalf("openSec OpenAppSync: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg, as
}

// seedInstall writes an installed=true row for (instanceULID, appID, version)
// into as at timestamp base.
func seedInstall(t *testing.T, as *multiinstance.AppSync, instanceULID, appID, version string, base time.Time) {
	t.Helper()
	cs, err := as.EmitChangeset(instanceULID, []multiinstance.AppRegistryEntry{
		{InstanceULID: instanceULID, AppID: appID, AppVersion: version,
			Installed: true, InstalledBy: instanceULID, UpdatedAt: base},
	})
	if err != nil {
		t.Fatalf("seedInstall EmitChangeset: %v", err)
	}
	if err := as.ApplyChangeset(cs); err != nil {
		t.Fatalf("seedInstall ApplyChangeset: %v", err)
	}
}

// assertInstalled fails the test if the app is NOT currently installed.
func assertInstalled(t *testing.T, as *multiinstance.AppSync, instanceULID, appID string) {
	t.Helper()
	apps, err := as.ListAppsForInstance(instanceULID, false)
	if err != nil {
		t.Fatalf("assertInstalled: %v", err)
	}
	for _, a := range apps {
		if a.AppID == appID && a.Installed {
			return
		}
	}
	t.Fatalf("assertInstalled: app %q on instance %q is NOT installed — the attack succeeded", appID, instanceULID)
}


// ─── QUORUM-SEC-01: single forged-origin cannot force uninstall ───────────────

// TestSEC_Quorum_ForgedMultipleOriginsCantUninstall is the core CRDT-QUORUM-01
// regression. An attacker who controls ONE secret-holder sends multiple
// changesets each asserting a different OriginULID. Since each OriginULID must
// be backed by a DISTINCT rostered Ed25519 key, and the attacker only controls
// ONE key, all changesets beyond the first verify as the SAME single-origin —
// they cannot reach distinct-origin quorum.
//
// This test simulates the attack: three changesets are sent, all signed with
// the SAME key but claiming three different OriginULIDs. The receiver only
// registers ONE verified origin (the one whose rostered key matches the
// signature). The app must remain installed.
// Guards: CRDT-QUORUM-01 — single attacker cannot forge N distinct origins.
func TestSEC_Quorum_ForgedMultipleOriginsCantUninstall(t *testing.T) {
	const (
		victimULID = "01SEC-QUORUM-VICTIM-ULID000"
		appID      = "notes"
	)

	reg, verifier := openSec(t)

	// Register 3 instances so quorum threshold is 2 distinct origins.
	const (
		realOriginB = "01SEC-QUORUM-REAL-ORIGIN-B0"
		fakeOriginC = "01SEC-QUORUM-FAKE-ORIGIN-C0"
		fakeOriginD = "01SEC-QUORUM-FAKE-ORIGIN-D0"
	)
	for _, u := range []string{victimULID, realOriginB, fakeOriginC} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   u,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert instance %s: %v", u, err)
		}
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedInstall(t, verifier, victimULID, appID, "1.0.0", base)

	// The attacker controls ONE signing identity (realOriginB) but tries to
	// send three changesets claiming three distinct OriginULIDs. The roster only
	// maps realOriginB → its pubkey; fakeOriginC/D have no rostered key, so their
	// signatures verify as UNKNOWN and contribute ZERO observations.
	attackerOrigin := newSignedOrigin(t, realOriginB)
	attackerOrigin.publishInto(t, reg) // publish only realOriginB's key

	uninstallTime := base.Add(time.Minute)

	// Changeset 1: attacker's real identity (realOriginB) — this IS verified,
	// contributes 1 observation.
	cs1 := attackerOrigin.emitUninstall(t, victimULID, appID, "1.0.0", uninstallTime)
	if err := verifier.ApplyChangeset(cs1); err != nil {
		t.Fatalf("apply cs1 (real): %v", err)
	}

	// Still installed — one verified origin is below the threshold of 2.
	assertInstalled(t, verifier, victimULID, appID)

	// Changeset 2: same signed payload but OriginULID changed to fakeOriginC.
	// The signature was made by realOriginB's key, but fakeOriginC has no
	// rostered key → verifyChangesetSignature returns false → observation ignored.
	cs2Fake := &multiinstance.AppChangeset{
		OriginULID:   fakeOriginC,
		SignerPubKey: cs1.SignerPubKey, // attacker re-uses realOriginB's pubkey
		Signature:    cs1.Signature,    // same signature — wrong origin binding
		Entries: []multiinstance.AppRegistryEntry{
			{InstanceULID: victimULID, AppID: appID, AppVersion: "1.0.0",
				Installed: false, InstalledBy: fakeOriginC, UpdatedAt: uninstallTime},
		},
		PeerCount: 3, // attacker inflates peer count
	}
	if err := verifier.ApplyChangeset(cs2Fake); err != nil {
		t.Fatalf("apply cs2 (fake origin C): %v", err)
	}

	// Changeset 3: another fake origin reusing the same signature.
	cs3Fake := &multiinstance.AppChangeset{
		OriginULID:   fakeOriginD,
		SignerPubKey: cs1.SignerPubKey,
		Signature:    cs1.Signature,
		Entries: []multiinstance.AppRegistryEntry{
			{InstanceULID: victimULID, AppID: appID, AppVersion: "1.0.0",
				Installed: false, InstalledBy: fakeOriginD, UpdatedAt: uninstallTime},
		},
		PeerCount: 99, // maximal inflation
	}
	if err := verifier.ApplyChangeset(cs3Fake); err != nil {
		t.Fatalf("apply cs3 (fake origin D): %v", err)
	}

	// The app MUST still be installed — only 1 verified distinct origin (realOriginB)
	// has been observed; fakeOriginC and fakeOriginD had no rostered key and
	// contributed NOTHING to quorum.
	assertInstalled(t, verifier, victimULID, appID)
}

// ─── QUORUM-SEC-02: PeerCount inflation ignored ───────────────────────────────

// TestSEC_Quorum_PeerCountInflationIgnored asserts that a changeset with
// PeerCount=1 (claiming to be the only peer) cannot unilaterally remove an
// app when the LOCAL registry shows 3 instances (quorum threshold = 2).
// Guards: CRDT-QUORUM-01 — self-reported PeerCount is telemetry only.
func TestSEC_Quorum_PeerCountInflationIgnored(t *testing.T) {
	const (
		victimULID = "01SEC-PEERCOUNT-VICTIM-0001"
		appID      = "mail"
	)

	reg, verifier := openSec(t)

	// 3 instances in local registry → quorum threshold 2.
	for _, u := range []string{
		victimULID,
		"01SEC-PEERCOUNT-EXTRA-A0001",
		"01SEC-PEERCOUNT-EXTRA-B0001",
	} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   u,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedInstall(t, verifier, victimULID, appID, "1.0.0", base)

	// Attacker sends an unsigned uninstall with PeerCount=1, hoping the receiver
	// evaluates quorum against PeerCount (it must not — it uses the LOCAL count).
	cs := &multiinstance.AppChangeset{
		OriginULID: "01SEC-PEERCOUNT-ATTACKER001",
		PeerCount:  1, // attacker claims only 1 peer (quorum threshold would be 1)
		Entries: []multiinstance.AppRegistryEntry{
			{InstanceULID: victimULID, AppID: appID, AppVersion: "1.0.0",
				Installed: false, InstalledBy: "attacker", UpdatedAt: base.Add(time.Minute)},
		},
		// No signature — attacker is not a rostered peer.
	}
	if err := verifier.ApplyChangeset(cs); err != nil {
		t.Fatalf("ApplyChangeset: %v", err)
	}

	// App must still be installed — PeerCount=1 did not lower the threshold.
	assertInstalled(t, verifier, victimULID, appID)
}

// ─── QUORUM-SEC-03: revoked key cannot contribute to quorum ───────────────────

// TestSEC_Quorum_RevokedKeyContributesNothing asserts that a changeset signed
// by a peer whose registry entry carries Revoked=true does NOT count toward
// the distinct-origin quorum, even if the signature is cryptographically valid.
//
// Setup: 3-instance registry (victim + revokedOrigin + goodOrigin) → threshold=2.
// Attack: the revoked origin sends a signed uninstall PLUS a second (good) origin
// also sends — total: 1 revoked (not counted) + 1 good = 1 verified distinct origin.
// Threshold is 2, so the app must stay installed.
// If the revoked key were counted, quorum (2) would be met and the app would be
// wrongly uninstalled — the test would fail.
// Guards: CRDT-QUORUM-01 / FABRIC-KEY-01 — revoked keys are always blocked.
func TestSEC_Quorum_RevokedKeyContributesNothing(t *testing.T) {
	const (
		victimULID    = "01SEC-REVOKED-VICTIM-00001"
		appID         = "browser"
		revokedOrigin = "01SEC-REVOKED-ORIGIN-00001"
		goodOrigin    = "01SEC-REVOKED-GOOD-O00001"
	)

	reg, verifier := openSec(t)

	// 3 instances → quorum threshold = 2.
	if err := reg.Upsert(multiinstance.Instance{ULID: victimULID, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("Upsert victim: %v", err)
	}

	// Create two signing origins: one to be revoked, one legitimate.
	revoked := newSignedOrigin(t, revokedOrigin)
	revoked.publishInto(t, reg) // publishes revokedOrigin into roster (3rd instance)

	good := newSignedOrigin(t, goodOrigin)
	good.publishInto(t, reg) // publishes goodOrigin into roster (4th instance — threshold still 2 with 4 peers would be 3; but we have 3 from publishInto)

	// Count the effective roster: victim, revokedOrigin, goodOrigin = 3 instances → threshold = 2.
	// (publishInto calls reg.Upsert which adds each origin as an Instance row)

	// REVOKE the attacking origin BEFORE it sends the uninstall.
	if revokedInst, ok := reg.Get(revokedOrigin); ok {
		revokedInst.Revoked = true
		if err := reg.Upsert(revokedInst); err != nil {
			t.Fatalf("Revoke origin: %v", err)
		}
	} else {
		t.Fatalf("revoked origin not found in registry after publishInto")
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedInstall(t, verifier, victimULID, appID, "1.0.0", base)

	uninstallTime := base.Add(time.Minute)

	// Step 1: Revoked origin sends a signed uninstall — cryptographic signature is
	// valid but the key is revoked → verifyChangesetSignature returns false →
	// the observation is NOT recorded and does NOT count toward quorum.
	if err := verifier.ApplyChangeset(revoked.emitUninstall(t, victimULID, appID, "1.0.0", uninstallTime)); err != nil {
		t.Fatalf("apply revoked uninstall: %v", err)
	}
	// After the revoked origin's attempt, app must still be installed (0 valid origins < threshold 2).
	assertInstalled(t, verifier, victimULID, appID)

	// Step 2: Good origin sends its signed uninstall — 1 verified distinct origin.
	// Threshold is 2, so STILL not met.
	if err := verifier.ApplyChangeset(good.emitUninstall(t, victimULID, appID, "1.0.0", uninstallTime)); err != nil {
		t.Fatalf("apply good uninstall: %v", err)
	}

	// Key assertion: 1 revoked (= 0 counted) + 1 good = 1 verified distinct origin.
	// Threshold is 2 → quorum NOT met → app must remain installed.
	// If the revoked key had been counted (regression), quorum would be 2 and the
	// app would be uninstalled — the test would fail here.
	assertInstalled(t, verifier, victimULID, appID)
}

// TestSEC_Quorum_RevokedKeyAloneCannotUninstall verifies the simpler case:
// in a 3-node registry (threshold=2), a single revoked-key changeset, even
// if it were the ONLY uninstall report, cannot achieve quorum alone.
func TestSEC_Quorum_RevokedKeyAloneCannotUninstall(t *testing.T) {
	const (
		victimULID    = "01SEC-REVALONE-VICTIM-0001"
		appID         = "terminal"
		revokedOrigin = "01SEC-REVALONE-ORIGIN-0001"
		extraInstance = "01SEC-REVALONE-EXTRA-A0001"
	)

	reg, verifier := openSec(t)

	// 3-instance registry → quorum threshold = 2.
	for _, u := range []string{victimULID, revokedOrigin, extraInstance} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   u,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", u, err)
		}
	}

	// Revoke the attacking origin.
	revOrigin := newSignedOrigin(t, revokedOrigin)
	revOrigin.publishInto(t, reg)
	if inst, ok := reg.Get(revokedOrigin); ok {
		inst.Revoked = true
		if err := reg.Upsert(inst); err != nil {
			t.Fatalf("revoke: %v", err)
		}
	}

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedInstall(t, verifier, victimULID, appID, "1.0.0", base)

	// Revoked origin sends a signed uninstall. With 3 instances, threshold=2.
	// The revoked origin contributes 0 verified observations → quorum not met.
	if err := verifier.ApplyChangeset(
		revOrigin.emitUninstall(t, victimULID, appID, "1.0.0", base.Add(time.Minute)),
	); err != nil {
		t.Fatalf("apply: %v", err)
	}

	assertInstalled(t, verifier, victimULID, appID)
}

// ─── QUORUM-SEC-04: cross-tenant instance isolation ───────────────────────────

// TestSEC_CrossTenant_ListAppsDoesNotLeakOtherInstance asserts that listing
// apps for instance A never returns apps that belong to instance B. The
// app_registry PRIMARY KEY is (instance_ulid, app_id), so a query scoped to
// instanceULID-A must be row-isolated from instanceULID-B.
// Guards: PENTEST-01 IDOR — per-instance app inventory must be isolated.
func TestSEC_CrossTenant_ListAppsDoesNotLeakOtherInstance(t *testing.T) {
	const (
		instanceA = "01SEC-TENANT-INSTANCE-A001"
		instanceB = "01SEC-TENANT-INSTANCE-B001"
		appA      = "calendar"
		appB      = "vault"
	)

	_, as := openSec(t)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// Install app A on instance A only.
	seedInstall(t, as, instanceA, appA, "1.0.0", base)

	// Install app B on instance B only.
	seedInstall(t, as, instanceB, appB, "2.0.0", base)

	// List apps for instance A — must NOT include appB from instance B.
	appsA, err := as.ListAppsForInstance(instanceA, false)
	if err != nil {
		t.Fatalf("ListAppsForInstance A: %v", err)
	}
	for _, a := range appsA {
		if a.AppID == appB {
			t.Fatalf("PENTEST-01 IDOR REGRESSION: ListAppsForInstance(%q) returned "+
				"appID %q which belongs to instance %q — cross-tenant app data leakage",
				instanceA, appB, instanceB)
		}
		if a.InstanceULID == instanceB {
			t.Fatalf("PENTEST-01 IDOR REGRESSION: ListAppsForInstance(%q) returned "+
				"a row with instance_ulid=%q — wrong tenant data in response",
				instanceA, instanceB)
		}
	}

	// List apps for instance B — must NOT include appA from instance A.
	appsB, err := as.ListAppsForInstance(instanceB, false)
	if err != nil {
		t.Fatalf("ListAppsForInstance B: %v", err)
	}
	for _, a := range appsB {
		if a.AppID == appA {
			t.Fatalf("PENTEST-01 IDOR REGRESSION: ListAppsForInstance(%q) returned "+
				"appID %q which belongs to instance %q — cross-tenant app data leakage",
				instanceB, appA, instanceA)
		}
		if a.InstanceULID == instanceA {
			t.Fatalf("PENTEST-01 IDOR REGRESSION: ListAppsForInstance(%q) returned "+
				"a row with instance_ulid=%q — wrong tenant data in response",
				instanceB, instanceA)
		}
	}

	// Positive path: each instance sees exactly its own app.
	if len(appsA) != 1 || appsA[0].AppID != appA {
		t.Errorf("instance A app list incorrect: %+v", appsA)
	}
	if len(appsB) != 1 || appsB[0].AppID != appB {
		t.Errorf("instance B app list incorrect: %+v", appsB)
	}
}

// ─── QUORUM-SEC-05: observation-set GC prevents stale-observation replay ──────

// TestSEC_Quorum_StaleObservationReplayBlocked asserts that uninstall
// observations persisted against an old install generation (gen N) are NOT
// reused to satisfy quorum after a re-install bumps the generation to N+1.
//
// Threat model: two peers (B and C) each emit signed uninstalls for gen1.
// Only B's observation arrives before the re-install; C's arrives after.
// The re-install bumps the epoch. B's gen1 observation is now stale (epoch N).
// C's post-re-install changeset is ALSO for the old uninstall timestamp
// (strictly older than the re-install) so LWW discards it entirely — it
// never even reaches recordUninstallObservation for the new epoch. With 0
// valid current-gen observations, quorum (2) is not met and the app stays
// installed.
// Guards: CRDT-QUORUM-01 observation-set GC.
func TestSEC_Quorum_StaleObservationReplayBlocked(t *testing.T) {
	const (
		victimULID = "01SEC-GC-VICTIM-ULID-00001"
		appID      = "spaces"
	)

	reg, verifier := openSec(t)

	// 3-instance registry → quorum threshold = 2.
	const (
		originB = "01SEC-GC-ORIGIN-B-0000001"
		originC = "01SEC-GC-ORIGIN-C-0000001"
	)
	oB := newSignedOrigin(t, originB)
	oC := newSignedOrigin(t, originC)

	if err := reg.Upsert(multiinstance.Instance{ULID: victimULID, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("Upsert victim: %v", err)
	}
	oB.publishInto(t, reg) // adds originB to roster → 3 instances total → threshold=2
	oC.publishInto(t, reg) // adds originC → 4 instances → threshold=3 (majority of 4)

	// Count actual instances in registry to understand the threshold.
	// With victimULID + originB + originC = 3 instances after publishInto adds rows.
	// (publishInto calls reg.Upsert which adds Instance rows — same 3 count as QUORUM-SEC-01 setup)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gen1Install := base
	gen1Uninstall := base.Add(time.Minute)
	gen2Install := base.Add(2 * time.Minute) // re-install timestamp — strictly newer than uninstall

	// PHASE 1: install v1.0.0 (generation 1).
	seedInstall(t, verifier, victimULID, appID, "1.0.0", gen1Install)

	// originB observes the gen1 uninstall — 1 observation, below threshold.
	if err := verifier.ApplyChangeset(
		oB.emitUninstall(t, victimULID, appID, "1.0.0", gen1Uninstall)); err != nil {
		t.Fatalf("apply oB uninstall gen1: %v", err)
	}
	assertInstalled(t, verifier, victimULID, appID)

	// PHASE 2: re-install v2.0.0 at a STRICTLY NEWER timestamp → bumps generation.
	// The gen1 uninstall observation from oB is now epoch-stale (epoch 1, current is 2).
	seedInstall(t, verifier, victimULID, appID, "2.0.0", gen2Install)

	// PHASE 3: originC sends an uninstall stamped at the OLD gen1 uninstall time.
	// This timestamp (gen1Uninstall) is strictly OLDER than the current row's
	// updated_at (gen2Install). The LWW rule in mergeEntry's default branch only
	// calls recordUninstallObservation when it decides to apply the uninstall
	// (i.e., when remote.Installed==false and the current row is installed==true
	// after the strictly-older timestamp check). Since gen1Uninstall < gen2Install,
	// the strictly-older branch runs but only flips the row if quorum is met, and
	// oB's gen1 observation is stale (wrong epoch) → distinctUninstallOrigins = 0
	// → quorum not met → app stays installed.
	if err := verifier.ApplyChangeset(
		oC.emitUninstall(t, victimULID, appID, "1.0.0", gen1Uninstall)); err != nil {
		t.Fatalf("apply oC stale uninstall: %v", err)
	}

	// The app MUST remain installed. The stale gen1 observation from oB (wrong
	// epoch) combined with oC's LWW-discardable (older than current install)
	// changeset cannot reach the quorum threshold on the current generation.
	assertInstalled(t, verifier, victimULID, appID)
}

// ─── QUORUM-SEC-06: stale-timestamp replay cannot bypass GC (CRDT-GC-01) ──────

// TestSEC_Quorum_StaleTimestampReplayBypassBlocked is the CRDT-GC-01 regression.
// After a reinstall bumps the epoch, two verified peers resend their PRE-REINSTALL
// signed uninstall changesets (same signatures, same old timestamps). Without the
// temporal watermark guard (observed_at >= generation_at), recordUninstallObservation
// would stamp both observations at the current epoch, making them appear "fresh"
// and letting them jointly satisfy quorum — the reinstall's GC guarantee would be
// bypassed.
//
// The fix: distinctUninstallOrigins adds AND observed_at >= generation_at so that
// an observation whose timestamp predates the reinstall is excluded even when its
// stored epoch matches the current generation.
func TestSEC_Quorum_StaleTimestampReplayBypassBlocked(t *testing.T) {
	const (
		victimULID = "01SEC-GC01-VICTIM-ULID0001"
		appID      = "vault"
	)

	reg, verifier := openSec(t)

	const (
		originB = "01SEC-GC01-ORIGIN-B-000001"
		originC = "01SEC-GC01-ORIGIN-C-000001"
	)
	oB := newSignedOrigin(t, originB)
	oC := newSignedOrigin(t, originC)

	// 3 instances → quorum threshold = 2.
	if err := reg.Upsert(multiinstance.Instance{ULID: victimULID, Kind: multiinstance.KindDevice, Status: multiinstance.StatusOnline}); err != nil {
		t.Fatalf("Upsert victim: %v", err)
	}
	oB.publishInto(t, reg)
	oC.publishInto(t, reg)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	gen1Install := base
	gen1Uninstall := base.Add(time.Minute)
	gen2Install := base.Add(2 * time.Minute)

	// PHASE 1: install v1.0.0 (generation 1).
	seedInstall(t, verifier, victimULID, appID, "1.0.0", gen1Install)

	// oB sends an uninstall stamped BEFORE the reinstall (gen1Uninstall < gen2Install).
	if err := verifier.ApplyChangeset(oB.emitUninstall(t, victimULID, appID, "1.0.0", gen1Uninstall)); err != nil {
		t.Fatalf("apply oB gen1 uninstall: %v", err)
	}
	assertInstalled(t, verifier, victimULID, appID)

	// PHASE 2: reinstall v2.0.0 — bumps epoch to 2.
	seedInstall(t, verifier, victimULID, appID, "2.0.0", gen2Install)

	// PHASE 3: both oB and oC resend (replay) their pre-reinstall signed changesets
	// with the SAME old timestamp (gen1Uninstall < gen2Install). Without the
	// temporal watermark fix, recordUninstallObservation would stamp both at epoch 2
	// and distinctUninstallOrigins (epoch=2 only) would count them → quorum met →
	// app wrongly uninstalled. With the fix, observed_at < generation_at causes
	// them to be excluded from the quorum count → quorum not met → app stays installed.
	if err := verifier.ApplyChangeset(oB.emitUninstall(t, victimULID, appID, "1.0.0", gen1Uninstall)); err != nil {
		t.Fatalf("apply oB stale replay: %v", err)
	}
	if err := verifier.ApplyChangeset(oC.emitUninstall(t, victimULID, appID, "1.0.0", gen1Uninstall)); err != nil {
		t.Fatalf("apply oC stale: %v", err)
	}

	// The app MUST remain installed — stale-timestamp replays do not satisfy quorum
	// after a reinstall, regardless of how many verified peers send them.
	assertInstalled(t, verifier, victimULID, appID)
}
