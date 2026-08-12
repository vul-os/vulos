// lobby_test.go — unit tests for the pre-call lobby service (PEER-25).
package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── estimateLobbyCapacity ───────────────────────────────────────────────────

func TestEstimateLobbyCapacity_Zero(t *testing.T) {
	cap := estimateLobbyCapacity(0)
	if cap.VideoParticipants != 0 || cap.AudioParticipants != 0 {
		t.Errorf("zero upload should yield zero capacity, got video=%d audio=%d",
			cap.VideoParticipants, cap.AudioParticipants)
	}
}

func TestEstimateLobbyCapacity_Negative(t *testing.T) {
	cap := estimateLobbyCapacity(-5)
	if cap.VideoParticipants != 0 || cap.AudioParticipants != 0 {
		t.Errorf("negative upload should yield zero capacity, got video=%d audio=%d",
			cap.VideoParticipants, cap.AudioParticipants)
	}
}

func TestEstimateLobbyCapacity_Formula(t *testing.T) {
	type tc struct {
		upload float64
		video  int
		audio  int
	}
	typed := []tc{
		{3, 1, 31},
		{9, 3, 93},
		{30, 10, 200},
		{150, 50, 200},
	}

	for _, c := range typed {
		got := estimateLobbyCapacity(c.upload)
		// Verify formula math independently.
		wantVideo := int(math.Min(50, math.Floor(c.upload/3)))
		wantAudio := int(math.Min(200, math.Floor(c.upload/0.096)))
		if got.VideoParticipants != wantVideo {
			t.Errorf("upload=%.1f: video want %d got %d", c.upload, wantVideo, got.VideoParticipants)
		}
		if got.AudioParticipants != wantAudio {
			t.Errorf("upload=%.1f: audio want %d got %d", c.upload, wantAudio, got.AudioParticipants)
		}
		if got.HostUploadMbps != c.upload {
			t.Errorf("upload=%.1f: HostUploadMbps mismatch, got %.1f", c.upload, got.HostUploadMbps)
		}
	}
}

func TestEstimateLobbyCapacity_VideoCapAt50(t *testing.T) {
	cap := estimateLobbyCapacity(200)
	if cap.VideoParticipants != 50 {
		t.Errorf("expected video cap=50 for high upload, got %d", cap.VideoParticipants)
	}
}

func TestEstimateLobbyCapacity_AudioCapAt200(t *testing.T) {
	cap := estimateLobbyCapacity(200)
	if cap.AudioParticipants != 200 {
		t.Errorf("expected audio cap=200 for high upload, got %d", cap.AudioParticipants)
	}
}

// ─── POST /api/peering/call/lobby ────────────────────────────────────────────

// newReadyMeter returns a BandwidthMeter pre-loaded with a known result.
func newReadyMeter(up, down, latency float64) *BandwidthMeter {
	m := NewBandwidthMeter(BandwidthConfig{Interval: 10 * 60 * 1e9}) // 10 min, never fires
	m.mu.Lock()
	m.latest = BandwidthResult{
		UploadMbps:   up,
		DownloadMbps: down,
		LatencyMs:    latency,
		Source:       "speedtest",
	}
	m.ready = true
	m.mu.Unlock()
	return m
}

func postLobby(t *testing.T, svc *LobbyService, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/peering/call/lobby", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.handleLobby(rr, req)
	return rr
}

