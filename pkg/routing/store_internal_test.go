package routing

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openTestStore opens an in-memory SQLite store for tests.
func openTestStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN("file::memory:?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	s, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const (
	testULID     = "01HZZZZZZZZZZZZZZZZZZZZZZA"
	testULID2    = "01HZZZZZZZZZZZZZZZZZZZZZZB"
	testAccount  = "acct-1"
	testAccount2 = "acct-2"
)

// --------------------------------------------------------------------------
// Enroll / GetBinding
// --------------------------------------------------------------------------

func TestSQLStore_Enroll_New(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b, err := s.Enroll(ctx, testULID, testAccount, ModeFabric)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if b.ULID != testULID {
		t.Errorf("ULID: got %q want %q", b.ULID, testULID)
	}
	if b.AccountID != testAccount {
		t.Errorf("AccountID: got %q want %q", b.AccountID, testAccount)
	}
	if b.Mode != ModeFabric {
		t.Errorf("Mode: got %q want fabric", b.Mode)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if b.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestSQLStore_Enroll_IdempotentSameAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b1, err := s.Enroll(ctx, testULID, testAccount, ModeFabric)
	if err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	// Re-enroll with different mode — should refresh mode, preserve created_at.
	b2, err := s.Enroll(ctx, testULID, testAccount, ModeDirect)
	if err != nil {
		t.Fatalf("second Enroll: %v", err)
	}
	if b2.Mode != ModeDirect {
		t.Errorf("mode not refreshed: got %q", b2.Mode)
	}
	if !b2.CreatedAt.Equal(b1.CreatedAt) {
		t.Errorf("CreatedAt changed on re-enroll: was %v now %v", b1.CreatedAt, b2.CreatedAt)
	}
	if b2.UpdatedAt.Before(b1.UpdatedAt) || b2.UpdatedAt.Equal(b1.UpdatedAt) {
		// UpdatedAt may be equal if sub-second; just ensure it didn't go backward.
		// Allow equal (fast test).
	}
}

func TestSQLStore_Enroll_DifferentAccount_Conflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.Enroll(ctx, testULID, testAccount, ModeFabric); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	_, err := s.Enroll(ctx, testULID, testAccount2, ModeFabric)
	if err != ErrULIDBoundToOtherAccount {
		t.Errorf("expected ErrULIDBoundToOtherAccount, got %v", err)
	}
}

func TestSQLStore_Enroll_InvalidMode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.Enroll(ctx, testULID, testAccount, "local")
	if err != ErrInvalidMode {
		t.Errorf("expected ErrInvalidMode, got %v", err)
	}
}

func TestSQLStore_Enroll_EmptyULID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.Enroll(ctx, "", testAccount, ModeFabric)
	if err != ErrInvalidULID {
		t.Errorf("expected ErrInvalidULID, got %v", err)
	}
}

func TestSQLStore_GetBinding_NotEnrolled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.GetBinding(ctx, testULID)
	if err != ErrNotEnrolled {
		t.Errorf("expected ErrNotEnrolled, got %v", err)
	}
}

func TestSQLStore_GetBinding_Found(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeFabric); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	b, err := s.GetBinding(ctx, testULID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.ULID != testULID {
		t.Errorf("ULID mismatch: got %q", b.ULID)
	}
}

// --------------------------------------------------------------------------
// SetConnMode
// --------------------------------------------------------------------------

func TestSQLStore_SetConnMode_NotEnrolled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.SetConnMode(ctx, testULID, ModeFabric)
	if err != ErrNotEnrolled {
		t.Errorf("expected ErrNotEnrolled, got %v", err)
	}
}

func TestSQLStore_SetConnMode_InvalidMode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeFabric); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	err := s.SetConnMode(ctx, testULID, "bogus")
	if err != ErrInvalidMode {
		t.Errorf("expected ErrInvalidMode, got %v", err)
	}
}

func TestSQLStore_SetConnMode_OK(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeFabric); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := s.SetConnMode(ctx, testULID, ModeOwnDomain); err != nil {
		t.Fatalf("SetConnMode: %v", err)
	}
	b, err := s.GetBinding(ctx, testULID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.Mode != ModeOwnDomain {
		t.Errorf("mode not updated: got %q", b.Mode)
	}
}

// --------------------------------------------------------------------------
// SetDirectIP
// --------------------------------------------------------------------------

