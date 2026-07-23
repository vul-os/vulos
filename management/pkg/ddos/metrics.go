package ddos

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// requestSample records the path and status of one request for the live dashboard.
type requestSample struct {
	ts     time.Time
	path   string
	status int
	ip     string
}

// MetricsCollector accumulates per-request data for the DDoS dashboard.
// All operations are goroutine-safe.
type MetricsCollector struct {
	mu      sync.Mutex
	samples []requestSample

	// IPCounts tracks request counts per IP in the last hour.
	ipCounts map[string]int64
	// RouteCounts tracks 4xx counts per route in the last hour.
	route4xxCounts map[string]int64
	// HoneypotHits records recent honeypot hits.
	honeypotHits []HoneypotHit

	totalRequests atomic.Int64
	lastGC        time.Time
}

// HoneypotHit records a single honeypot trigger.
type HoneypotHit struct {
	IP   string
	Path string
	At   time.Time
}

// NewMetricsCollector creates a MetricsCollector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		ipCounts:       make(map[string]int64),
		route4xxCounts: make(map[string]int64),
		lastGC:         time.Now(),
	}
}

// Record adds a request sample.
func (m *MetricsCollector) Record(ip, path string, status int) {
	m.totalRequests.Add(1)
	now := time.Now()
	sample := requestSample{ts: now, path: path, status: status, ip: ip}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.samples = append(m.samples, sample)
	m.ipCounts[ip]++
	if status >= 400 && status < 500 {
		m.route4xxCounts[path]++
	}

	// Lazy GC: evict samples older than 1h.
	if now.Sub(m.lastGC) > 5*time.Minute {
		cutoff := now.Add(-time.Hour)
		fresh := m.samples[:0]
		freshIP := make(map[string]int64)
		freshRoute := make(map[string]int64)
		for _, s := range m.samples {
			if s.ts.After(cutoff) {
				fresh = append(fresh, s)
				freshIP[s.ip]++
				if s.status >= 400 && s.status < 500 {
					freshRoute[s.path]++
				}
			}
		}
		m.samples = fresh
		m.ipCounts = freshIP
		m.route4xxCounts = freshRoute
		m.lastGC = now
	}
}

// RecordHoneypot records a honeypot hit.
func (m *MetricsCollector) RecordHoneypot(ip, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.honeypotHits = append(m.honeypotHits, HoneypotHit{IP: ip, Path: path, At: time.Now()})
	// Keep last 100.
	if len(m.honeypotHits) > 100 {
		m.honeypotHits = m.honeypotHits[len(m.honeypotHits)-100:]
	}
}

// RequestsPerSecLast5Min returns the average requests/sec over the last 5 minutes.
func (m *MetricsCollector) RequestsPerSecLast5Min() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	count := 0
	for _, s := range m.samples {
		if s.ts.After(cutoff) {
			count++
		}
	}
	return float64(count) / (5 * 60)
}

// TopIPsByRequestCount returns the top N IPs by request count in the last hour.
func (m *MetricsCollector) TopIPsByRequestCount(n int) []IPStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return topNInt64(m.ipCounts, n)
}

// Top4xxRoutes returns the top N routes by 4xx count in the last hour.
func (m *MetricsCollector) Top4xxRoutes(n int) []IPStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return topNInt64(m.route4xxCounts, n)
}

// RecentHoneypotHits returns recent honeypot hits (most recent first).
func (m *MetricsCollector) RecentHoneypotHits(n int) []HoneypotHit {
	m.mu.Lock()
	defer m.mu.Unlock()
	hits := m.honeypotHits
	if len(hits) > n {
		hits = hits[len(hits)-n:]
	}
	// Reverse for most-recent-first.
	out := make([]HoneypotHit, len(hits))
	for i, h := range hits {
		out[len(hits)-1-i] = h
	}
	return out
}

// RateSparkline produces a simple ASCII sparkline of req/sec for the last 5min (30 buckets of 10s each).
func (m *MetricsCollector) RateSparkline() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	const buckets = 30
	const bucketSize = 10 * time.Second
	counts := make([]int, buckets)

	for _, s := range m.samples {
		age := now.Sub(s.ts)
		if age < 0 || age >= buckets*bucketSize {
			continue
		}
		idx := int(age / bucketSize)
		if idx < buckets {
			counts[buckets-1-idx]++
		}
	}

	sparks := "▁▂▃▄▅▆▇█"
	maxV := 1
	for _, c := range counts {
		if c > maxV {
			maxV = c
		}
	}
	line := ""
	for _, c := range counts {
		idx := int(float64(c) / float64(maxV) * float64(len([]rune(sparks))-1))
		runes := []rune(sparks)
		if idx >= len(runes) {
			idx = len(runes) - 1
		}
		line += string(runes[idx])
	}
	return line
}

// IPStat is a key-value count pair for top-N display.
type IPStat struct {
	Key   string
	Count int64
}

// topNInt64 returns the top n entries from a map[string]int64.
func topNInt64(m map[string]int64, n int) []IPStat {
	type kv struct {
		k string
		v int64
	}
	var all []kv
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	// Simple selection sort for small n.
	result := make([]IPStat, 0, n)
	used := make(map[string]bool)
	for i := 0; i < n && i < len(all); i++ {
		best := kv{"", -1}
		for _, item := range all {
			if !used[item.k] && item.v > best.v {
				best = item
			}
		}
		if best.k != "" {
			result = append(result, IPStat{Key: best.k, Count: best.v})
			used[best.k] = true
		}
	}
	return result
}

// MetricsMiddleware wraps a handler to record requests in the collector.
func MetricsMiddleware(mc *MetricsCollector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := &metricsResponseWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			mc.Record(RealClientIP(r), r.URL.Path, rw.status)
		})
	}
}

// metricsResponseWriter captures the status code.
type metricsResponseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (m *metricsResponseWriter) WriteHeader(status int) {
	if !m.wrote {
		m.status = status
		m.wrote = true
	}
	m.ResponseWriter.WriteHeader(status)
}
func (m *metricsResponseWriter) Write(b []byte) (int, error) {
	if !m.wrote {
		m.status = 200
		m.wrote = true
	}
	return m.ResponseWriter.Write(b)
}
