package crdtsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The sync loop's health must be readable from OUTSIDE the process, and the
// case it has to make visible is the QUIET one.
//
// A box that discovers no peers runs its loop forever, logs nothing, errors on
// nothing, and holds a perfectly consistent local database. Every signal the
// engine exposes — version vectors, log sizes, register counts — looks
// identical to a box that is replicating happily. The only distinguishing fact
// is that each round dials nobody, and until this endpoint existed that fact
// was computed on every round and discarded.
//
// That is not a hypothetical failure mode. It is the one the two-box e2e test
// hit, and the reason its diagnosis had to be reconstructed from which log
// lines were ABSENT rather than from any line that was present.

func statusOf(t *testing.T, srv *httptest.Server, secret string) SyncerStatus {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/crdt/sync-status", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if secret != "" {
		req.Header.Set(AuthHeader, secret)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("get sync-status: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sync-status: status %d, want 200", res.StatusCode)
	}
	var st SyncerStatus
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return st
}

// syncStatusFixture builds a syncer whose peer source is under the test's
// control, serves the status endpoint, and returns both.
func syncStatusFixture(t *testing.T, peers func() []SyncPeer) (*Syncer, *httptest.Server, string) {
	t.Helper()
	const secret = "sync-status-secret"
	store := newTestStore(t, "SELF")

	sy, err := NewSyncer(SyncerConfig{
		Store: store,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return peers(), nil
		}),
		Domains:    []string{dom},
		Secret:     secret,
		HTTPClient: http.DefaultClient,
		Interval:   time.Hour, // rounds are driven by the test, not the clock
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	mux := http.NewServeMux()
	RegisterSyncStatusHandler(mux, SecretAuthorizer(secret), sy)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return sy, srv, secret
}

func TestSyncStatus_ReportsRoundsAndDialledPeers(t *testing.T) {
	// An unreachable address on purpose: what matters is that the peer was
	// DIALLED, not that the exchange succeeded. A peer that is listed and
	// failing is a completely different diagnosis from a peer that was never
	// discovered, and the endpoint must tell them apart.
	const peerURL = "http://127.0.0.1:1/unreachable"
	sy, srv, secret := syncStatusFixture(t, func() []SyncPeer {
		return []SyncPeer{{InstanceID: "PEER", BaseURL: peerURL}}
	})

	// Before the first round there is no recorded peer list at all. It must
	// still serialise as [] rather than null: a reader polling this endpoint
	// during startup has to be able to say "dialled nobody" without having to
	// treat a missing field as a third state.
	if st := statusOf(t, srv, secret); st.Rounds != 0 {
		t.Fatalf("before any round: Rounds = %d, want 0", st.Rounds)
	} else if st.Peers == nil {
		t.Fatal("before any round: Peers is null in JSON, want []")
	} else if len(st.Peers) != 0 {
		t.Fatalf("before any round: Peers = %v, want empty", st.Peers)
	}

	sy.SyncOnce(context.Background())

	st := statusOf(t, srv, secret)
	if st.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", st.Rounds)
	}
	if st.Actor != "SELF" {
		t.Errorf("Actor = %q, want SELF", st.Actor)
	}
	if len(st.Peers) != 1 || st.Peers[0] != peerURL {
		t.Errorf("Peers = %v, want exactly [%s]", st.Peers, peerURL)
	}
	// It was dialled and it could not be reached, so the error must surface
	// against that peer rather than vanishing.
	if st.PeerErrors[peerURL] == "" {
		t.Errorf("PeerErrors has no entry for the unreachable peer: %v", st.PeerErrors)
	}
	if st.LastSyncMS == 0 {
		t.Error("LastSyncMS is zero after a completed round")
	}
}

// THE case this endpoint exists for: a loop in perfect health with nobody to
// talk to. Rounds climb, no error is recorded, nothing is logged — and the peer
// list is empty. Without that last field the two states are indistinguishable
// from outside the process.
func TestSyncStatus_EmptyPeerListIsVisible(t *testing.T) {
	sy, srv, secret := syncStatusFixture(t, func() []SyncPeer { return nil })

	sy.SyncOnce(context.Background())
	sy.SyncOnce(context.Background())

	st := statusOf(t, srv, secret)
	if st.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2 — the loop must still be running", st.Rounds)
	}
	if len(st.PeerErrors) != 0 {
		t.Fatalf("PeerErrors = %v, want none — nothing was attempted", st.PeerErrors)
	}
	if len(st.Peers) != 0 {
		t.Fatalf("Peers = %v, want empty", st.Peers)
	}
}

// A peer filtered out as self must not appear as dialled. Otherwise the status
// would claim replication against a box's own URL, which is the exact
// misreading that made the two-box failure look like a working transport.
func TestSyncStatus_SelfIsNotReportedAsDialled(t *testing.T) {
	const selfURL = "http://self.invalid"
	store := newTestStore(t, "SELF")
	const secret = "s"
	sy, err := NewSyncer(SyncerConfig{
		Store: store,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return []SyncPeer{
				{InstanceID: "SELF", BaseURL: "http://elsewhere.invalid"}, // self by id
				{BaseURL: selfURL}, // self by URL
			}, nil
		}),
		Domains:      []string{dom},
		Secret:       secret,
		HTTPClient:   http.DefaultClient,
		SelfBaseURLs: []string{selfURL},
		Interval:     time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	mux := http.NewServeMux()
	RegisterSyncStatusHandler(mux, SecretAuthorizer(secret), sy)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sy.SyncOnce(context.Background())

	st := statusOf(t, srv, secret)
	if len(st.Peers) != 0 {
		t.Errorf("Peers = %v, want empty — both entries were this box", st.Peers)
	}
}

// The loop's health is operational detail about who this box talks to. It sits
// behind the same authorizer as pull and push, and an unauthenticated read must
// be refused rather than served.
func TestSyncStatus_RequiresAuthorization(t *testing.T) {
	_, srv, secret := syncStatusFixture(t, func() []SyncPeer { return nil })

	res, err := srv.Client().Get(srv.URL + "/api/crdt/sync-status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read: status %d, want 401", res.StatusCode)
	}

	// And the secret that IS accepted is the right one — proving the 401 above
	// came from the authorizer rather than from the route being absent.
	if st := statusOf(t, srv, secret); st.Actor == "" {
		t.Error("authorised read returned no actor")
	}
}
