package appnet

// PUBWEB-02: HTTP handlers for subdomain provisioning.
//
// Hooks into the existing PATCH /api/apps/{id}/visibility endpoint: when an
// app is set to "public", a subdomain is provisioned automatically.
//
// New endpoints:
//
//	GET  /api/apps/{id}/deployment   — return the current Deployment for an app
//	POST /api/apps/{id}/deprovision  — tear down a public subdomain (used when
//	                                   visibility is set back to private/local)
//
// RegisterSubdomainHandlers must be called alongside RegisterVisibilityHandlers
// in the server setup (via the server's own RegisterHandlers aggregate).  It
// does NOT touch main.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// deployWriteJSON writes v as a 200 JSON response.
func deployWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// deployWriteErr writes a JSON error body with the given HTTP status.
func deployWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// deploymentResponse is the JSON shape returned to clients.
type deploymentResponse struct {
	AppID       string `json:"app_id"`
	Profile     string `json:"profile"`
	FQDN        string `json:"fqdn"`
	Provisioned string `json:"provisioned"` // RFC3339
	TLSObtained bool   `json:"tls_obtained"`
}

func toDeploymentResponse(d *Deployment) deploymentResponse {
	return deploymentResponse{
		AppID:       d.AppID,
		Profile:     d.Profile,
		FQDN:        d.FQDN,
		Provisioned: d.Provisioned.Format("2006-01-02T15:04:05Z"),
		TLSObtained: d.TLSObtained,
	}
}

// upstreamAddrForApp returns the reverse-proxy upstream the public-web edge
// (Caddy / nginx) must target for an app.
//
// SECURITY (Finding 1, HIGH): this is the auth gateway's loopback address — NOT
// the app's namespace host port. Previously this returned 127.0.0.1:{hostPort},
// which pointed the public edge straight at the app namespace and BYPASSED the
// :8080 gateway: attacker-supplied X-Vulos-* headers were never stripped and
// visibility was never enforced. Routing through the gateway (which rewrites to
// PubwebPathPrefix+{appID} — see the Caddy/nginx snippet builders) makes the
// gateway's anonymous PublicHandler the single choke point: it strips all seam
// headers, injects no identity, and serves only apps published "public".
//
// The netMgr parameter is retained for signature compatibility (callers pass it)
// but is no longer consulted — the upstream is always the gateway. The
// VULOS_UPSTREAM_<APPID> override still wins for tests / bespoke deployments.
func upstreamAddrForApp(appID string, netMgr *Manager) string {
	envKey := fmt.Sprintf("VULOS_UPSTREAM_%s", appID)
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return GatewayLoopbackAddr()
}

// RegisterSubdomainHandlers registers the PUBWEB-02 endpoints on mux.
//
//	GET  /api/apps/{id}/deployment   — current deployment info
//	POST /api/apps/{id}/deprovision  — remove the deployment
//
// It also wraps the existing visibility mutation so that changing to "public"
// automatically calls Provisioner.ProvisionSubdomain, and changing away from
// "public" calls DeprovisionSubdomain.
//
// The wrapper is registered on the same PATCH/POST paths as
// RegisterVisibilityHandlers, but with a "/deploy" suffix mux.
// To avoid double-registration: call this AFTER RegisterVisibilityHandlers;
// the caller (server setup) must pass the same *http.ServeMux.
func RegisterSubdomainHandlers(mux *http.ServeMux, vis *VisibilityStore, deployer *Provisioner, netMgr *Manager) {
	// GET /api/apps/{id}/deployment — return the stored deployment for an app.
	mux.HandleFunc("GET /api/apps/{id}/deployment", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			deployWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}
		d := deployer.store.Get(appID, "default")
		if d == nil {
			deployWriteErr(w, http.StatusNotFound, "no deployment for app")
			return
		}
		deployWriteJSON(w, toDeploymentResponse(d))
	})

	// POST /api/apps/{id}/deprovision — tear down a deployment.
	mux.HandleFunc("POST /api/apps/{id}/deprovision", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			deployWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}
		if err := deployer.DeprovisionSubdomain(appID, "default"); err != nil {
			deployWriteErr(w, http.StatusInternalServerError, "deprovision: "+err.Error())
			return
		}
		// Also revert visibility to private.
		_ = vis.Set(appID, VisibilityPrivate)
		deployWriteJSON(w, map[string]string{"status": "deprovisioned", "app_id": appID})
	})

	// PATCH /api/apps/{id}/visibility — override to hook subdomain provisioning.
	// We register a new handler under a distinct pattern; the server must call
	// this after RegisterVisibilityHandlers so this override takes precedence.
	// (Go 1.22 ServeMux: more-specific patterns win; both patterns are identical
	//  so last-registered wins — register this AFTER the visibility handlers.)
	//
	// Implementation note: rather than registering a duplicate PATCH route (which
	// would conflict), we expose a separate helper that callers invoke from their
	// own PATCH handler, keeping this file free of route conflicts.
	//
	// The actual hook is performed by SetVisibilityWithProvisioning below.
	_ = mux // mux already received the deployment routes above
}

// SetVisibilityWithProvisioning is the combined visibility-set + auto-provision
// action.  Call this from the PATCH /api/apps/{id}/visibility handler (or any
// handler that mutates visibility) instead of vis.Set directly.
//
// When visibility is "public":
//   - ProvisionSubdomain is called; the FQDN is returned in the response.
//
// When visibility changes away from "public":
//   - DeprovisionSubdomain is called to clean up the subdomain.
func SetVisibilityWithProvisioning(
	ctx context.Context,
	appID, profile, newVisibility string,
	vis *VisibilityStore,
	deployer *Provisioner,
	netMgr *Manager,
) (*Deployment, error) {
	prev := vis.Get(appID)

	if err := vis.Set(appID, newVisibility); err != nil {
		return nil, err
	}

	switch {
	case newVisibility == VisibilityPublic:
		upstream := upstreamAddrForApp(appID, netMgr)
		d, err := deployer.ProvisionSubdomain(ctx, appID, profile, upstream)
		if err != nil {
			// Roll back visibility on provisioning failure.
			_ = vis.Set(appID, prev)
			return nil, fmt.Errorf("provision subdomain: %w", err)
		}
		return d, nil

	case prev == VisibilityPublic && newVisibility != VisibilityPublic:
		// Was public, now private/local — tear down.
		if err := deployer.DeprovisionSubdomain(appID, profile); err != nil {
			return nil, fmt.Errorf("deprovision subdomain: %w", err)
		}
	}

	return nil, nil
}