func TestLobby_SingleLocalParticipant(t *testing.T) {
	meter := newReadyMeter(25, 100, 12)
	svc := NewLobbyService(meter)

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{
			{ID: "vulos:alice", DisplayName: "Alice"},
		},
		InitiatorID: "vulos:alice",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	var report LobbyReport
	if err := json.NewDecoder(rr.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(report.Participants) != 1 {
		t.Fatalf("expected 1 participant, got %d", len(report.Participants))
	}
	p := report.Participants[0]
	if p.ID != "vulos:alice" {
		t.Errorf("ID: got %q, want vulos:alice", p.ID)
	}
	if p.UploadMbps != 25 {
		t.Errorf("upload: got %.1f, want 25", p.UploadMbps)
	}
	if p.LatencyMs != 12 {
		t.Errorf("latency: got %.1f, want 12", p.LatencyMs)
	}
	if p.Error != "" {
		t.Errorf("unexpected error: %q", p.Error)
	}

	// Host should default to the single participant.
	if report.HostID != "vulos:alice" {
		t.Errorf("hostID: got %q, want vulos:alice", report.HostID)
	}
	// Capacity must match formula.
	want := estimateLobbyCapacity(25)
	if report.Capacity.VideoParticipants != want.VideoParticipants {
		t.Errorf("video capacity: got %d, want %d", report.Capacity.VideoParticipants, want.VideoParticipants)
	}
	if report.Capacity.AudioParticipants != want.AudioParticipants {
		t.Errorf("audio capacity: got %d, want %d", report.Capacity.AudioParticipants, want.AudioParticipants)
	}
}

func TestLobby_EmptyParticipants_400(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(10, 50, 20))
	rr := postLobby(t, svc, lobbyReq{Participants: nil})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty participants, got %d", rr.Code)
	}
}

func TestLobby_InvalidJSON_400(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(10, 50, 20))
	req := httptest.NewRequest(http.MethodPost, "/api/peering/call/lobby",
		bytes.NewBufferString(`not-json`))
	rr := httptest.NewRecorder()
	svc.handleLobby(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", rr.Code)
	}
}

func TestLobby_LocalMeterNotReady(t *testing.T) {
	// Meter with no measurement yet.
	meter := NewBandwidthMeter(BandwidthConfig{Interval: 10 * 60 * 1e9})
	svc := NewLobbyService(meter)

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{{ID: "vulos:bob"}},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even when meter not ready, got %d: %s", rr.Code, rr.Body)
	}

	var report LobbyReport
	json.NewDecoder(rr.Body).Decode(&report) //nolint:errcheck
	if len(report.Participants) != 1 {
		t.Fatalf("expected 1 participant entry")
	}
	if report.Participants[0].Error == "" {
		t.Error("expected error field when meter not ready")
	}
}

func TestLobby_RemotePeerProxied(t *testing.T) {
	// Stand up a fake "peer" server that serves bandwidth JSON.
	peerPayload := `{"upload_mbps":40,"download_mbps":200,"latency_ms":8,"source":"speedtest"}`
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(peerPayload)) //nolint:errcheck
	}))
	defer peerSrv.Close()

	meter := newReadyMeter(20, 80, 15)
	svc := NewLobbyService(meter)
	// Use plain http:// test server — strip scheme/prefix from URL.
	peerAddr := peerSrv.URL[len("http://"):]

	// Override client transport to allow http:// (test servers are not https).
	svc.client = &http.Client{
		Timeout: 5 * 1e9,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			return http.DefaultTransport.RoundTrip(req2)
		}),
	}

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{
			{ID: "vulos:alice"},                 // local
			{ID: "vulos:bob", Server: peerAddr}, // remote
		},
		InitiatorID: "vulos:alice",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	var report LobbyReport
	json.NewDecoder(rr.Body).Decode(&report) //nolint:errcheck

	if len(report.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(report.Participants))
	}

	// Find bob's entry.
	var bobRpt LobbyParticipantReport
	for _, p := range report.Participants {
		if p.ID == "vulos:bob" {
			bobRpt = p
		}
	}
	if bobRpt.UploadMbps != 40 {
		t.Errorf("bob upload: got %.1f, want 40", bobRpt.UploadMbps)
	}
	if bobRpt.Error != "" {
		t.Errorf("bob: unexpected error: %q", bobRpt.Error)
	}
}

func TestLobby_RemotePeerUnreachable_ErrorField(t *testing.T) {
	meter := newReadyMeter(10, 50, 20)
	svc := NewLobbyService(meter)
	svc.client = &http.Client{Timeout: 50 * 1e6} // 50 ms

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{
			{ID: "vulos:alice"},
			{ID: "vulos:bob", Server: "127.0.0.1:1"}, // unreachable
		},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even with unreachable peer, got %d", rr.Code)
	}

	var report LobbyReport
	json.NewDecoder(rr.Body).Decode(&report) //nolint:errcheck

	var bobRpt LobbyParticipantReport
	for _, p := range report.Participants {
		if p.ID == "vulos:bob" {
			bobRpt = p
		}
	}
	if bobRpt.Error == "" {
		t.Error("expected error field for unreachable peer")
	}
}

