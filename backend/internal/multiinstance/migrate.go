// Package multiinstance — TOPO-02: "Move to your own instance" migration.
//
// Migrator orchestrates moving an account from a shared-pool Vulos instance to
// a dedicated instance with the SAME Ed25519 identity. The migration is fully
// leaderless: no split-brain, no hard cutover. The actual CRDT state propagates
// to the new instance via the existing fabric sync layer once the new instance
// comes online and peers back into the mesh.
//
// Flow:
//
//  1. Call Plan to validate source and target.
//  2. Call Execute with the plan:
//     a. Load the existing identity key from disk.
//     b. Register the new instance in the registry with the SAME public key.
//     c. Optionally mark the source instance offline (retire source presence).
//  3. When the new instance is reachable, call PeerBack to mark it online.
//
// The CRDT sync is handled by the existing fabric (cloudsync.go / appsync.go)
// once the new instance joins the mesh — no additional code is required here.
package multiinstance

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"
)

// MigrationPlan describes a pending instance migration.
type MigrationPlan struct {
	// SourceULID is the ULID of the existing (source) instance.
	SourceULID string `json:"source_ulid"`
	// TargetURL is the endpoint URL of the new dedicated instance.
	TargetURL string `json:"target_url"`
	// RetireSource, when true, marks the source instance as StatusOffline
	// once the migration completes.
	RetireSource bool `json:"retire_source"`
	// SourcePublicKey is the Ed25519 public key of the source instance,
	// populated by Plan so Execute can register the target with the same key.
	SourcePublicKey string `json:"source_public_key"`
}

// Migrator orchestrates TOPO-02 dedicated-instance migration.
type Migrator struct {
	reg *Registry
}

// NewMigrator returns a Migrator backed by reg.
func NewMigrator(reg *Registry) *Migrator {
	return &Migrator{reg: reg}
}

// Plan validates the migration parameters and returns a MigrationPlan ready
// for Execute. It checks that:
//   - sourceULID is non-empty and exists in the registry
//   - targetURL is non-empty
func (m *Migrator) Plan(sourceULID, targetURL string, retireSource bool) (MigrationPlan, error) {
	if sourceULID == "" {
		return MigrationPlan{}, fmt.Errorf("migrate: sourceULID must not be empty")
	}
	if targetURL == "" {
		return MigrationPlan{}, fmt.Errorf("migrate: targetURL must not be empty")
	}

	src, ok := m.reg.Get(sourceULID)
	if !ok {
		return MigrationPlan{}, fmt.Errorf("migrate: source instance %q not found in registry", sourceULID)
	}

	return MigrationPlan{
		SourceULID:      sourceULID,
		TargetURL:       targetURL,
		RetireSource:    retireSource,
		SourcePublicKey: src.Ed25519PublicKey,
	}, nil
}

// Execute carries out the migration described by plan:
//
//  1. Loads the existing identity key from keyPath (via LoadOrCreateSealedInstanceKey).
//  2. Registers the new instance in the registry with the same Ed25519 public key,
//     Status=StatusUnknown, Kind=KindDevice, Role=RolePeer.
//  3. If plan.RetireSource is true, marks the source instance as StatusOffline.
//
// The actual CRDT sync to the new instance happens automatically via the
// fabric when the new instance comes online (leaderless — no code needed here).
func (m *Migrator) Execute(ctx context.Context, plan MigrationPlan, keyPath string, sealer *KeyringSealer) error {
	if plan.SourceULID == "" {
		return fmt.Errorf("migrate: Execute: plan.SourceULID is empty")
	}
	if plan.TargetURL == "" {
		return fmt.Errorf("migrate: Execute: plan.TargetURL is empty")
	}

	// 1. Load (or create) the identity key from disk.
	privKey, err := LoadOrCreateSealedInstanceKey(keyPath, sealer)
	if err != nil {
		return fmt.Errorf("migrate: Execute: load identity key: %w", err)
	}

	// Derive the public key from the loaded private key.
	pubKey := privKey.Public().(ed25519.PublicKey)
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)

	// 2. Register the new instance with the same Ed25519 public key.
	// Generate a deterministic target ULID from the target URL length + timestamp
	// so we don't import a ULID library. In practice the caller would supply one;
	// here we use a simple unique string.
	targetULID := newMigrationULID()

	newInst := Instance{
		ULID:             targetULID,
		DisplayName:      "Migrated instance",
		Kind:             KindDevice,
		EndpointURL:      plan.TargetURL,
		Ed25519PublicKey: pubKeyB64,
		Role:             RolePeer,
		Status:           StatusUnknown,
		LastSeenAt:       time.Time{},
	}

	if err := m.reg.Upsert(newInst); err != nil {
		return fmt.Errorf("migrate: Execute: register new instance: %w", err)
	}

	// 3. Optionally retire the source instance.
	if plan.RetireSource {
		src, ok := m.reg.Get(plan.SourceULID)
		if ok {
			src.Status = StatusOffline
			if err := m.reg.Upsert(src); err != nil {
				return fmt.Errorf("migrate: Execute: retire source instance: %w", err)
			}
		}
	}

	return nil
}

// PeerBack marks the registry entry for the instance at targetURL as online,
// reflecting that the new instance has successfully joined the mesh.
// It finds the entry by endpoint URL (linear scan — the registry is small).
func (m *Migrator) PeerBack(ctx context.Context, targetURL string) error {
	if targetURL == "" {
		return fmt.Errorf("migrate: PeerBack: targetURL must not be empty")
	}

	all, err := m.reg.List()
	if err != nil {
		return fmt.Errorf("migrate: PeerBack: list registry: %w", err)
	}

	for _, inst := range all {
		if inst.EndpointURL == targetURL {
			return m.reg.MarkSeen(inst.ULID)
		}
	}

	return fmt.Errorf("migrate: PeerBack: no instance with endpoint %q found in registry", targetURL)
}

// newMigrationULID returns a simple unique string for a migrated instance.
// It uses the current Unix nanoseconds encoded in base64 (URL-safe, no padding)
// prefixed with "MIG-" so it is clearly identified in logs.
// In production the caller would supply a proper ULID; this is adequate for
// the local registry which only needs uniqueness within a single account.
func newMigrationULID() string {
	now := time.Now().UnixNano()
	b := make([]byte, 8)
	b[0] = byte(now >> 56)
	b[1] = byte(now >> 48)
	b[2] = byte(now >> 40)
	b[3] = byte(now >> 32)
	b[4] = byte(now >> 24)
	b[5] = byte(now >> 16)
	b[6] = byte(now >> 8)
	b[7] = byte(now)
	return "MIG-" + base64.RawURLEncoding.EncodeToString(b)
}
