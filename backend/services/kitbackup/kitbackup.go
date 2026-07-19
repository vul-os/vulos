// Package kitbackup assembles a re-downloadable Recovery Kit JSON for the
// local Vulos instance. The kit contains only static identity + storage
// credentials — never passphrases, session tokens, or password hashes.
package kitbackup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is incremented whenever the Kit structure changes in a
// backward-incompatible way so consumers can detect schema differences.
const SchemaVersion = 1

// Kit is the JSON-serialisable recovery kit. Fields marked omitempty are
// only present when the corresponding local state file exists.
type Kit struct {
	// Schema version — bump when shape changes.
	SchemaVersion int `json:"schema_version"`

	// InstanceID is the ULID stored in ~/.vulos/db/instance.json.
	InstanceID string `json:"instance_id,omitempty"`

	// Hostname is the OS hostname at build time.
	Hostname string `json:"hostname,omitempty"`

	// StorageEndpoint, StorageBucket, StorageRegion, StorageAccessKey, and
	// StorageSecretKey come from ~/.vulos/db/storage.json when present.
	// Intentionally omitted when storage.json does not exist.
	StorageEndpoint  string `json:"storage_endpoint,omitempty"`
	StorageBucket    string `json:"storage_bucket,omitempty"`
	StorageRegion    string `json:"storage_region,omitempty"`
	StorageAccessKey string `json:"storage_access_key,omitempty"`
	StorageSecretKey string `json:"storage_secret_key,omitempty"`

	// IssuedAt records when this copy of the kit was generated.
	IssuedAt time.Time `json:"issued_at"`
}

// instanceFile is the on-disk shape of ~/.vulos/db/instance.json.
type instanceFile struct {
	ID       string `json:"id"`
	ULID     string `json:"ulid"`
	Hostname string `json:"hostname"`
}

// storageFile is the on-disk shape of ~/.vulos/db/storage.json.
type storageFile struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// BuildKit assembles a Kit from the local state files rooted at home
// (typically the value returned by os.UserHomeDir).
//
// Rules:
//   - instance.json is read if present; its ULID/ID field is used as
//     InstanceID and its hostname field is preferred over os.Hostname().
//   - storage.json is included only when the file exists and is parseable.
//   - Password hashes, passphrases, and session tokens are NEVER included.
func BuildKit(home string) (*Kit, error) {
	dbDir := filepath.Join(home, "db")

	kit := &Kit{
		SchemaVersion: SchemaVersion,
		IssuedAt:      time.Now().UTC(),
	}

	// --- instance.json ---
	instPath := filepath.Join(dbDir, "instance.json")
	if raw, err := os.ReadFile(instPath); err == nil {
		var inst instanceFile
		if err := json.Unmarshal(raw, &inst); err == nil {
			// Prefer explicit "ulid" field, fall back to "id".
			if inst.ULID != "" {
				kit.InstanceID = inst.ULID
			} else if inst.ID != "" {
				kit.InstanceID = inst.ID
			}
			if inst.Hostname != "" {
				kit.Hostname = inst.Hostname
			}
		}
	}

	// Fall back to OS hostname when instance.json does not carry one.
	if kit.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			kit.Hostname = h
		}
	}

	// --- storage.json ---
	storPath := filepath.Join(dbDir, "storage.json")
	if raw, err := os.ReadFile(storPath); err == nil {
		var stor storageFile
		if err := json.Unmarshal(raw, &stor); err == nil {
			kit.StorageEndpoint = stor.Endpoint
			kit.StorageBucket = stor.Bucket
			kit.StorageRegion = stor.Region
			kit.StorageAccessKey = stor.AccessKey
			kit.StorageSecretKey = stor.SecretKey
		}
	}

	// Validate: at minimum we should have something useful.
	if kit.InstanceID == "" && kit.Hostname == "" {
		return nil, fmt.Errorf("kitbackup: no identity found — instance.json missing and hostname unavailable")
	}

	return kit, nil
}
