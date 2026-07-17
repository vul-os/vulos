// register_all.go — the operational composition root.
//
// RegisterOperational mounts the standalone operational route surface onto a
// mux, taking the commercial layer only through the billingport /storageport
// seams (never a commercial impl). It is what a self-hosted control plane (the
// cmd/server binary, via cpserver) calls to serve the full operational API with
// the free no-op defaults.
//
// SCOPE. This wires every operational route whose collaborators can be
// constructed from the core inputs here (the shared auth store + DB, the
// entitlement seam, the DB dir, and the deployment domain) plus the Wire* store
// helpers that already live in this package. Route groups whose store-opening
// Wire* helper still lives in the commercial module (fleet, enroll, storage
// service, routing, keydir, compliance, edge, cdn, residency, telemetry, …) are
// mounted by that module's own composition root once those helpers migrate; each
// is noted below. Nothing here opens a commercial store or charges money.
package cproutes

import (
	"context"
	"net/http"
	"os"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/oauthclient"
)

// OperationalDeps carries the shared collaborators the operational route set is
// built against. Only the seams are pluggable; a self-hoster passes the no-op
// billingport resolver and everything is uncapped/uncharged.
type OperationalDeps struct {
	// AuthStore is the shared account/session store nearly every route gates on.
	AuthStore *auth.Store
	// AuthDB is the auth-subsystem DB handle (used by RegisterLegal).
	AuthDB *cpdb.DB
	// Entitlements is the entitlement/quota seam (default: NoopResolver).
	Entitlements billingport.EntitlementResolver
	// DBDir is the per-subsystem SQLite base dir (VULOS_DB_DIR); ignored when a
	// Postgres DSN is configured in the process env.
	DBDir string
	// AdminAccountID is the account id treated as the platform super-admin for
	// the abuse console (empty disables those admin gates → deny).
	AdminAccountID string
}

// RegisterOperational mounts the operational route surface and returns the
// closers that must run on shutdown (in reverse order). Safe to call once during
// server assembly. It never fails the caller: a store that cannot open logs and
// leaves its routes answering 503, matching every Wire* helper's own posture.
func RegisterOperational(mux *http.ServeMux, deps OperationalDeps) []func() {
	if deps.Entitlements == nil {
		deps.Entitlements = billingport.NewNoopResolver()
	}
	var closers []func()
	add := func(c func()) {
		if c != nil {
			closers = append(closers, c)
		}
	}

	// Secrets provider (env-backed by default) — every secretOrEnv call-site
	// reads through it. Harmless no-op behaviour when CP_KMS_PROVIDER is unset.
	initSecrets()

	// Admin audit log (single hash-chained logger the whole surface shares).
	add(WireAuditLog(deps.DBDir))
	al := AuditLogger()

	// App-identity introspection: teach the auth store to resolve the short-lived
	// app tokens the reverse proxy mints (SECURITY-C1). Nil-safe.
	initAppIdentity(deps.AuthStore)

	// Signup-time anchor-inbox provisioning (nil-safe if the store fails to open).
	wireAnchorInbox(deps.DBDir)

	// ── Auth + account surfaces ───────────────────────────────────────────────
	RegisterAuthRoutes(mux, deps.AuthStore)
	RegisterWebAuthnRoutes(mux, deps.AuthStore)
	registerOpaqueRoutes(mux, deps.AuthStore)
	RegisterOAuthLoginRoutes(mux, deps.AuthStore, oauthclient.NewRegistryFromEnv())

	// Account recovery (government-ID upload is fail-closed without RECOVERY_KEK).
	if rec := wireRecovery(context.Background(), deps.DBDir); rec != nil {
		registerRecoveryRoutes(mux, deps.AuthStore, rec)
	}

	// Developer API keys + LLM keys (each opens its own cpdb store).
	add(WireDeveloperKeys(mux, deps.DBDir, deps.AuthStore, deps.Entitlements))
	add(WireLLMKeys(mux, deps.DBDir, deps.AuthStore))

	// Mobile push registry + dispatch.
	sub, disp, pushCloser := WireMobilePush(deps.DBDir)
	RegisterMobilePush(mux, sub, disp, deps.AuthStore, func(ctx context.Context) string {
		return secretOrEnv(ctx, "CP_SHARED_SECRET")
	})
	add(pushCloser)

	// DDoS defence layer (registers its own honeypot/captcha/dashboard routes).
	// fly/tigris cost readers are nil in self-host (budget readers return 0).
	ddosR := WireDDoS(mux, deps.DBDir, al, nil, nil)
	add(ddosR.Closer)

	// Abuse detector + console. The shared-secret gate reads CP_SHARED_SECRET.
	_, abuseCloser := WireAbuse(mux, deps.DBDir, deps.AuthStore, deps.AdminAccountID, al, os.Getenv("CP_SHARED_SECRET"))
	add(abuseCloser)

	// Security hardening layer + super-admin dashboard. WireSecurityRequireAdmin
	// falls back to deny-all when no super-admin store is registered, so the
	// dashboard is never accidentally exposed in a standalone deployment.
	secR := WireSecurity(deps.DBDir)
	WireSecurityRequireAdmin(deps.AuthStore, al)
	RegisterSecurity(mux, secR, SARequireAdmin(), nil, deps.AuthStore)
	add(secR.Closer)

	// Legal pages (ToS/privacy/DPA acceptance) — gated by the shared auth DB.
	if deps.AuthDB != nil {
		RegisterLegal(mux, deps.AuthStore, deps.AuthDB)
	}

	// Operational surfaces the management /console SPA consumes: fleet + devices,
	// account/support/cell status, compliance/privacy, org audit + product
	// catalogue, developer webhooks + MCP. Each opens its own operational store
	// (cpdb; in-memory fallback) so a self-hoster's console shows their own data
	// instead of 404. This also registers the product catalogue (GET/PATCH
	// /api/products) with the real caller gate — so it is NOT registered again
	// below.
	add2 := registerConsoleOperational(mux, deps)
	closers = append(closers, add2...)

	// Boot/first-run endpoints.
	RegisterBoot(mux)

	// NOT wired by this zero-config default (each is fail-closed on a required
	// secret / needs a store-opening Wire* helper that still lives in the
	// commercial module — a configured or commercial composition root mounts them):
	//   - enroll + boot-enroll, integrations, the storage service
	//     (RegisterStorage/RegisterFiles/RegisterAccountExport), storagesel,
	//     DNS plane, routing/relay status, CDN, edge, cloud-home, keydir,
	//     residency.
	//   - the box billing read (GET /api/box, /api/box/billing) and the whole
	//     commercial billing/pricing/superadmin-billing surface.

	return closers
}
