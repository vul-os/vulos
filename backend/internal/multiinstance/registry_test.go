package multiinstance_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// openTempRegistry creates a Registry backed by a temporary SQLite file that
// is removed when the test ends.
func openTempRegistry(t *testing.T) *multiinstance.Registry {
	t.Helper()
	dir := t.TempDir()
	r, err := multiinstance.Open(filepath.Join(dir, "instances.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestUpsertAndGet(t *testing.T) {
	r := openTempRegistry(t)

	inst := multiinstance.Instance{
		ULID:             "01HWZTEST0000000000000001",
		DisplayName:      "My Laptop",
		Kind:             multiinstance.KindDevice,
		EndpointURL:      "https://laptop.example.com",
		Ed25519PublicKey: "AAAATEST",
		Role:             multiinstance.RoleOwner,
		Status:           multiinstance.StatusOnline,
		LastSeenAt:       time.Now().UTC().Truncate(time.Second),
	}

	if err := r.Upsert(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, ok := r.Get(inst.ULID)
	if !ok {
		t.Fatal("Get: instance not found after Upsert")
	}
	if got.DisplayName != inst.DisplayName {
		t.Errorf("DisplayName: got %q want %q", got.DisplayName, inst.DisplayName)
	}
	if got.Kind != inst.Kind {
		t.Errorf("Kind: got %q want %q", got.Kind, inst.Kind)
	}
	if got.Role != inst.Role {
		t.Errorf("Role: got %q want %q", got.Role, inst.Role)
	}
	if got.Status != inst.Status {
		t.Errorf("Status: got %q want %q", got.Status, inst.Status)
	}
}

func TestUpsertDeduplicatesByULID(t *testing.T) {
	r := openTempRegistry(t)

	ulid := "01HWZTEST0000000000000002"
	first := multiinstance.Instance{
		ULID:        ulid,
		DisplayName: "Original Name",
		Kind:        multiinstance.KindDevice,
		Status:      multiinstance.StatusUnknown,
	}
	updated := multiinstance.Instance{
		ULID:        ulid,
		DisplayName: "Updated Name",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOffline,
	}

	if err := r.Upsert(first); err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	if err := r.Upsert(updated); err != nil {
		t.Fatalf("Upsert updated: %v", err)
	}

	// List must contain exactly one entry (deduplicated by ULID).
	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List: expected 1 instance, got %d", len(list))
	}
	if list[0].DisplayName != "Updated Name" {
		t.Errorf("DisplayName after Upsert: got %q want %q", list[0].DisplayName, "Updated Name")
	}
	if list[0].Kind != multiinstance.KindCloud {
		t.Errorf("Kind after Upsert: got %q want %q", list[0].Kind, multiinstance.KindCloud)
	}
}

func TestList(t *testing.T) {
	r := openTempRegistry(t)

	for i, ulid := range []string{
		"01HWZTEST0000000000000010",
		"01HWZTEST0000000000000011",
		"01HWZTEST0000000000000012",
	} {
		if err := r.Upsert(multiinstance.Instance{
			ULID:        ulid,
			DisplayName: "Instance " + ulid[len(ulid)-2:],
			Kind:        multiinstance.KindDevice,
			Role:        multiinstance.RolePeer,
			Status:      multiinstance.StatusUnknown,
		}); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List: expected 3 instances, got %d", len(list))
	}
}

func TestMarkSeen(t *testing.T) {
	r := openTempRegistry(t)

	ulid := "01HWZTEST0000000000000020"
	if err := r.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindDevice,
		Status: multiinstance.StatusOffline,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	before := time.Now().Add(-time.Second)
	if err := r.MarkSeen(ulid); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	got, ok := r.Get(ulid)
	if !ok {
		t.Fatal("Get after MarkSeen: not found")
	}
	if got.Status != multiinstance.StatusOnline {
		t.Errorf("Status after MarkSeen: got %q want %q", got.Status, multiinstance.StatusOnline)
	}
	if got.LastSeenAt.Before(before) {
		t.Errorf("LastSeenAt not updated: got %v, expected after %v", got.LastSeenAt, before)
	}
}

func TestMarkSeenUnknownULID(t *testing.T) {
	r := openTempRegistry(t)
	// Should not error for a non-existent ULID.
	if err := r.MarkSeen("01HWZTEST9999999999999999"); err != nil {
		t.Errorf("MarkSeen unknown ULID: unexpected error: %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	r := openTempRegistry(t)
	_, ok := r.Get("01HWZTEST9999999999999999")
	if ok {
		t.Error("Get: expected not-found for unknown ULID")
	}
}

func TestUpsertEmptyULIDErrors(t *testing.T) {
	r := openTempRegistry(t)
	err := r.Upsert(multiinstance.Instance{})
	if err == nil {
		t.Error("Upsert with empty ULID: expected error")
	}
}

// TestSurvivesRestart verifies that data written to the registry persists
// across a close/reopen of the database file.
func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "instances.db")

	// Write data.
	r1, err := multiinstance.Open(dbPath)
	if err != nil {
		t.Fatalf("Open (first): %v", err)
	}
	inst := multiinstance.Instance{
		ULID:        "01HWZTEST0000000000000030",
		DisplayName: "Persistent Instance",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
	}
	if err := r1.Upsert(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	r1.Close()

	// Verify the file exists.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file missing after close: %v", err)
	}

	// Reopen and read back.
	r2, err := multiinstance.Open(dbPath)
	if err != nil {
		t.Fatalf("Open (second): %v", err)
	}
	defer r2.Close()

	got, ok := r2.Get(inst.ULID)
	if !ok {
		t.Fatal("Get after reopen: instance not found")
	}
	if got.DisplayName != inst.DisplayName {
		t.Errorf("DisplayName after reopen: got %q want %q", got.DisplayName, inst.DisplayName)
	}
	if got.Kind != inst.Kind {
		t.Errorf("Kind after reopen: got %q want %q", got.Kind, inst.Kind)
	}
}
