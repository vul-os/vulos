// security.go — wires the security hardening layer into the control-plane.
//
// Initialises:
//   - security.Store (security.db)
//   - WAF middleware (coraza)
//   - Bot detection (JA3/JA4)
//   - Step-up credential-stuffing defense
//   - Account takeover monitoring
//   - Anti-enumeration (timing + handle-available)
//   - CT monitoring (daily background worker)
//   - Honeypot accounts (seeded at bootstrap)
//   - Egress anomaly tracking
//   - Security dashboard page at /superadmin/security
package cproutes

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/security"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

// authInboxSenderAdapter adapts auth.InboxSender to security.InboxSender.
//
// auth.InboxSender.DeliverSystemMessage expects a bare handle (e.g. "alice"),
// whereas security.InboxSender.Send receives a full email address
// (e.g. "alice@vulos.org"). The adapter strips the @vulos.org suffix to derive
// the handle, then delegates to the underlying auth sender.
type authInboxSenderAdapter struct {
	inner auth.InboxSender
}

func (a authInboxSenderAdapter) Send(ctx context.Context, toEmail, subject, body string) error {
	// Derive handle from email: "alice@vulos.org" → "alice".
	handle := toEmail
	if idx := strings.Index(toEmail, "@"); idx > 0 {
		handle = toEmail[:idx]
	}
	return a.inner.DeliverSystemMessage(ctx, handle, subject, body)
}

// NewStepUpSender returns a security.InboxSender backed by the forgotPasswordSender
// (the same auth.InboxSender used for password-reset delivery). Call after
// SetForgotPasswordSender has been called (i.e. during wire_stores / main init).
func NewStepUpSender() security.InboxSender {
	return authInboxSenderAdapter{inner: forgotPasswordSender}
}

// saRequireAdmin is the package-level RequireSuperAdmin middleware built by
// WireSecurityRequireAdmin. It is set once during server startup so that
// security.go can gate the security dashboard without needing to re-open
// the superadmin store (which shares auth.db lifecycle).
var saRequireAdmin func(http.Handler) http.Handler

// SARequireAdmin returns the shared RequireSuperAdmin middleware built by
// WireSecurityRequireAdmin. Exported so commercial route files can gate their
// own superadmin surfaces against the SAME middleware instance. Returns nil
// until WireSecurityRequireAdmin has run; callers should treat nil as deny.
func SARequireAdmin() func(http.Handler) http.Handler { return saRequireAdmin }

// SecurityResult bundles the wired security components. Fields are exported so a
// commercial composition root can reach the store + closer it wired.
type SecurityResult struct {
	Store  *security.Store
	WAF    *security.WAF
	Closer func()
}

// WireSecurity opens the security store, bootstraps honeypot accounts,
// initialises WAF + egress tracker, and returns the bundle.
// Call after WireAuditLog and wireSuperAdmin.
func WireSecurity(dbDir string) SecurityResult {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Fatalf("[security] could not set DB dir: %v", err)
	}
	db, err := cpdb.Open("security")
	if err != nil {
		log.Fatalf("[security] failed to open db: %v", err)
	}
	store, err := security.Open(db)
	if err != nil {
		log.Fatalf("[security] failed to open store: %v", err)
	}

	// Seed honeypot accounts (idempotent).
	if err := store.SeedHoneypotAccounts(context.Background(), security.HoneypotAccountCount()); err != nil {
		log.Printf("[security] honeypot seed: %v", err)
	}

	// Initialise WAF.
	waf, err := security.NewWAF(store)
	if err != nil {
		log.Fatalf("[security] WAF init: %v", err)
	}

	// Initialise egress tracker.
	security.InitEgressTracker(store)

	return SecurityResult{
		Store: store,
		WAF:   waf,
		Closer: func() {
			if err := store.Close(); err != nil {
				log.Printf("[security] store close: %v", err)
			}
		},
	}
}

