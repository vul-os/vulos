package main

// routes_newfeatures.go — integration wiring for packages that were built but
// deliberately NOT wired into the server to avoid parallel-edit conflicts.
//
// Wired here:
//   - internal/airouter     — AI model router (BYO + cloud mode) replacing the
//                             inline /api/ai/chat and /api/ai/status handlers
//                             that have been removed from main.go.
//   - internal/identity     — E2E-encrypted OS mail identity + mailbox.
//   - internal/multiinstance — per-instance app routing table.
//   - services/appnet       — subdomain provisioning (PUBWEB-02).
//
// Also wired here (second wave):
//   - internal/auth.RegisterRecoveryHandlers — BIP39 account recovery (IDENTITY-06).
//   - internal/multiinstance.RegisterSyncHandlers — cloud sync (MINST-02).
//   - services/appnet.RegisterEdgeCacheHandlers — edge-cache purge/stats (PUBWEB-03).
//
// Also wired here (third wave):
//   - services/appnet.RegisterCustomDomainHandlers — custom domains for published
//     apps (PUBWEB-07): POST/GET/DELETE /api/apps/{id}/domain + /domain/verify.
//
// Called from main.go via registerNewFeatureRoutes(mux, deps).

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	internalauth "vulos/backend/internal/auth"
	"vulos/backend/internal/airouter"
	"vulos/backend/internal/cgroups"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/identity"
	svcauth "vulos/backend/services/auth"
	"vulos/backend/services/appnet"
)

// newFeatureDeps carries the server-level objects that the new-feature routes
// need.
type newFeatureDeps struct {
	dbDir     string
	netMgr    *appnet.Manager
	visStore  *appnet.VisibilityStore
	authStore *svcauth.Store // for IDENTITY-06 recovery handlers
}

