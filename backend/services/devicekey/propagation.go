// propagation.go — fleet-wide propagation of device-key revocations.
//
// # Why this needs no new transport
//
// A DeviceRevocationCert is SELF-VERIFYING (see revocation.go's Verify): its
// authenticity does not depend on who delivered it or how. That means
// propagation reduces to the SAME pull idiom this codebase already uses for
// fleet state — internal/multiinstance's CloudSync (GET /api/instances) and
// AppSync (GET /api/instances/{ulid}/apps, ChangesetSince) are both "ask a
// peer what it knows, verify every item yourself, merge what verifies." This
// file's MergeRevocationBatch is that same pattern applied to device-key
// revocations: GET /api/auth/device/revocations (routes) exposes
// RevocationStore.List() as the wire format below, and any peer — pulling
// directly, or via whatever periodic fabric sync loop the orchestrator wires
// this into (see the integration manifest) — feeds the response straight into
// MergeRevocationBatch.
//
// # Fail-closed propagation
//
// Each entry in a batch is independently verified by MergeVerified before it
// is ever recorded (RevocationMethodSelf checks the embedded signature;
// RevocationMethodBreakGlass re-runs fleetid.VerifyQuorum against THIS box's
// OWN roster — never the sender's claim). A box that has not yet pulled a
// given revocation simply doesn't reject that key yet; the moment ANY future
// pull carries a verifiable cert for it, this box starts rejecting that key
// forever — merge order, partial network partitions, and which peer happened
// to deliver it first are all irrelevant to the eventual guarantee.
package devicekey

import (
	"encoding/json"
	"fmt"
	"time"

	"vulos/backend/services/fleetid"
)

// RevocationBatch is the wire format for GET /api/auth/device/revocations —
// a peer requests it, verifies every entry against its OWN roster, and merges
// what verifies via MergeRevocationBatch.
type RevocationBatch struct {
	Revocations []*DeviceRevocationCert `json:"revocations"`
}

// MarshalRevocationBatch serialises a RevocationBatch to JSON, mirroring
// internal/multiinstance's MarshalChangeset/UnmarshalChangeset helpers.
func MarshalRevocationBatch(b *RevocationBatch) ([]byte, error) {
	return json.Marshal(b)
}

// UnmarshalRevocationBatch parses JSON bytes into a RevocationBatch.
func UnmarshalRevocationBatch(data []byte) (*RevocationBatch, error) {
	var b RevocationBatch
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("devicekey: unmarshal revocation batch: %w", err)
	}
	return &b, nil
}

// MergeRevocationBatch verifies and merges every cert in batch into store.
// Each cert is checked INDEPENDENTLY via RevocationStore.MergeVerified — one
// malformed or unverifiable entry is skipped (recorded in the returned
// errs slice, indexed to match batch.Revocations) without blocking the rest of
// the batch from merging. Returns the number of NEWLY recorded revocations
// (already-known fingerprints and rejected entries do not count).
func MergeRevocationBatch(store *RevocationStore, batch *RevocationBatch, roster fleetid.Roster, threshold int, now time.Time) (merged int, errs []error) {
	if store == nil || batch == nil {
		return 0, nil
	}
	for _, cert := range batch.Revocations {
		if cert == nil {
			continue
		}
		alreadyKnown := store.IsRevoked(cert.Fingerprint)
		if err := store.MergeVerified(cert, roster, threshold, now); err != nil {
			errs = append(errs, fmt.Errorf("fingerprint %s: %w", cert.Fingerprint, err))
			continue
		}
		if !alreadyKnown {
			merged++
		}
	}
	return merged, errs
}
