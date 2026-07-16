package fleet_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

func openPGFleetStore(t *testing.T) *fleet.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("fleet_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := fleet.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("fleet.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS fleet_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Fleet_CreateOrg(t *testing.T) {
	st := openPGFleetStore(t)
	ctx := context.Background()
	org, err := st.CreateOrg(ctx, "TestOrg", "owner1")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if org.Name != "TestOrg" {
		t.Errorf("name: %q", org.Name)
	}
}

func TestPG_Fleet_AddMember(t *testing.T) {
	st := openPGFleetStore(t)
	ctx := context.Background()
	org, err := st.CreateOrg(ctx, "Org2", "owner2")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := st.AddMember(ctx, org.ID, "member1", "member"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	members, err := st.Members(ctx, org.ID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) < 2 {
		t.Errorf("expected at least 2 members, got %d", len(members))
	}
}

func TestPG_Fleet_UpsertDevice(t *testing.T) {
	st := openPGFleetStore(t)
	ctx := context.Background()
	d := fleet.Device{ULID: "01DEVULID00000000000000001", AccountID: "acc1", Name: "box1", Channel: "stable", Health: "healthy"}
	if err := st.UpsertDevice(ctx, d); err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	got, err := st.GetDevice(ctx, "01DEVULID00000000000000001")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.Name != "box1" {
		t.Errorf("name: %q", got.Name)
	}
}