// registerNewFeatureRoutes wires the four previously-unwired handler packages
// onto mux.  Must be called after RegisterVisibilityHandlers (appnet) because
// RegisterSubdomainHandlers adds routes that extend the visibility surface.
func registerNewFeatureRoutes(mux *http.ServeMux, deps newFeatureDeps, serverCtx ...context.Context) {
	// Resolve the server shutdown context.  A variadic parameter lets existing
	// call-sites compile without change; pass serverCtx explicitly to bind
	// background goroutines to the server lifecycle.
	bgCtx := context.Background()
	if len(serverCtx) > 0 && serverCtx[0] != nil {
		bgCtx = serverCtx[0]
	}
	// ── 1. AI Router (internal/airouter) ─────────────────────────────────────
	//
	// Routes registered:
	//   POST /api/ai/chat    — SSE chat completion (replaces inline handler
	//                          removed from main.go)
	//   POST /api/ai/models  — list available providers/models
	//   GET  /api/ai/status  — current mode + active model (replaces inline
	//                          handler removed from main.go)
	//   PUT  /api/ai/config  — update mode / model / provider
	//
	// The store DB lands next to the other per-service DBs in dbDir.
	// AIROUTER_DB overrides the default path; AIROUTER_KEY_HEX provides the
	// AES-256 key (dev falls back to a deterministic constant — not for prod).
	if deps.dbDir != "" {
		os.Setenv("AIROUTER_DB", filepath.Join(deps.dbDir, "airouter.db"))
	}
	airStore, err := airouter.NewStoreFromEnv()
	if err != nil {
		// In VULOS_ENV=prod with AIROUTER_KEY_HEX unset the store refuses to
		// initialise (fail-closed). Disable the AI router routes with a clear
		// message rather than crashing the whole server so other features stay
		// available.
		log.Printf("[airouter] store init refused: %v — /api/ai/* routes DISABLED (set AIROUTER_KEY_HEX to a 64-hex-char value)", err)
	} else {
		airRouter := airouter.NewRouter(airStore)
		airouter.RegisterHandlers(mux, airRouter)
		log.Printf("[airouter] registered POST /api/ai/chat, POST /api/ai/models, GET /api/ai/status, PUT /api/ai/config")
	}

	// ── 2. Identity (internal/identity) ─────────────────────────────────────
	//
	// Routes registered:
	//   POST  /api/identity/send
	//   GET   /api/identity/mailbox
	//   GET   /api/identity/mailbox/{id}
	//   PATCH /api/identity/mailbox/{id}
	//   GET   /api/identity/identity
	//   POST  /api/identity/identity/rotate
	//
	// Identity is nil on first boot (no keypair generated yet); the handlers
	// return 404/412 gracefully.  The relay key resolver returns a "not
	// implemented" error for Send until the relay client is complete.
	{
		vmailDBPath := ""
		if deps.dbDir != "" {
			vmailDBPath = filepath.Join(deps.dbDir, "identity.db")
		}
		vmailStore, _ := identity.NewStore(vmailDBPath) // degraded mode on error

		relayURL := os.Getenv("VULOS_RELAY_URL") // empty → DefaultRelayURL
		vmailSvc := identity.New(nil, vmailStore, relayURL)

		// Real relay-backed KeyResolver (IDENTITY-05).
		relayClient := identity.NewRelayClient(relayURL)

		// SessionValidator: resolve X-OS-Session against the auth Store so
		// the identity handler no longer accepts an arbitrary non-empty header as proof of
		// identity. When authStore is nil (degraded mode), pass nil — the
		// identity handler is fail-closed in prod and dev-permissive otherwise.
		var sessVal identity.SessionValidator
		if deps.authStore != nil {
			sessVal = identitySessionAdapter{store: deps.authStore}
		}
		identity.RegisterHandlersWithSession(mux, vmailSvc, relayClient, sessVal)
		log.Printf("[identity] registered /api/identity/* routes")
	}

	// ── 3. Multi-instance app router (internal/multiinstance) ────────────────
	//
	// Routes registered:
	//   GET /api/routing/apps — cross-instance app routing table
	//
	// The registry SQLite DB sits in dbDir.  On open failure we fall back to a
	// temp-dir path so the route still registers (returns an empty table).
	{
		regPath := filepath.Join(deps.dbDir, "multiinstance.db")
		reg, regErr := multiinstance.Open(regPath)
		if regErr != nil {
			log.Printf("[multiinstance] registry open warning: %v — trying temp dir", regErr)
			reg, regErr = multiinstance.Open(filepath.Join(os.TempDir(), "vulos-multiinstance.db"))
			if regErr != nil {
				log.Printf("[multiinstance] registry fallback open error: %v — routing disabled", regErr)
			}
		}
		if reg != nil {
			appRouter := multiinstance.NewAppRouter(reg, "")
			// Stub AppProvider: returns nil so the handler returns [] not null.
			// MINST-02 (cloud sync) will replace this with a real provider.
			appRouter.WithAppProvider(func() []multiinstance.PublishedApp { return nil })
			multiinstance.RegisterHandlers(mux, appRouter)
			log.Printf("[multiinstance] registered GET /api/routing/apps")
		}
	}

	// ── 4. Subdomain provisioning (services/appnet) ───────────────────────────
	//
	// Routes registered:
	//   GET  /api/apps/{id}/deployment   — current deployment info
	//   POST /api/apps/{id}/deprovision  — remove a deployment
	//
	// VULOS_DNS_API=noop   — skips the real cloud DNS call (dev/CI safe).
	// VULOS_CADDY_DIR=noop — skips Caddyfile snippet writes.
	//
	// In dev/local we default both to "noop" (with a warning) so a fresh
	// checkout boots.  In VULOS_ENV=prod a noop default would silently lie to
	// customers (their domain is "provisioned" but no DNS/Caddy work happens),
	// so we refuse to register the subdomain routes when either is unset.
	prodMode := os.Getenv("VULOS_ENV") == "prod"
	provisioningMisconfigured := false
	if os.Getenv("VULOS_DNS_API") == "" {
		if prodMode {
			log.Printf("[appnet/subdomain] DNS provisioning DISABLED in prod: VULOS_DNS_API is unset — refusing to register routes so customers are not falsely told their domain is being provisioned")
			provisioningMisconfigured = true
		} else {
			log.Printf("[appnet/subdomain] WARNING: VULOS_DNS_API unset — defaulting to noop (dev/CI only)")
			os.Setenv("VULOS_DNS_API", "noop")
		}
	}
	if os.Getenv("VULOS_CADDY_DIR") == "" {
		if prodMode {
			log.Printf("[appnet/subdomain] Caddy snippet writes DISABLED in prod: VULOS_CADDY_DIR is unset — refusing to register routes")
			provisioningMisconfigured = true
		} else {
			log.Printf("[appnet/subdomain] WARNING: VULOS_CADDY_DIR unset — defaulting to noop (dev/CI only)")
			os.Setenv("VULOS_CADDY_DIR", "noop")
		}
	}
	if !provisioningMisconfigured {
		deployStore, dsErr := appnet.NewDeploymentStore()
		if dsErr != nil {
			log.Printf("[appnet/subdomain] deployment store init warning: %v — trying temp path", dsErr)
			os.Setenv("VULOS_DEPLOY_DB", filepath.Join(os.TempDir(), "vulos-app-deployments.json"))
			deployStore, dsErr = appnet.NewDeploymentStore()
			if dsErr != nil {
				log.Printf("[appnet/subdomain] deployment store fallback error: %v — subdomain routes disabled", dsErr)
				return
			}
		}
		provisioner := appnet.NewProvisioner(deployStore, nil)
		appnet.RegisterSubdomainHandlers(mux, deps.visStore, provisioner, deps.netMgr)
		log.Printf("[appnet/subdomain] registered GET /api/apps/{id}/deployment, POST /api/apps/{id}/deprovision")

		// ── 4a. Edge-cache purge/stats (services/appnet, PUBWEB-03) ─────────────
		//
		// Routes registered:
		//   POST /api/apps/{id}/cache/purge — reload Nginx cache for an app
		//   GET  /api/apps/{id}/cache/stats — Nginx stub_status metrics
		//
		// VULOS_NGINX_DIR=noop  — disables actual Nginx config writes in dev/CI.
		// VULOS_NGINX_CACHE_DIR — left to default (/var/cache/nginx/vulos) unless
		//                         already set.
		// The same provisioner constructed above is reused so both sets of routes
		// share the same DeploymentStore.
		//
		// In VULOS_ENV=prod an unset value must NOT silently default to noop —
		// purge requests would be acked while leaving the edge cache stale.
		edgeCacheMisconfigured := false
		if os.Getenv("VULOS_NGINX_DIR") == "" {
			if prodMode {
				log.Printf("[appnet/edgecache] DISABLED in prod: VULOS_NGINX_DIR is unset — refusing to register cache purge/stats routes")
				edgeCacheMisconfigured = true
			} else {
				log.Printf("[appnet/edgecache] WARNING: VULOS_NGINX_DIR unset — defaulting to noop (dev/CI only)")
				os.Setenv("VULOS_NGINX_DIR", "noop")
			}
		}
		if !edgeCacheMisconfigured {
			ecm := appnet.NewEdgeCacheManager()
			appnet.RegisterEdgeCacheHandlers(mux, ecm, provisioner)
			log.Printf("[appnet/edgecache] registered POST /api/apps/{id}/cache/purge, GET /api/apps/{id}/cache/stats")
		}

		// ── 4b. Custom domain support (services/appnet, PUBWEB-07) ──────────────────
		//
		// Routes registered:
		//   POST   /api/apps/{id}/domain         — attach a custom domain (TXT challenge)
		//   GET    /api/apps/{id}/domain         — return current domain record
		//   POST   /api/apps/{id}/domain/verify  — verify TXT record and activate domain
		//   DELETE /api/apps/{id}/domain         — remove custom domain
		//
		// VULOS_CUSTOMDOMAIN_DB overrides the default JSON store path.
		// VULOS_CADDY_DIR (already set to "noop" above in dev/CI) is honoured by the
		// custom domain handler so no Caddyfile writes happen in non-production.
		// The same provisioner constructed above is reused to validate that an app is
		// already deployed before a custom domain can be attached.
		{
			cdStore, cdErr := appnet.NewCustomDomainStore()
			if cdErr != nil {
				log.Printf("[appnet/customdomain] store init warning: %v — trying temp path", cdErr)
				os.Setenv("VULOS_CUSTOMDOMAIN_DB", filepath.Join(os.TempDir(), "vulos-app-custom-domains.json"))
				cdStore, cdErr = appnet.NewCustomDomainStore()
				if cdErr != nil {
					log.Printf("[appnet/customdomain] store fallback error: %v — custom domain routes disabled", cdErr)
				}
			}
			if cdStore != nil {
				appnet.RegisterCustomDomainHandlers(mux, cdStore, provisioner, deps.netMgr)
				log.Printf("[appnet/customdomain] registered POST/GET/DELETE /api/apps/{id}/domain, POST /api/apps/{id}/domain/verify")
			}
		}
	}

	// ── 5. BIP39 account recovery (internal/auth, IDENTITY-06) ───────────────────
	//
	// Routes registered:
	//   POST /api/auth/recovery/generate — generate + store recovery kit (session required)
	//   POST /api/auth/recovery/restore  — restore keyring from mnemonic (public)
	//   POST /api/auth/recovery/escrow   — verify + return escrow blob (session required)
	//
	// The auth.Store implements internal/auth.RecoveryStore via recovery_store.go.
	// When authStore is nil (degraded mode) the routes are skipped with a warning.
	if deps.authStore != nil {
		internalauth.RegisterRecoveryHandlers(mux, deps.authStore)
		log.Printf("[auth/recovery] registered POST /api/auth/recovery/{generate,restore,escrow}")
	} else {
		log.Printf("[auth/recovery] authStore unavailable — recovery routes disabled")
	}

	// ── 6. Cloud-sync instance list (internal/multiinstance, MINST-02) ────────────
	//
	// Routes registered:
	//   GET /api/instances — merged registry list (graceful offline fallback)
	//
	// CloudSyncer contacts VULOS_CLOUD_URL (default https://api.vulos.org).  When
	// the cloud is unreachable it returns the last-known local registry state.
	// The registry opened above (section 3) is shared; if it was not opened we
	// open a fresh one here so the route still registers.
	{
		regPath := filepath.Join(deps.dbDir, "multiinstance.db")
		reg, regErr := multiinstance.Open(regPath)
		if regErr != nil {
			reg, regErr = multiinstance.Open(filepath.Join(os.TempDir(), "vulos-multiinstance-sync.db"))
		}
		if reg != nil {
			// deviceToken is empty at startup; it is updated on successful cloud login.
			syncer := multiinstance.NewCloudSyncer(reg, "")
			multiinstance.RegisterSyncHandlers(mux, syncer)
			log.Printf("[multiinstance/sync] registered GET /api/instances (cloud_url=%s)", multiinstance.DefaultCloudBaseURL)
		} else {
			log.Printf("[multiinstance/sync] registry unavailable (%v) — /api/instances disabled", regErr)
		}
	}

	// ── 7. cgroup Alerter (internal/cgroups, PUBWEB-08) ─────────────────────────
	//
	// Routes registered:
	//   GET /api/cgroups/alerts/status — per-app throttle/alert state
	//
	// The Governor uses the shared cgroups DB in dbDir.  The Alerter background
	// loop starts in a goroutine tied to a background context (it runs for the
	// lifetime of the process).
	{
		cgDBPath := ""
		if deps.dbDir != "" {
			cgDBPath = filepath.Join(deps.dbDir, "cgroups.db")
		}
		gov, cgErr := cgroups.NewGovernor(cgDBPath, "", nil)
		if cgErr != nil {
			log.Printf("[cgroups/alerter] governor init warning: %v — trying temp path", cgErr)
			gov, cgErr = cgroups.NewGovernor(
				filepath.Join(os.TempDir(), "vulos-cgroups.db"), "", nil)
		}
		if cgErr != nil {
			log.Printf("[cgroups/alerter] governor fallback error: %v — alerts route disabled", cgErr)
		} else {
			alerter := cgroups.NewAlerter(gov)
			go alerter.Start(bgCtx)
			alerter.RegisterRoutes(mux)
			log.Printf("[cgroups/alerter] registered GET /api/cgroups/alerts/status")
		}
	}

	// ── 8. Cloud instance provisioning (internal/multiinstance, MINST-05) ────────
	//
	// Routes registered:
	//   POST /api/instances/provision      — request a new Koyeb instance
	//   GET  /api/instances/{ulid}/status  — poll provisioning status
	//
	// Reuses a fresh registry handle (parallel to sections 3/6).  deviceToken is
	// empty until cloud login; the provisioner degrades to 503 in that case.
	{
		regPath := filepath.Join(deps.dbDir, "multiinstance.db")
		reg, regErr := multiinstance.Open(regPath)
		if regErr != nil {
			reg, regErr = multiinstance.Open(
				filepath.Join(os.TempDir(), "vulos-multiinstance-provision.db"))
		}
		if reg != nil {
			provisioner := multiinstance.NewProvisioner(reg, "")
			multiinstance.RegisterProvisionHandlers(mux, provisioner)
			log.Printf("[multiinstance/provision] registered POST /api/instances/provision, GET /api/instances/{ulid}/status")
		} else {
			log.Printf("[multiinstance/provision] registry unavailable (%v) — provision routes disabled", regErr)
		}
	}

	// ── 9. Multi-instance notification fan-out (internal/multiinstance, MINST-06) ─
	//
	// Routes registered:
	//   POST /api/notify/fanout   — fan out notification to all online instances
	//   POST /api/notify/receive  — inbound notification from relay (dedup + deliver)
	//
	// The batcher goroutine runs for the lifetime of the process.  Reuses a fresh
	// registry handle (parallel to sections 3/6/8).
	{
		regPath := filepath.Join(deps.dbDir, "multiinstance.db")
		reg, regErr := multiinstance.Open(regPath)
		if regErr != nil {
			reg, regErr = multiinstance.Open(
				filepath.Join(os.TempDir(), "vulos-multiinstance-notify.db"))
		}
		if reg != nil {
			fanout := multiinstance.NewNotifyFanout(reg)
			go fanout.RunBatcher(bgCtx)
			multiinstance.RegisterNotifyHandlers(mux, fanout)
			log.Printf("[multiinstance/notify] registered POST /api/notify/fanout, POST /api/notify/receive")
		} else {
			log.Printf("[multiinstance/notify] registry unavailable (%v) — notify routes disabled", regErr)
		}
	}
}

// identitySessionAdapter bridges svcauth.Store.ValidateToken into the
// identity.SessionValidator interface so the identity package can resolve X-OS-Session tokens
// to a live, unrevoked user without importing services/auth directly.
type identitySessionAdapter struct {
	store *svcauth.Store
}

func (a identitySessionAdapter) ValidateOSSession(token string) (string, bool) {
	if a.store == nil {
		return "", false
	}
	sess, ok := a.store.ValidateToken(token)
	if !ok || sess == nil {
		return "", false
	}
	return sess.UserID, true
}

