package devicekey

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRevSyncer_PullOnce_MergesPeerRevocations proves the fleet-propagation
// path: a box that never issued a revocation learns of it by pulling a peer's
// GET /api/auth/device/revocations batch and merging every entry that
// re-verifies. This is what makes a self- or quorum-revoked device key known
// fleet-wide, not just on the box that recorded it.
func TestRevSyncer_PullOnce_MergesPeerRevocations(t *testing.T) {
	resetRevocationChecker(t)

	// A peer self-revokes its own device key and publishes the batch.
	peerKS := newTestSoftwareStore(t)
	peerStore := newTestRevocationStore(t)
	cert, err := SelfRevoke(peerKS, peerStore, "peer decommission")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}
	batchBytes, err := MarshalRevocationBatch(&RevocationBatch{Revocations: []*DeviceRevocationCert{cert}})
	if err != nil {
		t.Fatalf("MarshalRevocationBatch: %v", err)
	}

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/device/revocations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(batchBytes)
	}))
	defer srv.Close()

	// A DIFFERENT box's local store, initially ignorant of the revocation.
	localStore := newTestRevocationStore(t)
	if localStore.IsRevoked(cert.Fingerprint) {
		t.Fatal("local store should not know the peer's revocation before pulling")
	}

	rs := NewRevSyncer(localStore, func(context.Context) ([]string, error) {
		return []string{srv.URL}, nil
	}, nil, 0)
	rs.PullOnce(context.Background())

	if !localStore.IsRevoked(cert.Fingerprint) {
		t.Fatal("PullOnce should have merged the peer's self-revocation into the local store")
	}
	if hits == 0 {
		t.Fatal("peer revocations endpoint was never hit")
	}
	if got := rs.Status()[srv.URL].LastMerged; got != 1 {
		t.Fatalf("first PullOnce merged %d, want 1", got)
	}

	// Idempotent: a second round merges nothing new and records no error.
	rs.PullOnce(context.Background())
	st := rs.Status()[srv.URL]
	if st.LastMerged != 0 {
		t.Fatalf("second PullOnce merged %d, want 0 (idempotent)", st.LastMerged)
	}
	if st.LastErr != "" {
		t.Fatalf("second PullOnce recorded error %q, want none", st.LastErr)
	}
}

// TestRevSyncer_UnwiredAndBadPeersAreSafe proves the loop degrades to a no-op
// (never a panic) when unwired, when peer discovery fails, when the peer set
// is empty, and when a peer is unreachable — so RevSyncer can be started
// before fleet peering exists and one bad peer never stalls a round.
func TestRevSyncer_UnwiredAndBadPeersAreSafe(t *testing.T) {
	resetRevocationChecker(t)
	store := newTestRevocationStore(t)

	// Entirely unwired: nil store/peers must be a safe no-op.
	(&RevSyncer{}).PullOnce(context.Background())

	// Peer discovery error => no-op.
	NewRevSyncer(store, func(context.Context) ([]string, error) {
		return nil, context.DeadlineExceeded
	}, nil, 0).PullOnce(context.Background())

	// Empty peer set => no-op.
	NewRevSyncer(store, func(context.Context) ([]string, error) {
		return nil, nil
	}, nil, 0).PullOnce(context.Background())

	// Unreachable peer => recorded as an error, no panic, no block.
	const dead = "http://127.0.0.1:1"
	rsBad := NewRevSyncer(store, func(context.Context) ([]string, error) {
		return []string{dead}, nil
	}, nil, 0)
	rsBad.PullOnce(context.Background())
	if rsBad.Status()[dead].LastErr == "" {
		t.Fatal("expected an error recorded for an unreachable peer")
	}
}

// TestRevSyncer_StopBeforeRunIsSafe proves Stop is safe to call before Run and
// multiple times (mirrors the fabric Service lifecycle contract).
func TestRevSyncer_StopBeforeRunIsSafe(t *testing.T) {
	rs := NewRevSyncer(newTestRevocationStore(t), func(context.Context) ([]string, error) {
		return nil, nil
	}, nil, 0)
	rs.Stop()
	rs.Stop() // idempotent
	(*RevSyncer)(nil).Stop()
}
