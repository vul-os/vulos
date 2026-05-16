// Package identity manages the persistent instance ULID and hostname for a Vulos node.
// It reads/writes ~/.vulos/db/instance.json (preferred) and falls back to the
// legacy NET-06 path ~/.vulos/instance-id for migration.
package identity

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Instance holds the persistent identity of this Vulos node.
type Instance struct {
	ULID      string `json:"ulid"`
	Hostname  string `json:"hostname"`
	FirstBoot string `json:"first_boot"` // RFC3339
}

const instanceFile = "instance.json"
const legacyFile = "instance-id"

// Load reads the instance identity from disk, or generates a new one on first boot.
// home is the user home directory (e.g. os.UserHomeDir()).
// Preferred path: <home>/.vulos/db/instance.json
// Fallback path:  <home>/.vulos/instance-id  (NET-06 legacy)
func Load(home string) (*Instance, error) {
	dbDir := filepath.Join(home, ".vulos", "db")
	newPath := filepath.Join(dbDir, instanceFile)
	legacyPath := filepath.Join(home, ".vulos", legacyFile)

	// Try the preferred new path first.
	if inst, err := readJSON(newPath); err == nil {
		return inst, nil
	}

	// Fall back to the legacy NET-06 plain-text file.
	if data, err := os.ReadFile(legacyPath); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			inst := &Instance{
				ULID:      id,
				Hostname:  autoHostname(id),
				FirstBoot: time.Now().UTC().Format(time.RFC3339),
			}
			// Migrate to the new path; best-effort.
			_ = os.MkdirAll(dbDir, 0755)
			_ = Save(inst, home)
			return inst, nil
		}
	}

	// First boot: generate a fresh identity.
	id := newULID()
	hostname := chooseHostname(id)
	inst := &Instance{
		ULID:      id,
		Hostname:  hostname,
		FirstBoot: time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}
	if err := Save(inst, home); err != nil {
		return nil, err
	}
	return inst, nil
}

// Save persists the instance to ~/.vulos/db/instance.json with mode 0600.
func Save(inst *Instance, home string) error {
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dbDir, instanceFile)
	data, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// readJSON reads and unmarshals instance.json. Returns an error if missing/corrupt.
func readJSON(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var inst Instance
	if err := json.Unmarshal(data, &inst); err != nil {
		return nil, err
	}
	if inst.ULID == "" {
		return nil, os.ErrInvalid
	}
	return &inst, nil
}

// newULID generates a new monotonic ULID string.
func newULID() string {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// autoHostname derives a hostname from the last 6 chars of a ULID.
func autoHostname(id string) string {
	if len(id) >= 6 {
		return "vulos-" + strings.ToLower(id[len(id)-6:])
	}
	return "vulos-node"
}

// chooseHostname picks either the OS hostname (if non-trivial) or a derived one.
func chooseHostname(id string) string {
	if h, err := os.Hostname(); err == nil {
		h = strings.TrimSpace(h)
		// Reject generic/container defaults
		if h != "" && h != "localhost" && h != "vula" && !looksLikeContainerID(h) {
			return h
		}
	}
	return autoHostname(id)
}

// looksLikeContainerID returns true for Docker-style 12-char hex names.
func looksLikeContainerID(h string) bool {
	if len(h) != 12 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
