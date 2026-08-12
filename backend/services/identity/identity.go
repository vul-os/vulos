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
	// Region is the home cell of this box (default "eu").
	// Phase-0: only "eu" exists; a second cell is config-only later.
	Region string `json:"region,omitempty"`
}

const instanceFile = "instance.json"
const legacyFile = "instance-id"

// Load reads the instance identity from disk, or generates a new one on first boot.
//
// root is the VULOS DATA ROOT — datadir.Root(), i.e. $VULOS_DATA_DIR or
// ~/.vulos. It is NOT the user's home directory, and the difference is not
// cosmetic: this function joins "db" onto what it is given, so a caller that
// passes os.UserHomeDir() gets ~/db/instance.json, finds nothing there, and
// silently MINTS A NEW IDENTITY in a directory nothing else reads. The instance
// ULID is what peers know this box by, so a second identity is not a cosmetic
// fault — it is a box that has changed who it is.
//
// The parameter used to be called `home` and the comment used to say "the user
// home directory (e.g. os.UserHomeDir())" while promising
// <home>/.vulos/db/instance.json. Both were wrong; only the paths below are
// authoritative. A first-boot e2e test believed the comment, looked in
// <tmp>/.vulos/db/, and had been failing ever since — unnoticed, because that
// suite is `//go:build e2e` and nothing ran it.
//
// Preferred path: <root>/db/instance.json
// Fallback path:  <root>/instance-id  (NET-06 legacy)
func Load(root string) (*Instance, error) {
	dbDir := filepath.Join(root, "db")
	newPath := filepath.Join(dbDir, instanceFile)
	legacyPath := filepath.Join(root, legacyFile)

	// Try the preferred new path first.
	if inst, err := readJSON(newPath); err == nil {
		// Backfill Region for instance.json files written before this field
		// existed (Phase-0: empty → "eu").
		if inst.Region == "" {
			inst.Region = instanceRegion()
		}
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
				Region:    instanceRegion(),
			}
			// Migrate to the new path; best-effort.
			_ = os.MkdirAll(dbDir, 0755)
			_ = Save(inst, root)
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
		Region:    instanceRegion(),
	}
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}
	if err := Save(inst, root); err != nil {
		return nil, err
	}
	return inst, nil
}

// instanceRegion returns the box's home region from the VULOS_REGION env variable,
// defaulting to "eu" for Phase-0 (single-cell deployment).
func instanceRegion() string {
	if v := strings.TrimSpace(os.Getenv("VULOS_REGION")); v != "" {
		return v
	}
	return "eu"
}

// Save persists the instance to ~/.vulos/db/instance.json with mode 0600.
func Save(inst *Instance, root string) error {
	dbDir := filepath.Join(root, "db")
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
		if h != "" && h != "localhost" && h != "vulos" && !looksLikeContainerID(h) {
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