// WireSecurityRequireAdmin constructs and stores the RequireSuperAdmin middleware
// into the package-level saRequireAdmin variable. Must be called after
// wireSuperAdmin so that superadmin.SuperAdminStore() is non-nil.
func WireSecurityRequireAdmin(authStore *auth.Store, al *auditlog.Logger) {
	// Use the singleton store registered by wireSuperAdmin to avoid opening
	// a second handle to auth.db (fix: saStore single-instance).
	saStore := superadmin.SuperAdminStore()
	if saStore == nil {
		// SEC-M8 (audit): fall back to DENY-ALL rather than pass-through.
		// A mis-wired startup must not accidentally leave superadmin routes open.
		log.Printf("[security] WARNING: superadmin singleton not registered — security dashboard will deny all requests. Ensure wireSuperAdmin runs before WireSecurityRequireAdmin.")
		saRequireAdmin = func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"superadmin not configured"}`, http.StatusForbidden)
			})
		}
		return
	}
	saRequireAdmin = superadmin.RequireSuperAdmin(saStore, authStore, al)
}

// RegisterSecurity registers the super-admin security dashboard routes
// and wires the step-up middleware on POST /api/auth/login.
//
// IMPORTANT: call this AFTER registerAuthRoutes so loginInnerHandler is set.
// This function wraps loginInnerHandler with StepUpMiddleware; the mux entry
// for POST /api/auth/login always delegates through loginInnerHandler so no
// re-registration (and no ServeMux panic) is needed.
func RegisterSecurity(
	mux *http.ServeMux,
	secR SecurityResult,
	requireAdmin func(http.Handler) http.Handler,
	_ *superadmin.Store, // reserved for future cross-package needs
	authStore *auth.Store,
) {
	pages := security.NewPages(secR.Store)

	mux.Handle("GET /superadmin/security",
		requireAdmin(http.HandlerFunc(pages.SecurityDashboard)))
	mux.Handle("POST /superadmin/security/ato/dismiss",
		requireAdmin(http.HandlerFunc(pages.DismissATO)))

	log.Println("[security] dashboard routes registered (/superadmin/security)")

	// Wire step-up middleware on POST /api/auth/login by wrapping the inner
	// handler stored by registerAuthRoutes.  The mux entry for the login route
	// delegates through loginInnerHandler (package var in routes_auth.go), so
	// replacing loginInnerHandler here atomically enables the step-up gate
	// without any ServeMux re-registration.
	if loginInnerHandler == nil {
		log.Println("[security] WARNING: loginInnerHandler not set — step-up middleware not applied (call registerAuthRoutes before RegisterSecurity)")
		return
	}
	stepUpCfg := security.StepUpConfig{
		Store:     secR.Store,
		Threshold: security.StepUpThreshold,
		Sender:    NewStepUpSender(),
		// LookupUserID: resolve email → account ID so risk history is keyed on
		// the stable account ID rather than the email string. Wired here (not
		// in security/) to avoid a circular import: security ← auth.
		LookupUserID: func(ctx context.Context, email string) (string, error) {
			return authStore.UserIDForEmail(ctx, email)
		},
	}
	loginInnerHandler = security.StepUpMiddleware(stepUpCfg, loginInnerHandler)
	log.Println("[security] step-up middleware applied to POST /api/auth/login with real InboxSender")
}

// StartCTMonitor starts the daily CT monitoring background worker.
// The first run is deferred by VULOS_CT_FIRST_RUN_DEFER_SECONDS (default 60s)
// so that the ~30s crt.sh round-trip does not block server startup.
func StartCTMonitor(ctx context.Context, store *security.Store) {
	monitor := newCTMonitor(store)
	monitor.RunDaily(ctx)
	log.Println("[security/ct] CT monitor started (daily, first-run deferred 60s)")
}

// newCTMonitor builds a CT monitor for the deployment's own zone (env.Domain())
// so a self-host / non-prod cloud watches its own certs instead of vulos.org.
// Prod default = vulos.org. Split out from StartCTMonitor so the domain wiring
// is unit-testable without launching the background worker.
func newCTMonitor(store *security.Store) *security.CTMonitor {
	return security.NewCTMonitor(store, env.Domain())
}
