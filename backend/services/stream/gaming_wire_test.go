package stream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterGamingHandlers_MountsRoutes verifies the gaming HTTP surface is
// actually reachable once wired (previously it was orphaned — never mounted in
// cmd/server). The capability route must respond 200 with a JSON body carrying
// the hardware_encode flag the UI reads; the owner-gated routes must reject an
// unauthenticated caller rather than 404 (proving they are mounted).
func TestRegisterGamingHandlers_MountsRoutes(t *testing.T) {
	mux := http.NewServeMux()
	NewGamingManager(NewPool()).RegisterGamingHandlers(mux)

	// capability — public read, deterministic (reads gpu.Detect(), headless-safe).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/gaming/capability", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capability: status=%d want 200", rec.Code)
	}
	var cap GamingCapability
	if err := json.Unmarshal(rec.Body.Bytes(), &cap); err != nil {
		t.Fatalf("capability body not GamingCapability JSON: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hardware_encode") {
		t.Fatalf("capability body missing hardware_encode: %s", rec.Body.String())
	}

	// active — owner-gated; unauthenticated (no X-User-ID) must 401, NOT 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/gaming/active", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("active route not mounted (404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("active without auth: status=%d want 401", rec.Code)
	}

	// start — owner-gated; unauthenticated must 401, NOT 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(`{}`)))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("start route not mounted (404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("start without auth: status=%d want 401", rec.Code)
	}
}

// TestSession_GamingExposedInJSON verifies the Session serialises the gaming
// flag (json:"gaming"). The launch path relies on this: the frontend reads
// data.gaming from the launch-app response to decide whether to engage gaming
// input behaviour (pointer-lock, split channels) — only for real games.
func TestSession_GamingExposedInJSON(t *testing.T) {
	on, err := json.Marshal(&Session{ID: "g1", Gaming: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(on), `"gaming":true`) {
		t.Fatalf("gaming=true not exposed in JSON: %s", on)
	}
	off, err := json.Marshal(&Session{ID: "d1", Gaming: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(off), `"gaming":false`) {
		t.Fatalf("gaming=false not exposed in JSON: %s", off)
	}
}
