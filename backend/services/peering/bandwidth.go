// Package peering implements direct Vula-to-Vula instance communication.
// This file provides bandwidth measurement and the /api/peering/bandwidth endpoint.
//
// Measurement strategy:
//  1. Periodic speed-test against a configurable HTTP endpoint (download) and
//     by timing an upload of a synthetic payload (upload).
//  2. Fallback: if the test endpoint is unreachable, the most recent cached
//     result is returned; on the very first call before any measurement has
//     completed a lightweight estimate derived from recent real traffic is used.
//
// The background worker starts non-blocking (go routine) and the HTTP handler
// always returns the cached result, so latency is negligible.
package peering

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// BandwidthResult is the JSON payload returned by GET /api/peering/bandwidth.
type BandwidthResult struct {
	UploadMbps   float64 `json:"upload_mbps"`
	DownloadMbps float64 `json:"download_mbps"`
	LatencyMs    float64 `json:"latency_ms"`
	MeasuredAt   string  `json:"measured_at,omitempty"`
	Source       string  `json:"source,omitempty"` // "speedtest" | "traffic" | "cache"
}

// BandwidthConfig controls measurement behaviour. Zero values use safe defaults.
type BandwidthConfig struct {
	// TestEndpoint is a URL that serves a large file used for download speed
	// measurement. Defaults to a well-known Cloudflare latency probe.
	TestEndpoint string

	// UploadEndpoint is a URL that accepts POST bodies (used to measure upload
	// speed). When empty, upload is estimated from recent traffic data.
	UploadEndpoint string

	// Interval between periodic measurements. Default: 5 minutes.
	Interval time.Duration

	// TestPayloadBytes is the number of bytes downloaded during a test.
	// Default: 10 MB.
	TestPayloadBytes int64

	// UploadPayloadBytes is the number of bytes sent in an upload test.
	// Default: 5 MB.
	UploadPayloadBytes int64

	// HTTPTimeout caps each individual HTTP request. Default: 30 s.
	HTTPTimeout time.Duration
}

func (c *BandwidthConfig) applyDefaults() {
	if c.TestEndpoint == "" {
		c.TestEndpoint = "https://speed.cloudflare.com/__down?bytes=10000000"
	}
	if c.Interval == 0 {
		c.Interval = 5 * time.Minute
	}
	if c.TestPayloadBytes == 0 {
		c.TestPayloadBytes = 10_000_000
	}
	if c.UploadPayloadBytes == 0 {
		c.UploadPayloadBytes = 5_000_000
	}
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 30 * time.Second
	}
}

// BandwidthMeter manages periodic bandwidth measurement and caching.
type BandwidthMeter struct {
	cfg    BandwidthConfig
	client *http.Client

	mu     sync.RWMutex
	latest BandwidthResult
	ready  bool // true once at least one measurement (or estimate) is available
}

// NewBandwidthMeter creates a meter using cfg. Call Start to begin measuring.
func NewBandwidthMeter(cfg BandwidthConfig) *BandwidthMeter {
	cfg.applyDefaults()
	return &BandwidthMeter{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
	}
}

// Start launches the background measurement loop non-blocking.
// ctx cancellation stops the loop cleanly.
func (m *BandwidthMeter) Start(ctx context.Context) {
	go m.run(ctx)
}

func (m *BandwidthMeter) run(ctx context.Context) {
	// First measurement immediately so the endpoint has data right away.
	m.measure(ctx)

	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.measure(ctx)
		}
	}
}

// measure performs one full measurement cycle and updates the cache.
func (m *BandwidthMeter) measure(ctx context.Context) {
	result, err := m.runSpeedTest(ctx)
	if err != nil {
		log.Printf("[peering/bandwidth] speed-test failed: %v — using traffic estimate or prior cache", err)
		// Attempt a traffic-based estimate only if we have no prior result.
		m.mu.RLock()
		alreadyReady := m.ready
		m.mu.RUnlock()
		if !alreadyReady {
			est := m.trafficEstimate()
			m.mu.Lock()
			m.latest = est
			m.ready = true
			m.mu.Unlock()
		}
		return
	}

	m.mu.Lock()
	m.latest = result
	m.ready = true
	m.mu.Unlock()

	log.Printf("[peering/bandwidth] measured: up=%.1f down=%.1f latency=%.0fms",
		result.UploadMbps, result.DownloadMbps, result.LatencyMs)
}

