package appnet

// PUBWEB-07: HTTP handlers for custom domain support.
//
// Endpoints registered by RegisterCustomDomainHandlers:
//
//	POST   /api/apps/{id}/domain         — attach a custom domain; issues a DNS TXT challenge
//	GET    /api/apps/{id}/domain         — return current custom-domain record (pending/verified)
//	POST   /api/apps/{id}/domain/verify  — check the TXT record and activate the domain
//	DELETE /api/apps/{id}/domain         — remove the custom domain; revert to default subdomain
//
// Does NOT touch main.go.

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// customDomainResponse is the JSON shape returned to clients.
type customDomainResponse struct {
	AppID          string `json:"app_id"`
	Domain         string `json:"domain"`
	ChallengeToken string `json:"challenge_token"`
	TXTRecord      string `json:"txt_record"`  // the exact DNS name to set
	Status         string `json:"status"`      // "pending" | "verified"
	CreatedAt      string `json:"created_at"`  // RFC3339
	VerifiedAt     string `json:"verified_at,omitempty"`
}

func toCustomDomainResponse(cd *CustomDomain) customDomainResponse {
	resp := customDomainResponse{
		AppID:          cd.AppID,
		Domain:         cd.Domain,
		ChallengeToken: cd.ChallengeToken,
		TXTRecord:      cd.TXTRecordName(),
		Status:         cd.Status,
		CreatedAt:      cd.CreatedAt.Format(time.RFC3339),
	}
	if !cd.VerifiedAt.IsZero() {
		resp.VerifiedAt = cd.VerifiedAt.Format(time.RFC3339)
	}
	return resp
}

// cdWriteJSON writes v as a 200 JSON response.
func cdWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// cdWriteErr writes a JSON error body with the given HTTP status.
func cdWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// caddyDirFromEnv returns the Caddy snippet directory, consistent with the
// subdomain provisioner's VULOS_CADDY_DIR env var.
func caddyDirFromEnv() string {
	if v := os.Getenv("VULOS_CADDY_DIR"); v != "" {
		return v
	}
	return "/etc/caddy/vulos-apps"
}

// RegisterCustomDomainHandlers registers the PUBWEB-07 endpoints on mux.
//
// The deployer is used to validate that an app is already publicly deployed
// before a custom domain is attached. netMgr may be nil (used for upstream
// resolution).
func RegisterCustomDomainHandlers(
	mux *http.ServeMux,
	cs *CustomDomainStore,
	deployer *Provisioner,
	netMgr *Manager,
) {
	// -----------------------------------------------------------------------
	// POST /api/apps/{id}/domain — issue a TXT challenge for a new domain
	// -----------------------------------------------------------------------
	mux.HandleFunc("POST /api/apps/{id}/domain", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cdWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		// App must already be publicly deployed.
		if deployer.store.Get(appID, "default") == nil {
			cdWriteErr(w, http.StatusConflict, "app is not publicly deployed; publish it first")
			return
		}

		var body struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Domain == "" {
			cdWriteErr(w, http.StatusBadRequest, "domain field required")
			return
		}

		// Generate a fresh challenge token.
		token, err := generateChallengeToken()
		if err != nil {
			cdWriteErr(w, http.StatusInternalServerError, "generate challenge: "+err.Error())
			return
		}

		cd := &CustomDomain{
			AppID:          appID,
			Domain:         body.Domain,
			ChallengeToken: token,
			Status:         CustomDomainStatusPending,
			CreatedAt:      time.Now().UTC(),
		}
		if err := cs.Set(cd); err != nil {
			cdWriteErr(w, http.StatusInternalServerError, "store domain: "+err.Error())
			return
		}

		w.WriteHeader(http.StatusCreated)
		cdWriteJSON(w, toCustomDomainResponse(cd))
	})

	// -----------------------------------------------------------------------
	// GET /api/apps/{id}/domain — return current custom-domain record
	// -----------------------------------------------------------------------
	mux.HandleFunc("GET /api/apps/{id}/domain", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cdWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}
		cd := cs.Get(appID)
		if cd == nil {
			cdWriteErr(w, http.StatusNotFound, "no custom domain for app")
			return
		}
		cdWriteJSON(w, toCustomDomainResponse(cd))
	})

	// -----------------------------------------------------------------------
	// POST /api/apps/{id}/domain/verify — check TXT record and activate
	// -----------------------------------------------------------------------
	mux.HandleFunc("POST /api/apps/{id}/domain/verify", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cdWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		cd := cs.Get(appID)
		if cd == nil {
			cdWriteErr(w, http.StatusNotFound, "no custom domain for app")
			return
		}
		if cd.Status == CustomDomainStatusVerified {
			// Already verified — idempotent success.
			cdWriteJSON(w, toCustomDomainResponse(cd))
			return
		}

		// Perform live DNS TXT verification.
		if err := VerifyDNSTXT(cd.Domain, cd.ChallengeToken); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"status": CustomDomainStatusPending,
				"error":  err.Error(),
			})
			return
		}

		// Verification succeeded — write Caddy snippet and persist.
		upstream := upstreamAddrForApp(appID, netMgr)
		caddyDir := caddyDirFromEnv()
		if err := writeCustomDomainCaddySnippet(caddyDir, appID, cd.Domain, upstream); err != nil {
			// Non-fatal: log but do not abort activation.
			_ = err
		}

		cd.Status = CustomDomainStatusVerified
		cd.VerifiedAt = time.Now().UTC()
		if err := cs.Set(cd); err != nil {
			cdWriteErr(w, http.StatusInternalServerError, "persist verified domain: "+err.Error())
			return
		}

		cdWriteJSON(w, toCustomDomainResponse(cd))
	})

	// -----------------------------------------------------------------------
	// DELETE /api/apps/{id}/domain — remove custom domain
	// -----------------------------------------------------------------------
	mux.HandleFunc("DELETE /api/apps/{id}/domain", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cdWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		cd := cs.Get(appID)
		if cd == nil {
			cdWriteErr(w, http.StatusNotFound, "no custom domain for app")
			return
		}

		// Remove the Caddy snippet (verified domains may have one).
		caddyDir := caddyDirFromEnv()
		if err := removeCustomDomainCaddySnippet(caddyDir, appID); err != nil {
			cdWriteErr(w, http.StatusInternalServerError, "remove caddy snippet: "+err.Error())
			return
		}

		if err := cs.Delete(appID); err != nil {
			cdWriteErr(w, http.StatusInternalServerError, "delete domain record: "+err.Error())
			return
		}

		cdWriteJSON(w, map[string]string{
			"status": "removed",
			"app_id": appID,
		})
	})
}
