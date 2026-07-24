package relayconfig

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TestResult is returned by TestReachability — mirrors network.TURNTestResult
// so the Settings panel can reuse the same rendering it already has for
// /api/turn/test.
type TestResult struct {
	Success   bool  `json:"success"`
	LatencyMS int64 `json:"latency_ms,omitempty"`
	// Detail carries either the failure reason or a human note for providers
	// that have nothing to TCP-dial (none/libp2p).
	Detail string `json:"detail,omitempty"`
}

const testDialTimeout = 5 * time.Second

// TestReachability performs a best-effort, ADMIN-ONLY-gated (at the HTTP
// layer) reachability probe of the currently-configured provider:
//   - ephor: dials the box's own coturn if TURN_SECRET is set; otherwise
//     reports that only public STUN is in play (nothing to dial).
//   - turn:   TCP-dials the host:port of the first configured ICE server.
//   - wireguard: TCP-dials the coordinator endpoint.
//   - none:   nothing to test — always reports success (relay is
//     intentionally off).
//   - libp2p: not TCP-dialable from a simple host:port probe; reports that
//     explicitly rather than a misleading pass/fail.
func TestReachability() TestResult {
	cfg := currentConfig()
	switch cfg.Provider {
	case ProviderNone:
		return TestResult{Success: true, Detail: "relay disabled — direct connections only (static IP / port-forward); nothing to test"}
	case ProviderTURN:
		host, port, ok := firstICEHostPort(cfg.TURN.ICEServers)
		if !ok {
			return TestResult{Success: false, Detail: "no ICE servers configured"}
		}
		return dialTest(host, port)
	case ProviderWireGuard:
		host, port, ok := wireGuardHostPort(cfg.WireGuard.Endpoint)
		if !ok {
			return TestResult{Success: false, Detail: "no endpoint configured"}
		}
		return dialTest(host, port)
	case ProviderLibp2p:
		return TestResult{Success: false, Detail: "reachability test not supported for libp2p relay peers — check the box logs for a successful Circuit Relay v2 reservation instead"}
	default: // ephor
		tc := effectiveTURNConfig()
		if !tc.Enabled {
			return TestResult{Success: true, Detail: "ephor default — public STUN only (configure TURN in Settings, or set TURN_SECRET, to enable this box's own TURN relay)"}
		}
		return dialTest(tc.Host, strconv.Itoa(tc.Port))
	}
}

// firstICEHostPort extracts a dialable host:port from the first URL of the
// first configured ICE server.
func firstICEHostPort(servers []ICEServer) (host, port string, ok bool) {
	for _, s := range servers {
		for _, u := range s.URLs {
			if h, p, found := parseICEHostPort(u); found {
				return h, p, true
			}
		}
	}
	return "", "", false
}

// parseICEHostPort pulls host[:port] out of a stun:/stuns:/turn:/turns: URI,
// defaulting the port by scheme when the URI omits one (3478 for stun/turn,
// 5349 for stuns/turns — the IANA-registered defaults).
func parseICEHostPort(raw string) (host, port string, ok bool) {
	s := strings.TrimSpace(raw)
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", false
	}
	scheme := strings.ToLower(s[:idx])
	rest := s[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[:q]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(rest); err == nil {
		return h, p, true
	}
	// No port in the URI — default by scheme.
	switch scheme {
	case "stuns", "turns":
		return rest, "5349", true
	default:
		return rest, "3478", true
	}
}

// wireGuardHostPort extracts a dialable host:port from a WireGuard endpoint
// (either "host:port" or an http(s) URL).
func wireGuardHostPort(endpoint string) (host, port string, ok bool) {
	s := strings.TrimSpace(endpoint)
	if s == "" {
		return "", "", false
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		p := u.Port()
		if p == "" {
			if u.Scheme == "https" {
				p = "443"
			} else {
				p = "80"
			}
		}
		return u.Hostname(), p, true
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", false
	}
	return h, p, true
}

// dialTest TCP-dials host:port and reports latency — same primitive as
// network.TURNStore.TestReachability, duplicated locally to keep this
// package dependency-free of that store (which is a different, legacy
// single-coturn-host config surface).
func dialTest(host, port string) TestResult {
	addr := net.JoinHostPort(host, port)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, testDialTimeout)
	latency := time.Since(start)
	if err != nil {
		return TestResult{Success: false, LatencyMS: latency.Milliseconds(), Detail: err.Error()}
	}
	conn.Close()
	return TestResult{Success: true, LatencyMS: latency.Milliseconds()}
}