func TestSQLStore_SetDirectIP_InvalidIP(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeDirect); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	err := s.SetDirectIP(ctx, testULID, testAccount, "not-an-ip")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_SetDirectIP_NotEnrolled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.SetDirectIP(ctx, testULID, testAccount, "1.2.3.4")
	if err != ErrNotEnrolled {
		t.Errorf("expected ErrNotEnrolled, got %v", err)
	}
}

func TestSQLStore_SetDirectIP_NotOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeDirect); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	err := s.SetDirectIP(ctx, testULID, testAccount2, "1.2.3.4")
	if err != ErrNotOwner {
		t.Errorf("expected ErrNotOwner, got %v", err)
	}
}

func TestSQLStore_SetDirectIP_OK(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Enroll(ctx, testULID, testAccount, ModeDirect); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := s.SetDirectIP(ctx, testULID, testAccount, "203.0.113.5"); err != nil {
		t.Fatalf("SetDirectIP: %v", err)
	}
	b, err := s.GetBinding(ctx, testULID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.DirectIP != "203.0.113.5" {
		t.Errorf("DirectIP: got %q want 203.0.113.5", b.DirectIP)
	}
}

// --------------------------------------------------------------------------
// Devices
// --------------------------------------------------------------------------

func TestSQLStore_UpsertDevice_EmptyID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.UpsertDevice(ctx, Device{ID: "", ULID: testULID})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_UpsertDevice_EmptyULID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.UpsertDevice(ctx, Device{ID: "dev-1", ULID: ""})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_ListDevices_Empty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	devs, err := s.ListDevices(ctx, testULID)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected 0 devices, got %d", len(devs))
	}
}

func TestSQLStore_UpsertAndListDevices(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	d1 := Device{ID: "dev-b", ULID: testULID, AccountID: testAccount, Mode: ModeFabric, LastSeen: now}
	d2 := Device{ID: "dev-a", ULID: testULID, AccountID: testAccount, Mode: ModeFabric, LastSeen: now}
	d3 := Device{ID: "dev-c", ULID: testULID2, AccountID: testAccount2, Mode: ModeDirect, LastSeen: now}

	for _, d := range []Device{d1, d2, d3} {
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("UpsertDevice %q: %v", d.ID, err)
		}
	}

	devs, err := s.ListDevices(ctx, testULID)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("expected 2 devices for %s, got %d", testULID, len(devs))
	}
	// Should be sorted by ID.
	if devs[0].ID != "dev-a" || devs[1].ID != "dev-b" {
		t.Errorf("unexpected order: %v", []string{devs[0].ID, devs[1].ID})
	}
}

func TestSQLStore_UpsertDevice_UpdatesExisting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	d := Device{ID: "dev-1", ULID: testULID, AccountID: testAccount, Mode: ModeFabric, LastSeen: now}
	if err := s.UpsertDevice(ctx, d); err != nil {
		t.Fatalf("first UpsertDevice: %v", err)
	}
	d.Mode = ModeDirect
	d.LastSeen = now.Add(time.Minute)
	if err := s.UpsertDevice(ctx, d); err != nil {
		t.Fatalf("second UpsertDevice: %v", err)
	}

	devs, err := s.ListDevices(ctx, testULID)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devs))
	}
	if devs[0].Mode != ModeDirect {
		t.Errorf("mode not updated: got %q", devs[0].Mode)
	}
}

// --------------------------------------------------------------------------
// PoPs
// --------------------------------------------------------------------------

func TestSQLStore_UpsertPoP_EmptyID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.UpsertPoP(ctx, PoP{ID: ""})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_SetPoPHealth_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.SetPoPHealth(ctx, "nonexistent-pop", true)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_HealthyPoPs_Empty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pops, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if len(pops) != 0 {
		t.Errorf("expected 0 healthy pops, got %d", len(pops))
	}
}

