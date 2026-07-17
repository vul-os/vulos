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
	"github.com/vul-os/vulos-management/pkg/relayscale"
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
	// RelayProvisioner is the relay-pool scaling seam (default: manual). Its name
	// + the relay-scale policy drive the demand API mounted below.
	RelayProvisioner relayscale.RelayProvisioner
	// DDoSMiddleware, when non-nil, receives the DDoS defence middleware stack
	// (metrics/blocklist/geoip/conncap/iprate/tarpit/captcha). The server builder
	// (cpserver.New) uses this to thread those layers into the global middleware
	// chain that wraps the whole mux — previously the stack was registered's
	// routes were mounted but the middleware layers themselves were dropped, so a
	// self-host binary served a bare mux with no global protections.
	DDoSMiddleware func([]func(http.Handler) http.Handler)
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
	// Hand the DDoS middleware stack to the server builder so it can wrap the mux
	// (M1: the self-host binary otherwise served a bare mux with these layers
	// registered-but-never-applied).
	if deps.DDoSMiddleware != nil {
		deps.DDoSMiddleware(ddosR.Middlewares)
	}

	// Abuse detector + console. The shared-secret gate reads CP_SHARED_SECRET.
	_, abuseCloser := WireAbuse(mux, deps.DBDir, deps.AuthStore, deps.AdminAccountID, al, os.Getenv("CP_SHARED_SECRET"))
	add(abuseCloser)

	// Security hardening layer + super-admin dashboard.
	secR := WireSecurity(deps.DBDir)
	add(secR.Closer)

	// Operator (super-admin) console — OPT-IN (VULOS_ENABLE_SUPERADMIN=1 or a
	// VULOS_BOOTSTRAP_SUPERADMIN email). Mounts the operator HTML pages + the JSON
	// admin API the React /console/admin section consumes. Left disabled by
	// default so the zero-config self-host admin surface stays mounted-but-deny-
	// all (403) exactly as before. Wired here (after WireSecurity) so it can share
	// the already-open security telemetry store and the shared audit logger.
	//
	// ORDERING (L1): this MUST run BEFORE WireSecurityRequireAdmin. It registers
	// the super-admin store singleton (RegisterSuperAdminStore), which
	// WireSecurityRequireAdmin snapshots to build the security-dashboard gate. If
	// the order were reversed the singleton would still be nil when the gate is
	// built, so the security telemetry dashboard would deny ALL requests even with
	// the console enabled (dead-gated). With this order the gate authenticates.
	if superAdminConsoleEnabled() {
		closers = append(closers, wireSuperAdminConsole(mux, superAdminConsoleDeps{
			AuthStore: deps.AuthStore,
			AuthDB:    deps.AuthDB,
			Audit:     al,
			Security:  secR.Store,
		})...)
	}

	// WireSecurityRequireAdmin falls back to deny-all when no super-admin store is
	// registered, so the dashboard is never accidentally exposed in a standalone
	// (console-disabled) deployment. When the console IS enabled it was registered
	// just above, so the gate now snapshots a live store and authenticates.
	WireSecurityRequireAdmin(deps.AuthStore, al)
	RegisterSecurity(mux, secR, SARequireAdmin(), nil, deps.AuthStore)

	// Legal pages (ToS/privacy/DPA acceptance) — gated by the shared auth DB.
	if deps.AuthDB != nil {
		RegisterLegal(mux, deps.AuthStore, deps.AuthDB)
	}

	// Shared operational stores threaded into BOTH the console-operational groups
	// and the network/routing + storage groups below. Opened ONCE here (cpdb;
	// in-memory fallback) so, e.g., a device row created by enrollment is visible
	// to the fleet/support/status console views, and a routing binding written by
	// enrollment is the same one the DNS plane / relay-status / resolver read.
	fleetStore, fleetCloser := openFleetStore(deps.DBDir)
	add(fleetCloser)
	routingStore, routingCloser := openRoutingStore(deps.DBDir)
	add(routingCloser)

	// Operational surfaces the management /console SPA consumes: fleet + devices,
	// account/support/cell status, compliance/privacy, org audit + product
	// catalogue, developer webhooks + MCP. Each opens its own operational store
	// (cpdb; in-memory fallback) so a self-hoster's console shows their own data
	// instead of 404. This also registers the product catalogue (GET/PATCH
	// /api/products) with the real caller gate — so it is NOT registered again
	// below.
	closers = append(closers, registerConsoleOperational(mux, deps, fleetStore)...)

	// Network/routing + storage operational surface: device enrollment (RFC-8628),
	// the OS routing plane (routing-enroll, DNS plane, relay status, edge, CDN,
	// multi-location), the third-party OAuth data-broker, the mail key directory,
	// the cloud-home directory + peering intake, and the storage/files/export +
	// storage-selection + mail-resolver plane. All gate on the shared auth store
	// and are fail-closed (a store that cannot open logs and answers 503; a group
	// whose required secret is unset stays deny/unavailable, never fail-open).
	closers = append(closers, registerNetworkOperational(mux, deps, fleetStore, routingStore)...)

	// Boot/first-run endpoints.
	RegisterBoot(mux)

	// Relay-scaling demand API (GET /api/relay/scale/demand, POST .../observe).
	// Publishes desired per-region relay counts for an external scaler regardless
	// of the active provisioner (manual by default). Observe is CP_SHARED_SECRET-
	// gated (fail-closed).
	wireRelayScaleDemand(mux, deps.RelayProvisioner)

	// NOT wired by this zero-config default (inherently commercial, or a library
	// package with no operational HTTP handler in this module — a configured or
	// commercial composition root mounts these):
	//   - the box billing read (GET /api/box, /api/box/billing) and the whole
	//     commercial billing/pricing/superadmin-billing surface.
	//   - residency (pkg/residency), OS-router (pkg/osrouter) and the public
	//     status/incidents pages (pkg/status): library packages with no
	//     pkg/cproutes route handler yet. (The account/cloud/support/per-cell
	//     STATUS surface the console consumes IS wired — see
	//     registerConsoleOperational.)
	//
	// Everything else — device enrollment (RFC-8628), the OS routing plane
	// (routing-enroll, DNS, relay status, edge, CDN, multi-location), the
	// third-party OAuth broker, storage/files/export, storage-selection, the mail
	// key directory, the cloud-home directory + peering intake, and the mail
	// resolver — is now wired above via registerNetworkOperational, fail-closed.

	return closers
}
