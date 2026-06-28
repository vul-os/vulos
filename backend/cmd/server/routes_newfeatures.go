package main

// routes_newfeatures.go — integration wiring for packages that were built but
// deliberately NOT wired into the server to avoid parallel-edit conflicts.
//
// Wired here:
//   - internal/airouter     — AI model router (BYO + cloud mode) replacing the
//                             inline /api/ai/chat and /api/ai/status handlers
//                             that have been removed from main.go.
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
	"vulos/backend/internal/cgroups"
	"vulos/backend/internal/cpbilling"
	"vulos/backend/internal/llmuxclient"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/appnet"
	svcauth "vulos/backend/services/auth"
	"vulos/backend/services/integrations"
)

// newFeatureDeps carries the server-level objects that the new-feature routes
// need.
type newFeatureDeps struct {
	dbDir     string
	netMgr    *appnet.Manager
	visStore  *appnet.VisibilityStore
	authStore *svcauth.Store // for IDENTITY-06 recovery handlers
	// integrationsClient is the shared cloud OAuth broker client (INTEG-03/04).
	// The gateway uses the same instance to inject tokens, so they share one
	// token cache. nil → a fresh client is created from env (e.g. tests).
	integrationsClient *integrations.Client
}

// registerNewFeatureRoutes wires the four previously-unwired handler packages
// onto mux.  Must be called after RegisterVisibilityHandlers (appnet) because
// RegisterSubdomainHandlers adds routes that extend the visibility surface.
//
// It returns the shared *multiinstance.AppSync (CRDT merge primitive) opened on
// the single shared registry handle so the caller can reuse it for the fabric
// P2P same-LAN sync service WITHOUT opening a second *sql.DB to the
// single-writer file (audit P1-4). Returns nil when the registry is
// unavailable.
func registerNewFeatureRoutes(mux *http.ServeMux, deps newFeatureDeps, serverCtx ...context.Context) *multiinstance.AppSync {
	var sharedAppSync *multiinstance.AppSync
	// Resolve the server shutdown context.  A variadic parameter lets existing
	// call-sites compile without change; pass serverCtx explicitly to bind
	// background goroutines to the server lifecycle.
	bgCtx := context.Background()
	if len(serverCtx) > 0 && serverCtx[0] != nil {
		bgCtx = serverCtx[0]
	}

	// ── Shared multi-instance registry ───────────────────────────────────────
	//
	// FIX (audit P1-4): the multiinstance registry is a single-writer SQLite
	// file (SetMaxOpenConns(1)). Previously sections 3/6/8/9 each called
	// multiinstance.Open() on the SAME path, producing four independent *sql.DB
	// handles to one file — concurrent writers across those handles race for the
	// single writer lock and surface SQLITE_BUSY. Open it ONCE here and share the
	// one *Registry (hence one *sql.DB) across every section below.
	sharedReg := openSharedRegistry(deps.dbDir)
	// ── 1. LLM gateway client (internal/llmuxclient) ──────────────────────────
	//
	// Routes registered:
	//   POST /api/ai/chat    — SSE chat completion via llmux gateway
	//   POST /api/ai/models  — list models (proxied from llmux)
	//   GET  /api/ai/status  — llmux gateway status
	//   PUT  /api/ai/config  — 410 Gone (config now lives in llmux)
	//   POST /api/ai/embed   — embedding via llmux
	//   GET  /api/ai/embed/stats — embedding cache stats
	//   POST /api/ai/notes/index   — index a note embedding
	//   POST /api/ai/notes/search  — semantic search over notes
	//   DELETE /api/ai/notes/{note_id}/embed — remove note embedding
	//
	// LLM/AI requests are forwarded to the llmux gateway (LLMUX_URL).
	// Billing and metering are handled by llmux → CP; the OS is transparent.
	{
		lmCfg, lmOk := llmuxclient.FromEnv()
		if !lmOk {
			log.Printf("[llmuxclient] LLMUX_URL unset — /api/ai/* routes will return 503 (set LLMUX_URL to enable)")
		}
		lmClient := llmuxclient.New(lmCfg)
		lmStore, lmErr := llmuxclient.NewStoreFromEnv(deps.dbDir)
		if lmErr != nil {
			log.Printf("[llmuxclient] store init warning: %v — note embeddings disabled", lmErr)
			lmStore = nil
		}
		llmuxclient.RegisterHandlers(mux, lmClient, lmStore)
		log.Printf("[llmuxclient] registered /api/ai/* routes (gateway=%s configured=%v)", lmCfg.BaseURL, lmOk)
	}

	// ── 3. Multi-instance app router (internal/multiinstance) ────────────────
	//
	// Routes registered:
	//   GET /api/routing/apps — cross-instance app routing table
	//
	// The registry SQLite DB sits in dbDir.  The shared handle opened above is
	// reused so all multiinstance sections write through one *sql.DB.
	//
	// deployStore is declared here and set in section 4. The AppProvider closure
	// captures it by reference so it returns real deployment data once section 4
	// has completed (the func is called at request time, not at registration time).
	var deployStore *appnet.DeploymentStore
	{
		reg := sharedReg
		if reg != nil {
			appRouter := multiinstance.NewAppRouter(reg, "")
			appRouter.WithAppProvider(func() []multiinstance.PublishedApp {
				if deployStore == nil {
					return nil
				}
				var apps []multiinstance.PublishedApp
				for _, d := range deployStore.All() {
					if d == nil {
						continue
					}
					apps = append(apps, multiinstance.PublishedApp{
						App:     d.AppID,
						Profile: d.Profile,
					})
				}
				return apps
			})
			multiinstance.RegisterHandlers(mux, appRouter)
			log.Printf("[multiinstance] registered GET /api/routing/apps")
		} else {
			log.Printf("[multiinstance] registry unavailable — /api/routing/apps disabled")
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
		var dsErr error
		deployStore, dsErr = appnet.NewDeploymentStore()
		if dsErr != nil {
			log.Printf("[appnet/subdomain] deployment store init warning: %v — trying temp path", dsErr)
			os.Setenv("VULOS_DEPLOY_DB", filepath.Join(os.TempDir(), "vulos-app-deployments.json"))
			deployStore, dsErr = appnet.NewDeploymentStore()
			if dsErr != nil {
				log.Printf("[appnet/subdomain] deployment store fallback error: %v — subdomain routes disabled", dsErr)
				return sharedAppSync
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
	// FIX (audit P1-4): reuse the single shared registry handle opened above.
	{
		reg := sharedReg
		if reg != nil {
			// deviceToken is empty at startup; it is updated on successful cloud login.
			syncer := multiinstance.NewCloudSyncer(reg, "")
			multiinstance.RegisterSyncHandlers(mux, syncer)
			log.Printf("[multiinstance/sync] registered GET /api/instances (cloud_url=%s)", multiinstance.DefaultCloudBaseURL)
		} else {
			log.Printf("[multiinstance/sync] registry unavailable — /api/instances disabled")
		}
	}

	// ── 6b. Cloud OAuth integrations consumer (services/integrations, INTEG-03) ──
	//
	// Routes registered:
	//   GET /api/integrations/google/status — connection status (via broker probe)
	//   GET /api/integrations/google/token  — short-lived Google access token
	//
	// The box mints short-lived access tokens from the cloud broker over the
	// device-HMAC channel (DEVICE_SHARED_SECRET + VULOS_DEVICE_ULID). The refresh
	// token + client secret stay in the control plane. Unprovisioned boxes report
	// configured=false and the token endpoint 503s — fail-closed.
	{
		integClient := deps.integrationsClient
		if integClient == nil {
			integClient = integrations.NewClientFromEnv()
		}
		integrations.RegisterHandlers(mux, integClient)
		log.Printf("[integrations] registered GET /api/integrations/google/{status,token} (configured=%v)", integClient.Configured())
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
	//   POST /api/instances/provision      — request a new Fly Machine
	//   GET  /api/instances/{ulid}/status  — poll provisioning status
	//
	// FIX (audit P1-4): reuse the single shared registry handle opened above.
	// deviceToken is empty until cloud login; the provisioner degrades to 503.
	{
		reg := sharedReg
		if reg != nil {
			provisioner := multiinstance.NewProvisioner(reg, "").
				WithBilling(cpbilling.New(cpbilling.Config{}))
			multiinstance.RegisterProvisionHandlers(mux, provisioner)
			log.Printf("[multiinstance/provision] registered POST /api/instances/provision, GET /api/instances/{ulid}/status")
		} else {
			log.Printf("[multiinstance/provision] registry unavailable — provision routes disabled")
		}
	}

	// ── 9. Multi-instance notification fan-out (internal/multiinstance, MINST-06) ─
	//
	// Routes registered:
	//   POST /api/notify/fanout   — fan out notification to all online instances
	//   POST /api/notify/receive  — inbound notification from relay (dedup + deliver)
	//
	// The batcher goroutine runs for the lifetime of the process.
	// FIX (audit P1-4): reuse the single shared registry handle opened above.
	{
		reg := sharedReg
		if reg != nil {
			fanout := multiinstance.NewNotifyFanout(reg)
			go fanout.RunBatcher(bgCtx)
			multiinstance.RegisterNotifyHandlers(mux, fanout)
			log.Printf("[multiinstance/notify] registered POST /api/notify/fanout, POST /api/notify/receive")
		} else {
			log.Printf("[multiinstance/notify] registry unavailable — notify routes disabled")
		}
	}

	// ── 10. CRDT app-registry sync (internal/multiinstance, MINST-04) ────────────
	//
	// FIX (audit P2-7): RegisterAppSyncHandlers was implemented but never wired.
	// Wire it on the shared registry so the per-instance app inventory surface
	// (GET /api/instances/{ulid}/apps) is actually reachable.
	{
		reg := sharedReg
		if reg != nil {
			if appSync, asErr := multiinstance.OpenAppSync(reg); asErr != nil {
				log.Printf("[multiinstance/appsync] open warning: %v — app-sync routes disabled", asErr)
			} else {
				multiinstance.RegisterAppSyncHandlers(mux, appSync)
				sharedAppSync = appSync // reused by the fabric P2P sync service (FABRIC-P2P-01)
				log.Printf("[multiinstance/appsync] registered GET /api/instances/{ulid}/apps")
			}
		} else {
			log.Printf("[multiinstance/appsync] registry unavailable — app-sync routes disabled")
		}
	}

	return sharedAppSync
}

// openSharedRegistry opens the single multiinstance registry handle shared by
// all multiinstance route sections. It mirrors the previous per-section
// fallback behaviour (temp-dir on primary open failure) but produces ONE
// *sql.DB so the single-writer SQLite file is never contended by independent
// handles (audit P1-4).
func openSharedRegistry(dbDir string) *multiinstance.Registry {
	regPath := filepath.Join(dbDir, "multiinstance.db")
	reg, err := multiinstance.Open(regPath)
	if err != nil {
		log.Printf("[multiinstance] registry open warning: %v — trying temp dir", err)
		reg, err = multiinstance.Open(filepath.Join(os.TempDir(), "vulos-multiinstance.db"))
		if err != nil {
			log.Printf("[multiinstance] registry fallback open error: %v — multiinstance routes disabled", err)
			return nil
		}
	}
	return reg
}
