package multiinstance_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/internal/multiinstance"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedSealer returns a deterministic KeyringSealer suitable for testing.
func seedSealer(t *testing.T) *multiinstance.KeyringSealer {
	t.Helper()
	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i + 1)
	}
	s, err := multiinstance.NewKeyringSealer(root)
	if err != nil {
		t.Fatalf("NewKeyringSealer: %v", err)
	}
	return s
}

// writeTestKey writes a fresh Ed25519 key to a temp file and returns the path.
func writeTestKey(t *testing.T, sealer *multiinstance.KeyringSealer) (string, ed25519.PublicKey) {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "fabric.key")

	priv, err := multiinstance.LoadOrCreateSealedInstanceKey(keyPath, sealer)
	if err != nil {
		t.Fatalf("LoadOrCreateSealedInstanceKey: %v", err)
	}
	return keyPath, priv.Public().(ed25519.PublicKey)
}

// insertInstance is a convenience wrapper around registry.Upsert.
func insertInstance(t *testing.T, reg *multiinstance.Registry, ulid, endpoint, pubKey string) {
	t.Helper()
	if err := reg.Upsert(multiinstance.Instance{
		ULID:             ulid,
		DisplayName:      "test-" + ulid,
		Kind:             multiinstance.KindDevice,
		EndpointURL:      endpoint,
		Ed25519PublicKey: pubKey,
		Role:             multiinstance.RoleOwner,
		Status:           multiinstance.StatusOnline,
	}); err != nil {
		t.Fatalf("Upsert(%s): %v", ulid, err)
	}
}

// ---------------------------------------------------------------------------
// TestMigrationPlanValidation
// ---------------------------------------------------------------------------

func TestMigrationPlanValidation(t *testing.T) {
	reg := openTempRegistry(t)
	m := multiinstance.NewMigrator(reg)

	// Seed a source instance so valid ULIDs can pass.
	const sourceULID = "01MIGRATE00000000000000001"
	insertInstance(t, reg, sourceULID, "https://shared.example.com", "AAAATEST")

	t.Run("empty sourceULID", func(t *testing.T) {
		_, err := m.Plan("", "https://target.example.com", false)
		if err == nil {
			t.Fatal("expected error for empty sourceULID, got nil")
		}
	})

	t.Run("empty targetURL", func(t *testing.T) {
		_, err := m.Plan(sourceULID, "", false)
		if err == nil {
			t.Fatal("expected error for empty targetURL, got nil")
		}
	})

	t.Run("unknown sourceULID", func(t *testing.T) {
		_, err := m.Plan("UNKNOWN-ULID-DOES-NOT-EXIST", "https://target.example.com", false)
		if err == nil {
			t.Fatal("expected error for unknown sourceULID, got nil")
		}
	})

	t.Run("valid plan returns source public key", func(t *testing.T) {
		plan, err := m.Plan(sourceULID, "https://target.example.com", true)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.SourceULID != sourceULID {
			t.Errorf("SourceULID: got %q want %q", plan.SourceULID, sourceULID)
		}
		if plan.TargetURL != "https://target.example.com" {
			t.Errorf("TargetURL: got %q want %q", plan.TargetURL, "https://target.example.com")
		}
		if plan.SourcePublicKey != "AAAATEST" {
			t.Errorf("SourcePublicKey: got %q want %q", plan.SourcePublicKey, "AAAATEST")
		}
		if !plan.RetireSource {
			t.Error("RetireSource should be true")
		}
	})
}

// ---------------------------------------------------------------------------
// TestMigrationExecute
// ---------------------------------------------------------------------------

