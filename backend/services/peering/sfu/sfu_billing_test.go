package sfu

// Billing-gate tests for Meet room creation (surface 4).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/cpbilling"
)

type sfuCPStub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	usage []cpbilling.UsageEvent
	got   chan struct{}
}

func newSFUCPStub(ent cpbilling.Entitlement) *sfuCPStub {
	cs := &sfuCPStub{got: make(chan struct{}, 1)}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/entitlements":
			_ = json.NewEncoder(w).Encode(ent)
		case "/api/usage":
			var ev cpbilling.UsageEvent
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &ev)
			cs.mu.Lock()
			cs.usage = append(cs.usage, ev)
			cs.mu.Unlock()
			select {
			case cs.got <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return cs
}

func (cs *sfuCPStub) client() *cpbilling.Client {
	return cpbilling.New(cpbilling.Config{BaseURL: cs.srv.URL, SharedSecret: "s", CacheTTL: time.Hour})
}

func TestSFUCreateRoom_AllowedWhenEntitled_AndMeters(t *testing.T) {
	cp := newSFUCPStub(cpbilling.Entitlement{Tier: "pro"})
	defer cp.srv.Close()
	s := New().WithBilling(cp.client())

	req := httptest.NewRequest("POST", "/api/sfu/rooms", strings.NewReader(`{"last_n":6}`))
	req.Header.Set("X-User-ID", "user@x.com")
	rr := httptest.NewRecorder()
	s.handleCreateRoom(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("entitled create-room status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if s.RoomCount() != 1 {
		t.Fatalf("expected 1 room, got %d", s.RoomCount())
	}
	select {
	case <-cp.got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for meet usage meter")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	ev := cp.usage[0]
	if ev.Product != cpbilling.ProductMeet || ev.Kind != cpbilling.KindMeetRoom || ev.Count != 1 {
		t.Errorf("meet usage = %+v", ev)
	}
	if ev.AccountID != "user@x.com" {
		t.Errorf("meet usage account = %q", ev.AccountID)
	}
}

func TestSFUCreateRoom_RefusedWhenSuspended(t *testing.T) {
	cp := newSFUCPStub(cpbilling.Entitlement{Tier: "pro", Suspended: true})
	defer cp.srv.Close()
	s := New().WithBilling(cp.client())

	req := httptest.NewRequest("POST", "/api/sfu/rooms", strings.NewReader(`{"last_n":6}`))
	req.Header.Set("X-User-ID", "user@x.com")
	rr := httptest.NewRecorder()
	s.handleCreateRoom(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("suspended create-room status = %d, want 402", rr.Code)
	}
	if s.RoomCount() != 0 {
		t.Fatalf("suspended create-room must not create a room, got %d", s.RoomCount())
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 0 {
		t.Errorf("suspended create-room must not meter, got %d", len(cp.usage))
	}
}

func TestSFUCreateRoom_DisabledBillingUnchanged(t *testing.T) {
	s := New().WithBilling(cpbilling.New(cpbilling.Config{}))
	req := httptest.NewRequest("POST", "/api/sfu/rooms", strings.NewReader(`{"last_n":6}`))
	rr := httptest.NewRecorder()
	s.handleCreateRoom(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("standalone create-room status = %d, want 201", rr.Code)
	}
}
