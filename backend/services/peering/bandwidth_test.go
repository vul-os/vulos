package peering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/safedial"
)

// ---------------------------------------------------------------------------
// BandwidthConfig.applyDefaults
// ---------------------------------------------------------------------------

func TestBandwidthConfig_ApplyDefaults(t *testing.T) {
	var cfg BandwidthConfig
	cfg.applyDefaults()

	if cfg.TestEndpoint == "" {
		t.Error("expected non-empty TestEndpoint after applyDefaults")
	}
	if cfg.Interval <= 0 {
		t.Error("expected positive Interval after applyDefaults")
	}
	if cfg.TestPayloadBytes <= 0 {
		t.Error("expected positive TestPayloadBytes after applyDefaults")
	}
	if cfg.UploadPayloadBytes <= 0 {
		t.Error("expected positive UploadPayloadBytes after applyDefaults")
	}
	if cfg.HTTPTimeout <= 0 {
		t.Error("expected positive HTTPTimeout after applyDefaults")
	}
}

func TestBandwidthConfig_CustomValues(t *testing.T) {
	cfg := BandwidthConfig{
		TestEndpoint: "http://example.com/dl",
		Interval:     2 * time.Minute,
	}
	cfg.applyDefaults()

	if cfg.TestEndpoint != "http://example.com/dl" {
		t.Errorf("custom TestEndpoint was overwritten: got %q", cfg.TestEndpoint)
	}
	if cfg.Interval != 2*time.Minute {
		t.Errorf("custom Interval was overwritten: got %v", cfg.Interval)
	}
}

// ---------------------------------------------------------------------------
// BandwidthMeter — cache and non-blocking start
// ---------------------------------------------------------------------------

func TestBandwidthMeter_NotReadyBeforeMeasure(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint: "http://127.0.0.1:0", // guaranteed-unreachable
		Interval:     10 * time.Minute,
		HTTPTimeout:  50 * time.Millisecond,
	})

	_, ready := meter.Latest()
	if ready {
		t.Error("expected meter to not be ready before Start is called")
	}
}

func TestBandwidthMeter_StartIsNonBlocking(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint: "http://127.0.0.1:0",
		Interval:     10 * time.Minute,
		HTTPTimeout:  50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		meter.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Good — Start returned immediately.
	case <-time.After(200 * time.Millisecond):
		t.Error("Start blocked the caller for too long (should be non-blocking)")
	}
}

// ---------------------------------------------------------------------------
// BandwidthMeter — real speed-test with a local HTTP server
// ---------------------------------------------------------------------------

func newTestDownloadServer(payloadSize int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		payload := make([]byte, payloadSize)
		w.Write(payload) //nolint:errcheck
	}))
}

func TestBandwidthMeter_SpeedTestSuccess(t *testing.T) {
	srv := newTestDownloadServer(1_000_000) // 1 MB
	defer srv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint:     srv.URL,
		TestPayloadBytes: 1_000_000,
		Interval:         10 * time.Minute,
		HTTPTimeout:      10 * time.Second,
	})

	ctx := context.Background()
	meter.measure(ctx)

	result, ready := meter.Latest()
	if !ready {
		t.Fatal("meter should be ready after a successful measurement")
	}
	if result.DownloadMbps <= 0 {
		t.Errorf("expected positive DownloadMbps, got %.3f", result.DownloadMbps)
	}
	if result.LatencyMs < 0 {
		t.Errorf("expected non-negative LatencyMs, got %.3f", result.LatencyMs)
	}
	if result.Source != "speedtest" {
		t.Errorf("expected source=speedtest, got %q", result.Source)
	}
	if result.MeasuredAt == "" {
		t.Error("expected non-empty MeasuredAt timestamp")
	}
}

func TestBandwidthMeter_SpeedTestFallbackToEstimate(t *testing.T) {
	// Point to a guaranteed-unreachable address.
	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint: "http://127.0.0.1:1",
		Interval:     10 * time.Minute,
		HTTPTimeout:  50 * time.Millisecond,
	})

	ctx := context.Background()
	meter.measure(ctx)

	// Should fall back to traffic estimate and mark ready.
	_, ready := meter.Latest()
	if !ready {
		t.Error("meter should become ready even after a failed speed-test (fallback estimate)")
	}
}

