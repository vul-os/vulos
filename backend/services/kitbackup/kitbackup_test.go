package kitbackup_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/kitbackup"
)

// makeHome creates a temporary home directory tree and returns its path.
func makeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0755); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// IK11_TestBuildKit_NoFiles verifies that BuildKit still succeeds (hostname
// fallback) when neither instance.json nor storage.json exist.
func IK11_TestBuildKit_NoFiles(t *testing.T) {
	home := makeHome(t)
	kit, err := kitbackup.BuildKit(home)
	if err != nil {
		t.Fatalf("expected no error with hostname fallback, got: %v", err)
	}
	if kit.InstanceID != "" {
		t.Errorf("expected empty InstanceID, got %q", kit.InstanceID)
	}
	if kit.Hostname == "" {
		t.Error("expected non-empty Hostname from OS fallback")
	}
	if kit.SchemaVersion != kitbackup.SchemaVersion {
		t.Errorf("SchemaVersion mismatch: got %d", kit.SchemaVersion)
	}
}

// IK11_TestBuildKit_WithInstance verifies that InstanceID and Hostname are
// read from instance.json correctly.
func IK11_TestBuildKit_WithInstance(t *testing.T) {
	home := makeHome(t)
	dbDir := filepath.Join(home, "db")

	writeJSON(t, filepath.Join(dbDir, "instance.json"), map[string]string{
		"ulid":     "01HV2M5X7QABCDEF",
		"id":       "fallback-id",
		"hostname": "my-device",
	})

	kit, err := kitbackup.BuildKit(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kit.InstanceID != "01HV2M5X7QABCDEF" {
		t.Errorf("expected ULID as InstanceID, got %q", kit.InstanceID)
	}
	if kit.Hostname != "my-device" {
		t.Errorf("expected hostname from instance.json, got %q", kit.Hostname)
	}
}

// IK11_TestBuildKit_IDFallback verifies that "id" field is used when "ulid"
// is absent in instance.json.
func IK11_TestBuildKit_IDFallback(t *testing.T) {
	home := makeHome(t)
	dbDir := filepath.Join(home, "db")

	writeJSON(t, filepath.Join(dbDir, "instance.json"), map[string]string{
		"id":       "only-id-no-ulid",
		"hostname": "boxname",
	})

	kit, err := kitbackup.BuildKit(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kit.InstanceID != "only-id-no-ulid" {
		t.Errorf("expected id field as InstanceID, got %q", kit.InstanceID)
	}
}

// IK11_TestBuildKit_WithStorage verifies that storage credentials are
// included when storage.json is present.
func IK11_TestBuildKit_WithStorage(t *testing.T) {
	home := makeHome(t)
	dbDir := filepath.Join(home, "db")

	writeJSON(t, filepath.Join(dbDir, "instance.json"), map[string]string{
		"ulid":     "01TEST",
		"hostname": "testhost",
	})
	writeJSON(t, filepath.Join(dbDir, "storage.json"), map[string]string{
		"endpoint":   "s3.example.com",
		"bucket":     "my-bucket",
		"region":     "us-east-1",
		"access_key": "AKIAIOSFODNN7EXAMPLE",
		"secret_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	})

	kit, err := kitbackup.BuildKit(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kit.StorageEndpoint != "s3.example.com" {
		t.Errorf("wrong endpoint: %q", kit.StorageEndpoint)
	}
	if kit.StorageBucket != "my-bucket" {
		t.Errorf("wrong bucket: %q", kit.StorageBucket)
	}
	if kit.StorageAccessKey != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("wrong access key: %q", kit.StorageAccessKey)
	}
	if kit.StorageSecretKey != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" {
		t.Errorf("wrong secret key: %q", kit.StorageSecretKey)
	}
}

// IK11_TestBuildKit_ExcludesSecrets verifies that the kit JSON does NOT
// contain the words passphrase, password, session, or token.
func IK11_TestBuildKit_ExcludesSecrets(t *testing.T) {
	home := makeHome(t)
	dbDir := filepath.Join(home, "db")

	writeJSON(t, filepath.Join(dbDir, "instance.json"), map[string]string{
		"ulid":       "01TEST",
		"hostname":   "testhost",
		"passphrase": "should-never-appear",
		"password":   "also-never",
		"session":    "nope",
	})

	kit, err := kitbackup.BuildKit(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := json.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(raw))
	for _, forbidden := range []string{"passphrase", "password_hash", "session_token"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("kit JSON contains forbidden field %q: %s", forbidden, s)
		}
	}
}

// IK11_TestBuildKit_IssuedAt verifies that IssuedAt is set to a recent time.
func IK11_TestBuildKit_IssuedAt(t *testing.T) {
	home := makeHome(t)
	writeJSON(t, filepath.Join(home, "db", "instance.json"), map[string]string{
		"ulid":     "01TIMETEST",
		"hostname": "timehost",
	})

	before := time.Now().Add(-time.Second)
	kit, err := kitbackup.BuildKit(home)
	after := time.Now().Add(time.Second)

	if err != nil {
		t.Fatal(err)
	}
	if kit.IssuedAt.Before(before) || kit.IssuedAt.After(after) {
		t.Errorf("IssuedAt %v out of expected range [%v, %v]", kit.IssuedAt, before, after)
	}
}

// TestKitBackup is the top-level test that runs all subtests.
func TestKitBackup(t *testing.T) {
	t.Run("NoFiles", IK11_TestBuildKit_NoFiles)
	t.Run("WithInstance", IK11_TestBuildKit_WithInstance)
	t.Run("IDFallback", IK11_TestBuildKit_IDFallback)
	t.Run("WithStorage", IK11_TestBuildKit_WithStorage)
	t.Run("ExcludesSecrets", IK11_TestBuildKit_ExcludesSecrets)
	t.Run("IssuedAt", IK11_TestBuildKit_IssuedAt)
}
