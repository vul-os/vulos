package consoleboot

// relayscale_e2e_test.go — end-to-end proof that the relay-scaling #41 CONTROL
// LOOP is real and its state flows through to the surfaces the console reads.
//
// It boots the console-enabled OSS control plane on a real port, pushes per-region
// load through the secret-gated observe endpoint exactly as a relay PoP would, and
// then asserts:
//
//   1. GET /api/relay/scale/demand           reflects the observation synchronously
//      (published desired state — what an external scaler polls).
//   2. GET /api/relay/scale/controller       reflects it after the background loop
//      ticks (the #41 control loop actually consumed the observed demand — real
//      feedback, not a static echo).
//   3. GET /api/superadmin/relay-scale        (the gated OPERATOR view) is mounted
//      and FAILS CLOSED for an unauthenticated caller (never a fail-open 2xx).
//
// This is the real-data + fail-closed-gating proof for the operator Relay-scaling
// console panel.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRelayScale_ControlLoop_E2E(t *testing.T) {
	// Fast reconcile cadence so the background loop demonstrably picks up the
	// observation within the test window, and a shared secret so observe is live.
	// Set BEFORE booting: ControllerConfigFromEnv is read during server assembly.
	t.Setenv("RELAY_SCALE_INTERVAL", "40ms")
	t.Setenv("CP_SHARED_SECRET", "relayscale-e2e-shared-secret")

	_, base, client := bootConsoleServerOnPort(t)

	// ── Push per-region load (as a relay PoP / aggregator would) ──────────────
	const region = "eu-central"
	observe := `{"regions":[{"region":"` + region + `","instances":2,"saturation":0.92}]}`
	code, body := postRelay(t, client, base+"/api/relay/scale/observe", "relayscale-e2e-shared-secret", observe)
	if code != http.StatusOK {
		t.Fatalf("POST /api/relay/scale/observe = %d, want 200; body=%s", code, body)
	}

	// ── 1) Demand reflects it synchronously ───────────────────────────────────
	code, body = getE2E(t, client, base+"/api/relay/scale/demand", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/relay/scale/demand = %d, want 200; body=%s", code, body)
	}
	var demand struct {
		Provisioner string `json:"provisioner"`
		Actuated    bool   `json:"actuated"`
		Regions     []struct {
			Region  string `json:"region"`
			Current int    `json:"current"`
			Desired int    `json:"desired"`
		} `json:"regions"`
	}
	if err := json.Unmarshal([]byte(body), &demand); err != nil {
		t.Fatalf("decode demand: %v; body=%s", err, body)
	}
	if demand.Provisioner != "manual" || demand.Actuated {
		t.Fatalf("demand provisioner/actuated = %q/%v, want manual/false (advisory)", demand.Provisioner, demand.Actuated)
	}
	var demandOK bool
	for _, r := range demand.Regions {
		if r.Region == region {
			demandOK = true
			if r.Desired < r.Current {
				t.Fatalf("saturated region: demand desired=%d < current=%d (no scale-up signal)", r.Desired, r.Current)
			}
		}
	}
	if !demandOK {
		t.Fatalf("demand did not reflect observed region %q; body=%s", region, body)
	}

	// ── 2) The background #41 control loop consumed the demand ────────────────
	deadline := time.Now().Add(3 * time.Second)
	var ctrlOK bool
	var lastBody string
	for time.Now().Before(deadline) && !ctrlOK {
		code, lastBody = getE2E(t, client, base+"/api/relay/scale/controller", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /api/relay/scale/controller = %d, want 200; body=%s", code, lastBody)
		}
		var ctrl struct {
			Provisioner string `json:"provisioner"`
			Regions     []struct {
				Region  string `json:"region"`
				Desired int    `json:"desired"`
			} `json:"regions"`
		}
		if err := json.Unmarshal([]byte(lastBody), &ctrl); err != nil {
			t.Fatalf("decode controller: %v; body=%s", err, lastBody)
		}
		for _, r := range ctrl.Regions {
			if r.Region == region {
				ctrlOK = true
			}
		}
		if !ctrlOK {
			time.Sleep(30 * time.Millisecond)
		}
	}
	if !ctrlOK {
		t.Fatalf("#41 control loop did not surface observed region %q within 3s; last body=%s", region, lastBody)
	}

	// ── 3) The GATED operator view is mounted and fails closed unauth ─────────
	code, body = getE2E(t, client, base+"/api/superadmin/relay-scale", nil)
	if code == http.StatusNotFound {
		t.Fatalf("GET /api/superadmin/relay-scale = 404 — gated operator view not mounted")
	}
	if code >= 200 && code < 300 {
		t.Fatalf("GET /api/superadmin/relay-scale (no session) = %d — FAIL-OPEN operator surface; body=%s", code, body)
	}
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("GET /api/superadmin/relay-scale (no session) = %d, want 401/403 (fail-closed); body=%s", code, body)
	}
}

// postRelay issues a POST with the relay shared-secret header (X-Relay-Auth), the
// same header a relay PoP uses for its CP calls.
func postRelay(t *testing.T, c *http.Client, url, secret, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Relay-Auth", secret)
	resp, err := c.Do(r)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