func TestLobby_ExplicitHostIDOverridesAutoSelect(t *testing.T) {
	// Alice (local) upload=50, Bob (peer) upload=100 — normally Bob would win auto-select.
	// But we explicitly pass host_id=alice; alice should be the host.
	peerPayload := `{"upload_mbps":100,"download_mbps":300,"latency_ms":5}`
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(peerPayload)) //nolint:errcheck
	}))
	defer peerSrv.Close()
	peerAddr := peerSrv.URL[len("http://"):]

	meter := newReadyMeter(50, 200, 10)
	svc := NewLobbyService(meter)
	svc.client = &http.Client{
		Timeout: 5 * 1e9,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			return http.DefaultTransport.RoundTrip(req2)
		}),
	}

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{
			{ID: "vulos:alice"},
			{ID: "vulos:bob", Server: peerAddr},
		},
		HostID: "vulos:alice",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}
	var report LobbyReport
	json.NewDecoder(rr.Body).Decode(&report) //nolint:errcheck

	if report.HostID != "vulos:alice" {
		t.Errorf("hostID: got %q, want vulos:alice (explicit override)", report.HostID)
	}
	// Capacity should be based on alice's upload (50 Mbps).
	wantCap := estimateLobbyCapacity(50)
	if report.Capacity.VideoParticipants != wantCap.VideoParticipants {
		t.Errorf("video cap: got %d, want %d", report.Capacity.VideoParticipants, wantCap.VideoParticipants)
	}
}

func TestLobby_AutoSelectHighestUpload(t *testing.T) {
	// Three participants; the middle one (vulos:carol, upload=80) has the highest upload.
	payloadCarol := `{"upload_mbps":80,"download_mbps":200,"latency_ms":10}`
	payloadDave := `{"upload_mbps":20,"download_mbps":100,"latency_ms":15}`

	mux := http.NewServeMux()
	mux.HandleFunc("/carol/api/peering/bandwidth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payloadCarol)) //nolint:errcheck
	})
	mux.HandleFunc("/dave/api/peering/bandwidth", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payloadDave)) //nolint:errcheck
	})
	fakeSrv := httptest.NewServer(mux)
	defer fakeSrv.Close()

	carolAddr := fakeSrv.URL[len("http://"):] + "/carol"
	daveAddr := fakeSrv.URL[len("http://"):] + "/dave"

	meter := newReadyMeter(30, 100, 20) // alice = 30 Mbps upload
	svc := NewLobbyService(meter)
	svc.client = &http.Client{
		Timeout: 5 * 1e9,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			return http.DefaultTransport.RoundTrip(req2)
		}),
	}

	rr := postLobby(t, svc, lobbyReq{
		Participants: []LobbyParticipant{
			{ID: "vulos:alice"},                    // local, 30 Mbps
			{ID: "vulos:carol", Server: carolAddr}, // peer, 80 Mbps
			{ID: "vulos:dave", Server: daveAddr},   // peer, 20 Mbps
		},
		InitiatorID: "vulos:alice",
		// No explicit HostID — auto-select.
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}
	var report LobbyReport
	json.NewDecoder(rr.Body).Decode(&report) //nolint:errcheck

	if report.HostID != "vulos:carol" {
		t.Errorf("auto-select host: got %q, want vulos:carol (highest upload=80)", report.HostID)
	}
}

// ─── POST /api/peering/call/lobby/host ───────────────────────────────────────

func postLobbyHost(t *testing.T, svc *LobbyService, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/peering/call/lobby/host", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.handleLobbyHost(rr, req)
	return rr
}