// runSpeedTest hits the configured test endpoint to measure download speed and
// latency. Upload is either tested against UploadEndpoint or synthesised.
func (m *BandwidthMeter) runSpeedTest(ctx context.Context) (BandwidthResult, error) {
	// --- Latency (RTT to test host) ---
	latencyMs, err := m.measureLatency(ctx)
	if err != nil {
		return BandwidthResult{}, err
	}

	// --- Download ---
	downloadMbps, err := m.measureDownload(ctx)
	if err != nil {
		return BandwidthResult{}, err
	}

	// --- Upload ---
	uploadMbps := m.measureUpload(ctx)

	return BandwidthResult{
		UploadMbps:   uploadMbps,
		DownloadMbps: downloadMbps,
		LatencyMs:    latencyMs,
		MeasuredAt:   time.Now().UTC().Format(time.RFC3339),
		Source:       "speedtest",
	}, nil
}

// measureLatency sends a HEAD request and measures round-trip time.
func (m *BandwidthMeter) measureLatency(ctx context.Context) (float64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, m.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, m.cfg.TestEndpoint, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	elapsed := time.Since(start)

	return float64(elapsed.Milliseconds()), nil
}

// measureDownload fetches the test file and computes throughput.
func (m *BandwidthMeter) measureDownload(ctx context.Context) (float64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, m.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, m.cfg.TestEndpoint, nil)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		// Partial read is still useful if we got something.
		if n == 0 {
			return 0, err
		}
	}

	elapsed := time.Since(start).Seconds()
	if elapsed == 0 {
		elapsed = 0.001
	}

	mbps := (float64(n) * 8) / (elapsed * 1_000_000)
	return mbps, nil
}

// measureUpload times a synthetic POST upload when an UploadEndpoint is set;
// otherwise it returns a conservative estimate from recent traffic data.
func (m *BandwidthMeter) measureUpload(ctx context.Context) float64 {
	if m.cfg.UploadEndpoint == "" {
		return m.trafficEstimate().UploadMbps
	}

	reqCtx, cancel := context.WithTimeout(ctx, m.cfg.HTTPTimeout)
	defer cancel()

	// Synthesise a zero payload (os.DevNull equivalent via LimitedReader).
	body := io.LimitReader(zeroReader{}, m.cfg.UploadPayloadBytes)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, m.cfg.UploadEndpoint, body)
	if err != nil {
		return m.trafficEstimate().UploadMbps
	}
	req.ContentLength = m.cfg.UploadPayloadBytes
	req.Header.Set("Content-Type", "application/octet-stream")

	start := time.Now()
	resp, err := m.client.Do(req)
	if err != nil {
		return m.trafficEstimate().UploadMbps
	}
	resp.Body.Close()

	elapsed := time.Since(start).Seconds()
	if elapsed == 0 {
		elapsed = 0.001
	}
	return (float64(m.cfg.UploadPayloadBytes) * 8) / (elapsed * 1_000_000)
}

// trafficEstimate returns a bandwidth estimate derived from /proc/net/dev
// counters or falls back to a sensible minimum.
func (m *BandwidthMeter) trafficEstimate() BandwidthResult {
	rx1, tx1, err1 := readProcNetDevBytes()
	if err1 != nil {
		return BandwidthResult{
			UploadMbps:   1.0,
			DownloadMbps: 5.0,
			LatencyMs:    50,
			Source:       "cache",
		}
	}

	time.Sleep(500 * time.Millisecond)

	rx2, tx2, err2 := readProcNetDevBytes()
	if err2 != nil {
		return BandwidthResult{
			UploadMbps:   1.0,
			DownloadMbps: 5.0,
			LatencyMs:    50,
			Source:       "cache",
		}
	}

	rxDelta := float64(rx2-rx1) * 2 // bytes per second (sampled 0.5 s)
	txDelta := float64(tx2-tx1) * 2

	downMbps := (rxDelta * 8) / 1_000_000
	upMbps := (txDelta * 8) / 1_000_000

	// Floor values to avoid reporting 0 when idle.
	if downMbps < 1 {
		downMbps = 1
	}
	if upMbps < 1 {
		upMbps = 1
	}

	return BandwidthResult{
		UploadMbps:   upMbps,
		DownloadMbps: downMbps,
		LatencyMs:    50,
		MeasuredAt:   time.Now().UTC().Format(time.RFC3339),
		Source:       "traffic",
	}
}

