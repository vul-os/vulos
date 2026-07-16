package ota_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/ota"
)

func openPGOTAStore(t *testing.T) *ota.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("ota_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := ota.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("ota.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ota_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_OTA_InsertRelease(t *testing.T) {
	st := openPGOTAStore(t)
	ctx := context.Background()
	r := ota.Release{
		Version:      "1.0.0",
		Channel:      "stable",
		ArtifactURL:  "https://example.com/v1.0.0.tar.gz",
		Sha256:       "abc123",
		MinFrom:      "0.0.0",
		RolloutPct:   100,
		SignatureB64: "sig",
		DeferMaxSec:  604800,
		CreatedAt:    time.Now(),
	}
	id, err := st.InsertRelease(ctx, r)
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestPG_OTA_HaltRelease(t *testing.T) {
	st := openPGOTAStore(t)
	ctx := context.Background()
	r := ota.Release{Version: "1.0.1", Channel: "stable", ArtifactURL: "https://x.com", Sha256: "abc", MinFrom: "0.0.0", RolloutPct: 100, SignatureB64: "s", DeferMaxSec: 604800, CreatedAt: time.Now()}
	id, err := st.InsertRelease(ctx, r)
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if err := st.HaltRelease(ctx, id); err != nil {
		t.Fatalf("HaltRelease: %v", err)
	}
}

func TestPG_OTA_SetGetPolicy(t *testing.T) {
	st := openPGOTAStore(t)
	ctx := context.Background()
	p := ota.DevicePolicy{ULID: "01TESTULID", Channel: "stable", UpdatedAt: time.Now()}
	if err := st.SetPolicy(ctx, p); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	got, err := st.GetPolicy(ctx, "01TESTULID")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.Channel != "stable" {
		t.Errorf("channel: %q", got.Channel)
	}
}
