package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthHandlerLeader(t *testing.T) {
	s := NewMemStore()
	mgr := NewManager(s, Config{
		PeerID:   "peer-a",
		LeaseTTL: 30 * time.Second,
	})
	if err := mgr.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !mgr.IsLeader() {
		t.Fatalf("manager did not become leader")
	}

	h := NewHandler(mgr, s)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/ha/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("leader returned %d, want 200", rr.Code)
	}
	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PeerID != "peer-a" {
		t.Fatalf("peer_id=%s, want peer-a", resp.PeerID)
	}
	if !resp.Self.IsLeader {
		t.Fatalf("self.is_leader=false")
	}
	if resp.Lease == nil || resp.Lease.HolderID != "peer-a" {
		t.Fatalf("lease=%+v", resp.Lease)
	}
	if len(resp.Peers) == 0 {
		t.Fatalf("peers empty")
	}
}

func TestHealthHandlerStandby(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	_, _ = s.TryAcquire(ctx, DefaultLeaseKey, "peer-a", 30*time.Second)

	mgrB := NewManager(s, Config{PeerID: "peer-b", LeaseTTL: 30 * time.Second})
	_ = mgrB.Tick(ctx)
	if mgrB.IsLeader() {
		t.Fatalf("peer-b should not be leader")
	}

	h := NewHandler(mgrB, s)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/ha/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("standby returned %d, want 503", rr.Code)
	}
	var resp healthResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Self.IsLeader {
		t.Fatalf("standby reports self.is_leader=true")
	}
	if resp.Lease == nil || resp.Lease.HolderID != "peer-a" {
		t.Fatalf("lease=%+v", resp.Lease)
	}
}
