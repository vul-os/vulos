package main

// routes_arch_test.go — ARCH-01 route gate.
//
// GET /api/system/arch is the App Hub's source for "what can this box install".
// The failure it must never have is a plausible one: reporting the CLIENT's
// architecture. That agrees with the server on every machine a developer tests
// on and disagrees on exactly the mixed setups Vulos is built for — an arm64
// Mac driving an amd64 box, which must be offered amd64 apps.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/services/appnet"
)

func archResponse(t *testing.T, mutate func(*http.Request)) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/arch", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"arch":      appnet.BoxArch(),
			"supported": appnet.SupportedArches(),
		})
	})
	req := httptest.NewRequest("GET", "/api/system/arch", nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestSystemArch_ReportsTheBox pins the shape the App Hub reads
// (arch.ts: `GET /api/system/arch -> {"arch": "amd64"}`) and that it is the
// box's own, Debian-spelled.
func TestSystemArch_ReportsTheBox(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "x86_64")
	appnet.InvalidateArchCache()
	defer appnet.InvalidateArchCache()

	out := archResponse(t, nil)
	if out["arch"] != "amd64" {
		t.Fatalf(`arch = %v, want "amd64" — the box is x86_64 and the API speaks `+
			`Debian spelling; a raw x86_64 here would fail every registry `+
			`comparison silently`, out["arch"])
	}
	sup, ok := out["supported"].([]any)
	if !ok || len(sup) == 0 {
		t.Fatalf("supported = %v, want a non-empty list", out["supported"])
	}
	if sup[0] != "amd64" {
		t.Fatalf("supported[0] = %v, want the native amd64 first", sup[0])
	}
}

// TestSystemArch_IgnoresTheClient. Every one of these headers is a plausible
// place to derive an architecture from, and every one of them is the client's.
// The answer must not move.
func TestSystemArch_IgnoresTheClient(t *testing.T) {
	t.Setenv("VULOS_BOX_ARCH", "arm64")
	appnet.InvalidateArchCache()
	defer appnet.InvalidateArchCache()

	baseline := archResponse(t, nil)
	if baseline["arch"] != "arm64" {
		t.Fatalf("baseline arch = %v, want arm64", baseline["arch"])
	}

	for _, h := range []struct{ key, val string }{
		{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"},
		{"Sec-CH-UA-Arch", `"x86"`},
		{"Sec-CH-UA-Platform", `"Windows"`},
		{"X-Arch", "amd64"},
		{"X-Forwarded-Arch", "amd64"},
	} {
		got := archResponse(t, func(r *http.Request) { r.Header.Set(h.key, h.val) })
		if got["arch"] != "arm64" {
			t.Fatalf("a request carrying %s: %s changed the reported box arch to %v. "+
				"Desktop apps run ON the box — an x86 laptop driving an arm64 box "+
				"must still be told arm64.", h.key, h.val, got["arch"])
		}
	}
}