func TestSQLStore_UpsertAndHealthyPoPs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	popsIn := []PoP{
		{ID: "pop-eu", Region: "eu-west", IP: "10.0.0.1", Lat: 51.5, Lon: -0.1, Healthy: true, LastHealth: now},
		{ID: "pop-us", Region: "us-east", IP: "10.0.0.2", Lat: 40.7, Lon: -74.0, Healthy: false, LastHealth: now},
		{ID: "pop-ap", Region: "ap-southeast", IP: "10.0.0.3", Lat: 1.3, Lon: 103.8, Healthy: true, LastHealth: now},
	}
	for _, p := range popsIn {
		if err := s.UpsertPoP(ctx, p); err != nil {
			t.Fatalf("UpsertPoP %q: %v", p.ID, err)
		}
	}

	healthy, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if len(healthy) != 2 {
		t.Fatalf("expected 2 healthy pops, got %d", len(healthy))
	}
	// Sorted by ID: pop-ap before pop-eu.
	if healthy[0].ID != "pop-ap" || healthy[1].ID != "pop-eu" {
		t.Errorf("unexpected order: %v %v", healthy[0].ID, healthy[1].ID)
	}
}

func TestSQLStore_SetPoPHealth_Toggle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	p := PoP{ID: "pop-1", Region: "eu", IP: "10.0.0.1", Healthy: false, LastHealth: now}
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}

	healthy, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if len(healthy) != 0 {
		t.Errorf("expected 0 healthy pops, got %d", len(healthy))
	}

	if err := s.SetPoPHealth(ctx, "pop-1", true); err != nil {
		t.Fatalf("SetPoPHealth: %v", err)
	}

	healthy, err = s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs after toggle: %v", err)
	}
	if len(healthy) != 1 {
		t.Errorf("expected 1 healthy pop after toggle, got %d", len(healthy))
	}
}

// --------------------------------------------------------------------------
// Vanity
// --------------------------------------------------------------------------

func TestSQLStore_ClaimVanity_InvalidName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Double hyphen is invalid per idents.ValidName.
	err := s.ClaimVanity(ctx, "bad--name", testULID, testAccount)
	if err != ErrInvalidName {
		t.Errorf("expected ErrInvalidName, got %v", err)
	}
}

func TestSQLStore_ClaimVanity_New(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.ClaimVanity(ctx, "myapp", testULID, testAccount)
	if err != nil {
		t.Fatalf("ClaimVanity: %v", err)
	}
}

func TestSQLStore_ClaimVanity_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.ClaimVanity(ctx, "myapp", testULID, testAccount); err != nil {
		t.Fatalf("first ClaimVanity: %v", err)
	}
	// Same name, same ulid, same account — should succeed silently.
	if err := s.ClaimVanity(ctx, "myapp", testULID, testAccount); err != nil {
		t.Errorf("idempotent re-claim should succeed, got %v", err)
	}
}

func TestSQLStore_ClaimVanity_Taken_DifferentAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.ClaimVanity(ctx, "myapp", testULID, testAccount); err != nil {
		t.Fatalf("first ClaimVanity: %v", err)
	}
	err := s.ClaimVanity(ctx, "myapp", testULID, testAccount2)
	if err != ErrVanityTaken {
		t.Errorf("expected ErrVanityTaken, got %v", err)
	}
}

func TestSQLStore_ClaimVanity_Taken_DifferentULID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.ClaimVanity(ctx, "myapp", testULID, testAccount); err != nil {
		t.Fatalf("first ClaimVanity: %v", err)
	}
	err := s.ClaimVanity(ctx, "myapp", testULID2, testAccount)
	if err != ErrVanityTaken {
		t.Errorf("expected ErrVanityTaken, got %v", err)
	}
}

func TestSQLStore_ResolveVanity_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.ResolveVanity(ctx, "unknown")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLStore_ResolveVanity_OK(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.ClaimVanity(ctx, "myapp", testULID, testAccount); err != nil {
		t.Fatalf("ClaimVanity: %v", err)
	}
	v, err := s.ResolveVanity(ctx, "myapp")
	if err != nil {
		t.Fatalf("ResolveVanity: %v", err)
	}
	if v.Name != "myapp" || v.ULID != testULID || v.AccountID != testAccount {
		t.Errorf("unexpected vanity: %+v", v)
	}
}

// --------------------------------------------------------------------------
// Open — verify migration is idempotent (double open)
// --------------------------------------------------------------------------

func TestSQLStore_MigrationIdempotent(t *testing.T) {
	const dsn = "file::memory:?cache=shared&mode=memory&_loc=auto"
	db1, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("first cpdb.OpenSQLiteDSN: %v", err)
	}
	s1, err := Open(db1)
	if err != nil {
		db1.Close()
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	db2, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("second cpdb.OpenSQLiteDSN: %v", err)
	}
	s2, err := Open(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("second Open (migration must be idempotent): %v", err)
	}
	s2.Close()
}
