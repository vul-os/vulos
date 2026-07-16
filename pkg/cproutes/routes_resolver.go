// routes_resolver.go — backend resolver + fabric proxy routes.
//
// Routes registered:
//
//	GET  /api/resolve/backend            — session-gated; returns BackendTarget
//	     ?service=mail|office|talk|calendar|meet
//
//	ANY  /backend/{service}/{path...}    — reverse-proxy; the web client
//	     (webmail-vulos / office bundles) hits this path. The cloud forwards:
//	     * hosted     → directly to the Tigris-backed JMAP endpoint.
//	     * selfhost   → over the peering/relay fabric (opaque byte-forward;
//	       no decryption — E2E property is preserved).
//
// Session gate: both routes require a valid vulos.cloud session cookie.
// Audit log: every resolve + proxy error is logged via cpAuditLog.
//
// Wire anchor: RESOLVER_WIRE in main.go.
package cproutes

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/resolver"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// ---------------------------------------------------------------------------
// Wire helper — called from main.go RESOLVER_WIRE block
// ---------------------------------------------------------------------------

// resolverResult holds the state created by wireResolver.
type resolverResult struct {
	res    *resolver.Resolver
	closer func()
}

// wireResolver constructs the Resolver, wiring the real storage.Service as
// the StorageSource. Enrollment is nil — BYO-mail self-host enrollment is
// de-scoped; self-host accounts fall back to the hosted JMAP endpoint.
//
//   - storageService is the already-wired storage.Service from wireStorage.
func wireResolver(storageService *storage.Service) resolverResult {
	storageSrc := &resolverStorageAdapter{svc: storageService}

	res := &resolver.Resolver{
		HostedEndpointBase: "https://jmap.vulos.org/jmap",
		Storage:            storageSrc,
		// Enrollment is nil — BYO-mail self-host enrollment is de-scoped.
		// Self-host accounts fall back to the hosted JMAP endpoint.
	}
	return resolverResult{
		res:    res,
		closer: func() {}, // stateless; nothing to close
	}
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

// registerResolverRoutes wires the resolve API and the proxy into mux.
func registerResolverRoutes(mux *http.ServeMux, res *resolver.Resolver, authStore *auth.Store) {
	h := &resolverHandlers{res: res, auth: authStore}
	mux.HandleFunc("GET /api/resolve/backend", h.resolveBackend)
	// /backend/{service}/ prefix: accepts any method.
	mux.HandleFunc("/backend/", h.proxyBackend)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type resolverHandlers struct {
	res  *resolver.Resolver
	auth *auth.Store
}

// GET /api/resolve/backend?service=<name>
func (h *resolverHandlers) resolveBackend(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}

	svcStr := r.URL.Query().Get("service")
	svc := resolver.Service(svcStr)

	target, err := h.res.ResolveBackend(r.Context(), u.ID, svc)
	if err != nil {
		h.mapErr(w, r, u.ID, "resolve.backend", err)
		return
	}

	auditRecord(r.Context(), u.ID, "resolver.resolve",
		string(svc), map[string]string{"kind": string(target.Kind)})

	httpx.JSON(w, target)
}

// /backend/{service}/{path...}
// Parses the service from the URL, resolves the backend, then proxies:
//   - hosted:          forwards the request to the JMAP endpoint.
//   - selfhost-fabric: opens an outbound fabric tunnel and forwards bytes
//     opaquely (no decryption — E2E).
func (h *resolverHandlers) proxyBackend(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}

	// Parse /backend/<service>[/<rest>]
	svc, rest := parseFabricPath(r.URL.Path)
	if svc == "" {
		httpx.Err(w, http.StatusBadRequest, "missing service in path")
		return
	}

	target, err := h.res.ResolveBackend(r.Context(), u.ID, resolver.Service(svc))
	if err != nil {
		h.mapErr(w, r, u.ID, "proxy.resolve", err)
		return
	}

	switch target.Kind {
	case resolver.KindHosted:
		h.proxyHosted(w, r, target, rest)
	case resolver.KindSelfHostFabric:
		h.proxySelfHost(w, r, target, rest)
	default:
		httpx.Err(w, http.StatusInternalServerError, "unknown backend kind")
	}
}

