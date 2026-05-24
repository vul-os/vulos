// Tests for the storagemode package — covers the four acceptance criteria
// for STORE-LOCAL-01:
//
//  1. Mode is selectable and persists across re-open.
//  2. The default mode is central-tigris (empty store, missing row).
//  3. local-minio-sync produces the env/config the co-located mail + office
//     services consume — VULOS_STORAGE_MODE + the four VULOS_MINIO_* vars.
//  4. The default-mode path is unaffected (no MinIO vars emitted) and the
//     legacy storage.yaml continues to provide credentials.
package storagemode_test

import (
	"path/filepath"
	"testing"

	"vulos/backend/internal/storagemode"
)

// openTmp opens a fresh store under t.TempDir().
func openTmp(t *testing.T) *storagemode.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "storagemode.db")
	s, err := storagemode.Open(dbPath)
	if err != nil {
		t.Fatalf("storagemode.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ─── AC 2: defaults ─────────────────────────────────────────────────────────

func TestDefault_FreshStore_IsCentralTigris(t *testing.T) {
	s := openTmp(t)

	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get on fresh store: %v", err)
	}
	if cfg.Mode != storagemode.ModeCentralTigris {
		t.Fatalf("default mode: got %q, want %q", cfg.Mode, storagemode.ModeCentralTigris)
	}
	if cfg.MinIOEndpoint != "" || cfg.MinIOBucket != "" {
		t.Fatalf("default mode must not have MinIO fields populated; got %+v", cfg)
	}
}

func TestDefaults_HelperReturnsCentralTigris(t *testing.T) {
	d := storagemode.Defaults()
	if d.Mode != storagemode.ModeCentralTigris {
		t.Fatalf("Defaults().Mode = %q, want %q", d.Mode, storagemode.ModeCentralTigris)
	}
}

func TestModeValid(t *testing.T) {
	cases := []struct {
		m    storagemode.Mode
		want bool
	}{
		{storagemode.ModeCentralTigris, true},
		{storagemode.ModeLocalMinIOSync, true},
		{"", false},
		{"unknown", false},
		{"local", false}, // close-but-wrong should not accidentally validate
	}
	for _, c := range cases {
		if got := c.m.Valid(); got != c.want {
			t.Errorf("Mode(%q).Valid() = %v, want %v", c.m, got, c.want)
		}
	}
}

// ─── AC 1: mode selectable and persists ─────────────────────────────────────

func TestSet_PersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "storagemode.db")

	s1, err := storagemode.Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	want := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIORegion:   "us-east-1",
		MinIOBucket:   "vulos-bundle",
		MinIOCredsRef: "/var/lib/vulos/minio/.minio_secret",
	}
	if err := s1.Set(want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_ = s1.Close()

	s2, err := storagemode.Open(dbPath)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()
	got, err := s2.Get()
	if err != nil {
		t.Fatalf("Get after re-open: %v", err)
	}
	if got != want {
		t.Fatalf("after re-open: got %+v, want %+v", got, want)
	}
}

func TestSet_SingleRowUpsert(t *testing.T) {
	s := openTmp(t)

	// Setting twice must update the same row, not append new ones — Get must
	// always return the most recent value.
	if err := s.Set(storagemode.Config{Mode: storagemode.ModeCentralTigris}); err != nil {
		t.Fatalf("Set #1: %v", err)
	}
	second := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://10.0.0.5:9000",
		MinIOBucket:   "node-b",
	}
	if err := s.Set(second); err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mode != second.Mode || got.MinIOEndpoint != second.MinIOEndpoint || got.MinIOBucket != second.MinIOBucket {
		t.Fatalf("upsert lost the update: got %+v, want %+v", got, second)
	}
}

func TestSet_FlipBackToTigrisDoesNotWipeMinioFields(t *testing.T) {
	// Switching back to central-tigris must keep the MinIO endpoint+bucket
	// values on disk so a later flip to local-minio-sync doesn't lose them.
	s := openTmp(t)

	full := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIOBucket:   "vulos-bundle",
		MinIORegion:   "auto",
		MinIOCredsRef: "/var/lib/vulos/minio/.minio_secret",
	}
	if err := s.Set(full); err != nil {
		t.Fatalf("Set local-minio-sync: %v", err)
	}
	// Flip back, supplying the same MinIO bits so they aren't lost. (Callers
	// are expected to preserve previous values on a mode-only toggle; the
	// store does not auto-merge.)
	back := full
	back.Mode = storagemode.ModeCentralTigris
	if err := s.Set(back); err != nil {
		t.Fatalf("Set central-tigris with merged values: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mode != storagemode.ModeCentralTigris {
		t.Fatalf("expected central-tigris mode after flip-back, got %q", got.Mode)
	}
	if got.MinIOEndpoint != full.MinIOEndpoint || got.MinIOBucket != full.MinIOBucket {
		t.Fatalf("MinIO fields were dropped on flip-back: got %+v", got)
	}
}

// ─── Validation ─────────────────────────────────────────────────────────────

func TestSet_RejectsInvalidMode(t *testing.T) {
	s := openTmp(t)
	err := s.Set(storagemode.Config{Mode: "garbage"})
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestSet_LocalMinioSyncRequiresEndpointAndBucket(t *testing.T) {
	s := openTmp(t)

	// Missing endpoint.
	if err := s.Set(storagemode.Config{
		Mode:        storagemode.ModeLocalMinIOSync,
		MinIOBucket: "vulos-bundle",
	}); err == nil {
		t.Error("expected error when local-minio-sync has no endpoint")
	}
	// Missing bucket.
	if err := s.Set(storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
	}); err == nil {
		t.Error("expected error when local-minio-sync has no bucket")
	}
}

