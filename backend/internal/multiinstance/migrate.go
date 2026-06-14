// Package multiinstance — TOPO-02: DedicatedInstanceMigrator.
//
// "Move to your own instance" = spin up a NEW instance with the SAME Ed25519
// identity, sync the CRDT state to it, and register/peer the new instance into
// the existing mesh.  Leaderless → no split-brain, no hard cutover; the old
// shared-pool presence continues to serve until the caller explicitly retires it.
//
// This file implements the OS-side migration helper.  It reuses existing
// primitives and does NOT build parallel mechanisms:
//
//   - Identity adoption: accepts a pre-loaded ed25519.PrivateKey + VulaID
//     (obtained by the caller from peering.Service.PrivateKey/VulaID) and
//     installs them into the new instance's registry entry — same key,
//     different ULID/endpoint.
//
//   - CRDT sync: reuses the sync.Bootstrap path (snapshot + changeset tail)
//     so the new instance rehydrates the full shared state from the durable
//     S3/Tigris bucket into its own local store.
//
//   - Mesh registration: registers the new instance in the local Registry
//     (multiinstance.Registry) and marks it as peered back (StatusOnline).
//
// The caller is responsible for:
//   - Building the encrypted S3 client (the BootstrapS3 interface; never pass
//     the raw passphrase — use the post-derivation cluster.Client).
//   - Optionally retiring the old shared-pool presence after a health check.
//   - Wiring the CRDT apply loop (HotPath) on the new instance once it is up.
package multiinstance

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"time"

	"vulos/backend/services/sync"
)

// MigratorBootstrapS3 aliases sync.BootstrapS3 so callers can import this
// package without also importing services/sync for the interface type.
// *cluster.Client satisfies this interface (via GetEncrypted / ListPrefix).
type MigratorBootstrapS3 = sync.BootstrapS3

// MigratorDBInstaller aliases sync.DBInstaller.
type MigratorDBInstaller = sync.DBInstaller

// MigratorChangesetApplier aliases sync.ChangesetApplier.
type MigratorChangesetApplier = sync.ChangesetApplier

// MigrationConfig holds the parameters for a single DedicatedInstanceMigrator
// migration.
type MigrationConfig struct {
	// NewULID is the ULID of the NEW dedicated instance being set up.
	// It must be unique across the account and is used as the registry key.
	NewULID string

	// DisplayName is the human-readable name for the new instance (e.g.
	// "My Dedicated Instance").  Used only for the registry entry.
	DisplayName string

	// EndpointURL is the public base URL of the new instance
	// (e.g. "https://<ulid>.vulos.org:8080").  Used for the registry entry
	// and as the mesh peer address.  May be empty for local-only testing.
	EndpointURL string

	// PrivKey is the Ed25519 private key being adopted (SAME key as the source
	// instance — identity is portable, ULID/endpoint changes).  Obtained from
	// peering.Service.PrivateKey() on the source instance.
	PrivKey ed25519.PrivateKey

	// VulaID is the canonical Vula ID string ("vula:ed25519:<base58>") for
	// PrivKey.  Obtained from peering.Service.VulaID() on the source instance.
	VulaID string

	// ClearLocalCache discards the new instance's local DB before syncing from
	// the durable bucket (the new instance starts from an empty store — this is
	// effectively always a wipe on a fresh machine).  Must not be nil.
	ClearLocalCache sync.ClearLocalCache

	// Install installs the snapshot blob into the new instance's local DB.
	Install MigratorDBInstaller

	// Applier applies one changeset object from S3 to the new instance's DB.
	Applier MigratorChangesetApplier

	// RetireOldPresence, if non-nil, is called after the new instance is
	// successfully synced and registered.  It should mark the old shared-pool
	// instance as offline / retired.  Leaderless → the old instance continues
	// to serve until the caller explicitly retires it; no hard cutover.
	RetireOldPresence func(ctx context.Context) error
}

// MigrationResult reports what the migrator did.
type MigrationResult struct {
	// NewInstance is the registry entry that was inserted for the new instance.
	NewInstance Instance
	// RehydrateResult reports what the CRDT sync step restored.
	RehydrateResult sync.RehyResult
	// OldPresenceRetired is true when RetireOldPresence was called and
	// succeeded.
	OldPresenceRetired bool
}

// DedicatedInstanceMigrator performs the OS-side "move to own instance"
// migration.  Construct with NewMigrator and call Run.
type DedicatedInstanceMigrator struct {
	reg *Registry
}

// NewMigrator creates a DedicatedInstanceMigrator backed by reg.
func NewMigrator(reg *Registry) *DedicatedInstanceMigrator {
	if reg == nil {
		panic("multiinstance: NewMigrator: reg must not be nil")
	}
	return &DedicatedInstanceMigrator{reg: reg}
}

