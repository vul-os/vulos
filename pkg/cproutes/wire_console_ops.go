// wire_console_ops.go — operational route groups the self-hosted management
// console needs to show a self-hoster their OWN data.
//
// Phase-3a finding: the minimal composition root (RegisterOperational) mounted
// auth + developer-keys + the abuse/security consoles, but NOT the operational
// groups the React /console pages call — so a zero-config self-host binary
// answered 404 on the very data the console renders (its own fleet, devices,
// account status, audit trail, compliance/privacy requests, per-cell status).
//
// These are all OPERATIONAL surfaces — a self-hoster's own fleet/devices/audit/
// telemetry/compliance — so they belong in the OSS default. Every store here is
// opened via cpdb (SQLite by default, Postgres when DATABASE_URL is set) and
// degrades to an in-memory store when the DB cannot be opened, so the console
// always renders rather than erroring. Nothing here opens a commercial store or
// charges money; the only seam consulted is the entitlement resolver (no-op by
// default), matching every other operational group.
//
// Route groups wired here (all session-gated, account/tenant-scoped to the
// signed-in user — no IDOR surface):
//
//	fleet + invite-accept   GET/POST/PUT /api/fleet/* , POST /api/org/invite/accept
//	account status          GET /api/account/status , GET /api/cloud/status (public)
//	support / per-cell       GET /api/support/status , GET /api/status/mycell
//	compliance / privacy     POST /api/compliance/request , GET /api/compliance/requests
//	org audit + products      GET /api/org/audit , GET/PATCH /api/products
//	developer webhooks + mcp  GET/POST/DELETE /api/developer/{webhooks,mcp-servers}
//
// GROUPS LEFT TO THE COMMERCIAL SEAM (not wired here): the box billing surface
// (GET /api/box, /api/box/billing) and the account-export bundle
// (POST /api/account/export) — the former is a pricing/billing read, the latter
// needs a provisioned storage.Service (bring-your-own-bucket) that only a
// configured/commercial composition root constructs. The console degrades
// gracefully without them (billing nav is hidden unless the commercial seam is
// enabled).
package cproutes

import (
	"log"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/compliance"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
	"github.com/vul-os/vulos-management/pkg/telemetry"
)

// registerConsoleOperational mounts the operational route groups the management
// /console SPA consumes and returns the store closers (run in reverse on
// shutdown). Called from RegisterOperational. Never fails the caller: a store
// that cannot open falls back to an in-memory store and logs.
func registerConsoleOperational(mux *http.ServeMux, deps OperationalDeps) []func() {
	var closers []func()
	add := func(c func()) {
		if c != nil {
			closers = append(closers, c)
		}
	}

	// ── Fleet (orgs / devices / heartbeats / invites) ─────────────────────────
	// The shared fleet store also backs the support/status and account-status
	// groups below, so it is opened once and threaded through.
	fleetStore, fleetCloser := openFleetStore(deps.DBDir)
	add(fleetCloser)
	// ownerResolver is nil in self-host: device access is gated on account
	// ownership (and org-admin role), which needs no routing-side resolver.
	RegisterFleet(mux, fleetStore, deps.AuthStore, nil)
	RegisterInviteAcceptRoutes(mux, fleetStore, deps.AuthStore)

	// ── Account status (authenticated) + public cloud status ──────────────────
	// PoPs/relay stores are nil in self-host (those sections degrade to unknown);
	// the DB pinger is the shared auth store, the device section reads fleet.
	agg := NewCloudStatusAggregator(deps.AuthStore, nil, fleetStore, nil)
	RegisterCloudStatus(mux, agg, deps.AuthStore)

	// ── Support / per-cell status (SUPPORT-04) ────────────────────────────────
	// Telemetry store degrades to MemStore (empty-but-valid status) when no
	// persistent DB is available; ownership is checked against the fleet store.
	telStore, telCloser := openTelemetryStore(deps.DBDir)
	add(telCloser)
	registerSupportRoutes(mux, telStore, deps.AuthStore, fleetStore)

	// ── Compliance / privacy (POPIA / GDPR data-rights intake) ────────────────
	compStore, compCloser := openComplianceStore(deps.DBDir)
	add(compCloser)
	RegisterCompliance(mux, compStore, deps.AuthStore)

	// ── Org audit trail + product-status overrides ────────────────────────────
	// A minimal org-admin service: the AuditReader adapts the shared hash-chained
	// audit log (own-org, hard tenant-filtered) so GET /api/org/audit shows the
	// self-hoster their own admin-action history. The remaining orgadmin seams
	// (members/instances/usage/quota/billing) are nil — those routes degrade to
	// structurally-empty responses (the console has no page for them) rather than
	// erroring. Under the 1:1 account==org model a missing RoleResolver leaves the
	// session user at owner, which is correct for self-host.
	gate := consoleCallerGate{auth: deps.AuthStore}
	oaSvc := orgadmin.NewService(nil, nil, nil, nil, nil, nil, nil)
	oaSvc.AuditR = OrgAuditReader{}
	orgadmin.RegisterRoutes(mux, oaSvc, gate)
	// Product catalogue + per-org status. Registered HERE (with the real gate) so
	// PATCH /api/products/{id} is a gated write; RegisterOperational must NOT also
	// register the nil-gate variant or the ServeMux panics on the duplicate GET.
	registerProductsRoutes(mux, oaSvc, gate, deps.AuthStore)

	// ── Developer console: webhooks + MCP servers ─────────────────────────────
	// (Developer API keys + LLM keys are wired separately in RegisterOperational.)
	RegisterDeveloperWebhookRoutes(mux, deps.AuthStore)
	RegisterDeveloperMCPRoutes(mux, deps.AuthStore)

	return closers
}