func TestBandwidthMeter_CacheRetainedOnSubsequentFailure(t *testing.T) {
	srv := newTestDownloadServer(500_000)
	defer srv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint:     srv.URL,
		TestPayloadBytes: 500_000,
		Interval:         10 * time.Minute,
		HTTPTimeout:      10 * time.Second,
	})

	ctx := context.Background()
	// First measure — should succeed.
	meter.measure(ctx)
	first, _ := meter.Latest()

	// Now point to broken endpoint.
	meter.cfg.TestEndpoint = "http://127.0.0.1:1"
	meter.cfg.HTTPTimeout = 50 * time.Millisecond
	meter.measure(ctx)

	second, ready := meter.Latest()
	if !ready {
		t.Fatal("meter lost ready state after a failed re-measurement")
	}
	// The cached result should be the first successful measurement.
	if second.DownloadMbps != first.DownloadMbps {
		t.Errorf("expected cache to remain %.3f, got %.3f", first.DownloadMbps, second.DownloadMbps)
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func TestRegisterBandwidthHandlers_OwnBandwidth_Ready(t *testing.T) {
	srv := newTestDownloadServer(500_000)
	defer srv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint:     srv.URL,
		TestPayloadBytes: 500_000,
		Interval:         10 * time.Minute,
		HTTPTimeout:      10 * time.Second,
	})
	meter.measure(context.Background())

	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var result BandwidthResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}
	if result.DownloadMbps <= 0 {
		t.Errorf("expected positive DownloadMbps in response, got %.3f", result.DownloadMbps)
	}
}

func TestRegisterBandwidthHandlers_OwnBandwidth_NotReady(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{
		TestEndpoint: "http://127.0.0.1:1",
		Interval:     10 * time.Minute,
		HTTPTimeout:  1 * time.Millisecond,
	})
	// Deliberately do NOT call measure — meter stays unready.

	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted when not ready, got %d", w.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected JSON body, got: %s", w.Body.String())
	}
	if body["status"] != "measuring" {
		t.Errorf("expected status=measuring, got %q", body["status"])
	}
}

func TestRegisterBandwidthHandlers_PeerBandwidth_MissingServer(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{Interval: 10 * time.Minute})

	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth/peer", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when server param is missing, got %d", w.Code)
	}
}

func TestRegisterBandwidthHandlers_PeerBandwidth_ProxiesResponse(t *testing.T) {
	// Stand up a fake "peer" server.
	fakePayload := `{"upload_mbps":22,"download_mbps":95,"latency_ms":18}`
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fakePayload)) //nolint:errcheck
	}))
	defer peerSrv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{
		Interval:    10 * time.Minute,
		HTTPTimeout: 5 * time.Second,
	})
	// httptest.Server uses 127.0.0.1 which is blocked by SSRF in production;
	// skip the check in this unit test only.
	// peeringSSRFBypass set by TestMain allows httptest.Server on 127.0.0.1.

	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth/peer?server="+peerSrv.URL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	body := strings.TrimSpace(w.Body.String())
	if body != fakePayload {
		t.Errorf("expected proxied payload %q, got %q", fakePayload, body)
	}
}

func TestRegisterBandwidthHandlers_PeerBandwidth_PeerUnreachable(t *testing.T) {
	meter := NewBandwidthMeter(BandwidthConfig{
		Interval:    10 * time.Minute,
		HTTPTimeout: 50 * time.Millisecond,
	})
	// 127.0.0.1:1 is loopback (SSRF blocked in prod); skip check to test the
	// unreachable-peer path directly.
	// peeringSSRFBypass set by TestMain allows httptest.Server on 127.0.0.1.

	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth/peer?server=http://127.0.0.1:1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when peer unreachable, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func TestParseProcNetDev(t *testing.T) {
	// Minimal synthetic /proc/net/dev content.
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:   12345      100    0    0    0     0          0         0     12345      100    0    0    0     0       0          0
  eth0: 9000000      500    0    0    0     0          0         0   3000000      200    0    0    0     0       0          0
`

	rx, tx, err := parseProcNetDev(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// lo is excluded; eth0: rx=9000000, tx=3000000
	if rx != 9_000_000 {
		t.Errorf("expected rx=9000000, got %d", rx)
	}
	if tx != 3_000_000 {
		t.Errorf("expected tx=3000000, got %d", tx)
	}
}

func TestParseProcNetDev_LoopbackSkipped(t *testing.T) {
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 99999      50    0    0    0     0          0         0     99999       50    0    0    0     0       0          0
`
	rx, tx, err := parseProcNetDev(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rx != 0 || tx != 0 {
		t.Errorf("loopback bytes should be excluded, got rx=%d tx=%d", rx, tx)
	}
}

func TestZeroReader(t *testing.T) {
	var r zeroReader
	buf := make([]byte, 16)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 16 {
		t.Errorf("expected 16 bytes, got %d", n)
	}
	for i, b := range buf {
		if b != 0 {
			t.Errorf("byte %d is %d, want 0", i, b)
		}
	}
}

func TestSplitLines(t *testing.T) {
	lines := splitLines("a\nb\nc")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Errorf("unexpected lines: %v", lines)
	}

	empty := splitLines("")
	if len(empty) != 0 {
		t.Errorf("expected 0 lines for empty string, got %d", len(empty))
	}
}

