// recovery_anchor.go — LIVE plumbing for the account recovery anchor.
//
// # Why this seam exists (trust boundary)
//
// identity_lifecycle.go can already DERIVE a recovery anchor from the account
// recovery seed (DeriveRecoveryAnchor) and build/verify RecoveryCerts. What was
// missing for recovery to work in PRODUCTION (not just as a library) was getting
// a REAL anchor id into the LifecycleStore that the live well-known endpoint
// publishes. The blocker is a deliberate security property:
//
//   - The box's peering identity key is generated independently and is NOT
//     derived from the account recovery mnemonic.
//   - The recovery mnemonic / 64-byte seed is shown to the user once and is
//     NEVER persisted in plaintext on the box (internal/auth/recovery.go).
//
// So at peering-init time the box has no seed and therefore cannot derive the
// anchor. The seam below resolves this WITHOUT weakening the boundary:
//
//   - The recovery seed is in hand transiently exactly twice: when a recovery
//     kit is GENERATED and when an account is RESTORED from a mnemonic. At those
//     moments internal/auth notifies an observer (SetRecoverySeedObserver). The
//     live server's observer derives the anchor and calls PersistRecoveryAnchor.
//   - PersistRecoveryAnchor writes ONLY the anchor's PUBLIC VulaID to disk
//     (recovery_anchor.json). The anchor PRIVATE key — the actual power to sign a
//     RecoveryCert — is discarded immediately and only ever re-derivable from the
//     off-box recovery kit. A box compromise thus yields the public anchor id
//     (harmless) but never the ability to forge a recovery.
//   - On every boot the live server reads the persisted anchor id
//     (LoadRecoveryAnchorID) and seeds the LifecycleStore with it, so the
//     well-known endpoint publishes the anchor and peers TOFU-pin it. Recovery is
//     then performed off-box: the user restores from the mnemonic, re-derives the
//     anchor private key, signs a RecoveryCert binding the lost identity to a new
//     key, and publishes it; pinned peers follow it (ResolveCurrentKey).
package peering

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// recoveryAnchorFile is the on-disk record (under the peering identity dir)
// holding ONLY the public recovery anchor VulaID. No private material.
const recoveryAnchorFile = "recovery_anchor.json"

type recoveryAnchorRecord struct {
	AnchorVulaID string `json:"anchor_vula_id"`
}

// PersistRecoveryAnchor derives the account recovery anchor from recoverySeed and
// persists ONLY its public VulaID under identityDir, returning that public id.
//
// The anchor PRIVATE key is derived, used to compute the public id, and then
// dropped — it is never written to disk (see the trust-boundary note above). Call
// this from the recovery-kit generation / restore path, where the seed is briefly
// available. Idempotent: re-persisting the same anchor is fine; attempting to
// overwrite an existing record with a DIFFERENT anchor fails closed.
func PersistRecoveryAnchor(identityDir string, recoverySeed []byte) (string, error) {
	anchorPriv, anchorVulaID, err := DeriveRecoveryAnchor(recoverySeed)
	if err != nil {
		return "", err
	}
	// Drop the private key immediately; only the public id is retained on the box.
	for i := range anchorPriv {
		anchorPriv[i] = 0
	}

	if err := os.MkdirAll(identityDir, 0o700); err != nil {
		return "", fmt.Errorf("lifecycle: mkdir identity dir: %w", err)
	}
	path := filepath.Join(identityDir, recoveryAnchorFile)

	// Takeover guard: never silently replace an already-recorded anchor.
	if existing := LoadRecoveryAnchorID(identityDir); existing != "" && existing != anchorVulaID {
		return "", errors.New("lifecycle: a different recovery anchor is already persisted; refusing to overwrite")
	}

	rec := recoveryAnchorRecord{AnchorVulaID: anchorVulaID}
	raw, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", fmt.Errorf("lifecycle: write anchor: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("lifecycle: persist anchor: %w", err)
	}
	return anchorVulaID, nil
}

// LoadRecoveryAnchorID reads the persisted public recovery anchor VulaID from
// identityDir, returning "" when none is recorded (recovery not yet enabled —
// rotation and self-revocation still function in that state).
func LoadRecoveryAnchorID(identityDir string) string {
	raw, err := os.ReadFile(filepath.Join(identityDir, recoveryAnchorFile))
	if err != nil {
		return ""
	}
	var rec recoveryAnchorRecord
	if json.Unmarshal(raw, &rec) != nil {
		return ""
	}
	if _, err := decodeVulaID(rec.AnchorVulaID); err != nil {
		return ""
	}
	return rec.AnchorVulaID
}

// AnchorFromRecoverySeed is a convenience used by the live observer: it persists
// the anchor public id under identityDir and installs it on store (so recovery
// becomes active immediately, without a restart). Either step failing returns an
// error; both are no-ops when the anchor is already configured identically.
func AnchorFromRecoverySeed(store *LifecycleStore, identityDir string, recoverySeed []byte) (string, error) {
	anchorVulaID, err := PersistRecoveryAnchor(identityDir, recoverySeed)
	if err != nil {
		return "", err
	}
	if store != nil {
		if err := store.SetAnchor(anchorVulaID); err != nil {
			return anchorVulaID, err
		}
	}
	return anchorVulaID, nil
}
