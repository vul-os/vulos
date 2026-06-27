package main

// routes_apps_platform.go — OS adoption of the shared Vulos Apps & Bots platform
// (github.com/vul-os/vulos-apps) and its MCP layer, giving Vulos OS an
// agent-operable surface over OS-level capabilities (chiefly the ACL-gated Files
// service, plus read-only app/system info).
//
// Two handlers are mounted on the OS gateway mux, BOTH reusing the SAME OS
// ProductAdapter + the SAME app-token Registry:
//
//	/api/apps   appsplatform.NewHandler  — REST apps & bots place (management is
//	                                       session-authed; runtime/hooks are vat_-authed)
//	/mcp        mcp.NewHandler           — MCP server (vat_-authed; tools = Act,
//	                                       resources = Read), for LLM/agents
//
// Open-core seam: the StandaloneRegistry (SQLite, in-memory fallback) is the
// default. A Vulos Cloud control plane / MCP-aggregating gateway implements the
// SAME appsplatform.Registry / mcp.Gateway in a CLOSED package this OSS core never
// imports; only this composition root would wire it, and only when explicitly
// selected via env (VULOS_APPS_CP_URL / VULOS_MCP_GATEWAY). Removing the cloud
// package never breaks this build. Both routes are independently token/session
// authed and live ALONGSIDE — never weakening — the OS gateway's existing seam
// security (X-Vulos-* strip/inject, storage-broker auth).

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"vulos/backend/services/appnet"
	"vulos/backend/services/apps"
	"vulos/backend/services/auth"
	"vulos/backend/services/files"

	"github.com/vul-os/vulos-apps/appsplatform"
	"github.com/vul-os/vulos-apps/mcp"
)

// mountAppsPlatform wires the OS apps & bots place and MCP server onto mux,
// reusing one OS ProductAdapter + one app-token registry. It is best-effort: any
// failure logs and leaves the OS booting without the agent surface. Disabled
// entirely when VULOS_APPS=off.
func mountAppsPlatform(mux *http.ServeMux, authStore *auth.Store, filesSvc *files.Service, appsDir, dbDir string) {
	if os.Getenv("VULOS_APPS") == "off" {
		log.Printf("[apps] VULOS_APPS=off — apps & bots place + MCP disabled")
		return
	}

	// Open-core: standalone registry by default. The cloud control-plane registry
	// is env-gated and lives in a closed package not built into this OSS binary;
	// fall back to standalone rather than fail when the hook is set but unbuilt.
	if cp := os.Getenv("VULOS_APPS_CP_URL"); cp != "" {
		log.Printf("[apps] VULOS_APPS_CP_URL set but the cloud apps registry is not built into this OSS binary; using the standalone registry")
	}
	reg, err := appsplatform.NewStandaloneRegistry(
		filepath.Join(dbDir, "apps.db"),
		appsplatform.WithScopeSet(appsplatform.NewScopeSet(appsplatform.ScopeAppsRead, appsplatform.ScopeAppsWrite)),
	)
	if err != nil {
		log.Printf("[apps] registry unavailable (%v) — apps & bots place + MCP disabled", err)
		return
	}

	adapter := apps.NewOSAdapter(filesSvc, osAppLister(appsDir), osSysInfo)
	disp := appsplatform.NewDispatcher(reg, appsplatform.ProductOS)

	// Management API auth: reuse the OS session. auth.Handler.Middleware has
	// already validated the session and injected X-User-ID upstream of this mux;
	// the owner is that user and admins (RoleAdmin) manage every app.
	admin := func(r *http.Request) (string, bool, bool) {
		uid := r.Header.Get("X-User-ID")
		if uid == "" {
			return "", false, false
		}
		isAdmin := false
		if p, _ := authStore.GetProfile(uid); p != nil && p.Role == auth.RoleAdmin {
			isAdmin = true
		}
		return uid, isAdmin, true
	}

	appsH, err := appsplatform.NewHandler(appsplatform.MountConfig{
		Adapter:    adapter,
		Registry:   reg,
		Dispatcher: disp,
		Admin:      admin,
		BasePath:   "/api/apps",
	})
	if err != nil {
		log.Printf("[apps] handler init failed (%v) — apps & bots place disabled", err)
		return
	}
	// Mount at the base + subtree. The OS's own native /api/apps/* routes (running,
	// launch, stop, namespaces, ports, traffic, health) are registered as
	// method+path-specific patterns and remain MORE specific than this subtree, so
	// the ServeMux routes them to the native handlers and everything else under
	// /api/apps/ to the platform (management /{id}, /commands, runtime /v1/*,
	// /hooks/{id}).
	mux.Handle("/api/apps", appsH)
	mux.Handle("/api/apps/", appsH)
	log.Printf("[apps] apps & bots place mounted at /api/apps (product=os, registry=standalone)")

	// MCP server — the SAME adapter, registry and vat_ token auth in agent shape.
	// Open-core: MCPConfig.Gateway (cloud MCP aggregation) is left nil here; a
	// Vulos Cloud composition root wires it env-gated. The OSS core never imports a
	// Gateway implementation.
	if gw := os.Getenv("VULOS_MCP_GATEWAY"); gw != "" {
		log.Printf("[mcp] VULOS_MCP_GATEWAY set but no MCP aggregating gateway is built into this OSS binary; serving standalone")
	}
	mcpH, err := mcp.NewHandler(mcp.MCPConfig{
		Adapter:  adapter,
		Registry: reg,
		Emit:     disp.EmitFunc(),
		BasePath: "/mcp",
	})
	if err != nil {
		log.Printf("[mcp] handler init failed (%v) — MCP server disabled", err)
		return
	}
	mux.Handle("/mcp", mcpH)
	mux.Handle("/mcp/", mcpH)
	log.Printf("[mcp] MCP server mounted at /mcp (agents operate the OS via an apps token + the OS adapter)")
}

// osAppLister returns an apps.AppLister that scans the OS app directory for
// installed manifests, projecting only safe, read-only metadata.
func osAppLister(appsDir string) apps.AppLister {
	return func() []apps.AppInfo {
		manifests, err := appnet.ScanApps(appsDir)
		if err != nil {
			return nil
		}
		out := make([]apps.AppInfo, 0, len(manifests))
		for _, m := range manifests {
			if m == nil {
				continue
			}
			out = append(out, apps.AppInfo{
				ID:          m.ID,
				Name:        m.Name,
				Description: m.Description,
				Version:     m.Version,
				Type:        m.Type,
				Category:    m.Category,
				Author:      m.Author,
				License:     m.License,
				Homepage:    m.Homepage,
			})
		}
		return out
	}
}

// osSysInfo returns conservative, read-only system information for agents. It
// deliberately exposes only coarse build/runtime facts — no secrets, no host
// mutation surface.
func osSysInfo() map[string]any {
	return map[string]any{
		"product":    "vulos",
		"version":    Version,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"go_version": runtime.Version(),
		"num_cpu":    runtime.NumCPU(),
	}
}