// Latest returns the most recently cached result plus whether it is ready.
func (m *BandwidthMeter) Latest() (BandwidthResult, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest, m.ready
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// RegisterBandwidthHandlers registers all bandwidth-related HTTP routes on mux.
// It is the single exported integration point for the peering bandwidth feature.
//
// Routes registered:
//
//	GET /api/peering/bandwidth
//	    Returns own bandwidth metrics (no auth required; auth is the caller's
//	    responsibility at the mux level).
//
//	GET /api/peering/bandwidth/peer
//	    Returns a remote peer's bandwidth by proxying GET /api/peering/bandwidth
//	    against the peer's server. Query param: server=<host:port>.
//	    Only succeeds if the server is reachable (no approval check here — the
//	    caller's auth/trust layer enforces that).
func RegisterBandwidthHandlers(mux *http.ServeMux, meter *BandwidthMeter) {
	mux.HandleFunc("GET /api/peering/bandwidth", meter.handleOwnBandwidth())
	mux.HandleFunc("GET /api/peering/bandwidth/peer", meter.handlePeerBandwidth())
}

// handleOwnBandwidth serves the locally measured bandwidth result.
func (m *BandwidthMeter) handleOwnBandwidth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, ready := m.Latest()
		if !ready {
			// Still measuring on first startup — return 202 Accepted.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "measuring",
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// handlePeerBandwidth fetches bandwidth info from a peer's Vula server.
// Query params:
//
//	server — required — peer's base URL, e.g. "https://bob.vulos.org:8080"
func (m *BandwidthMeter) handlePeerBandwidth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peerBase := r.URL.Query().Get("server")
		if peerBase == "" {
			http.Error(w, `{"error":"missing query param: server"}`, http.StatusBadRequest)
			return
		}

		target := peerBase + "/api/peering/bandwidth"
		reqCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, `{"error":"invalid peer URL"}`, http.StatusBadRequest)
			return
		}
		req.Header.Set("Accept", "application/json")

		resp, err := m.client.Do(req)
		if err != nil {
			http.Error(w, `{"error":"peer unreachable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Forward the peer's response verbatim.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// zeroReader is an io.Reader that returns an infinite stream of zero bytes.
// Used to synthesise upload payloads without allocating large buffers.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// readProcNetDevBytes sums RX and TX byte counters from /proc/net/dev across
// all non-loopback interfaces. Returns (rxBytes, txBytes, error).
func readProcNetDevBytes() (uint64, uint64, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	return parseProcNetDev(string(data))
}

// parseProcNetDev parses the /proc/net/dev file format and returns totals.
// The format has two header lines then one line per interface:
//
//	iface: rx_bytes packets errs drop fifo frame compressed multicast tx_bytes ...
func parseProcNetDev(content string) (uint64, uint64, error) {
	var totalRx, totalTx uint64
	lines := splitLines(content)
	for _, line := range lines[2:] { // skip two header lines
		if len(line) == 0 {
			continue
		}
		var iface string
		var rxBytes, rxPkts, rxErrs, rxDrop, rxFifo, rxFrame, rxCompressed, rxMulticast uint64
		var txBytes uint64
		n, err := scanNetDevLine(line,
			&iface, &rxBytes, &rxPkts, &rxErrs, &rxDrop, &rxFifo, &rxFrame, &rxCompressed, &rxMulticast,
			&txBytes)
		if err != nil || n < 10 {
			continue
		}
		// Skip loopback.
		if iface == "lo:" || iface == "lo" {
			continue
		}
		totalRx += rxBytes
		totalTx += txBytes
	}
	return totalRx, totalTx, nil
}

// splitLines splits a string by newline characters without allocating a slice
// of substrings larger than necessary.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// scanNetDevLine is a minimal sscanf-style parser for /proc/net/dev lines.
// It avoids importing fmt.Sscanf to keep dependencies minimal.
func scanNetDevLine(line string, iface *string, fields ...*uint64) (int, error) {
	// Trim leading whitespace.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return 0, io.EOF
	}

	// Read interface name (ends with ':').
	j := i
	for j < len(line) && line[j] != ' ' && line[j] != '\t' {
		j++
	}
	*iface = line[i:j]
	// Remove trailing colon if present.
	if len(*iface) > 0 && (*iface)[len(*iface)-1] == ':' {
		*iface = (*iface)[:len(*iface)-1]
	}
	i = j

	// Read numeric fields.
	count := 1
	for _, fp := range fields {
		// Skip whitespace.
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i == len(line) {
			break
		}
		// Read digits.
		j = i
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		var val uint64
		for _, ch := range line[i:j] {
			val = val*10 + uint64(ch-'0')
		}
		*fp = val
		i = j
		count++
	}
	return count, nil
}
