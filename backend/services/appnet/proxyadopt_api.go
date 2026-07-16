package appnet

// proxyadopt_api.go — ADOPT-A-PORT HTTP surface.
//
// Lets a signed-in owner point the OS at an already-running LOCAL (loopback)
// service so it is reachable as a first-class OS app — but KEPT BEHIND the :8080
// auth gateway, NEVER the public-web path. Registration produces an
// ExternalUpstream (see external_upstream.go) that gateway.Handler resolves via
// GetForProfile, so the adopted port inherits the FULL pipeline: session auth,
// entitlement gating, X-Vulos-* strip + audience-bound identity inject
// (including the per-app derived X-Vulos-Session — never the raw session), and
// rate limiting.
//
// Routes:
//
//	POST   /api/apps/proxy       — adopt {name, port, profile?, requires_product?}
//	GET    /api/apps/proxy       — list the caller's adopted upstreams (+ health)
//	DELETE /api/apps/proxy/{id}  — revoke one adopted upstream
//
// All three are session-gated by the global auth middleware (X-User-ID is
// stamped there and is never client-supplied) and are owner-scoped: a caller
// only ever sees / mutates upstreams keyed to its own user id.

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProxyAdoptDeps carries the collaborators the adopt-a-port handlers need,
// injected so this package need not import the gateway (which imports appnet).
type ProxyAdoptDeps struct {
	Mgr   *Manager
	Store *ExternalUpstreamStore

	// OnRegister is called after a successful adopt so the gateway can record any
	// required product entitlement (appGateway.AllowApp). Safe to leave nil.
	OnRegister func(appID, product string)
	// OnRemove is called on revoke so the gateway can drop the app's secret and
	// manifest-derived grants (RemoveAppSecret + RemoveAppGrants). Safe nil.
	OnRemove func(appID string)
}

type adoptRequest struct {
	Name            string `json:"name"`
	Port            int    `json:"port"`
	Profile         string `json:"profile,omitempty"`
	RequiresProduct string `json:"requires_product,omitempty"`
}

type adoptResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Port            int    `json:"port"`
	Profile         string `json:"profile"`
	RequiresProduct string `json:"requires_product,omitempty"`
	URL             string `json:"url"`
	Healthy         bool   `json:"healthy"`
}

// slugifyAppID derives a stable, filesystem/URL-safe app id from a display name:
// lowercase, [a-z0-9-] only, collapsed hyphens, trimmed. Returns "" if nothing
// usable remains.
func slugifyAppID(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		case r == '-' || r == '_' || r == ' ':
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// probeLoopbackHealth reports whether something is listening on 127.0.0.1:port.
func probeLoopbackHealth(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// RegisterProxyAdoptHandlers wires the adopt-a-port routes onto mux.
func RegisterProxyAdoptHandlers(mux *http.ServeMux, deps ProxyAdoptDeps) {
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	writeErr := func(w http.ResponseWriter, code int, msg string) {
		writeJSON(w, code, map[string]string{"error": msg})
	}

	// POST /api/apps/proxy — adopt a loopback port.
	mux.HandleFunc("POST /api/proxyadopt", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID") // stamped by auth middleware; never client-supplied
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req adoptRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		appID := slugifyAppID(req.Name)
		if appID == "" {
			writeErr(w, http.StatusBadRequest, "name must contain at least one alphanumeric character")
			return
		}
		// Never let an adopted port shadow a protected system app id.
		if IsSystemApp(appID) {
			writeErr(w, http.StatusForbidden, "cannot adopt a port under a system app name")
			return
		}
		// Loopback-only + reserved-port validation (rejects non-loopback by
		// construction, privileged ports, the gateway port, and the namespace
		// host-port range).
		if err := ValidateAdoptablePort(req.Port); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		profile := req.Profile
		if profile == "" {
			profile = "default"
		}

		u := ExternalUpstream{
			AppID:     appID,
			Name:      strings.TrimSpace(req.Name),
			OwnerID:   userID,
			Profile:   profile,
			Port:      req.Port,
			Product:   strings.TrimSpace(req.RequiresProduct),
			CreatedAt: time.Now().UTC(),
		}
		// Register live first (this re-validates the port); persist second so a
		// persisted record always corresponds to a live registration.
		if err := deps.Mgr.RegisterExternalUpstream(u); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if deps.Store != nil {
			if err := deps.Store.Set(u); err != nil {
				// Roll back the live registration so we don't leave an
				// unpersisted upstream that vanishes on restart.
				deps.Mgr.RemoveExternalUpstream(appID, userID, profile)
				writeErr(w, http.StatusInternalServerError, "persist adopted port: "+err.Error())
				return
			}
		}
		if deps.OnRegister != nil {
			deps.OnRegister(appID, u.Product)
		}

		writeJSON(w, http.StatusCreated, adoptResponse{
			ID:              appID,
			Name:            u.Name,
			Port:            u.Port,
			Profile:         u.Profile,
			RequiresProduct: u.Product,
			URL:             "/app/" + appID + "/",
			Healthy:         probeLoopbackHealth(u.Port),
		})
	})

	// GET /api/apps/proxy — list the caller's adopted upstreams.
	mux.HandleFunc("GET /api/proxyadopt", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		list := deps.Mgr.ListExternalUpstreamsForOwner(userID)
		out := make([]adoptResponse, 0, len(list))
		for _, u := range list {
			out = append(out, adoptResponse{
				ID:              u.AppID,
				Name:            u.Name,
				Port:            u.Port,
				Profile:         u.Profile,
				RequiresProduct: u.Product,
				URL:             "/app/" + u.AppID + "/",
				Healthy:         probeLoopbackHealth(u.Port),
			})
		}
		writeJSON(w, http.StatusOK, out)
	})

	// DELETE /api/apps/proxy/{id} — revoke an adopted upstream.
	mux.HandleFunc("DELETE /api/proxyadopt/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		appID := r.PathValue("id")
		if appID == "" {
			writeErr(w, http.StatusBadRequest, "app id required")
			return
		}
		profile := r.URL.Query().Get("profile")
		if profile == "" {
			profile = "default"
		}
		// Owner-scoped: only revoke an upstream the caller actually owns.
		if _, ok := deps.Mgr.GetExternalUpstream(appID, userID, profile); !ok {
			writeErr(w, http.StatusNotFound, "no adopted port for this app")
			return
		}
		deps.Mgr.RemoveExternalUpstream(appID, userID, profile)
		if deps.Store != nil {
			_ = deps.Store.Delete(userID, appID, profile)
		}
		if deps.OnRemove != nil {
			deps.OnRemove(appID)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": appID})
	})
}
