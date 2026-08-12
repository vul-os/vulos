// fabric_test.go — host registration against a mocked relay endpoint.

package gpuhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// mockRelay records every POST it receives and returns the configured status.
type mockRelay struct {
	server     *httptest.Server
	registerN  atomic.Int32
	heartbeatN atomic.Int32
	deregN     atomic.Int32
	lastBody   atomic.Pointer[HostRegistration]
	status     atomic.Int32 // HTTP status to return
}

func newMockRelay(t *testing.T) *mockRelay {
	t.Helper()
	m := &mockRelay{}
	m.status.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stream/host/register", func(w http.ResponseWriter, r *http.Request) {
		m.recordBody(r)
		m.registerN.Add(1)
		w.WriteHeader(int(m.status.Load()))
	})
	mux.HandleFunc("/api/stream/host/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		m.recordBody(r)
		m.heartbeatN.Add(1)
		w.WriteHeader(int(m.status.Load()))
	})
	mux.HandleFunc("/api/stream/host/deregister", func(w http.ResponseWriter, r *http.Request) {
		m.recordBody(r)
		m.deregN.Add(1)
		w.WriteHeader(int(m.status.Load()))
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockRelay) recordBody(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var h HostRegistration
	if err := json.Unmarshal(body, &h); err == nil {
		m.lastBody.Store(&h)
	}
}

func sampleIdentity() FabricIdentity {
	return FabricIdentity{
		HostID:       "vulos:ed25519:testhost",
		PublicKeyB64: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", // 32 bytes b64
		Domain:       "host.testbox.vulos.org",
	}
}

func sampleRegistration() HostRegistration {
	return HostRegistration{
		HostID:       "vulos:ed25519:testhost",
		PublicKeyB64: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		Domain:       "host.testbox.vulos.org",
		Hostname:     "host.testbox.vulos.org",
		Port:         47989,
		Capabilities: HostCapabilities{
			Codec:       "h264",
			MaxBitrate:  20000,
			GPUVendor:   "nvidia",
			HasHardware: true,
		},
	}
}

func TestFabric_Register_PostsJSON(t *testing.T) {
	relay := newMockRelay(t)
	cli := newHTTPFabricClient(relay.server.URL, sampleIdentity(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cli.Register(ctx, sampleRegistration()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := relay.registerN.Load(); got != 1 {
		t.Errorf("registerN = %d, want 1", got)
	}
	body := relay.lastBody.Load()
	if body == nil {
		t.Fatal("relay never received a body")
	}
	if body.HostID != "vulos:ed25519:testhost" {
		t.Errorf("HostID = %q", body.HostID)
	}
	if body.Capabilities.Codec != "h264" {
		t.Errorf("Codec = %q", body.Capabilities.Codec)
	}
}

func TestFabric_Heartbeat(t *testing.T) {
	relay := newMockRelay(t)
	cli := newHTTPFabricClient(relay.server.URL, sampleIdentity(), nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := cli.Heartbeat(ctx, sampleRegistration()); err != nil {
			t.Fatalf("Heartbeat #%d: %v", i, err)
		}
	}
	if got := relay.heartbeatN.Load(); got != 3 {
		t.Errorf("heartbeatN = %d, want 3", got)
	}
}

func TestFabric_Deregister(t *testing.T) {
	relay := newMockRelay(t)
	cli := newHTTPFabricClient(relay.server.URL, sampleIdentity(), nil)
	if err := cli.Deregister(context.Background(), "vulos:ed25519:testhost"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if got := relay.deregN.Load(); got != 1 {
		t.Errorf("deregN = %d, want 1", got)
	}
}

func TestFabric_RelayErrorSurfaces(t *testing.T) {
	relay := newMockRelay(t)
	relay.status.Store(http.StatusInternalServerError)
	cli := newHTTPFabricClient(relay.server.URL, sampleIdentity(), nil)
	err := cli.Register(context.Background(), sampleRegistration())
	if err == nil {
		t.Fatal("expected error on relay 500, got nil")
	}
}

func TestFabric_EmptyBaseURLRejected(t *testing.T) {
	cli := newHTTPFabricClient("", sampleIdentity(), nil)
	err := cli.Register(context.Background(), sampleRegistration())
	if err == nil {
		t.Fatal("expected error with empty base URL, got nil")
	}
}
