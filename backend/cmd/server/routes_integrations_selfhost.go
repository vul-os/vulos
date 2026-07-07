// routes_integrations_selfhost.go — composition-root wiring + CP-vs-self-host
// selection for the OAuth integrations surface (self-host P2).
//
// The selection mirrors the mail/office seam pattern: a single gate at this
// composition root chooses the backing implementation. When a control plane is
// configured the box uses the cloud broker (services/integrations.Client);
// otherwise it uses the sovereign on-box provider (services/integrations/
// selfhost) with the owner's own OAuth app credentials.
package main

import (
	"crypto/sha256"
	"log"
	"net/http"
	"os"
	"strings"

	vulenv "vulos/backend/services/env"
	"vulos/backend/services/integrations/selfhost"
)

// cpConfigured reports whether a Vulos control plane is configured for this box.
// VULOS_CP_URL is the canonical selector named by the self-host contract;
// VULOS_CLOUD_URL is the historical variable the rest of the backend reads, so
// either being set means "cloud mode". When BOTH are unset the box is sovereign
// and the self-host integrations path is used.
func cpConfigured() bool {
	return vulenv.FirstNonEmptyEnv("VULOS_CP_URL", "VULOS_CLOUD_URL") != ""
}

// selfHostStateSecretEnv optionally pins the HMAC key for the CSRF/PKCE state
// cookie. When unset a stable per-box secret is derived from other box secrets so
// the flow works out-of-the-box while still being unforgeable.
const selfHostStateSecretEnv = "INTEGRATIONS_STATE_SECRET"

// selfHostStateSecret returns the HMAC key for the OAuth state cookie. It prefers
// an explicit INTEGRATIONS_STATE_SECRET, else derives a stable key from the box's
// other secrets (INTEGRATIONS_KEK / DEVICE_SHARED_SECRET). Deriving (rather than
// randomizing per boot) keeps an in-flight consent valid across a restart.
func selfHostStateSecret() []byte {
	if v := strings.TrimSpace(os.Getenv(selfHostStateSecretEnv)); v != "" {
		return []byte(v)
	}
	seed := os.Getenv("INTEGRATIONS_KEK") + "|" + os.Getenv("DEVICE_SHARED_SECRET") + "|vulos-integrations-selfhost-state"
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

// registerSelfHostIntegrations wires the sovereign OAuth integrations provider.
//
// It opens the encrypted-at-rest connection store, loads the local KEK
// (fail-closed in prod), builds the service with the production exchangers, and
// registers the session-gated routes. On any hard failure (KEK unset in prod,
// store open error) it logs and registers NOTHING for the integrations surface —
// the box simply has no integrations rather than crashing or leaking an insecure
// custody path.
func registerSelfHostIntegrations(mux *http.ServeMux, dbDir string, activeEnv vulenv.Env) {
	kek, err := selfhost.LoadKEK(activeEnv)
	if err != nil {
		// FAIL-CLOSED: in prod an unset/invalid INTEGRATIONS_KEK means we refuse to
		// custody refresh tokens. Do not register the routes — no insecure fallback.
		log.Printf("[integrations] SELF-HOST path DISABLED: %v (set INTEGRATIONS_KEK to enable sovereign integrations)", err)
		return
	}
	store, err := selfhost.OpenStore(dbDir)
	if err != nil {
		log.Printf("[integrations] SELF-HOST path DISABLED: could not open store: %v", err)
		return
	}
	svc := selfhost.NewService(store, selfhost.DefaultExchangers(), kek)

	// Secure cookie in prod (HTTPS); relaxed off-prod so a laptop http:// dev flow
	// works. The auth session cookie already follows this same posture.
	secureCookie := activeEnv.IsProd()
	selfhost.RegisterHandlers(mux, svc, selfHostStateSecret(), secureCookie)

	// Report which providers the owner has configured (client creds present).
	var configured []string
	for _, p := range []string{selfhost.ProviderGoogle, selfhost.ProviderMicrosoft, selfhost.ProviderDropbox} {
		if selfhost.ProviderConfigured(p) {
			configured = append(configured, p)
		}
	}
	log.Printf("[integrations] SELF-HOST path: registered /api/integrations/{provider}/{connect,callback,status,token} (owner BYO-OAuth; configured providers=%v)", configured)
}
