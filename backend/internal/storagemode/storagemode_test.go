// Tests for the storagemode package — covers the four acceptance criteria
// for STORE-LOCAL-01, restated for D-STORE-LOCAL-DEFAULT:
//
//  1. Mode is selectable and persists across re-open.
//  2. The default mode on a NEW box is local-fs (empty store, missing row),
//     and a box that pre-dates that change stays on central-tigris.
//  3. local-minio-sync produces the env/config the co-located mail + office
//     services consume — VULOS_STORAGE_MODE + the four VULOS_MINIO_* vars.
//  4. The central-tigris path is unaffected (no MinIO vars emitted) and the
//     legacy storage.yaml continues to provide credentials.
package storagemode_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/internal/storagemode"

	_ "modernc.org/sqlite" // raw access for the upgrade-shape helper
)

// openTmp opens a fresh store under t.TempDir(), with legacy detection pointed
// at a path that does NOT exist — so the result is a function of the test, not
// of whatever /etc/vulos/storage.yaml happens to be on the machine running it.
func openTmp(t *testing.T) *storagemode.Store {
	t.Helper()
	dir := t.TempDir()
	return openIn(t, filepath.Join(dir, "storagemode.db"), filepath.Join(dir, "no-such-storage.yaml"))
}

func openIn(t *testing.T, dbPath, legacyPath string) *storagemode.Store {
	t.Helper()
	s, err := storagemode.OpenWithOptions(storagemode.Options{
		DBPath:              dbPath,
		LegacyStorageConfig: legacyPath,
		Quiet:               true,
	})
	if err != nil {
		t.Fatalf("storagemode.OpenWithOptions(%s): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// clearRows empties the storagemode table, reproducing the on-disk shape of a
// store created by a build that had no pin step (schema present, zero rows).
func clearRows(t *testing.T, dbPath, legacyPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM storagemode`); err != nil {
		t.Fatalf("clear rows: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM storagemode`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("clearRows left %d rows — the upgrade case would not be exercised", n)
	}
	_ = legacyPath
}

// writeLegacy writes a bundle storage.yaml declaring backend and returns its path.
func writeLegacy(t *testing.T, dir, backend string) string {
	t.Helper()
	p := filepath.Join(dir, "storage.yaml")
	body := "# /etc/vulos/storage.yaml — shared S3 storage selector\n\nbackend: \"" + backend + "\"\n\ntigris:\n  bucket: \"x\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	return p
}

// ─── AC 2: defaults ─────────────────────────────────────────────────────────

func TestDefault_FreshStore_IsLocalFS(t *testing.T) {
	s := openTmp(t)

	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get on fresh store: %v", err)
	}
	if cfg.Mode != storagemode.ModeLocalFS {
		t.Fatalf("default mode: got %q, want %q", cfg.Mode, storagemode.ModeLocalFS)
	}
	if cfg.Mode.Hosted() {
		t.Fatalf("the DEFAULT mode must not be a hosted third-party service; got %q", cfg.Mode)
	}
	if cfg.MinIOEndpoint != "" || cfg.MinIOBucket != "" {
		t.Fatalf("default mode must not have MinIO fields populated; got %+v", cfg)
	}
}

func TestDefaults_HelperReturnsLocalFS(t *testing.T) {
	d := storagemode.Defaults()
	if d.Mode != storagemode.ModeLocalFS {
		t.Fatalf("Defaults().Mode = %q, want %q", d.Mode, storagemode.ModeLocalFS)
	}
	if storagemode.DefaultMode.Hosted() {
		t.Fatalf("DefaultMode %q is hosted — the default must be self-contained", storagemode.DefaultMode)
	}
}

func TestHosted_OnlyTigrisIsHosted(t *testing.T) {
	if !storagemode.ModeCentralTigris.Hosted() {
		t.Error("central-tigris must report Hosted()==true")
	}
	if storagemode.ModeLocalFS.Hosted() || storagemode.ModeLocalMinIOSync.Hosted() {
		t.Error("local modes must report Hosted()==false")
	}
}

func TestModeValid(t *testing.T) {
	cases := []struct {
		m    storagemode.Mode
		want bool
	}{
		{storagemode.ModeLocalFS, true},
		{storagemode.ModeCentralTigris, true},
		{storagemode.ModeLocalMinIOSync, true},
		{"", false},
		{"unknown", false},
		{"local", false},    // close-but-wrong should not accidentally validate
		{"localfs", false},  // ditto
		{"local_fs", false}, // ditto
	}
	for _, c := range cases {
		if got := c.m.Valid(); got != c.want {
			t.Errorf("Mode(%q).Valid() = %v, want %v", c.m, got, c.want)
		}
	}
}

// ─── (b) DO NOT BREAK EXISTING INSTALLS ─────────────────────────────────────

func TestUpgrade_PreExistingDBWithNoSelection_StaysOnTigris(t *testing.T) {
	// A box installed before local-fs became the default: storagemode.db
	// exists (the old build created it) but the operator never chose a mode,
	// so they were running the old implicit default. Opening with the new
	// build must NOT move them to local storage.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "storagemode.db")
	legacy := filepath.Join(dir, "no-such-storage.yaml")

	// Simulate the old build: a db file with the schema and no row.
	old := openIn(t, dbPath, legacy)
	if _, err := old.Get(); err != nil {
		t.Fatalf("seed Get: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected a db file to exist after open: %v", err)
	}
	_ = old.Close()
	// Remove the pin the new build wrote, leaving exactly the old-build shape:
	// schema present, zero rows, file on disk.
	clearRows(t, dbPath, legacy)

	upgraded := openIn(t, dbPath, legacy)
	cfg, err := upgraded.Get()
	if err != nil {
		t.Fatalf("Get after upgrade: %v", err)
	}
	if cfg.Mode != storagemode.ModeCentralTigris {
		t.Fatalf("upgrading an existing install silently repointed it: got %q, want %q",
			cfg.Mode, storagemode.ModeCentralTigris)
	}
	mode, why := upgraded.DefaultOrigin()
	if mode != storagemode.ModeCentralTigris || why == "" {
		t.Fatalf("DefaultOrigin must explain the pin; got (%q, %q)", mode, why)
	}
}

func TestUpgrade_LegacyHostedStorageYAML_StaysOnTigris(t *testing.T) {
	// The other existing-install shape: the box has /etc/vulos/storage.yaml
	// with backend: "tigris" but no storagemode.db at all (the selector post-
	// dates their install). It must come up hosted, not local.
	dir := t.TempDir()
	legacy := writeLegacy(t, dir, "tigris")

	s := openIn(t, filepath.Join(dir, "storagemode.db"), legacy)
	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Mode != storagemode.ModeCentralTigris {
		t.Fatalf("box with hosted storage.yaml came up as %q, want %q — that would strand its data",
			cfg.Mode, storagemode.ModeCentralTigris)
	}
}

func TestUpgrade_LegacyLocalStorageYAML_GetsLocalDefault(t *testing.T) {
	for _, backend := range []string{"minio", "local"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			legacy := writeLegacy(t, dir, backend)
			s := openIn(t, filepath.Join(dir, "storagemode.db"), legacy)
			cfg, err := s.Get()
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if cfg.Mode != storagemode.ModeLocalFS {
				t.Fatalf("backend=%q box: got %q, want %q", backend, cfg.Mode, storagemode.ModeLocalFS)
			}
		})
	}
}

