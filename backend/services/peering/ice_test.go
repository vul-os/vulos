package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestICEConfigAlwaysIncludesSTUN verifies that the endpoint returns at least
// one STUN server regardless of whether TURN is configured.
func TestICEConfigAlwaysIncludesSTUN(t *testing.T) {
	t.Setenv("TURN_SECRET", "") // ensure TURN is disabled

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	rec := httptest.NewRecorder()
	handleICEConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp iceConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.IceServers) == 0 {
		t.Fatal("expected at least one ICE server, got none")
	}

	// At least one entry must contain a stun: URL.
	hasSTUN := false
	for _, s := range resp.IceServers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "stun:") {
				hasSTUN = true
			}
		}
	}
	if !hasSTUN {
		t.Errorf("response contains no stun: URLs: %+v", resp.IceServers)
	}
}

// TestICEConfigNoTURNWhenSecretAbsent verifies that no TURN entry is returned
// when TURN_SECRET is unset.
func TestICEConfigNoTURNWhenSecretAbsent(t *testing.T) {
	t.Setenv("TURN_SECRET", "")

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	rec := httptest.NewRecorder()
	handleICEConfig(rec, req)

	var resp iceConfigResponse
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck

	for _, s := range resp.IceServers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") {
				t.Errorf("expected no turn: URL without TURN_SECRET, got %q", u)
			}
		}
	}
}

// TestICEConfigTURNCredsWhenSecretSet verifies that short-lived TURN
// credentials are included when TURN_SECRET is configured.
func TestICEConfigTURNCredsWhenSecretSet(t *testing.T) {
	t.Setenv("TURN_SECRET", "test-shared-secret")
	t.Setenv("TURN_PORT", "3478")

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	req.Header.Set("X-User-ID", "user-abc")
	rec := httptest.NewRecorder()
	handleICEConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp iceConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	hasTURN := false
	var turnEntry iceServer
	for _, s := range resp.IceServers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") {
				hasTURN = true
				turnEntry = s
			}
		}
	}

	if !hasTURN {
		t.Fatal("expected at least one turn: URL when TURN_SECRET is set")
	}

	// Username must be present (time-based HMAC credential).
	if turnEntry.Username == "" {
		t.Error("TURN entry missing username")
	}
	// Credential must be present.
	if turnEntry.Credential == "" {
		t.Error("TURN entry missing credential")
	}
	// TTL must be positive.
	if turnEntry.TTL <= 0 {
		t.Errorf("TURN TTL should be positive, got %d", turnEntry.TTL)
	}
}

// TestICEConfigTURNCredsAreShortLived verifies that the TURN username encodes
// an expiry timestamp that is in the future (i.e. credentials are time-bounded).
func TestICEConfigTURNCredsAreShortLived(t *testing.T) {
	t.Setenv("TURN_SECRET", "another-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	req.Header.Set("X-User-ID", "user-xyz")
	rec := httptest.NewRecorder()
	handleICEConfig(rec, req)

	var resp iceConfigResponse
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck

	for _, s := range resp.IceServers {
		if s.Username == "" {
			continue // STUN entry
		}
		// Username format is "<unix_timestamp>:<user_id>"
		var expiry int64
		_, err := func() (int, error) {
			n, scanErr := countScanf(s.Username, &expiry)
			return n, scanErr
		}()
		if err != nil {
			t.Logf("parsing username %q: %v (non-fatal, format may differ)", s.Username, err)
			continue
		}
		// The expiry must be strictly after now.
		if expiry <= time.Now().Unix() {
			t.Errorf("TURN credential expiry %d is not in the future (now=%d)", expiry, time.Now().Unix())
		}
	}
}

// countScanf is a thin wrapper so the compiler doesn't complain about unused imports.
func countScanf(s string, expiry *int64) (int, error) {
	n, err := parseExpiry(s, expiry)
	return n, err
}

// parseExpiry extracts the leading Unix timestamp from a TURN username of the
// form "<expiry>:<user_id>". Returns 1 on success.
func parseExpiry(username string, out *int64) (int, error) {
	idx := strings.Index(username, ":")
	if idx < 0 {
		return 0, nil
	}
	ts := username[:idx]
	var v int64
	if _, err := func() (int, error) {
		n, e := parseInt64(ts, &v)
		return n, e
	}(); err != nil {
		return 0, err
	}
	*out = v
	return 1, nil
}

// parseInt64 parses a decimal string into *v. Returns (1, nil) on success.
func parseInt64(s string, v *int64) (int, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int64(c-'0')
	}
	*v = n
	return 1, nil
}

// TestICEConfigMethodNotAllowed verifies that non-GET methods return 405.
func TestICEConfigMethodNotAllowed(w *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/peering/ice", nil)
		rec := httptest.NewRecorder()
		handleICEConfig(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			w.Errorf("method %s: expected 405, got %d", method, rec.Code)
		}
	}
}

// TestRegisterICEHandlers verifies RegisterICEHandlers wires the route without
// panicking and that the mux can dispatch to it.
func TestRegisterICEHandlers(t *testing.T) {
	t.Setenv("TURN_SECRET", "")

	mux := http.NewServeMux()
	RegisterICEHandlers(mux) // must not panic

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from mux, got %d", rec.Code)
	}
}

// TestICEConfigGuestFallback verifies that when no X-User-ID header is
// present, TURN credentials are still generated (using the "guest" fallback).
func TestICEConfigGuestFallback(t *testing.T) {
	t.Setenv("TURN_SECRET", "guest-secret")

	req := httptest.NewRequest(http.MethodGet, "/api/peering/ice", nil)
	// Deliberately omit X-User-ID header.
	rec := httptest.NewRecorder()
	handleICEConfig(rec, req)

	var resp iceConfigResponse
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck

	for _, s := range resp.IceServers {
		if s.Username == "" {
			continue // STUN entry
		}
		if !strings.Contains(s.Username, "guest") {
			t.Errorf("expected username to contain 'guest', got %q", s.Username)
		}
	}
}
