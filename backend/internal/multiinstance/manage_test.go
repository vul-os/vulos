package multiinstance_test

// manage_test.go — Registry.Rename / Registry.Delete, the primitives behind the
// dashboard's Instances panel (PATCH /api/instances/{ulid}/rename, DELETE
// /api/instances/{ulid}).
//
// Both must be honest about doing nothing: an unknown ULID is ErrNotFound, not
// a silent no-op that the UI would render as success.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// seedInstance puts one peer instance in the registry and returns it.
func seedInstance(t *testing.T, r *multiinstance.Registry, ulid string, role multiinstance.Role) multiinstance.Instance {
	t.Helper()
	inst := multiinstance.Instance{
		ULID:        ulid,
		DisplayName: "Old Name",
		Kind:        multiinstance.KindDevice,
		EndpointURL: "https://peer.example.com",
		Role:        role,
		Status:      multiinstance.StatusOnline,
		LastSeenAt:  time.Now().UTC(),
	}
	if err := r.Upsert(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return inst
}

func TestRename_PersistsNewName(t *testing.T) {
	r := openTempRegistry(t)
	seedInstance(t, r, "01HWZMANAGE00000000000001", multiinstance.RolePeer)

	got, err := r.Rename("01HWZMANAGE00000000000001", "  Studio Box  ")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got.DisplayName != "Studio Box" {
		t.Errorf("returned display_name = %q, want trimmed %q", got.DisplayName, "Studio Box")
	}

	stored, ok := r.Get("01HWZMANAGE00000000000001")
	if !ok || stored.DisplayName != "Studio Box" {
		t.Errorf("registry not updated: %+v (ok=%v)", stored, ok)
	}
	// Renaming must not disturb the rest of the row.
	if stored.EndpointURL != "https://peer.example.com" || stored.Status != multiinstance.StatusOnline {
		t.Errorf("rename clobbered other fields: %+v", stored)
	}
}

func TestRename_UnknownULIDIsNotFound(t *testing.T) {
	r := openTempRegistry(t)
	if _, err := r.Rename("01HWZNOSUCHINSTANCE000001", "Ghost"); !errors.Is(err, multiinstance.ErrNotFound) {
		t.Fatalf("Rename unknown ULID: err = %v, want ErrNotFound", err)
	}
}

func TestRename_RejectsUnusableNames(t *testing.T) {
	r := openTempRegistry(t)
	seedInstance(t, r, "01HWZMANAGE00000000000002", multiinstance.RolePeer)

	for _, name := range []string{
		"",
		"   ",
		strings.Repeat("x", multiinstance.MaxDisplayNameLen+1),
		"line\nbreak",
		"bell\x07",
	} {
		if _, err := r.Rename("01HWZMANAGE00000000000002", name); err == nil {
			t.Errorf("Rename(%q) accepted, want rejection", name)
		}
	}
	if stored, _ := r.Get("01HWZMANAGE00000000000002"); stored.DisplayName != "Old Name" {
		t.Errorf("a rejected rename still wrote: display_name = %q", stored.DisplayName)
	}
}

// TestDelete_RemovesInstanceAndItsApps — "Remove" must actually remove: the row
// leaves the roster AND the app inventory replicated for that instance goes with
// it, so nothing keeps serving/routing apps for a box the user removed.
func TestDelete_RemovesInstanceAndItsApps(t *testing.T) {
	r := openTempRegistry(t)
	seedInstance(t, r, "01HWZMANAGE00000000000003", multiinstance.RolePeer)
	seedInstance(t, r, "01HWZMANAGE00000000000004", multiinstance.RolePeer)

	for _, ulid := range []string{"01HWZMANAGE00000000000003", "01HWZMANAGE00000000000004"} {
		if _, err := r.DB().Exec(
			`INSERT INTO app_registry (instance_ulid, app_id, app_version, installed, updated_at)
			 VALUES (?, 'mail', '1.0.0', 1, ?)`,
			ulid, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed app_registry: %v", err)
		}
	}

	if err := r.Delete("01HWZMANAGE00000000000003"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, ok := r.Get("01HWZMANAGE00000000000003"); ok {
		t.Error("instance still in the registry after Delete")
	}
	var apps int
	if err := r.DB().QueryRow(
		`SELECT COUNT(*) FROM app_registry WHERE instance_ulid = ?`,
		"01HWZMANAGE00000000000003").Scan(&apps); err != nil {
		t.Fatalf("count apps: %v", err)
	}
	if apps != 0 {
		t.Errorf("app inventory for the removed instance survived: %d rows", apps)
	}

	// The other instance is untouched — Delete is scoped, not a purge.
	if _, ok := r.Get("01HWZMANAGE00000000000004"); !ok {
		t.Error("Delete removed an unrelated instance")
	}
	if err := r.DB().QueryRow(
		`SELECT COUNT(*) FROM app_registry WHERE instance_ulid = ?`,
		"01HWZMANAGE00000000000004").Scan(&apps); err != nil {
		t.Fatalf("count apps: %v", err)
	}
	if apps != 1 {
		t.Errorf("Delete took an unrelated instance's apps: %d rows left", apps)
	}
}

func TestDelete_UnknownULIDIsNotFound(t *testing.T) {
	r := openTempRegistry(t)
	if err := r.Delete("01HWZNOSUCHINSTANCE000002"); !errors.Is(err, multiinstance.ErrNotFound) {
		t.Fatalf("Delete unknown ULID: err = %v, want ErrNotFound", err)
	}
}

// TestDelete_RefusesOwner — the owner row is this box's own identity in the
// fleet; removing it would leave routing and CRDT sync with no local instance.
func TestDelete_RefusesOwner(t *testing.T) {
	r := openTempRegistry(t)
	seedInstance(t, r, "01HWZMANAGE00000000000005", multiinstance.RoleOwner)

	if err := r.Delete("01HWZMANAGE00000000000005"); !errors.Is(err, multiinstance.ErrIsOwner) {
		t.Fatalf("Delete owner: err = %v, want ErrIsOwner", err)
	}
	if _, ok := r.Get("01HWZMANAGE00000000000005"); !ok {
		t.Error("owner instance was removed despite the refusal")
	}
}