// ---------------------------------------------------------------------------
// M1 fix: SSRF protections on handlePeerBandwidth (safedial.ValidateHost)
// ---------------------------------------------------------------------------
// checkSSRF and isPrivateIP have been removed. These tests now verify the
// canonical safedial.ValidateHost behaviour that replaced them.

// TestSafedialRejectsPrivateIP verifies that safedial blocks loopback addresses.
func TestSafedialRejectsPrivateIP(t *testing.T) {
	if _, err := safedial.ValidateHost("127.0.0.1", false); err == nil {
		t.Error("expected safedial to reject 127.0.0.1, got nil error")
	}
}

// TestSafedialRejectsLocalhost verifies that the literal "localhost"
// hostname is rejected (it resolves to 127.0.0.1 which is always blocked).
func TestSafedialRejectsLocalhost(t *testing.T) {
	if _, err := safedial.ValidateHost("localhost", false); err == nil {
		t.Error("expected safedial to reject 'localhost', got nil error")
	}
}

// TestSafedialRejectsDotLocal verifies that .local mDNS names are rejected.
// safedial fails closed on DNS resolution errors, so unresolvable .local names
// produce an error rather than being silently allowed.
func TestSafedialRejectsDotLocal(t *testing.T) {
	if _, err := safedial.ValidateHost("mydevice.local", false); err == nil {
		t.Error("expected safedial to reject .local hostname, got nil error")
	}
}

// TestPeerBandwidth_RedirectNotFollowed verifies that the peer-bandwidth proxy
// does not follow HTTP redirects.
func TestPeerBandwidth_RedirectNotFollowed(t *testing.T) {
	// Stand up a server that returns a redirect.
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This would be an internal server — not reached because redirect is blocked.
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"upload_mbps":1}`)) //nolint:errcheck
	}))
	defer redirectTarget.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/api/peering/bandwidth", http.StatusFound)
	}))
	defer redirectSrv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{Interval: 10 * time.Minute})
	// peeringSSRFBypass is set by TestMain — httptest servers on 127.0.0.1 are allowed.
	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth/peer?server="+redirectSrv.URL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should get a 302 forwarded back (redirect not followed), NOT a 200 from the target.
	// The response code will be 302 because we forward the peer's response verbatim.
	if w.Code == http.StatusOK {
		t.Error("expected redirect to NOT be followed (should not get 200 from redirect target)")
	}
}

// TestPeerBandwidth_OversizeBodyCapped verifies that the response body is
// capped at 10 KB even when the peer returns more.
func TestPeerBandwidth_OversizeBodyCapped(t *testing.T) {
	const oversize = 20 * 1024 // 20 KB — larger than the 10 KB cap
	large := make([]byte, oversize)
	for i := range large {
		large[i] = 'x'
	}

	largeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(large) //nolint:errcheck
	}))
	defer largeSrv.Close()

	meter := NewBandwidthMeter(BandwidthConfig{Interval: 10 * time.Minute})
	// httptest server uses 127.0.0.1; skip SSRF check to test body-cap logic.
	// peeringSSRFBypass set by TestMain allows httptest.Server on 127.0.0.1.
	mux := http.NewServeMux()
	RegisterBandwidthHandlers(mux, meter)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/bandwidth/peer?server="+largeSrv.URL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Body.Len() > 10*1024 {
		t.Errorf("response body %d bytes exceeds 10 KB cap", w.Body.Len())
	}
}

// TestSafedialAcceptsPublicIP verifies that safedial accepts public IP addresses.
// M1 fix: replaces TestCheckSSRF_PublicIPAccepted.
// Note: 203.0.113.x (TEST-NET-3) is blocked by safedial's alwaysDeniedCIDRs.
// Use 1.1.1.1 (Cloudflare) which is genuinely public.
func TestSafedialBandwidthAcceptsPublicIP(t *testing.T) {
	// 1.1.1.1 is a well-known public IP — safedial must not block it.
	if _, err := safedial.ValidateHost("1.1.1.1", false); err != nil {
		t.Errorf("safedial should accept public 1.1.1.1, got: %v", err)
	}
	// 203.0.113.1 is TEST-NET-3 — blocked by safedial (correct behavior for docs IPs).
	if _, err := safedial.ValidateHost("203.0.113.1", false); err == nil {
		t.Error("safedial should reject TEST-NET-3 (203.0.113.1)")
	}
}