// proxyHosted forwards the request directly to the hosted JMAP endpoint.
func (h *resolverHandlers) proxyHosted(w http.ResponseWriter, r *http.Request, target resolver.BackendTarget, rest string) {
	upstream := target.Endpoint
	if rest != "" {
		upstream = strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(rest, "/")
	}
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	upReq, err := buildUpstreamRequest(r.Context(), r.Method, upstream, r)
	if err != nil {
		log.Printf("[resolver/proxy-hosted] build request: %v", err)
		httpx.Err(w, http.StatusBadGateway, "proxy: failed to build upstream request")
		return
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(upReq)
	if err != nil {
		log.Printf("[resolver/proxy-hosted] upstream error: %v", err)
		auditRecord(r.Context(), "", "resolver.proxy.hosted.error",
			upstream, map[string]string{"error": err.Error()})
		httpx.Err(w, http.StatusBadGateway, "proxy: upstream unavailable")
		return
	}
	defer resp.Body.Close()

	forwardResponse(w, resp)
}

// proxySelfHost forwards bytes opaquely to the self-hosted box over the
// peering/relay fabric. The cloud does NOT decrypt the payload — it is an
// end-to-end opaque byte-forward (E2E property).
func (h *resolverHandlers) proxySelfHost(w http.ResponseWriter, r *http.Request, target resolver.BackendTarget, rest string) {
	fabricEndpoint := target.FabricRoute
	if fabricEndpoint == "" {
		httpx.Err(w, http.StatusBadGateway, "proxy: no fabric route available")
		return
	}

	upstream := strings.TrimRight(fabricEndpoint, "/")
	if rest != "" {
		upstream += "/" + strings.TrimLeft(rest, "/")
	}
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}

	upReq, err := buildUpstreamRequest(r.Context(), r.Method, upstream, r)
	if err != nil {
		log.Printf("[resolver/proxy-fabric] build request: %v", err)
		httpx.Err(w, http.StatusBadGateway, "proxy: failed to build fabric request")
		return
	}

	// E2E opaque forward: we do NOT inspect or buffer the body beyond the
	// streaming copy below. Content-Encoding (e.g. encrypted payloads from the
	// JMAP client) is passed through unmodified.
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(upReq)
	if err != nil {
		log.Printf("[resolver/proxy-fabric] fabric error: %v", err)
		auditRecord(r.Context(), "", "resolver.proxy.fabric.error",
			upstream, map[string]string{"error": err.Error()})
		httpx.Err(w, http.StatusBadGateway, "proxy: fabric tunnel unavailable")
		return
	}
	defer resp.Body.Close()

	// Opaque byte-forward — no body inspection.
	forwardResponse(w, resp)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseFabricPath splits "/backend/<service>[/<rest>]" into service + rest.
func parseFabricPath(path string) (svc, rest string) {
	trimmed := strings.TrimPrefix(path, "/backend/")
	idx := strings.IndexByte(trimmed, '/')
	if idx < 0 {
		return trimmed, ""
	}
	return trimmed[:idx], trimmed[idx+1:]
}

// buildUpstreamRequest constructs an outbound request copying headers from the
// inbound request. It strips hop-by-hop headers and streams the body directly
// without buffering, so encrypted payloads are not touched.
func buildUpstreamRequest(ctx context.Context, method, upstreamURL string, orig *http.Request) (*http.Request, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), orig.Body)
	if err != nil {
		return nil, err
	}

	for k, vv := range orig.Header {
		if isHopByHop(k) {
			continue
		}
		req.Header[k] = vv
	}
	if orig.RemoteAddr != "" {
		req.Header.Set("X-Forwarded-For", orig.RemoteAddr)
	}
	req.Header.Set("X-Forwarded-Proto", "https")

	return req, nil
}

// forwardResponse copies status, headers, and body from the upstream response
// to the downstream ResponseWriter without inspecting or buffering the body
// (E2E opaque forward).
func forwardResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Stream body without buffering — preserves E2E encryption.
	_, _ = io.Copy(w, resp.Body)
}

// hopByHopHeaders is the set of headers that must not be forwarded.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isHopByHop(h string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(h)]
}

// mapErr translates resolver errors to HTTP status codes.
func (h *resolverHandlers) mapErr(w http.ResponseWriter, r *http.Request, actor, action string, err error) {
	switch {
	case errors.Is(err, resolver.ErrUnknownAccount):
		httpx.Err(w, http.StatusNotFound, "account not found")
	case errors.Is(err, resolver.ErrInvalidService):
		httpx.Err(w, http.StatusBadRequest, "invalid service: "+err.Error())
	default:
		log.Printf("[resolver] %s error: %v", action, err)
		auditRecord(r.Context(), actor, action+".error",
			r.URL.Path, map[string]string{"error": err.Error()})
		httpx.Err(w, http.StatusInternalServerError, "internal error")
	}
}

// ---------------------------------------------------------------------------
// Real-store adapters (wiring the cp packages to the resolver interfaces)
// ---------------------------------------------------------------------------

// resolverStorageAdapter bridges storage.Service → resolver.StorageSource.
type resolverStorageAdapter struct {
	svc *storage.Service
}

func (a *resolverStorageAdapter) IsSelfHost(ctx context.Context, accountID string) (bool, error) {
	cfg, err := a.svc.GetConfig(ctx, accountID)
	if err != nil {
		if errors.Is(err, storage.ErrUnknownAccount) {
			return false, resolverStorageUnknownErr
		}
		return false, err
	}
	return cfg.BYO, nil
}

// resolverStorageUnknownErr is a sentinel with the message the resolver's
// isUnknownAccount helper matches ("storage: unknown account").
var resolverStorageUnknownErr = resolverStorageErr("storage: unknown account")

type resolverStorageErr string

func (e resolverStorageErr) Error() string { return string(e) }