func TestLobbyHost_RecomputesCapacity(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(10, 50, 20))

	rr := postLobbyHost(t, svc, lobbyHostReq{
		HostID: "vulos:alice",
		KnownBandwidth: map[string]float64{
			"vulos:alice": 30,
			"vulos:bob":   60,
		},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}

	var resp lobbyHostResp
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck

	if resp.HostID != "vulos:alice" {
		t.Errorf("hostID: got %q, want vulos:alice", resp.HostID)
	}
	want := estimateLobbyCapacity(30)
	if resp.Capacity.VideoParticipants != want.VideoParticipants {
		t.Errorf("video cap: got %d, want %d", resp.Capacity.VideoParticipants, want.VideoParticipants)
	}
	if resp.Capacity.HostUploadMbps != 30 {
		t.Errorf("HostUploadMbps: got %.1f, want 30", resp.Capacity.HostUploadMbps)
	}
}

func TestLobbyHost_MissingHostID_400(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(10, 50, 20))
	rr := postLobbyHost(t, svc, lobbyHostReq{})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestLobbyHost_UnknownHostZeroCapacity(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(10, 50, 20))

	rr := postLobbyHost(t, svc, lobbyHostReq{
		HostID:         "vulos:nobody",
		KnownBandwidth: map[string]float64{"vulos:alice": 30},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even for unknown host, got %d", rr.Code)
	}

	var resp lobbyHostResp
	json.NewDecoder(rr.Body).Decode(&resp) //nolint:errcheck

	// unknown host has 0 upload → capacity is all zeros.
	if resp.Capacity.VideoParticipants != 0 || resp.Capacity.AudioParticipants != 0 {
		t.Errorf("expected zero capacity for unknown host, got video=%d audio=%d",
			resp.Capacity.VideoParticipants, resp.Capacity.AudioParticipants)
	}
}

// ─── RegisterLobbyHandlers integration ───────────────────────────────────────

func TestRegisterLobbyHandlers_RoutesRegistered(t *testing.T) {
	svc := NewLobbyService(newReadyMeter(15, 60, 25))
	mux := http.NewServeMux()
	RegisterLobbyHandlers(mux, svc)

	// POST /api/peering/call/lobby should reach the handler (not 404/405).
	body, _ := json.Marshal(lobbyReq{
		Participants: []LobbyParticipant{{ID: "vulos:alice"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/peering/call/lobby", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Error("POST /api/peering/call/lobby route not registered")
	}

	// POST /api/peering/call/lobby/host should also reach its handler.
	body2, _ := json.Marshal(lobbyHostReq{HostID: "vulos:alice", KnownBandwidth: map[string]float64{"vulos:alice": 20}})
	req2 := httptest.NewRequest(http.MethodPost, "/api/peering/call/lobby/host", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)

	if rr2.Code == http.StatusNotFound {
		t.Error("POST /api/peering/call/lobby/host route not registered")
	}
}

// ─── Helper types ─────────────────────────────────────────────────────────────

// roundTripFunc is a test http.RoundTripper backed by a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fetchBandwidthDirect is a convenience wrapper for testing fetchBandwidth
// in isolation without an HTTP server.
func (s *LobbyService) fetchBandwidthDirect(p LobbyParticipant) LobbyParticipantReport {
	return s.fetchBandwidth(context.Background(), p)
}

func TestFetchBandwidth_LocalReady(t *testing.T) {
	meter := newReadyMeter(45, 150, 8)
	svc := NewLobbyService(meter)

	rpt := svc.fetchBandwidthDirect(LobbyParticipant{ID: "vulos:alice"})

	if rpt.Error != "" {
		t.Errorf("unexpected error: %q", rpt.Error)
	}
	if rpt.UploadMbps != 45 {
		t.Errorf("upload: got %.1f, want 45", rpt.UploadMbps)
	}
	if rpt.DownloadMbps != 150 {
		t.Errorf("download: got %.1f, want 150", rpt.DownloadMbps)
	}
	if rpt.LatencyMs != 8 {
		t.Errorf("latency: got %.1f, want 8", rpt.LatencyMs)
	}
}

func TestFetchBandwidth_LocalNotReady(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{Interval: 10 * 60 * 1e9})
	svc := NewLobbyService(meter)

	rpt := svc.fetchBandwidthDirect(LobbyParticipant{ID: "vulos:alice"})

	if rpt.Error == "" {
		t.Error("expected error when meter not ready")
	}
	if rpt.Source != "unavailable" {
		t.Errorf("source: got %q, want unavailable", rpt.Source)
	}
}