// consoleCallerGate adapts auth.Store.RequireSession onto orgadmin.CallerGate.
// Under the 1:1 account==org model the session user's id is BOTH the tenant id
// and the caller account id used for RBAC. On auth failure the gate writes the
// 401 itself and returns ok=false (callers return without writing anything).
type consoleCallerGate struct {
	auth *auth.Store
}

// TenantFromSession satisfies orgadmin.SessionGate.
func (g consoleCallerGate) TenantFromSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := g.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", false
	}
	return u.ID, true
}

// CallerFromSession satisfies orgadmin.CallerGate.
func (g consoleCallerGate) CallerFromSession(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	u := g.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", "", false
	}
	return u.ID, u.ID, true
}

// ── Store openers ────────────────────────────────────────────────────────────
//
// Each mirrors the WireDeveloperKeys pattern already in this package: point
// VULOS_DB_DIR at dbDir (so the SQLite file lands alongside the other stores),
// open through cpdb (Postgres when DATABASE_URL is set), and fall back to the
// package's in-memory store on any error so the console degrades gracefully.

func openFleetStore(dbDir string) (fleet.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[fleet] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return fleet.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("fleet")
	if err != nil {
		log.Printf("[fleet] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return fleet.NewMemStore(), func() {}
	}
	st, err := fleet.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[fleet] WARNING: could not open store (%v) — using in-memory store", err)
		return fleet.NewMemStore(), func() {}
	}
	log.Printf("[fleet] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[fleet] store close: %v", cerr)
		}
	}
}

func openTelemetryStore(dbDir string) (telemetry.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[telemetry] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return telemetry.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("telemetry")
	if err != nil {
		log.Printf("[telemetry] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return telemetry.NewMemStore(), func() {}
	}
	st, err := telemetry.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[telemetry] WARNING: could not open store (%v) — using in-memory store", err)
		return telemetry.NewMemStore(), func() {}
	}
	log.Printf("[telemetry] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[telemetry] store close: %v", cerr)
		}
	}
}

func openComplianceStore(dbDir string) (compliance.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[compliance] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return compliance.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("compliance")
	if err != nil {
		log.Printf("[compliance] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return compliance.NewMemStore(), func() {}
	}
	st, err := compliance.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[compliance] WARNING: could not open store (%v) — using in-memory store", err)
		return compliance.NewMemStore(), func() {}
	}
	log.Printf("[compliance] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[compliance] store close: %v", cerr)
		}
	}
}