// Run executes the migration in three steps:
//
//  1. AdoptIdentity — register the new dedicated instance in the local
//     Registry, carrying the SAME Ed25519 identity as the source instance
//     so the peering fabric can verify it.
//
//  2. SyncCRDT — call sync.RehydrateFromDurable to pull the full shared CRDT
//     state from the durable S3/Tigris bucket into the new instance's local
//     store.  The local cache is treated as non-authoritative; any stale data
//     is discarded before the bucket snapshot + changeset tail is applied.
//
//  3. PeerBack — mark the new instance as online in the Registry and
//     optionally retire the old shared-pool presence (no hard cutover;
//     leaderless CRDT means both instances can serve simultaneously until the
//     old one is retired).
//
// s3 must be the post-derivation SSE-C S3 client (BootstrapS3 interface;
// *cluster.Client satisfies this).  RehydrateFromDurable never receives the
// raw passphrase.
func (m *DedicatedInstanceMigrator) Run(ctx context.Context, cfg MigrationConfig, s3 MigratorBootstrapS3) (MigrationResult, error) {
	if err := validateMigrationConfig(&cfg); err != nil {
		return MigrationResult{}, err
	}

	// ── 1. AdoptIdentity ──────────────────────────────────────────────────────
	// Register the new dedicated instance in the local Registry, carrying the
	// same Ed25519 public key so the peering fabric can recognise it.
	//
	// Note: the VulaID is the canonical form of the public key (base58-encoded
	// ed25519 pub key with the "vula:ed25519:" prefix).  We store the raw
	// base64-standard-encoded public key in the registry for the quorum-signing
	// path (AppSync verifies against this via verifyChangesetSignature).
	pubKeyB64 := base64.StdEncoding.EncodeToString(cfg.PrivKey.Public().(ed25519.PublicKey))

	newInst := Instance{
		ULID:             cfg.NewULID,
		DisplayName:      cfg.DisplayName,
		Kind:             KindDevice,
		EndpointURL:      cfg.EndpointURL,
		Ed25519PublicKey: pubKeyB64,
		Role:             RoleOwner,
		Status:           StatusUnknown,
		LastSeenAt:       time.Time{}, // will be updated by PeerBack
	}

	log.Printf("[migrate/%s] step 1: adopting identity %s → new instance %s",
		cfg.NewULID, cfg.VulaID, cfg.NewULID)

	if err := m.reg.Upsert(newInst); err != nil {
		return MigrationResult{}, fmt.Errorf("migrate: adopt identity: %w", err)
	}

	// ── 2. SyncCRDT ───────────────────────────────────────────────────────────
	// Rehydrate the new instance's local store from the durable bucket.
	// This reuses sync.RehydrateFromDurable (which itself calls Bootstrap),
	// treating the new instance's local DB as a non-authoritative cache to
	// be discarded and rebuilt from the durable snapshot + changeset tail.
	log.Printf("[migrate/%s] step 2: syncing CRDT state from durable bucket", cfg.NewULID)

	rehyResult, err := sync.RehydrateFromDurable(ctx, sync.RehydrateConfig{
		NodeID:          cfg.NewULID,
		ClearLocalCache: cfg.ClearLocalCache,
		Install:         cfg.Install,
		Applier:         cfg.Applier,
	}, s3)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("migrate: sync CRDT: %w", err)
	}

	log.Printf("[migrate/%s] CRDT sync complete: snapshot_version=%d changeset_tail=%d",
		cfg.NewULID, rehyResult.SnapshotVersion, rehyResult.ChangesetsTailCount)

	// ── 3. PeerBack ──────────────────────────────────────────────────────────
	// Mark the new instance as online and registered in the mesh.
	// Leaderless CRDT means both the old shared-pool presence and this new
	// dedicated instance can serve concurrently — no hard cutover, no
	// split-brain.
	log.Printf("[migrate/%s] step 3: peering new instance back into the mesh", cfg.NewULID)

	if err := m.reg.MarkSeen(cfg.NewULID); err != nil {
		return MigrationResult{}, fmt.Errorf("migrate: peer back (MarkSeen): %w", err)
	}

	// Fetch the freshly-marked instance for the result.
	registeredInst, ok := m.reg.Get(cfg.NewULID)
	if !ok {
		return MigrationResult{}, fmt.Errorf("migrate: peer back: instance %s not found after MarkSeen", cfg.NewULID)
	}

	// ── Optionally retire the old shared-pool presence ────────────────────────
	// No hard cutover: if RetireOldPresence is nil, the old instance keeps
	// serving.  If it is set, we call it now that the new instance is ready.
	result := MigrationResult{
		NewInstance:     registeredInst,
		RehydrateResult: rehyResult,
	}

	if cfg.RetireOldPresence != nil {
		log.Printf("[migrate/%s] retiring old shared-pool presence", cfg.NewULID)
		if err := cfg.RetireOldPresence(ctx); err != nil {
			// Non-fatal: log and continue.  The new instance is already live.
			log.Printf("[migrate/%s] retire old presence: %v (non-fatal; new instance is live)", cfg.NewULID, err)
		} else {
			result.OldPresenceRetired = true
		}
	}

	log.Printf("[migrate/%s] migration complete: identity=%s status=%s",
		cfg.NewULID, cfg.VulaID, registeredInst.Status)

	return result, nil
}

// validateMigrationConfig checks required fields.
func validateMigrationConfig(cfg *MigrationConfig) error {
	if cfg.NewULID == "" {
		return errors.New("migrate: NewULID must not be empty")
	}
	if len(cfg.PrivKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("migrate: PrivKey has unexpected length %d (want %d)", len(cfg.PrivKey), ed25519.PrivateKeySize)
	}
	if cfg.VulaID == "" {
		return errors.New("migrate: VulaID must not be empty")
	}
	if cfg.ClearLocalCache == nil {
		return errors.New("migrate: ClearLocalCache must not be nil")
	}
	if cfg.Install == nil {
		return errors.New("migrate: Install must not be nil")
	}
	if cfg.Applier == nil {
		return errors.New("migrate: Applier must not be nil")
	}
	return nil
}
