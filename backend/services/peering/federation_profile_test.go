package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoadFederationProfile_AllUnsetDefaultsToCentral verifies the fully-off
// default: no relay/rendezvous, central directory, no TURN, public STUN on
// (implicitly, via stunURLs default) and NOT sovereign.
func TestLoadFederationProfile_AllUnsetDefaultsToCentral(t *testing.T) {
	t.Setenv("VULOS_RELAY_BASE_URL", "")
	t.Setenv("VULOS_VERIFY_URL", "")
	t.Setenv("VULOS_RENDEZVOUS_URL", "")
	t.Setenv("TURN_SECRET", "")
	t.Setenv("VULOS_STUN_DISABLE_PUBLIC", "")

	p := LoadFederationProfile()
	if p.RelayConfigured || p.RelayBaseURL != "" {
		t.Fatalf("expected no relay configured, got %+v", p)
	}
	if p.VerifyConfigured {
		t.Fatal("expected verify NOT explicitly configured (using central default)")
	}
	if p.VerifyURL != discoveryDefaultBaseURL {
		t.Fatalf("VerifyURL = %q, want central default %q", p.VerifyURL, discoveryDefaultBaseURL)
	}
	if p.RendezvousConfigured {
		t.Fatal("expected rendezvous not configured")
	}
	if p.RendezvousURL != proxDefaultRendezvousBase {
		t.Fatalf("RendezvousURL = %q, want central default %q", p.RendezvousURL, proxDefaultRendezvousBase)
	}
	if p.TURNEnabled {
		t.Fatal("expected TURN disabled")
	}
	if p.NoThirdPartySTUN {
		t.Fatal("must not report sovereign STUN posture with nothing configured")
	}
}

// TestLoadFederationProfile_FullySovereign verifies the "no third-party STUN"
// signal only fires when BOTH self-hosted TURN is on AND public STUN is
// explicitly disabled, alongside relay/rendezvous being reported when set.
func TestLoadFederationProfile_FullySovereign(t *testing.T) {
	t.Setenv("VULOS_RELAY_BASE_URL", "https://relay.example.net")
	t.Setenv("VULOS_VERIFY_URL", "https://directory.example.net")
	t.Setenv("VULOS_RENDEZVOUS_URL", "https://rendezvous.example.net")
	t.Setenv("TURN_SECRET", "s3cr3t")
	t.Setenv("TURN_HOST", "turn.example.net")
	t.Setenv("VULOS_STUN_DISABLE_PUBLIC", "1")

	p := LoadFederationProfile()
	if !p.RelayConfigured || p.RelayBaseURL != "https://relay.example.net" {
		t.Fatalf("relay not reported correctly: %+v", p)
	}
	if !p.VerifyConfigured || p.VerifyURL != "https://directory.example.net" {
		t.Fatalf("verify not reported correctly: %+v", p)
	}
	if !p.RendezvousConfigured || p.RendezvousURL != "https://rendezvous.example.net" {
		t.Fatalf("rendezvous not reported correctly: %+v", p)
	}
	if !p.TURNEnabled || p.TURNHost != "turn.example.net" {
		t.Fatalf("TURN not reported correctly: %+v", p)
	}
	if !p.PublicSTUNDisabled {
		t.Fatal("expected public STUN reported disabled")
	}
	if !p.NoThirdPartySTUN {
		t.Fatal("expected fully-sovereign STUN posture (self-hosted TURN + public STUN disabled)")
	}
}

// TestLoadFederationProfile_TURNWithoutDisablingPublicIsNotSovereign verifies
// that self-hosting TURN alone does not (and should not) claim the
// no-third-party-STUN posture — the public fallback is still in play unless
// explicitly turned off.
func TestLoadFederationProfile_TURNWithoutDisablingPublicIsNotSovereign(t *testing.T) {
	t.Setenv("TURN_SECRET", "s3cr3t")
	t.Setenv("VULOS_STUN_DISABLE_PUBLIC", "")

	p := LoadFederationProfile()
	if !p.TURNEnabled {
		t.Fatal("expected TURN enabled")
	}
	if p.NoThirdPartySTUN {
		t.Fatal("must not report sovereign STUN posture while public STUN is still enabled")
	}
}

// TestRegisterFederationProfileHandler_ServesJSON verifies the HTTP wiring.
func TestRegisterFederationProfileHandler_ServesJSON(t *testing.T) {
	t.Setenv("VULOS_RELAY_BASE_URL", "https://relay.example.net")

	mux := http.NewServeMux()
	RegisterFederationProfileHandler(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/federation", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var p FederationProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !p.RelayConfigured {
		t.Fatal("expected relay_configured=true in the served JSON")
	}
}
