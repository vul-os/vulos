package cproutes

// wire_relayscale_test.go — proves the operator relay-scaling VIEW is backed by
// the SAME live control-loop state the control plane acts on (real data, not a
// mock), and that the demand snapshot reflects load actually pushed through the
// secret-gated observe endpoint.
//
// The fail-closed GATING of the operator endpoint (GET /api/superadmin/relay-scale)
// is proven separately, end-to-end over the wire, in
// test/consoleboot/consoleboot_test.go (portal-user + no-session denials).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/relayscale"
)

// TestRelayScaleSurface_AdminViewReflectsObservedLoad pushes per-region load
// through the real (secret-gated) observe endpoint and asserts the operator
// AdminView — the exact value threaded into GET /api/superadmin/relay-scale —
// reflects that observation in its demand snapshot, with an honest advisory mode
// for the default manual provisioner.
func TestRelayScaleSurface_AdminViewReflectsObservedLoad(t *testing.T) {
	t.Setenv("CP_SHARED_SECRET", "relayscale-view-test-secret")

	s := newRelayScaleSurface(relayscale.NewManualProvisioner())
	defer s.stop()

	mux := http.NewServeMux()
	s.mountPublic(mux)

	// Observe: a saturated region should drive desired above current (scale-up).
	body := `{"regions":[{"region":"eu-central","instances":2,"saturation":0.95}]}`
	r := httptest.NewRequest(http.MethodPost, "/api/relay/scale/observe", strings.NewReader(body))
	r.Header.Set("X-Relay-Auth", "relayscale-view-test-secret")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /observe = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	view := s.AdminView()

	// Controller identity + honest actuation posture (manual = advisory).
	if view.Controller.Provisioner != "manual" {
		t.Fatalf("controller.provisioner = %q, want manual", view.Controller.Provisioner)
	}
	if view.Controller.Actuated {
		t.Fatalf("manual provisioner must be ADVISORY (actuated=false), got actuated=true")
	}

	// Demand is computed synchronously from the SAME store the observe filled, so
	// the observed region must appear with a desired count reflecting the load.
	var found bool
	for _, rp := range view.Demand.Regions {
		if rp.Region == "eu-central" {
			found = true
			if rp.Current != 2 {
				t.Errorf("demand current = %d, want 2 (as observed)", rp.Current)
			}
			if rp.Desired < rp.Current {
				t.Errorf("saturated region desired=%d < current=%d — policy did not scale up", rp.Desired, rp.Current)
			}
		}
	}
	if !found {
		t.Fatalf("AdminView demand did not reflect the observed region; regions=%+v", view.Demand.Regions)
	}
}

// TestRelayScaleSurface_ObserveFailsClosed proves the observe endpoint never
// accepts anonymous load pushes when no shared secret is configured.
func TestRelayScaleSurface_ObserveFailsClosed(t *testing.T) {
	t.Setenv("CP_SHARED_SECRET", "") // explicitly no secret

	s := newRelayScaleSurface(nil) // nil → manual
	defer s.stop()
	mux := http.NewServeMux()
	s.mountPublic(mux)

	r := httptest.NewRequest(http.MethodPost, "/api/relay/scale/observe",
		strings.NewReader(`{"regions":[{"region":"x","instances":1,"saturation":0.5}]}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /observe with no CP_SHARED_SECRET = %d, want 503 (fail-closed); body=%s", rr.Code, rr.Body.String())
	}
}
