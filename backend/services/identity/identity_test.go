package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFirstBoot(t *testing.T) {
	home := t.TempDir()
	inst, err := Load(home)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if inst.ULID == "" {
		t.Error("expected non-empty ULID")
	}
	if inst.Hostname == "" {
		t.Error("expected non-empty Hostname")
	}
	if inst.FirstBoot == "" {
		t.Error("expected non-empty FirstBoot")
	}
}

func TestLoadIdempotent(t *testing.T) {
	home := t.TempDir()
	first, err := Load(home)
	if err != nil {
		t.Fatalf("first Load() error: %v", err)
	}
	second, err := Load(home)
	if err != nil {
		t.Fatalf("second Load() error: %v", err)
	}
	if first.ULID != second.ULID {
		t.Errorf("ULID changed: %q vs %q", first.ULID, second.ULID)
	}
	if first.Hostname != second.Hostname {
		t.Errorf("Hostname changed: %q vs %q", first.Hostname, second.Hostname)
	}
	if first.FirstBoot != second.FirstBoot {
		t.Errorf("FirstBoot changed: %q vs %q", first.FirstBoot, second.FirstBoot)
	}
}

func TestLoadPersistFile(t *testing.T) {
	home := t.TempDir()
	inst, _ := Load(home)

	// Verify file exists at the expected path with mode 0600.
	path := filepath.Join(home, ".vulos", "db", "instance.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("instance.json not found: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected mode 0600, got %v", info.Mode().Perm())
	}

	// Verify JSON content.
	data, _ := os.ReadFile(path)
	var got Instance
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ULID != inst.ULID {
		t.Errorf("ULID mismatch: %q vs %q", got.ULID, inst.ULID)
	}
}

func TestLoadLegacyMigration(t *testing.T) {
	home := t.TempDir()
	// Write a legacy instance-id file.
	vulosDir := filepath.Join(home, ".vulos")
	os.MkdirAll(vulosDir, 0755)
	legacyID := "01HZXXXXXXXXXXXXXXXXXX"
	os.WriteFile(filepath.Join(vulosDir, "instance-id"), []byte(legacyID+"\n"), 0600)

	inst, err := Load(home)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if inst.ULID != legacyID {
		t.Errorf("expected legacy ULID %q, got %q", legacyID, inst.ULID)
	}
	// The new path should now exist.
	newPath := filepath.Join(home, ".vulos", "db", "instance.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Error("expected migrated instance.json to exist")
	}
	// Second load must return the same ULID (idempotent after migration).
	second, _ := Load(home)
	if second.ULID != legacyID {
		t.Errorf("expected same ULID after migration, got %q", second.ULID)
	}
}

func TestSave(t *testing.T) {
	home := t.TempDir()
	inst := &Instance{ULID: "TESTULIDVALUE", Hostname: "vulos-test", FirstBoot: "2024-01-01T00:00:00Z"}
	if err := Save(inst, home); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	path := filepath.Join(home, ".vulos", "db", "instance.json")
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "TESTULIDVALUE") {
		t.Error("saved file does not contain ULID")
	}
}

func TestAutoHostname(t *testing.T) {
	h := autoHostname("01ABCDEFGHIJKLMNOPQRSTUVWX")
	if !strings.HasPrefix(h, "vulos-") {
		t.Errorf("expected vulos- prefix, got %q", h)
	}
}

func TestLooksLikeContainerID(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"a1b2c3d4e5f6", true},
		{"localhost", false},
		{"mydevice", false},
		{"000000000000", true},
		{"A1B2C3D4E5F6", false}, // uppercase — not a container ID
	}
	for _, c := range cases {
		if got := looksLikeContainerID(c.s); got != c.want {
			t.Errorf("looksLikeContainerID(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