func TestMigrationExecute(t *testing.T) {
	reg := openTempRegistry(t)
	m := multiinstance.NewMigrator(reg)
	sealer := seedSealer(t)

	// Write a test identity key and get its public key.
	keyPath, pubKey := writeTestKey(t, sealer)
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)

	// Seed source instance with the same public key.
	const sourceULID = "01MIGRATE00000000000000010"
	insertInstance(t, reg, sourceULID, "https://shared.example.com", pubKeyB64)

	const targetURL = "https://dedicated.example.com"

	t.Run("registers new instance with same public key", func(t *testing.T) {
		plan, err := m.Plan(sourceULID, targetURL, false)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}

		if err := m.Execute(context.Background(), plan, keyPath, sealer); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		// The registry should now have at least 2 entries.
		list, err := reg.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) < 2 {
			t.Fatalf("expected at least 2 instances after Execute, got %d", len(list))
		}

		// Find the new instance by endpoint URL.
		var newInst *multiinstance.Instance
		for i := range list {
			if list[i].EndpointURL == targetURL {
				newInst = &list[i]
				break
			}
		}
		if newInst == nil {
			t.Fatal("new instance with targetURL not found in registry")
		}
		if newInst.Ed25519PublicKey != pubKeyB64 {
			t.Errorf("new instance public key: got %q want %q", newInst.Ed25519PublicKey, pubKeyB64)
		}
		if newInst.Status != multiinstance.StatusUnknown {
			t.Errorf("new instance status: got %q want %q", newInst.Status, multiinstance.StatusUnknown)
		}
		if newInst.Kind != multiinstance.KindDevice {
			t.Errorf("new instance kind: got %q want %q", newInst.Kind, multiinstance.KindDevice)
		}
		if newInst.Role != multiinstance.RolePeer {
			t.Errorf("new instance role: got %q want %q", newInst.Role, multiinstance.RolePeer)
		}
	})

	t.Run("source is not retired when retireSource=false", func(t *testing.T) {
		reg2 := openTempRegistry(t)
		m2 := multiinstance.NewMigrator(reg2)

		const src2 = "01MIGRATE00000000000000020"
		insertInstance(t, reg2, src2, "https://shared2.example.com", pubKeyB64)

		plan, err := m2.Plan(src2, "https://dedicated2.example.com", false)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if err := m2.Execute(context.Background(), plan, keyPath, sealer); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		src, ok := reg2.Get(src2)
		if !ok {
			t.Fatal("source instance not found after Execute")
		}
		if src.Status == multiinstance.StatusOffline {
			t.Error("source should NOT be offline when retireSource=false")
		}
	})

	t.Run("retireSource=true marks source as offline", func(t *testing.T) {
		reg3 := openTempRegistry(t)
		m3 := multiinstance.NewMigrator(reg3)

		const src3 = "01MIGRATE00000000000000030"
		insertInstance(t, reg3, src3, "https://shared3.example.com", pubKeyB64)

		plan, err := m3.Plan(src3, "https://dedicated3.example.com", true)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if err := m3.Execute(context.Background(), plan, keyPath, sealer); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		src, ok := reg3.Get(src3)
		if !ok {
			t.Fatal("source instance not found after Execute")
		}
		if src.Status != multiinstance.StatusOffline {
			t.Errorf("source status: got %q want %q", src.Status, multiinstance.StatusOffline)
		}
	})
}

// ---------------------------------------------------------------------------
// TestMigrationPeerBack
// ---------------------------------------------------------------------------

func TestMigrationPeerBack(t *testing.T) {
	reg := openTempRegistry(t)
	m := multiinstance.NewMigrator(reg)
	sealer := seedSealer(t)

	keyPath, pubKey := writeTestKey(t, sealer)
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)

	const (
		sourceULID = "01MIGRATE00000000000000040"
		targetURL  = "https://dedicated4.example.com"
	)
	insertInstance(t, reg, sourceULID, "https://shared4.example.com", pubKeyB64)

	plan, err := m.Plan(sourceULID, targetURL, false)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := m.Execute(context.Background(), plan, keyPath, sealer); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// PeerBack should mark the new instance as online.
	if err := m.PeerBack(context.Background(), targetURL); err != nil {
		t.Fatalf("PeerBack: %v", err)
	}

	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, inst := range list {
		if inst.EndpointURL == targetURL {
			found = true
			if inst.Status != multiinstance.StatusOnline {
				t.Errorf("PeerBack: new instance status: got %q want %q", inst.Status, multiinstance.StatusOnline)
			}
		}
	}
	if !found {
		t.Fatal("PeerBack: new instance not found in registry")
	}
}

// TestMigrationPeerBackUnknown checks that PeerBack returns an error for an
// unknown target URL.
func TestMigrationPeerBackUnknown(t *testing.T) {
	reg := openTempRegistry(t)
	m := multiinstance.NewMigrator(reg)

	err := m.PeerBack(context.Background(), "https://nonexistent.example.com")
	if err == nil {
		t.Fatal("PeerBack with unknown URL: expected error, got nil")
	}
}

// TestMigrationKeyPathMissing verifies Execute creates a new key file when
// keyPath does not yet exist (first-use path).
func TestMigrationKeyPathMissing(t *testing.T) {
	reg := openTempRegistry(t)
	m := multiinstance.NewMigrator(reg)
	sealer := seedSealer(t)

	const (
		sourceULID = "01MIGRATE00000000000000050"
		targetURL  = "https://dedicated5.example.com"
	)
	insertInstance(t, reg, sourceULID, "https://shared5.example.com", "ANYPUBKEY")

	plan, err := m.Plan(sourceULID, targetURL, false)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Non-existent key file — LoadOrCreateSealedInstanceKey should generate one.
	keyPath := filepath.Join(t.TempDir(), "new-fabric.key")

	if err := m.Execute(context.Background(), plan, keyPath, sealer); err != nil {
		t.Fatalf("Execute with new key path: %v", err)
	}

	// The key file must have been written.
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("key file was not created at %q: %v", keyPath, err)
	}
}