func TestSet_LocalMinioSyncDefaultsRegionToAuto(t *testing.T) {
	s := openTmp(t)
	if err := s.Set(storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIOBucket:   "vulos-bundle",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MinIORegion != "auto" {
		t.Fatalf("region default: got %q, want %q", got.MinIORegion, "auto")
	}
}

// ─── AC 3: env produced for co-located services ─────────────────────────────

func TestEnvFor_LocalMinioSync_ContainsAllRequiredVars(t *testing.T) {
	cfg := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIORegion:   "auto",
		MinIOBucket:   "vulos-bundle",
		MinIOCredsRef: "/var/lib/vulos/minio/.minio_secret",
	}
	env := storagemode.EnvFor(cfg)

	want := map[string]string{
		"VULOS_STORAGE_MODE":    "local-minio-sync",
		"VULOS_MINIO_ENDPOINT":  "http://127.0.0.1:9000",
		"VULOS_MINIO_REGION":    "auto",
		"VULOS_MINIO_BUCKET":    "vulos-bundle",
		"VULOS_MINIO_CREDS_REF": "/var/lib/vulos/minio/.minio_secret",
	}
	for k, v := range want {
		if got := env[k]; got != v {
			t.Errorf("env[%q] = %q, want %q", k, got, v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env has extra keys: got %d, want %d (%v)", len(env), len(want), env)
	}
}

func TestEnvSlice_LocalMinioSync_DeterministicOrder(t *testing.T) {
	cfg := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIORegion:   "auto",
		MinIOBucket:   "vulos-bundle",
		MinIOCredsRef: "/var/lib/vulos/minio/.minio_secret",
	}
	want := []string{
		"VULOS_MINIO_BUCKET=vulos-bundle",
		"VULOS_MINIO_CREDS_REF=/var/lib/vulos/minio/.minio_secret",
		"VULOS_MINIO_ENDPOINT=http://127.0.0.1:9000",
		"VULOS_MINIO_REGION=auto",
		"VULOS_STORAGE_MODE=local-minio-sync",
	}
	got := storagemode.EnvSlice(cfg)
	if len(got) != len(want) {
		t.Fatalf("EnvSlice length: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("EnvSlice[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ─── AC 4: default-mode path unaffected ─────────────────────────────────────

func TestEnvFor_CentralTigris_OnlyEmitsMode(t *testing.T) {
	// FROZEN invariant: default mode (central Tigris direct) must be
	// completely unchanged. Specifically: NO VULOS_MINIO_* env vars must be
	// produced, so co-located services keep reading the existing
	// /etc/vulos/storage.yaml for Tigris credentials.
	cfg := storagemode.Defaults()
	env := storagemode.EnvFor(cfg)

	if got := env["VULOS_STORAGE_MODE"]; got != string(storagemode.ModeCentralTigris) {
		t.Errorf("VULOS_STORAGE_MODE = %q, want %q", got, storagemode.ModeCentralTigris)
	}
	for k := range env {
		if k != "VULOS_STORAGE_MODE" {
			t.Errorf("central-tigris env must only contain VULOS_STORAGE_MODE; found extra %q=%q",
				k, env[k])
		}
	}
}

func TestEnvFor_CentralTigris_IgnoresStaleMinioFields(t *testing.T) {
	// If a user flipped to local-minio-sync and then back to central-tigris,
	// the MinIO fields remain on disk. EnvFor must still emit only the mode
	// — otherwise mail/office would see endpoints they shouldn't use.
	cfg := storagemode.Config{
		Mode:          storagemode.ModeCentralTigris,
		MinIOEndpoint: "http://leftover:9000",
		MinIOBucket:   "leftover",
	}
	env := storagemode.EnvFor(cfg)
	if _, ok := env["VULOS_MINIO_ENDPOINT"]; ok {
		t.Error("central-tigris mode must not emit VULOS_MINIO_ENDPOINT even when stale field is set")
	}
	if _, ok := env["VULOS_MINIO_BUCKET"]; ok {
		t.Error("central-tigris mode must not emit VULOS_MINIO_BUCKET even when stale field is set")
	}
}

// ─── Misc ───────────────────────────────────────────────────────────────────

func TestOpen_CreatesMissingParentDir(t *testing.T) {
	// Open must create the parent directory so callers don't need a separate
	// mkdir step on first boot.
	dir := filepath.Join(t.TempDir(), "nested", "deeper", "db")
	s, err := storagemode.Open(filepath.Join(dir, "storagemode.db"))
	if err != nil {
		t.Fatalf("Open with missing parents: %v", err)
	}
	defer s.Close()
	if _, err := s.Get(); err != nil {
		t.Fatalf("Get on freshly-opened store: %v", err)
	}
}

func TestSet_TrimsWhitespace(t *testing.T) {
	s := openTmp(t)
	if err := s.Set(storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "  http://127.0.0.1:9000  ",
		MinIOBucket:   "  vulos-bundle  ",
		MinIOCredsRef: "  /var/lib/vulos/minio/.minio_secret  ",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MinIOEndpoint != "http://127.0.0.1:9000" || got.MinIOBucket != "vulos-bundle" {
		t.Fatalf("expected trimmed values, got %+v", got)
	}
}
