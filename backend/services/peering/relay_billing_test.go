// relay_billing_test.go — billing-gate tests for relay deposits (surface 4).
package peering

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/cpbilling"
)

type relayCPStub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	usage []cpbilling.UsageEvent
	got   chan struct{}
}

func newRelayCPStub(ent cpbilling.Entitlement) *relayCPStub {
	cs := &relayCPStub{got: make(chan struct{}, 1)}
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

func (cs *relayCPStub) client() *cpbilling.Client {
	return cpbilling.New(cpbilling.Config{BaseURL: cs.srv.URL, SharedSecret: "s", CacheTTL: time.Hour})
}

func TestRelayDeposit_AllowedWhenEntitled_AndMeters(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)
	cp := newRelayCPStub(cpbilling.Entitlement{Tier: "pro", RelayEnabled: true, RelayBytesBudget: 1 << 30})
	defer cp.srv.Close()
	rs.WithBilling(cp.client())

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	blobData := []byte("this is an encrypted blob")
	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-bill-1", blobData, 24)
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("entitled deposit should succeed: %v", err)
	}

	select {
	case <-cp.got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for relay usage meter")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 1 {
		t.Fatalf("got %d usage events, want 1", len(cp.usage))
	}
	ev := cp.usage[0]
	if ev.Product != cpbilling.ProductRelay || ev.Kind != cpbilling.KindRelayBytes {
		t.Errorf("relay usage product/kind = %s/%s", ev.Product, ev.Kind)
	}
	if ev.Bytes != int64(len(blobData)) {
		t.Errorf("relay usage bytes = %d, want %d", ev.Bytes, len(blobData))
	}
	if ev.AccountID != senderID {
		t.Errorf("relay usage account = %q, want sender VulaID", ev.AccountID)
	}
}

func TestRelayDeposit_RefusedWhenSuspended(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)
	cp := newRelayCPStub(cpbilling.Entitlement{Tier: "pro", Suspended: true, RelayEnabled: true, RelayBytesBudget: 1 << 30})
	defer cp.srv.Close()
	rs.WithBilling(cp.client())

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-susp", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil {
		t.Fatal("suspended deposit must be refused")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 0 {
		t.Errorf("suspended deposit must not meter, got %d", len(cp.usage))
	}
}

func TestRelayDeposit_RefusedWhenRelayDisabled(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)
	cp := newRelayCPStub(cpbilling.Entitlement{Tier: "free", RelayEnabled: false, RelayBytesBudget: 0})
	defer cp.srv.Close()
	rs.WithBilling(cp.client())

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-dis", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil {
		t.Fatal("relay-disabled deposit must be refused")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 0 {
		t.Errorf("relay-disabled deposit must not meter, got %d", len(cp.usage))
	}
}

func TestRelayDeposit_RefusedOnZeroBudget(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)
	cp := newRelayCPStub(cpbilling.Entitlement{Tier: "pro", RelayEnabled: true, RelayBytesBudget: 0})
	defer cp.srv.Close()
	rs.WithBilling(cp.client())

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-nobudget", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil {
		t.Fatal("zero-budget deposit must be refused")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 0 {
		t.Errorf("zero-budget deposit must not meter, got %d", len(cp.usage))
	}
}

func TestRelayDeposit_DisabledBillingUnchanged(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)
	// Disabled client (no CP_URL) → standalone OS: deposit must succeed.
	rs.WithBilling(cpbilling.New(cpbilling.Config{}))

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-nobill", []byte("data"), 24)
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("standalone deposit should succeed: %v", err)
	}
}