func TestUpgrade_ExplicitSelectionIsNeverRewritten(t *testing.T) {
	// An operator who explicitly chose a mode keeps it, whatever the defaults
	// and whatever the legacy file says.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "storagemode.db")
	legacy := writeLegacy(t, dir, "tigris")

	first := openIn(t, dbPath, legacy)
	want := storagemode.Config{
		Mode:          storagemode.ModeLocalMinIOSync,
		MinIOEndpoint: "http://127.0.0.1:9000",
		MinIORegion:   "auto",
		MinIOBucket:   "vulos-bundle",
	}
	if err := first.Set(want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_ = first.Close()

	again := openIn(t, dbPath, legacy)
	got, err := again.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("re-open rewrote an explicit selection: got %+v, want %+v", got, want)
	}
}

func TestEffectiveDefault_Evidence(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		path  string
		want  storagemode.Mode
		setup func() string
	}{
		{name: "missing file", path: filepath.Join(dir, "absent.yaml"), want: storagemode.ModeLocalFS},
		{name: "hosted", setup: func() string { return writeLegacy(t, t.TempDir(), "tigris") }, want: storagemode.ModeCentralTigris},
		{name: "unknown backend is treated as remote", setup: func() string { return writeLegacy(t, t.TempDir(), "s3") }, want: storagemode.ModeCentralTigris},
		{name: "minio", setup: func() string { return writeLegacy(t, t.TempDir(), "minio") }, want: storagemode.ModeLocalFS},
		{name: "empty file", setup: func() string {
			p := filepath.Join(t.TempDir(), "storage.yaml")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, want: storagemode.ModeLocalFS},
		{name: "nested backend key is not top-level", setup: func() string {
			p := filepath.Join(t.TempDir(), "storage.yaml")
			if err := os.WriteFile(p, []byte("store:\n  backend: \"tigris\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, want: storagemode.ModeLocalFS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.path
			if c.setup != nil {
				p = c.setup()
			}
			got, why := storagemode.EffectiveDefault(p)
			if got != c.want {
				t.Fatalf("EffectiveDefault(%s) = %q, want %q", p, got, c.want)
			}
			if why == "" {
				t.Fatal("EffectiveDefault must always state its evidence")
			}
		})
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

// ─── AC 4: central-tigris path unaffected ───────────────────────────────────

func TestEnvFor_CentralTigris_OnlyEmitsMode(t *testing.T) {
	// FROZEN invariant: the central-Tigris-direct path must be completely
	// unchanged for the installs that are on it. Specifically: NO
	// VULOS_MINIO_* env vars must be produced, so co-located services keep
	// reading the existing /etc/vulos/storage.yaml for Tigris credentials.
	cfg := storagemode.Config{Mode: storagemode.ModeCentralTigris}
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

// ─── The default (local-fs) env contract ────────────────────────────────────

func TestEnvFor_LocalFS_EmitsModeOnlyAndNoEndpoint(t *testing.T) {
	// local-fs is the default, so this is the most-travelled path. It must
	// emit the mode and NOTHING else: any endpoint variable here would point
	// the box at an object store it was never told to use.
	env := storagemode.EnvFor(storagemode.Defaults())

	if got := env["VULOS_STORAGE_MODE"]; got != string(storagemode.ModeLocalFS) {
		t.Errorf("VULOS_STORAGE_MODE = %q, want %q", got, storagemode.ModeLocalFS)
	}
	if len(env) != 1 {
		t.Errorf("local-fs env must be exactly {VULOS_STORAGE_MODE}; got %v", env)
	}
	for _, kv := range storagemode.EnvSlice(storagemode.Defaults()) {
		if kv != "VULOS_STORAGE_MODE="+string(storagemode.ModeLocalFS) {
			t.Errorf("unexpected env entry for the default mode: %q", kv)
		}
	}
}

func TestEnvFor_LocalFS_IgnoresStaleMinioFields(t *testing.T) {
	cfg := storagemode.Config{
		Mode:          storagemode.ModeLocalFS,
		MinIOEndpoint: "http://leftover:9000",
		MinIOBucket:   "leftover",
	}
	env := storagemode.EnvFor(cfg)
	if len(env) != 1 {
		t.Fatalf("local-fs must not emit MinIO vars from stale fields; got %v", env)
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
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "deeper", "db")
	s := openIn(t, filepath.Join(dir, "storagemode.db"), filepath.Join(root, "no-such-storage.yaml"))
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
