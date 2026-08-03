// no-broker-dep:allow-file: comments explain the optional self-hosted control-plane gateway seam
// (e.g. Pier) and a STALE legacy log string ('using ephor default') on
// relayconfig.Init's error-fallback path, which actually calls
// DefaultConfig() (Provider: vulos) -- no import, no code path actually
// defaults to dialling Pier. (Stale-wording finding reported separately,
// not fixed here -- out of scope for a broker-gate marker.)

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"vulos/backend/internal/appsgate"
	"vulos/backend/internal/datadir"

	apikeyseam "vulos/backend/internal/apikey"
	internalauth "vulos/backend/internal/auth"
	"vulos/backend/internal/config"
	"vulos/backend/internal/cpbilling"
	"vulos/backend/internal/deploymode"
	"vulos/backend/internal/directlisten"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/gpuhost"
	"vulos/backend/internal/lan"
	"vulos/backend/internal/llmuxclient"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/osroute"
	"vulos/backend/internal/storage"
	"vulos/backend/services/accountsecurity"
	"vulos/backend/services/ai"
	"vulos/backend/services/anchorinbox"
	"vulos/backend/services/appfs"
	"vulos/backend/services/appnet"
	"vulos/backend/services/assistant"
	"vulos/backend/services/audio"
	"vulos/backend/services/auth"
	"vulos/backend/services/authvault"
	"vulos/backend/services/bluetooth"
	"vulos/backend/services/bootmode"
	"vulos/backend/services/cluster"
	"vulos/backend/services/compliance"
	"vulos/backend/services/credvault"
	"vulos/backend/services/desktop"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/disks"
	"vulos/backend/services/display"
	"vulos/backend/services/drivers"
	"vulos/backend/services/embeddings"
	"vulos/backend/services/energy"
	vulenv "vulos/backend/services/env"
	"vulos/backend/services/files"
	"vulos/backend/services/fleetid"
	"vulos/backend/services/gateway"
	"vulos/backend/services/gpu"
	"vulos/backend/services/gwurl"
	"vulos/backend/services/installer"
	"vulos/backend/services/integrations"
	"vulos/backend/services/lease"
	"vulos/backend/services/models"
	"vulos/backend/services/network"
	"vulos/backend/services/notify"
	"vulos/backend/services/packages"
	"vulos/backend/services/passkeys"
	"vulos/backend/services/peering"
	"vulos/backend/services/peering/sfu"
	bprofiles "vulos/backend/services/profiles"
	ptyservice "vulos/backend/services/pty"
	"vulos/backend/services/reach"
	"vulos/backend/services/recall"
	"vulos/backend/services/relayconfig"
	"vulos/backend/services/sandbox"
	"vulos/backend/services/snapshot"
	"vulos/backend/services/storageprov"
	"vulos/backend/services/stream"
	"vulos/backend/services/sync"
	"vulos/backend/services/sysuser"
	"vulos/backend/services/telemetry"
	"vulos/backend/services/upload"
	"vulos/backend/services/vault"
	"vulos/backend/services/webbrowser"
	"vulos/backend/services/webproxy"
	"vulos/backend/services/wifi"
	"vulos/backend/services/wine"
	"vulos/backend/services/wltoplevel"

	"vulos/backend/internal/obs"
)

// Version is the server version string. The default is "dev"; it is overridden
// at build time via -ldflags "-X main.Version=vX.Y.Z" in the release pipeline.
var Version = "dev"

// shellCSP is the Content-Security-Policy applied to the OS shell + login HTML
// (and its same-origin assets) served from the static "/" handler. See the
// SEC-CSP-01 comment at that handler for the rationale behind each directive's
// strictness; the structural directives (frame-ancestors/object-src/base-uri/
// form-action) are strict, while content directives stay permissive enough to
// support runtime cloud/LAN failover and srcdoc-sandboxed AI viewports.
const shellCSP = "default-src 'self' blob: data: https:; " +
	"script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' https: wss: ws:; " +
	"frame-src 'self' blob: https:; " +
	"worker-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'self'"

// shellCSPFor returns shellCSP widened just enough to frame the per-app origins
// (ORIGIN-01). frame-src already allows https:, which covers app origins on any
// TLS deployment; a plaintext deployment (dev on lvh.me, or a box behind a proxy
// that terminates TLS) would otherwise have its own app frames blocked by its own
// CSP. We add ONLY the app-subdomain wildcard for the configured base domain —
// not a blanket http:, which would let the shell frame any plaintext origin.
//
// Nothing else in the policy moves: the structural directives (object-src,
// base-uri, form-action, frame-ancestors 'self') are untouched.
func shellCSPFor(baseDomain string) string {
	if !appnet.Enabled(baseDomain) {
		return shellCSP
	}
	appSrc := "https://*." + baseDomain + " http://*." + baseDomain
	return strings.Replace(shellCSP,
		"frame-src 'self' blob: https:;",
		"frame-src 'self' blob: https: "+appSrc+";", 1)
}

func main() {
	obs.Init()

	// Subcommand dispatch: `vulos backup` / `vulos restore` run a one-shot
	// snapshot/restore against S3 and exit, instead of starting the server.
	{
		cmdCtx, cmdCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		if handled, code := dispatchSubcommand(cmdCtx); handled {
			cmdCancel()
			os.Exit(code)
		}
		cmdCancel()
	}

	envFlag := flag.String("env", "", "Runtime environment: local, dev, or prod (default prod). Overrides VULOS_ENV.")
	versionFlag := flag.Bool("version", false, "Print the server version and exit.")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		os.Exit(0)
	}

	// Resolve and validate the environment.  An unrecognised value is fatal so
	// operators get immediate feedback instead of a silent misconfig.
	activeEnv, err := vulenv.Parse(*envFlag)
	if err != nil {
		log.Fatalf("[env] %v", err)
	}
	envDefaults := vulenv.DefaultsFor(activeEnv)

	log.Printf("[env] starting in %q mode (bind=%q skip_hw=%v debug_endpoints=%v)",
		activeEnv, envDefaults.BindHost, envDefaults.SkipHardwareChecks, envDefaults.DebugEndpoints)

	// Safety guard: abort if an obviously non-production shortcut has been
	// forced on while the runtime environment claims to be production.
	if activeEnv.IsProd() && envDefaults.DebugEndpoints {
		log.Fatal("[env] ABORT: debug endpoints are enabled but env=prod — this is a misconfiguration")
	}

	cfg := config.Load(activeEnv.String())

	// DEPLOY_MODE: standalone|os|cloud (typed enum, read once, self-reported at
	// boot). Unset ⇒ standalone — today's default behavior unchanged. Drives
	// storage-isolation defaults (STS auto-default) and app entitlement gating
	// (fail-closed on cloud/os, fully open on standalone/self-host).
	deployMode := deploymode.Load()

	// Ensure system state directory exists
	os.MkdirAll("/var/lib/vulos", 0755)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Data directory. `home` is the box DATA ROOT (not the OS home dir):
	// datadir.Root honours VULOS_DATA_DIR and defaults to $HOME/.vulos. Every
	// subsystem below takes this root and appends its own subdirectory, so the
	// whole box lives under one operator-selectable path.
	home := datadir.Root()
	dataDir := datadir.Join("data")
	os.MkdirAll(dataDir, 0755)
	dbDir := datadir.Join("db")
	os.MkdirAll(dbDir, 0755)

	// GATEWAY-01: load any persisted control-plane ("gateway") override before any
	// CP broker resolves its base URL. gwurl is the single source of truth read by
	// cloud login/signup/enroll, identity claim, instance routing, cloud sync, and
	// the OAuth integrations broker. Missing/corrupt file → env/unconfigured
	// (fail-safe) — Vulos operates no control plane, so an unconfigured box simply
	// has no gateway until the owner points it at one (e.g. a self-hosted Pier,
	// github.com/vul-os/pier) via env or Settings.
	if err := gwurl.Init(datadir.Root()); err != nil {
		log.Printf("[gateway] could not load persisted gateway override (%v) — falling back to env/unconfigured", err)
	}
	if u, src := gwurl.Resolved(); src != gwurl.SourceDefault {
		log.Printf("[gateway] control-plane URL = %s (source: %s)", u, src)
	} else {
		log.Printf("[gateway] no gateway configured — gateway-dependent features (cloud login/sync, OAuth integrations broker) report not-configured until an operator sets one (VULOS_CP_URL / VULOS_CLOUD_URL / VULOS_CLOUD_API_URL, or Settings)")
	}
	if err := relayconfig.Init(dbDir); err != nil {
		log.Printf("[relayconfig] could not load persisted relay provider override (%v) — falling back to the built-in default provider (vulos); ephor is opt-in and is NOT being dialled", err)
	}

	// S3 storage
	s3cfg := storage.LoadS3Config()
	if activeEnv.IsProd() && s3cfg.Configured() && s3cfg.Endpoint == "s3.amazonaws.com" && os.Getenv("S3_ENDPOINT") == "" {
		// Endpoint is the default — may be intentional (real AWS), but log for clarity.
		log.Printf("[s3] using default S3 endpoint s3.amazonaws.com — set S3_ENDPOINT if using S3-compatible storage (MinIO, Tigris, etc.)")
	}

	// Vault (Restic backup)
	v := vault.New(s3cfg, dataDir)
	if s3cfg.Configured() && vault.FindRestic() {
		if err := v.Init(ctx); err != nil {
			log.Printf("[vault] init warning: %v", err)
		} else {
			v.StartSchedule(ctx, 1*time.Hour)
		}
	} else {
		log.Printf("[vault] skipped — restic=%v s3=%v", vault.FindRestic(), s3cfg.Configured())
	}

	// Embeddings
	embCfg := embeddings.DefaultConfig()
	embedder := embeddings.New(embCfg)
	if activeEnv.IsProd() && embCfg.Backend == "ollama" && (embCfg.Endpoint == "http://localhost:11434" || embCfg.Endpoint == "") {
		log.Printf("[embeddings] WARNING: EMBED_ENDPOINT is unset in prod — using localhost:11434 which is almost certainly wrong; set EMBED_ENDPOINT or EMBED_BACKEND=openai")
	}

	// Recall (semantic search)
	recallSvc, err := recall.New(filepath.Join(dbDir, "recall.json"), dataDir, embedder)
	if err != nil {
		log.Printf("[recall] init warning: %v", err)
	} else {
		if err := embedder.HealthCheck(ctx); err != nil {
			log.Printf("[recall] embedder not available: %v — indexing disabled", err)
		} else {
			recallSvc.StartSchedule(ctx, 10*time.Minute)
		}
	}

	// App Networking (namespace isolation + port pool)
	netMgr := appnet.NewManager()
	portPool := appnet.NewPortPool(7070, 7999)
	launcher := appnet.NewLauncher(netMgr)
	trafficMon := appnet.NewTrafficMonitor()
	if err := netMgr.Init(ctx); err != nil {
		log.Printf("[appnet] init warning (needs root + iproute2): %v", err)
	}

	// DNS manager — writes /etc/hosts entries for app subdomains (calculator.vulos → namespace IP)
	dnsManager := appnet.NewDNSManager("vulos", netMgr)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				dnsManager.Remove()
				return
			case <-ticker.C:
				if err := dnsManager.Update(); err != nil {
					log.Printf("[dns] update failed: %v", err)
				}
			}
		}
	}()

	// Energy management
	energyMgr := energy.NewManager(energy.ModeBalanced)
	go energyMgr.Run(ctx)

	// Idle app killer — uses energy config for timeout
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				timeout := energyMgr.AppIdleTimeout()
				if timeout == 0 {
					continue // disabled in performance mode
				}
				idle := trafficMon.FindIdle(netMgr, timeout)
				for _, appID := range idle {
					log.Printf("[energy] app %s idle >%s — stopping + releasing port", appID, timeout)
					launcher.Stop(ctx, appID)
					portPool.Release(appID)
					trafficMon.Forget(appID)
				}
			}
		}
	}()

	// System user management (maps Vula profiles → Linux users)
	sysUserSvc := sysuser.New()

	// Remote access config — pass cfg so identity fields are populated from config/env.
	netSvc := network.New(cfg)

	// TURN/coturn persistent config store (NET-10)
	turnStore, err := network.NewTURNStore(dbDir)
	if err != nil {
		log.Printf("[turn] store init warning: %v", err)
		turnStore, _ = network.NewTURNStore(os.TempDir())
	}
	relayconfig.SetTURNStore(turnStore) // fixes the ICE/TURN split-brain

	// AI service
	aiSvc := ai.New()
	aiCfg := ai.DefaultConfig()
	if activeEnv.IsProd() && aiCfg.Provider == ai.ProviderOllama && (aiCfg.Endpoint == "http://localhost:11434" || aiCfg.Endpoint == "") {
		log.Printf("[ai] WARNING: AI_ENDPOINT is unset in prod — using localhost:11434 which is almost certainly wrong; set AI_ENDPOINT or AI_PROVIDER=claude")
	}
	chatHistory := ai.NewHistoryStore(dbDir)
	missionStore := ai.NewMissionStore(dbDir)

	// Peering (direct Vula-to-Vula communication)
	peeringSvc := peering.New(home)

	// Peering WebSocket multiplex hub
	peeringHub := peering.NewHub()

	// Notifications
	notifySvc := notify.New()

	// Proactive AI agent
	proactiveAgent := ai.NewProactiveAgent(aiSvc, aiCfg, notifySvc)
	// Register system checks

	// Threshold constants — named for readability and easy env-tuning.
	const (
		// proactiveMemThreshold is the memory usage percentage above which an alert fires.
		proactiveMemThreshold = 90.0
		// proactiveDiskThreshold is the disk usage percentage above which an alert fires.
		proactiveDiskThreshold = 90.0
		// proactiveTempThreshold is the CPU temperature (°C) above which an alert fires.
		proactiveTempThreshold = 85.0
		// proactiveCPUThreshold is the CPU load (%) used when no thermal sensor is present.
		proactiveCPUThreshold = 95.0
	)

	proactiveAgent.RegisterCheck(func(ctx context.Context) (string, string, notify.Level, bool) {
		// Low battery check
		st := energyMgr.State()
		if st.BatteryPercent > 0 && st.BatteryPercent <= 10 && !st.BatteryCharging {
			return "Battery Critical",
				fmt.Sprintf("Battery at %d%%. Connect charger soon.", st.BatteryPercent),
				notify.LevelUrgent, true
		}
		return "", "", "", false
	})

	proactiveAgent.RegisterCheck(func(ctx context.Context) (string, string, notify.Level, bool) {
		// High memory pressure check
		info := telemetry.SystemInfo()
		if info.MemPercent >= proactiveMemThreshold {
			return "High Memory Pressure",
				fmt.Sprintf("Memory usage is at %.0f%% (%d MB of %d MB used). Consider closing unused apps.",
					info.MemPercent, info.MemUsedMB, info.MemTotalMB),
				notify.LevelWarning, true
		}
		return "", "", "", false
	})

	proactiveAgent.RegisterCheck(func(ctx context.Context) (string, string, notify.Level, bool) {
		// Low disk space check — alert on any real mount above threshold
		status := disks.GetStatus()
		for _, m := range status.Mounts {
			if m.TotalMB > 0 && m.Percent >= proactiveDiskThreshold {
				return "Low Disk Space",
					fmt.Sprintf("Disk %s (%s) is %.0f%% full (%d MB free of %d MB). Free up space to avoid issues.",
						m.MountPoint, m.Device, m.Percent, m.FreeMB, m.TotalMB),
					notify.LevelWarning, true
			}
		}
		return "", "", "", false
	})

	proactiveAgent.RegisterCheck(func(ctx context.Context) (string, string, notify.Level, bool) {
		// High CPU temperature check; falls back to sustained high CPU load when
		// no thermal sensor is available (e.g. virtual machines, some ARM boards).
		//
		// Read max thermal zone temperature from sysfs (same source as telemetry).
		var maxTemp float64
		thermalMatches, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
		for _, m := range thermalMatches {
			if data, err := os.ReadFile(m); err == nil {
				var v float64
				if _, err2 := fmt.Sscanf(strings.TrimSpace(string(data)), "%f", &v); err2 == nil {
					if t := v / 1000; t > maxTemp {
						maxTemp = t
					}
				}
			}
		}
		if len(thermalMatches) > 0 {
			// Thermal sensors present — alert on high temperature.
			if maxTemp >= proactiveTempThreshold {
				return "High CPU Temperature",
					fmt.Sprintf("CPU temperature is %.1f°C, above the %.0f°C threshold. Check ventilation and reduce workload.",
						maxTemp, proactiveTempThreshold),
					notify.LevelWarning, true
			}
		} else {
			// No thermal sensors — fall back to 1-minute load average scaled per core.
			if data, err := os.ReadFile("/proc/loadavg"); err == nil {
				var load1 float64
				fmt.Sscanf(strings.TrimSpace(string(data)), "%f", &load1)
				info := telemetry.SystemInfo()
				numCPU := float64(info.CPUCores)
				if numCPU == 0 {
					numCPU = 1
				}
				if loadPct := (load1 / numCPU) * 100; loadPct >= proactiveCPUThreshold {
					return "Sustained High CPU Load",
						fmt.Sprintf("1-min load average is %.2f across %d cores (%.0f%% utilisation). System may be under heavy load.",
							load1, info.CPUCores, loadPct),
						notify.LevelWarning, true
				}
			}
		}
		return "", "", "", false
	})

	go proactiveAgent.Run(ctx, 60*time.Second)

	// Registry trust preflight (REGISTRY-SIGN-03).
	//
	// Resolve the app-signing trust chain HERE, at boot, rather than lazily on the
	// first install. A production box that has had signature verification switched
	// off must not start at all — a silent downgrade that only shows up when a user
	// clicks "Install" is exactly the failure this gate exists to prevent.
	switch pf := appnet.PreflightTrust(); {
	case pf.Fatal != nil:
		log.Fatalf("[registry] REFUSING TO START: %v\n"+
			"        App signature verification is mandatory in production.\n"+
			"        See docs/KEY-CEREMONY.md.", pf.Fatal)
	case pf.Degraded != nil:
		log.Printf("[registry] *** APP INSTALLS ARE DISABLED *** the trust chain did not resolve: %v", pf.Degraded)
		log.Printf("[registry] Signature verification is ON and refusing every entry. " +
			"Run the key ceremony (docs/KEY-CEREMONY.md) and re-sign registry.json to enable the App Hub.")
	case pf.Insecure:
		log.Printf("[registry] *** SECURITY DISABLED *** app signature verification is SKIPPED (%s). "+
			"This is permitted outside production ONLY. Never run a real box like this.", pf.Source)
	default:
		log.Printf("[registry] app signature verification active — trusted key: %s", pf.Source)
	}

	// App store
	appsDir := datadir.Join("apps")
	appStore := appnet.NewAppStore(appsDir)

	// App visibility store (private|local|public per app)
	visStore, err := appnet.NewVisibilityStoreAt(filepath.Join(dbDir, "visibility.json"))
	if err != nil {
		log.Printf("[visibility] init warning: %v", err)
	}

	// TURN server (WebRTC relay for remote mode)
	turnCfg := network.LoadTURNConfig()
	if turnCfg.Enabled {
		if cmd, err := turnCfg.StartCoturn(ctx, datadir.Join("tunnel")); err != nil {
			log.Printf("[turn] start warning: %v", err)
		} else {
			go func() { cmd.Wait(); log.Printf("[turn] coturn exited") }()
		}
	}

	// Sandbox (AI-generated Python scripts)
	sandboxSvc := sandbox.New(datadir.Root())

	// Browser profiles (isolated cookie jars / contexts)
	browserProfiles := bprofiles.NewStore(datadir.Join("db"))

	// Device profile — form-factor selection (pc|tv|car|watch)
	deviceProfile := bprofiles.NewDeviceProfileStore(dbDir)

	// Stream pool (generic X11 app streaming — Xvfb + GStreamer + WebRTC)
	streamPool := stream.NewPool()

	// cp billing client (entitlements + usage + suspension). Shared across the
	// billable OS surfaces wired directly in main.go (GPU/stream, SFU). When
	// CP_URL is unset this client is DISABLED and every gate Allows / every
	// meter is dropped — the standalone-OS path is unchanged.
	billingClient := cpbilling.New(cpbilling.Config{})

	// Wine prefix management (create/delete/DXVK per user)
	wineSvc := wine.New(datadir.Join("wine"))

	// Web proxy (for remote mode — kept for API proxy use)
	desktopSvc := desktop.New()
	proxySvc := webproxy.New()

	// System settings services
	wifiSvc := wifi.New()
	btSvc := bluetooth.New()
	audioSvc := audio.New()
	displaySvc := display.New()

	// Auth
	authStore, err := auth.NewStore(dbDir)
	if err != nil {
		log.Printf("[auth] init warning: %v", err)
	}
	authHandler := auth.NewHandler(authStore)
	authHandler.OnUserCreated = func(username, password, role string) {
		if err := sysUserSvc.EnsureUser(username, password, role); err != nil {
			log.Printf("[sysuser] failed to create Linux user %q: %v", username, err)
		}
	}

	authHandler.OnUserLogin = func(username, password, role string) {
		if err := sysUserSvc.EnsureUser(username, password, role); err != nil {
			log.Printf("[sysuser] login sync failed for %q: %v", username, err)
		}
		if err := sysUserSvc.SetPassword(username, password); err != nil {
			log.Printf("[sysuser] password sync failed for %q: %v", username, err)
		}
	}

	// vk_ API-key auth seam (VK-AUTH-01): accept `Authorization: Bearer vk_…`
	// on OS API endpoints by introspecting via the shared CP seam. Only active
	// when VULOS_CP_BASE_URL is set; unset = self-host mode, session-only auth
	// unchanged. Results are cached 60s in-process; fail-closed on CP errors.
	{
		vkCfg := apikeyseam.FromEnv()
		if vkCfg.Enabled() {
			authHandler.VKIntrospector = apikeyseam.NewIntrospector(vkCfg)
			log.Printf("[apikey] vk_ API-key auth enabled (cp=%s)", vkCfg.BaseURL)
		} else {
			log.Printf("[apikey] vk_ API-key auth disabled (VULOS_CP_BASE_URL not set — session-only auth)")
		}
	}

	authHandler.OnRoleChanged = func(username, role string) {
		if err := sysUserSvc.SetRole(username, role); err != nil {
			log.Printf("[sysuser] role sync failed for %q: %v", username, err)
		}
	}

	// Resolve user home from auth context for per-user data isolation in streamed apps
	streamPool.SetUserHomeResolver(func(r *http.Request) string {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			return "/root"
		}
		if u, ok := authStore.GetUser(userID); ok {
			if sysU := sysUserSvc.Lookup(u.Username); sysU != nil {
				return sysU.HomeDir
			}
		}
		return "/root"
	})

	// Streaming Chrome browser (real Chromium on the box via the stream pool),
	// offered ALONGSIDE the iframe "Smart Browser". Launched on-demand per user
	// with a persistent, isolated per-user Chrome profile (cookies/history/
	// logins). Session lifecycle + WebRTC are handled by streamPool; this service
	// only owns the virtual sound card and Chrome-specific launch/CDP concerns.
	// Restored from commit 12e7507^ (deleted in 12e7507) and adapted to the
	// current stream.Pool.Launch(LaunchOpts) API.
	browserSvc := webbrowser.New(streamPool)
	browserSvc.SetHomeResolver(func(r *http.Request) string {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			return "/root"
		}
		if u, ok := authStore.GetUser(userID); ok {
			if sysU := sysUserSvc.Lookup(u.Username); sysU != nil {
				return sysU.HomeDir
			}
		}
		return "/root"
	})

	// Ensure /dev/uinput exists (direct input injection, much faster than xdotool)
	if _, err := os.Stat("/dev/uinput"); os.IsNotExist(err) {
		exec.Command("mknod", "/dev/uinput", "c", "10", "223").Run()
		os.Chmod("/dev/uinput", 0666)
		log.Println("[init] created /dev/uinput")
	}

	// Set hostname to "vula" (Docker defaults to container ID)
	if os.Getuid() == 0 {
		os.WriteFile("/etc/hostname", []byte("vula\n"), 0644)
		exec.Command("hostname", "vula").Run()
		// Ensure hostname resolves in /etc/hosts (fixes sudo "unable to resolve host")
		if hosts, err := os.ReadFile("/etc/hosts"); err == nil {
			if !strings.Contains(string(hosts), "vula") {
				os.WriteFile("/etc/hosts", append(hosts, []byte("127.0.0.1 vula\n::1 vula\n")...), 0644)
			}
		}
	}

	// Ensure Linux users exist for all registered accounts (survives container rebuilds)
	if os.Getuid() == 0 {
		for _, ur := range authStore.ListUsersWithRoles() {
			if err := sysUserSvc.EnsureUser(ur.Username, "", ur.Role); err != nil {
				log.Printf("[sysuser] failed to reconcile user %q: %v", ur.Username, err)
			}
		}
	}

	// PTY service — resolves Vula userID → Linux username via auth store
	ptySvc := ptyservice.NewService(sysUserSvc, func(userID string) string {
		if u, ok := authStore.GetUser(userID); ok {
			return u.Username
		}
		return ""
	})

	// App auth gateway — all app traffic proxied through here
	appGateway := gateway.New(authStore, netMgr, portPool)

	// INTEG-04: cloud OAuth integration injection. Apps that declare
	// `integrations: ["google", ...]` in their manifest get a short-lived
	// provider access token injected as X-Vulos-Integration-<Provider> by the
	// gateway. The token is minted on demand from the cloud broker; the refresh
	// token never reaches the box. Mint failures degrade silently (no header).
	integrationsClient := integrations.NewClientFromEnv()
	// H3 fix: pass userID so the broker returns per-user tokens, not box-level tokens.
	appGateway.SetIntegrationTokenFunc(func(ctx context.Context, provider, userID string) (string, error) {
		tok, err := integrationsClient.MintToken(ctx, provider, userID)
		if err != nil {
			return "", err
		}
		return tok.AccessToken, nil
	})
	// Unified per-user object store (UNIFIED-STORAGE). The resolver yields the
	// per-user account bucket + credentials from box config/env (self-host) with a
	// CloudHook seam for CP-provided config. Apps that declare the "storage"
	// permission get X-Vulos-Storage-* headers injected by the gateway (server-side
	// only, per-user/app key prefix), mirroring the integration-token injection.
	storageResolver := storage.NewResolver(storage.LoadResolverConfig())

	// Scan installed app manifests ONCE, up front: the result feeds (a) the
	// boot-time storage fail-closed check below (needs to know whether ANY
	// app declares "storage" before deciding STS is mandatory) and (b) the
	// integration/storage/entitlement grants loop further down. Previously
	// this scan ran only after the STS decision, so that decision couldn't
	// see which apps actually need scoping; scanning once here also removes
	// a redundant second appsDir walk.
	//
	// BUG FIX (2026-07-12 perfection pass): this used to call
	// appnet.ScanApps(appsDir) directly, which only walks the LOCAL install
	// dir (~/.vulos/apps) — it silently missed any BUNDLED app (shipped under
	// /opt/vulos/apps or ./apps, see discoverBundledAppDirs) that was never
	// separately "installed" into appsDir. A bundled app declaring the
	// "storage" permission would then receive NO storage headers at all
	// (silently broken, not fail-open), and — more seriously — a bundled app
	// declaring a required "product" would NEVER get AllowApp() called,
	// so ENTITLE-01 gating would treat it as requiring no product at all: an
	// entitlement-bypass fail-open for exactly the apps most likely to be
	// premium (the ones the OS ships pre-installed). appStore.Installed()
	// merges bundledDirs + appsDir (same set the cockpit's
	// GET /api/store/installed already reports), so gateway wiring now
	// covers the SAME apps the box considers "installed".
	installedManifests, scanErr := appStore.Installed()
	if scanErr != nil {
		log.Printf("[appnet] manifest scan warning: %v", scanErr)
	}
	hasStorageApp := false
	for _, m := range installedManifests {
		for _, p := range m.Permissions {
			if p == "storage" {
				hasStorageApp = true
			}
		}
	}

	// C1/C3 (SECURITY): mint SHORT-LIVED, PREFIX-SCOPED credentials per app
	// instead of ever handing out the box's long-lived full-bucket creds.
	//
	// Self-host default: when VULOS_STORAGE_STS_ENDPOINT is unset, an object
	// store IS configured, and this is not a cloud deployment (Tigris-style
	// stores have no STS at all — the presign path below covers cloud
	// instead), default the STS endpoint to the box's OWN object-store
	// endpoint. MinIO serves its STS AssumeRole API on the SAME endpoint as
	// its S3 API, so this "just works" for the common self-host MinIO case
	// with zero extra operator configuration. Set VULOS_STORAGE_STS_DISABLE=1
	// to opt out (advanced/test use only).
	stsEndpoint := strings.TrimSpace(os.Getenv("VULOS_STORAGE_STS_ENDPOINT"))
	stsDisabled := os.Getenv("VULOS_STORAGE_STS_DISABLE") == "1"
	storageStaticallyConfigured := storageResolver.StaticallyConfigured()
	if stsEndpoint == "" && !stsDisabled && deployMode != deploymode.Cloud {
		if envEndpoint := firstNonEmpty(os.Getenv("VULOS_STORAGE_ENDPOINT"), os.Getenv("VULOS_S3_ENDPOINT")); envEndpoint != "" {
			stsEndpoint = envEndpoint
			log.Printf("[storage] VULOS_STORAGE_STS_ENDPOINT unset — defaulting to the box's own object-store endpoint %q (self-host default-on scoping)", stsEndpoint)
		}
	}
	if stsEndpoint != "" && !stsDisabled {
		durSec := 0
		if v := os.Getenv("VULOS_STORAGE_STS_DURATION_SECONDS"); v != "" {
			fmt.Sscanf(v, "%d", &durSec)
		}
		storageResolver.SetCredentialMinter(storageResolver.NewMinIOSTSMinter(storage.STSConfig{
			Endpoint:        stsEndpoint,
			RoleARN:         os.Getenv("VULOS_STORAGE_STS_ROLE_ARN"),
			DurationSeconds: durSec,
		}))
		log.Printf("[storage] STS credential minting enabled (endpoint=%s) — apps receive short-lived prefix-scoped creds", stsEndpoint)
	} else {
		// SECURITY (fail-closed, was warn-and-continue): a storage-permitted app
		// must NEVER receive a static full-bucket credential. When an object
		// store is statically configured (creds exist to protect) AND at least
		// one installed app declares the "storage" permission AND no STS is
		// available to scope them, this is an unsafe combination we can detect
		// fully at boot — abort rather than silently hand out unscoped creds,
		// mirroring the C2 SharedBucketConfigured fail-closed below. (This can
		// only happen when the operator explicitly disabled the default via
		// VULOS_STORAGE_STS_DISABLE=1, since the default-on branch above already
		// covers the common case.) The per-request path (ResolveScoped /
		// gateway.injectStorageHeaders) ALSO fails closed independently, so a
		// cloud/CloudHook-backed deployment (unknowable at boot) is covered too.
		if storageStaticallyConfigured && hasStorageApp {
			log.Fatalf("[storage] ABORT: app(s) declare the \"storage\" permission and an object store is configured, but no STS endpoint is available to scope per-app credentials — a storage-permitted app must NEVER receive a static full-bucket credential. Unset VULOS_STORAGE_STS_DISABLE, or set VULOS_STORAGE_STS_ENDPOINT explicitly, or remove the \"storage\" permission from the affected app(s).")
		}
		log.Printf("[storage] STS not configured — storage-permitted apps will NOT receive static full-bucket credentials (fail-closed); they must use the presign endpoint (POST /api/storage/presign) for object-scoped access instead.")
	}

	// C2: refuse to boot with a single shared bucket while multiple users exist —
	// a shared bucket across users defeats per-user isolation.
	if storageResolver.SharedBucketConfigured() && authStore != nil {
		if n := len(authStore.ListUsersWithRoles()); n > 1 {
			log.Fatalf("[storage] ABORT: an explicit shared storage bucket (VULOS_STORAGE_BUCKET) is configured but %d users exist — this breaks per-user isolation (C2). Unset VULOS_STORAGE_BUCKET to use per-user buckets (vulos-<userID>).", n)
		}
	}

	// H2/H3: the broker secret authenticates the gateway to consuming apps. When
	// unset the gateway fails CLOSED (injects no storage creds at all).
	storageBrokerSecret := os.Getenv("VULOS_STORAGE_BROKER_SECRET")
	appGateway.SetStorageBrokerSecret(storageBrokerSecret)
	if storageBrokerSecret == "" {
		log.Printf("[storage] WARNING: VULOS_STORAGE_BROKER_SECRET unset — storage credential injection DISABLED (fail-closed). Set it (and the matching app-side secret) to enable the storage seam.")
	}
	appGateway.SetStorageResolver(func(ctx context.Context, userID, prefix string) (storage.Resolution, bool) {
		return storageResolver.ResolveScoped(ctx, userID, prefix)
	})

	// PRESIGN-01: the cloud/no-STS storage seam. Same GrantBroker instance the
	// Files control plane uses below (internal/storage/grant.go) — it is
	// generic, not Files-specific: it mints presigned URLs / object-scoped STS
	// for exactly one object, so a storage-permitted app gets USABLE access
	// even when the header-injection seam refuses to hand out unscoped creds
	// (e.g. Tigris in cloud mode, or self-host with STS unavailable).
	storageGrantBroker := storage.NewGrantBroker(storageResolver, storage.STSConfig{
		Endpoint: stsEndpoint,
		RoleARN:  os.Getenv("VULOS_STORAGE_STS_ROLE_ARN"),
	}, 15*time.Minute)
	appGateway.SetGrantBroker(storageGrantBroker, storageResolver.BucketFor)

	// ENTITLE-01: app entitlement gating at dispatch. Off (fully open) on
	// standalone/self-host — matches today's all-or-nothing-open behavior.
	// On (fail-closed for apps that declare a required product) for cloud/os
	// deployments, driven by DEPLOY_MODE.
	appGateway.SetEntitlementGating(deployMode.IsCloudAdjacent())
	// wireAppGateway grants appID its manifest-declared integration/storage/
	// entitlement permissions on appGateway. Shared by the boot-time scan
	// below and by the store install handlers (BUG FIX 2026-07-12): those
	// handlers used to install an app's FILES without ever calling this, so
	// a newly-installed app got NO storage headers and — more seriously — a
	// newly-installed PREMIUM app was never registered in appProducts, so
	// ENTITLE-01 gating treated it as free-to-use for every user until the
	// next process restart (a live entitlement-bypass window, not just a
	// storage inconvenience).
	wireAppGateway := func(m *appnet.AppManifest) {
		for _, prov := range m.Integrations {
			appGateway.AllowIntegration(m.ID, prov)
			log.Printf("[integrations] app %q granted %q integration token", m.ID, prov)
		}
		for _, p := range m.Permissions {
			if p == "storage" {
				appGateway.AllowStorage(m.ID, m.ID+"/")
				log.Printf("[storage] app %q granted storage headers (prefix %q)", m.ID, m.ID+"/")
			}
		}
		if m.Product != "" {
			appGateway.AllowApp(m.ID, m.Product)
			log.Printf("[entitlement] app %q requires product %q (gating %s)", m.ID, m.Product, map[bool]string{true: "ENFORCED", false: "open (standalone)"}[deployMode.IsCloudAdjacent()])
		}
	}
	for _, m := range installedManifests {
		wireAppGateway(m)
	}

	// FILES-FOUNDATION: OS Files metadata/control-plane (the Drive index + ACLs +
	// share links + versions). It is the source of truth; bytes live in per-user
	// buckets. The grant broker reuses storageResolver to mint OBJECT-scoped,
	// short-lived, ACL-gated grants (presigned GET read / object-scoped STS write,
	// or a local-FS path in standalone mode). The Files service performs the ACL
	// check BEFORE calling the broker, so an unauthorized user never gets a grant.
	var filesSvc *files.Service
	{
		// Reuse the SAME grant broker the presign endpoint uses (constructed
		// above with the possibly-self-host-defaulted stsEndpoint) rather than
		// building a second one straight from the raw env var — that used to
		// silently miss the STS auto-default, so Files could fail to scope even
		// when the box's own MinIO was available for it.
		filesBroker := storageGrantBroker
		var ferr error
		filesSvc, ferr = files.New(
			filepath.Join(dbDir, "files.db"),
			filesBroker,
			func(uid string) string { return storageResolver.BucketFor(uid) },
		)
		if ferr != nil {
			log.Printf("[files] init warning: %v — Files API disabled (503)", ferr)
			filesSvc = nil
		} else {
			defer filesSvc.Close()
			log.Printf("[files] control plane ready (db=%s, sts=%v)", filepath.Join(dbDir, "files.db"), stsEndpoint != "")
			// FILES-2B: wire the OS peer-share seam (Mechanism B, bucket-less
			// box-to-box). Capabilities are signed with the peering box identity;
			// bytes stream over HTTP to/from peer boxes; redeemed bytes stage on
			// local disk until promoted into the recipient's Drive. Requires a
			// valid peering identity; without one, peer-share endpoints return 503.
			if priv := peeringSvc.PrivateKey(); len(priv) == ed25519.PrivateKeySize {
				filesSvc.WithPeer(
					peerShareSigner{selfID: peeringSvc.VulaID(), priv: priv},
					files.NewHTTPPeerTransport(),
					filepath.Join(dbDir, "peer-received"),
				)
				log.Printf("[files] OS peer-share active (id=%s)", peeringSvc.VulaID())
			} else {
				log.Printf("[files] OS peer-share disabled: no peering identity")
			}
			// ACCOUNT-SHARE: wire share-by-email resolution + locality routing
			// (Contract 2 + 3). Co-cloud recipients (a local OS account) take the
			// ACL grant path; remote recipients resolve via the configured directory
			// (VULOS_VERIFY_URL; none by default) to a {VulaID, server} and take the
			// peershare capability path, with the
			// minted capability delivered to the recipient's server intake.
			filesSvc.WithShareResolver(
				&osShareResolver{
					auth:      authStore,
					directory: peering.DiscoveryNewService(nil),
				},
				newHTTPCapabilityDeliverer(),
			)
			// FILES-4: wire the external-store seam (Google Drive). The box mints a
			// short-lived Drive access token on demand from the CP integration broker
			// (provider "google", Drive scope added CP-side); the refresh token never
			// reaches the box and minted tokens are never persisted. Requires the
			// integration broker to be configured; otherwise external mounts stay
			// unavailable and the connect action is disabled — local Drive +
			// peer-share work unchanged standalone (no hard cloud dependency).
			if integrationsClient.Configured() {
				filesSvc.WithExternal(
					filesIntegrationTokenSource{c: integrationsClient},
					files.NewGDriveProvider(),
					files.NewDropboxProvider(),
					files.NewGCSProvider(),
				)
				log.Printf("[files] external mounts active (providers: gdrive, dropbox, gcs)")
				// FILES IMPORT: wire the import engine (copy provider files into the
				// owner's Drive). Distinct from mounts: imports are Vulos-owned copies
				// that persist after disconnect. Reuses the same per-call token source;
				// Google-native docs export to Office formats on import, OneDrive Office
				// files copy as-is.
				//
				// PIM IMPORT (contacts + calendar): GoogleContactsSource and
				// GoogleCalendarSource are registered alongside the file sources. Their
				// DataKind() returns "contacts"/"calendar" so the job Kind is set
				// automatically, and the runPIMImport path posts bulk vCard/iCal
				// batches to lilmail rather than writing to the Drive.
				filesSvc.WithImport(
					files.NewGDriveProvider(),
					files.NewOneDriveProvider(),
					files.NewGoogleContactsSource(),
					files.NewGoogleCalendarSource(),
				)
				// Wire the lilmail bulk import endpoint so the PIM runner knows
				// where to POST. VULOS_MAIL_URL defaults to the same URL the OS shell
				// uses to embed the Mail app; VULOS_MAIL_BROKER_SECRET is the
				// LILMAIL_BROKER_SECRET shared with lilmail.
				pimMailURL := os.Getenv("VULOS_MAIL_URL")
				if pimMailURL == "" {
					pimMailURL = "http://localhost:3000"
				}
				pimMailSecret := os.Getenv("VULOS_MAIL_BROKER_SECRET")
				filesSvc.WithPIMConfig(pimMailURL, pimMailSecret, func(ownerID string) string {
					if u, ok := authStore.GetUser(ownerID); ok {
						return u.Email
					}
					return ""
				})
				log.Printf("[files] import engine active (sources: gdrive, onedrive, google-contacts, google-calendar)")
			} else {
				log.Printf("[files] external mounts + import disabled: integration broker not configured")
			}
		}
	}

	// Periodic auth flush
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				authStore.Flush()
				return
			case <-ticker.C:
				authStore.Flush()
			}
		}
	}()

	// HTTP routes
	mux := http.NewServeMux()

	// Step-up re-auth seam (short-lived elevated-trust cookie for sensitive actions)
	registerStepupRoutes(mux, authStore)

	// Peering: well-known identity endpoint + peer profile fetch (PEER-12).
	peering.RegisterWellKnownHandlers(mux)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	// /healthz — trivial liveness probe the status page expects (200 + status +
	// build version). Public, no auth.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok", "version": Version})
	})
	// /metrics — Prometheus scrape endpoint. NOT public: it exposes operational
	// counters (assistant Guard allow/block, proposal backlog, request/error
	// totals, RAG mode) that should not leak to unauthenticated callers. Two ways
	// in, both owner-scoped:
	//   1. Session: the box OWNER (admin) authenticated via the OS session — the
	//      auth middleware sets X-User-ID (after stripping any spoofed copy).
	//   2. A scrape token: an operator sets VULOS_METRICS_TOKEN and points their
	//      Prometheus at /metrics with `Authorization: Bearer <token>` (or
	//      ?token=). This lets a co-located scraper poll without an OS session.
	// No secret values are ever placed in metric names/labels (see obs.go), so a
	// scrape never leaks credentials — only counts and gauges.
	metricsHandler := obs.Handler()
	metricsToken := strings.TrimSpace(os.Getenv("VULOS_METRICS_TOKEN"))
	metricsIsOwner := func(userID string) bool {
		p, _ := authStore.GetProfile(userID)
		return p != nil && p.Role == auth.RoleAdmin
	}
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !metricsAuthorized(r, metricsToken, metricsIsOwner) {
			writeErr(w, 403, "metrics are owner-only")
			return
		}
		metricsHandler.ServeHTTP(w, r)
	})

	// Version — public, no auth. Returns the server build version.
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": Version, "deploy_mode": string(deployMode)})
	})

	// NET-07: cluster health (data-dir writable, disk space, sync lag) — public
	// syncer is wired below after cluster init; use a pointer so the handler
	// always reads the current value. nilSyncer is replaced once cluster is ready.
	var clusterSyncer *sync.Syncer
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handleClusterHealth(datadir.Root(), clusterSyncer)(w, r)
	})

	// Setup status — public, no auth needed
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) {
		_, err := os.Stat("/var/lib/vulos/.setup-complete")
		writeJSON(w, map[string]bool{"setup_complete": err == nil})
	})

	// Device profile — form-factor selection
	mux.HandleFunc("GET /api/device-profile", func(w http.ResponseWriter, r *http.Request) {
		profile, suggested := deviceProfile.Get()
		writeJSON(w, map[string]string{"profile": string(profile), "suggested": string(suggested)})
	})
	mux.HandleFunc("PUT /api/device-profile", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Profile string `json:"profile"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		ff := bprofiles.FormFactor(req.Profile)
		if !bprofiles.ValidFormFactor(ff) {
			writeErr(w, 400, "profile must be one of: pc, tv, car, watch")
			return
		}
		if err := deviceProfile.Set(ff); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		profile, suggested := deviceProfile.Get()
		writeJSON(w, map[string]string{"profile": string(profile), "suggested": string(suggested)})
	})

	// Auth routes
	authHandler.Register(mux)
	registerAdminTokenRoutes(mux, authStore, home) // AT10: rotating admin token

	// TOTP vault routes (/api/auth/totp/*)
	totpHandler := authvault.NewHandler()
	totpHandler.RegisterHandlers(mux)

	// TOTP migration routes (/api/auth/totp/import|export). These were written,
	// tested and then never mounted — the endpoints did not exist at runtime, so
	// nobody could bring Google Authenticator seeds IN, and nobody could get
	// their seeds OUT before losing the box. Same mux ⇒ same session gate as
	// every other TOTP route. Export additionally re-checks the account password
	// (step-up); without a reauthenticator wired it fails closed with 503, so
	// this SetReauthenticator call is load-bearing, not decoration.
	totpHandler.SetReauthenticator(authStore.VerifyUserPassword)
	authvault.RegisterMigrationHandlers(mux, totpHandler)

	// Credential vault HTTP API (password manager, per-user, AES-256-GCM)
	credVaultHandler := credvault.NewHandler(func(userID string) string {
		return datadir.Join("auth", "vault", userID)
	})
	credVaultHandler.RegisterHandlers(mux)

	// Credential vault import/export (/api/auth/vault/import|export). Same story:
	// complete, tested, never mounted — a password manager with no export is data
	// lock-in. Same mux ⇒ same session gate; export re-checks the master password.
	credvault.RegisterImportHandlers(mux, credVaultHandler)

	// Mail: URL of the embedded LilMail service (built-in Mail app).
	registerMailRoutes(mux)

	// PIM: credential-brokering proxy behind the standalone Calendar + Contacts
	// widgets. Browser → /api/pim/{calendar,contacts}/* → lilmail /v1/* with the
	// caller's cookie forwarded and the broker creds injected (never exposed to
	// the browser). See routes_pim.go.
	registerPIMRoutes(mux, mailBaseURLFromEnv(), assistantBrokerHeaders())

	// Files: OS Files metadata/control-plane API (Drive index, ACL-gated
	// object-scoped grants, shares, share links, versions). Session-authed.
	registerFilesRoutes(mux, filesSvc)

	// RESUMABLE upload (tus-style chunked): large files upload in bounded chunks
	// (each ≤ the relay request cap) so they ride the relay unchanged, with
	// resume-after-interruption, per-chunk + whole-file integrity, and expiry of
	// abandoned partials. Reassembles into THIS box's own /data + bucket via the
	// Files service (owner/ACL-checked). Additive: the single-shot upload path is
	// untouched; the UI opts large files into this endpoint. nil manager ⇒ 503.
	var uploadMgr *upload.Manager
	if filesSvc != nil {
		umgr, uerr := upload.New(
			filepath.Join(dbDir, "uploads.db"),
			&filesUploadSink{svc: filesSvc},
			upload.Config{StateDir: dataDir},
		)
		if uerr != nil {
			log.Printf("[files] resumable upload init warning: %v — resumable upload disabled (503)", uerr)
		} else {
			uploadMgr = umgr
			defer uploadMgr.Close()
			// Sweep abandoned partials hourly; bound to the server context so it
			// stops on shutdown.
			go uploadMgr.SweepLoop(ctx, time.Hour)
			log.Printf("[files] resumable upload ready (db=%s, staging=%s)", filepath.Join(dbDir, "uploads.db"), filepath.Join(dataDir, "uploads"))
		}
	}
	registerFilesResumableRoutes(mux, uploadMgr)

	// Tombstone purge sweep: Delete() only soft-deletes (deleted=1) — it never
	// frees bucket bytes and there is no trash/undelete UI yet. Until a full
	// trash feature exists, this hourly sweep is what reclaims bytes for nodes
	// tombstoned past the retention window (default 30d; override with
	// VULOS_FILES_TRASH_RETENTION, e.g. "720h"). Bound to the server context so
	// it stops on shutdown.
	if filesSvc != nil {
		retention := files.DefaultTombstoneRetention
		if v := os.Getenv("VULOS_FILES_TRASH_RETENTION"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				retention = d
			} else {
				log.Printf("[files] VULOS_FILES_TRASH_RETENTION=%q invalid, using default %s", v, files.DefaultTombstoneRetention)
			}
		}
		go filesSvc.PurgeTombstoneLoop(ctx, retention, time.Hour)
		log.Printf("[files] tombstone purge sweep active (retention=%s)", retention)
	}

	// FILES-2B: OS peer-share (Mechanism B). Session-authed issue/redeem/save
	// endpoints + the PUBLIC box-to-box serve endpoint (authenticated by the
	// signed capability + recipient fetch proof, listed in auth.publicPaths).
	registerFilesPeerRoutes(mux, filesSvc)
	registerFilesPeerServe(mux, filesSvc)
	registerFilesExternalRoutes(mux, filesSvc)

	// FILES IMPORT: copy provider files/folders into the owner's Drive (Vulos-
	// owned copies that persist after disconnect). Session-authed; distinct from
	// the external-mount endpoints above.
	registerFilesImportRoutes(mux, filesSvc)

	// EXPORT ("it's yours"): GET /api/export/data streams a single .zip of the
	// signed-in user's mail (.eml), Drive files, and — where the mail service
	// exposes them — calendar (.ics) / contacts (.vcf), in standard portable
	// formats. The anti-lock-in / data-permanence half of the legible-trust
	// surface. Session-authed; reuses the mail broker headers from the assistant.
	registerExportRoutes(mux, filesSvc, mailBaseURLFromEnv(), assistantBrokerHeaders(), safeProfileExport(authStore))

	// SSH key management (host key + authorized_keys)
	registerSSHKeyRoutes(mux, authStore, home)

	// Account security (login/session anomaly feed + emergency lock). Opened
	// here (rather than down by registerAccountSecurityRoutes below) so
	// acctSecSvc exists in time to be threaded into registerPasskeysRoutes,
	// which needs it to record passkey add/remove with a real client IP.
	acctSecSvc, acctSecErr := accountsecurity.Open(dbDir, notifySvc)
	if acctSecErr != nil {
		log.Printf("[accountsecurity] init warning: %v", acctSecErr)
	}
	// Feed auth's sensitive-mutation hook into the account-security anomaly
	// feed (kept as the fallback path for any Store method that doesn't have
	// an HTTP-handler-layer call — see ACCOUNTSECURITY-IP comments in
	// services/auth for the handlers that record directly instead). Best-
	// effort: acctSecSvc may be nil if Open() above failed, in which case
	// this closure is a documented no-op.
	authStore.SetSensitiveActionHook(func(uid, action, ip, ua string) {
		if acctSecSvc != nil {
			acctSecSvc.RecordAndCheck(context.Background(), uid, accountsecurity.Action(action), ip, ua) //nolint:errcheck
		}
	})

	// Webhooks (owner-gated outbound event delivery). Opened here (rather than
	// down by the rest of the admin-settings routes below) so
	// webhooksDispatcher exists in time to be threaded into the real event
	// sites registered below: auth.new_signin (authHandler.OnSignIn, wired
	// right after this, plus the passkey login handler further down),
	// device.enrolled/removed (instance manage + provision sections of
	// registerNewFeatureRoutes), snapshot.created (both the manual admin
	// endpoint and the scheduler), backup.completed (manual + periodic backup
	// below), and storage.low (the poller started right after this block).
	webhooksDispatcher := registerWebhooksRoutes(mux, authStore, home)
	// auth.new_signin: authHandler (created earlier, above) calls this on every
	// successful local/cloud sign-in with the real client IP/user-agent.
	// Best-effort — emitWebhookEvent is nil-safe if webhooksDispatcher is nil.
	authHandler.OnSignIn = func(userID, ip, userAgent string) {
		emitWebhookEvent(webhooksDispatcher, "auth.new_signin", map[string]any{
			"user_id":    userID,
			"ip":         ip,
			"user_agent": userAgent,
		})
	}

	// storage.low: periodically re-check disk usage (same disks.GetStatus()
	// the proactive low-disk-space check above uses, same proactiveDiskThreshold
	// bar) and emit once per threshold-crossing per mount point — NOT on every
	// poll — so a persistently-full disk doesn't spam the subscriber. wasLow
	// tracks per-mount state across ticks; the event fires only on the
	// false->true transition and re-arms once the mount recovers.
	go func() {
		wasLow := make(map[string]bool)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status := disks.GetStatus()
				for _, m := range status.Mounts {
					if m.TotalMB <= 0 {
						continue
					}
					low := m.Percent >= proactiveDiskThreshold
					if low && !wasLow[m.MountPoint] {
						emitWebhookEvent(webhooksDispatcher, "storage.low", map[string]any{
							"mount_point": m.MountPoint,
							"free_mb":     m.FreeMB,
							"total_mb":    m.TotalMB,
							"percent":     m.Percent,
						})
					}
					wasLow[m.MountPoint] = low
				}
			}
		}
	}()

	// AUTH-12: server-side passkey (FIDO2/WebAuthn) authenticator. Credentials
	// are sealed at rest per-user via the device KeyStore (TPM or software).
	// streamVerifier is set here when passkeys are available; used below for
	// AUTH-13 (input-injection re-auth gate).
	var streamVerifier *passkeys.StreamVerifier
	deviceKS, deviceKSErr := devicekey.Open(datadir.Join("auth", "tpm"))
	if deviceKSErr != nil {
		log.Printf("passkeys: devicekey unavailable, server-side passkeys disabled: %v", deviceKSErr)
	} else {
		// KEYSTORE-CUSTODY-01: a cloud-managed box (DEPLOY_MODE=os|cloud) must not
		// silently run with filesystem-only key custody. If the selected keystore
		// is the plaintext software fallback (no TPM), fail closed at boot — unless
		// the operator explicitly opts out (VULOS_ALLOW_SOFTWARE_KEYSTORE=1), which
		// the TPM-less Fly-hosted Cloud runtime uses. Standalone self-host is
		// unaffected (software keystore is its legitimate fallback).
		softwareKS := deviceKS.Status().Backend == devicekey.BackendSoftware
		optOut := strings.EqualFold(strings.TrimSpace(os.Getenv(deploymode.SoftwareKeystoreEnvOptOut)), "1")
		if deployMode.RefuseSoftwareKeystore(softwareKS, optOut) {
			log.Fatalf("[keystore] ABORT: DEPLOY_MODE=%q requires hardware-backed device key custody (TPM) but "+
				"the software (filesystem-only, plaintext-at-rest) keystore was selected — no TPM was found. "+
				"Provision a TPM, or set %s=1 to explicitly accept filesystem-only key custody on this managed box.",
				deployMode, deploymode.SoftwareKeystoreEnvOptOut)
		}

		// INTEG-SEC-01: bind the integrations token mint to THIS box's device
		// identity key. Registration is best-effort and runs in the background —
		// mint keeps working via the fleet-HMAC fallback until the key is pinned.
		integrationsClient.SetDeviceSigner(deviceKS)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := integrationsClient.Register(ctx); err != nil {
				log.Printf("[integrations] device-key registration deferred: %v", err)
			} else {
				log.Printf("[integrations] device key registered with cloud broker")
			}
		}()

		// INTEG-SEC-01 method 1: if this box has completed owner-attested cloud

		passkeysSvc := passkeys.New(datadir.Join("auth", "passkeys"), deviceKS)
		// TASK-2 (P0): RP ID prod safety — reject insecure defaults in prod.
		if activeEnv.IsProd() {
			if err := passkeysSvc.ValidateConfig(); err != nil {
				log.Fatalf("[passkeys] %v", err)
			}
		}
		registerPasskeysRoutes(mux, passkeysSvc, authStore, acctSecSvc)
		// LOGINISO-01: promote WebAuthn from re-auth gate to full login flow.
		// LOGINISO-02: QR / phone-approval kiosk login.
		loginSvc := passkeys.NewLoginService(passkeysSvc, authStore)
		qrSvc := passkeys.NewQRLoginService(authStore)
		// auth.new_signin: passkey login (unlike password/cloud login above) has
		// no auth.Handler.OnSignIn hook to piggyback on, so registerPasskeyLoginRoutes
		// is handed webhooksDispatcher directly and emits after a successful
		// finishLogin, matching the payload shape used by the password path.
		registerPasskeyLoginRoutes(mux, loginSvc, qrSvc, webhooksDispatcher)
		// AUTH-10c: device identity / TPM status / seal-unseal HTTP API.
		devicekey.RegisterHandlers(mux, deviceKS, func(r *http.Request) bool {
			p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
			return p != nil && p.Role == auth.RoleAdmin
		})
		// AUTH-13: wire the real WebAuthn verifier for input-injection re-auth.
		streamVerifier = passkeys.NewStreamVerifier(passkeysSvc)
		streamPool.SetWebAuthnVerifier(streamVerifier)
	}

	// App gateway — /app/{appId}/* proxied with auth
	mux.HandleFunc("/app/", appGateway.Handler())

	// ORIGIN-01: tells the shell whether this deployment can serve each app from
	// its OWN origin ({app}--{profile}.{base}) instead of from the shell's origin.
	// The shell uses it to build app frame URLs; it does NOT use it to decide the
	// iframe sandbox — that is derived from the frame URL's actual origin, so a
	// wrong answer here can only cost an app its own origin, never hand it the
	// shell's. See services/appnet/origin.go and src/core/AppOrigins.js.
	mux.HandleFunc("GET /api/apps/origins", func(w http.ResponseWriter, r *http.Request) {
		base := appnet.BaseDomain()
		writeJSON(w, map[string]any{
			"enabled":     appnet.Enabled(base),
			"base_domain": base,
			"profile":     appnet.DefaultProfile,
		})
	})

	// PRESIGN-01: storage-permitted apps mint short-lived, object-scoped
	// storage grants here instead of ever holding a raw AccessKey/Secret in
	// cloud mode (Tigris has no STS) or when self-host STS is unavailable.
	mux.Handle("POST /api/storage/presign", appGateway.PresignHandler())

	// DELETE-01 (PERFECTION PASS 2026-07-12): scoped object delete. The
	// gateway performs the delete server-side with its own credentials —
	// apps never presign DELETE and never hold raw creds. Same auth/scoping/
	// app_id-whitelist as presign; fails closed when no broker is configured.
	mux.Handle("POST /api/storage/delete", appGateway.DeleteHandler())

	// Chat history
	mux.HandleFunc("GET /api/ai/history", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		writeJSON(w, chatHistory.List(userID, 20))
	})
	mux.HandleFunc("GET /api/ai/history/{convId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		convID := r.PathValue("convId")
		conv := chatHistory.Get(userID, convID)
		if conv == nil {
			writeErr(w, 404, "not found")
			return
		}
		writeJSON(w, conv)
	})
	mux.HandleFunc("DELETE /api/ai/history/{convId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		chatHistory.Delete(userID, r.PathValue("convId"))
		chatHistory.Flush()
		writeJSON(w, map[string]string{"status": "deleted"})
	})

	// Sovereign mail assistant (the wedge): private AI over the user's mail,
	// on-instance by default with no third-party egress. Uses the same aiSvc/aiCfg
	// seam; enforcement lives in services/assistant.Guard.
	//
	// Semantic RAG retrieval: embed mail with the ON-INSTANCE ONNX embedder and
	// index it in the on-box vector store. Stays sovereign — NewMailIndex refuses
	// any embedder that can't certify on-instance operation, so we never use the
	// HTTP embeddings.Embedder (which may egress) here. If no local ONNX model is
	// present the assistant transparently falls back to lexical retrieval.
	var mailIndex *assistant.MailIndex
	modelsDir := datadir.Join("models")
	if embeddings.OnnxAvailable(modelsDir) {
		if onnx, oerr := embeddings.NewOnnxEmbedder(modelsDir); oerr != nil {
			log.Printf("[assistant] ONNX embedder init failed: %v — semantic mail index disabled (lexical fallback)", oerr)
		} else if mi, ierr := assistant.NewMailIndex(filepath.Join(dbDir, "assistant"), onnx); ierr != nil {
			log.Printf("[assistant] mail index init failed: %v", ierr)
		} else {
			mailIndex = mi
			log.Printf("[assistant] semantic mail index enabled (on-instance ONNX embeddings, on-box vector store)")
		}
	} else {
		log.Printf("[assistant] no local ONNX model in %s — using sovereign lexical retrieval (no external embedding API)", modelsDir)
	}
	// Durable REMINDERS store + poll-based scheduler (wave 62). The store is a
	// per-user SQLite file under the db dir; the scheduler sweeps for due
	// reminders and fires a notification (through notifySvc) for each, exactly
	// once, restart-safe (a reminder set before a restart is caught up on the
	// first sweep after boot). Best-effort: if the store fails to open, the
	// reminder tools degrade to "unavailable" rather than blocking startup.
	var remindersStore *assistant.RemindersStore
	if rs, rerr := assistant.OpenRemindersStore(filepath.Join(dbDir, "reminders.db")); rerr != nil {
		log.Printf("[assistant] reminders store init failed: %v — reminders disabled", rerr)
	} else {
		remindersStore = rs
		defer remindersStore.Close()
		scheduler := assistant.NewReminderScheduler(remindersStore, reminderNotifier{svc: notifySvc})
		go scheduler.Run(ctx)
		log.Printf("[assistant] reminders enabled (store=%s, poll scheduler running)", filepath.Join(dbDir, "reminders.db"))
	}
	registerAssistantRoutes(mux, aiSvc, aiCfg, mailIndex, filesSvc, remindersStore)

	// Private-AI model management (owner-only). Surfaces + manages the on-box
	// embedding/RAG model dir (which .onnx is installed, whether the real
	// tokenizer.json is present → semantic vs degraded RAG) and lists the chat
	// models the llmux gateway exposes. The import flow is how an operator
	// installs the tokenizer.json that upgrades RAG from the FNV fallback.
	modelMgr := models.New(modelsDir)
	var llmuxChatClient *llmuxclient.Client
	if lcfg, ok := llmuxclient.FromEnv(); ok {
		llmuxChatClient = llmuxclient.New(lcfg)
	}
	registerModelRoutes(mux, modelMgr, llmuxChatClient, func(userID string) bool {
		p, _ := authStore.GetProfile(userID)
		return p != nil && p.Role == auth.RoleAdmin
	})

	// Seed the exported RAG-mode gauge at startup so /metrics reflects reality
	// before the first model-management page load.
	if listing, lerr := modelMgr.List(); lerr == nil {
		obs.SetRAGMode(listing.RAGMode)
	}
	// Seed the pending-proposals gauge so an operator sees a real value from
	// boot; the assistant routes keep it live as proposals flow.
	obs.AssistantProposalsPending.Set(0)

	// Missions
	mux.HandleFunc("GET /api/missions", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, missionStore.ListForUser(userID, 20))
	})
	mux.HandleFunc("GET /api/missions/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := missionStore.Get(r.PathValue("id"))
		if m == nil {
			writeErr(w, 404, "not found")
			return
		}
		// IDOR-MISSION-01: only the owner or an admin may read a mission.
		if m.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 404, "not found")
				return
			}
		}
		writeJSON(w, m)
	})
	mux.HandleFunc("POST /api/missions", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		var req struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Steps       []string `json:"steps"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		m := missionStore.Create(userID, req.Title, req.Description, req.Steps)
		missionStore.Flush()
		writeJSON(w, m)
	})
	mux.HandleFunc("PUT /api/missions/{id}/step/{stepId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := missionStore.Get(r.PathValue("id"))
		if m == nil {
			writeErr(w, 404, "not found")
			return
		}
		// IDOR-MISSION-01: only the owner or an admin may update mission steps.
		if m.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 404, "not found")
				return
			}
		}
		var req struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		missionStore.UpdateStep(r.PathValue("id"), r.PathValue("stepId"), ai.MissionStatus(req.Status), req.Output)
		missionStore.Flush()
		writeJSON(w, missionStore.Get(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/missions/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := missionStore.Get(r.PathValue("id"))
		if m == nil {
			writeErr(w, 404, "not found")
			return
		}
		// IDOR-MISSION-01: only the owner or an admin may cancel a mission.
		if m.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 404, "not found")
				return
			}
		}
		missionStore.Cancel(r.PathValue("id"))
		missionStore.Flush()
		writeJSON(w, map[string]string{"status": "cancelled"})
	})
	mux.HandleFunc("GET /api/missions/active", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, map[string]int{"active": missionStore.ActiveCount(userID)})
	})

	// Telephony (TELE-01): box GSM — SMS + calls via a USB LTE modem / SIM through
	// ModemManager. Hardware-gated; idles cleanly when no modem is attached. Fires
	// a sovereign notification on each inbound SMS (via notifySvc, below). Scoped to
	// the box owner (authStore.AdminUserID). Stopped in the shutdown block.
	telephonySvc := registerTelephonyRoutes(mux, notifySvc, authStore)

	// Unified Contacts: the box-side address book that merges the owner's
	// CardDAV/Vulos cards, the phone's pushed device+SIM contacts, and the box
	// SIM phonebook into one de-duplicated list (GET /api/contacts/unified).
	// Owner-gated; the box SIM read is taken from the telephony service above.
	registerContactsRoutes(mux, authStore, telephonySvc)

	// Location (LOCATION-01): box-side per-user position cache. The browser
	// reports its geolocation via POST /api/location; box apps read it via GET
	// /api/location so each doesn't need its own browser permission prompt.
	// Modem-GPS is a stale/absent fallback and degrades cleanly with no modem.
	registerLocationRoutes(mux)

	// Android (redroid): opt-in, hardware/kernel-gated Android-in-a-container for
	// the few apps with no web/Linux client. Owner-gated; reports an honest
	// "unavailable" status when Docker/binder support is absent. The app-store
	// registry entry stays inert until the founder signs it.
	registerAndroidRoutes(mux, authStore)

	// Web hosting (PUBWEB): "vulos web deploy" — publish a built static/SPA site
	// from your own box, served at <site>.<user>.os.vulos.org with an SPA
	// fallback + hardened tar extraction, per-user quota, and a swappable cert
	// backend (honest no-op default until DNS-01/acme-dns is wired). Management
	// API is X-User-ID scoped (401 fail-closed); serving is public.
	registerWebhostRoutes(mux)

	// Notifications
	mux.Handle("/api/notifications/stream", notifySvc.Handler())
	mux.HandleFunc("GET /api/notifications", func(w http.ResponseWriter, r *http.Request) {
		// User-scoped: box-level notifications + this user's own private ones only,
		// so another account's private notification (e.g. a reminder's text) is
		// never listed here (NOTIF-USER-SCOPE).
		writeJSON(w, notifySvc.ListForUser(r.Header.Get("X-User-ID"), 50))
	})
	mux.HandleFunc("GET /api/notifications/unread", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]int{"unread": notifySvc.UnreadForUser(r.Header.Get("X-User-ID"))})
	})
	// M7 fix: notification mutation endpoints require an authenticated user
	// (X-User-ID enforced by auth Middleware) and /send additionally requires
	// admin role to prevent notification-injection attacks (phishing via injected
	// system-level UI pop-ups).
	mux.HandleFunc("POST /api/notifications/read", func(w http.ResponseWriter, r *http.Request) {
		// Authenticated user may mark their own notifications read.
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID == "" {
			notifySvc.MarkAllRead()
		} else {
			notifySvc.MarkRead(req.ID)
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/notifications/send", func(w http.ResponseWriter, r *http.Request) {
		// M7: /send is admin-only — prevents a non-admin app or user from
		// injecting phishing-style notifications into the OS notification feed.
		if !secI_isAdmin(r, authStore) {
			writeErr(w, 403, "forbidden: notification injection requires admin")
			return
		}
		var req struct {
			Title  string       `json:"title"`
			Body   string       `json:"body"`
			Level  notify.Level `json:"level"`
			Source string       `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Level == "" {
			req.Level = notify.LevelInfo
		}
		if req.Source == "" {
			req.Source = "system"
		}
		n := notifySvc.Send(req.Title, req.Body, req.Level, req.Source)
		writeJSON(w, n)
	})
	mux.HandleFunc("POST /api/notifications/clear", func(w http.ResponseWriter, r *http.Request) {
		// M7: /clear requires an authenticated user (X-User-ID checked explicitly).
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		notifySvc.Clear()
		writeJSON(w, map[string]string{"status": "cleared"})
	})
	notifyExtSvc := registerNotifyExtRoutes(mux, notifySvc, home, authStore) // NOTIF-05+06: DND + inline actions
	// PUSH-CELL-01: cell-side DIRECT Web Push send-path + subscribe surface.
	// Shares the DND policy from the ext routes so a user in Do-Not-Disturb is
	// not web-pushed. Additive + flag-gated (no-op without VAPID keys).
	registerNotifyPushRoutes(mux, notifySvc, home, notifyExtSvc.dnd)

	// Open Router — GET /api/router/classify?app=<id> → {lane}
	// Used by the shell launcher to dispatch per lane (WebApp/CPUStream/GPURoute/etc).
	registerRouterRoutes(mux)

	// xdg-open handler — opens URL in the OS browser and signals frontend.
	// Requires authentication (not in publicPaths). Hardened against SSRF (H6).
	// BROWSER-01/02: POST /api/open returns a host-browser-open instruction;
	// server-side Chromium streaming removed.
	registerOpenRoutes(mux, notifySvc)

	// Vault endpoints
	mux.HandleFunc("GET /api/vault/status", func(w http.ResponseWriter, r *http.Request) {
		// SEC: status exposes snapshot hostnames/timestamps — same admin gate as
		// the other vault endpoints (backup/sync below), not just any session.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		writeJSON(w, v.Status())
	})
	mux.HandleFunc("POST /api/vault/backup", func(w http.ResponseWriter, r *http.Request) {
		// H2 fix: backup triggers potentially heavy I/O and reads all vault data
		// — restrict to admin.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		if err := v.Backup(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/vault/snapshots", func(w http.ResponseWriter, r *http.Request) {
		// SEC: snapshot list leaks hostnames/timestamps — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		snaps, err := v.Snapshots(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, snaps)
	})

	mux.HandleFunc("GET /api/vault/sync", func(w http.ResponseWriter, r *http.Request) {
		// SEC: sync status leaks the same snapshot/host metadata — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		writeJSON(w, v.SyncStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/vault/sync", func(w http.ResponseWriter, r *http.Request) {
		// H2 fix: sync pulls encrypted vault data across the network — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		if err := v.SyncToDevice(r.Context(), dataDir); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "synced"})
	})

	// POST /init-passphrase — managed-box vault unlock.
	//
	// Called by the orchestrator (burst agent) immediately after a managed VM
	// boots to inject the vault passphrase at runtime. Gated by
	// X-Burst-Secret: <BURST_HEARTBEAT_SECRET> — no session cookie required
	// (listed in publicPaths). Accepts JSON {"passphrase": "..."}.
	mux.HandleFunc("POST /init-passphrase", func(w http.ResponseWriter, r *http.Request) {
		burstSecret := os.Getenv("BURST_HEARTBEAT_SECRET")
		if burstSecret == "" {
			writeErr(w, 503, "init-passphrase not configured (BURST_HEARTBEAT_SECRET unset)")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Burst-Secret")), []byte(burstSecret)) != 1 {
			writeErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if req.Passphrase == "" {
			writeErr(w, 400, "passphrase required")
			return
		}
		if err := v.SetPassword(r.Context(), req.Passphrase); err != nil {
			log.Printf("[init-passphrase] vault unlock failed: %v", err)
			writeErr(w, 500, "vault unlock failed: "+err.Error())
			return
		}
		log.Printf("[init-passphrase] vault unlocked successfully")
		writeJSON(w, map[string]string{"status": "ready"})
	})

	// Recall endpoints
	mux.HandleFunc("GET /api/recall/status", func(w http.ResponseWriter, r *http.Request) {
		if recallSvc == nil {
			writeErr(w, 503, "recall not initialized")
			return
		}
		writeJSON(w, recallSvc.Status())
	})
	mux.HandleFunc("POST /api/recall/search", func(w http.ResponseWriter, r *http.Request) {
		if recallSvc == nil {
			writeErr(w, 503, "recall not initialized")
			return
		}
		var req struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if req.TopK == 0 {
			req.TopK = 10
		}
		results, err := recallSvc.Search(r.Context(), req.Query, req.TopK)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, results)
	})
	mux.HandleFunc("POST /api/recall/index", func(w http.ResponseWriter, r *http.Request) {
		if recallSvc == nil {
			writeErr(w, 503, "recall not initialized")
			return
		}
		go recallSvc.IndexAll(r.Context())
		writeJSON(w, map[string]string{"status": "indexing started"})
	})

	// App namespace endpoints — now with port pool + traffic stats
	mux.HandleFunc("GET /api/apps/running", func(w http.ResponseWriter, r *http.Request) {
		apps := launcher.ListRunning()
		type appInfo struct {
			ID       string              `json:"id"`
			HostPort int                 `json:"host_port"`
			AppPort  int                 `json:"app_port"`
			Running  bool                `json:"running"`
			NSIP     string              `json:"ns_ip"`
			Traffic  appnet.TrafficStats `json:"traffic"`
		}
		var result []appInfo
		for _, a := range apps {
			info := appInfo{
				ID: a.ID, HostPort: a.Namespace.HostPort, AppPort: a.Namespace.AppPort,
				Running: a.Running, NSIP: a.Namespace.NSIP,
			}
			info.Traffic = trafficMon.Sample(a.Namespace)
			result = append(result, info)
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("POST /api/apps/launch", func(w http.ResponseWriter, r *http.Request) {
		// Kill-switch: operator can disable all privileged exec at runtime.
		if execDisabled() {
			writeErr(w, 503, "exec disabled by administrator")
			return
		}
		// Admin-only gate.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID   string `json:"app_id"`
			AppPort int    `json:"app_port"`
			// Command field is accepted for API compatibility but IGNORED — the
			// command is resolved exclusively from the validated app manifest.
			Command string   `json:"command"`
			Args    []string `json:"args"`
			WorkDir string   `json:"work_dir"`
			Env     []string `json:"env"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.AppID == "" {
			writeErr(w, 400, "app_id required")
			return
		}
		// Validate: app ID must be alphanumeric (defence-in-depth before filesystem lookup)
		for _, c := range req.AppID {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				writeErr(w, 400, "invalid app_id")
				return
			}
		}
		// Resolve command strictly from the validated installed manifest — never use
		// any client-supplied command value.
		manifest, err := appStore.GetManifest(req.AppID)
		if err != nil {
			writeErr(w, 404, "app not found or invalid manifest")
			return
		}
		// Use manifest values authoritatively; client WorkDir/AppPort are hints only
		// when manifest does not override them.
		manifestCmd := manifest.Command
		manifestAppPort := manifest.Port
		if req.AppPort != 0 && manifestAppPort == 0 {
			manifestAppPort = req.AppPort
		}
		if manifestAppPort == 0 {
			manifestAppPort = 80
		}
		// Validate: block dangerous env vars supplied by caller
		for _, e := range req.Env {
			lower := strings.ToLower(e)
			if strings.HasPrefix(lower, "ld_preload=") || strings.HasPrefix(lower, "ld_library_path=") {
				writeErr(w, 400, "forbidden env var")
				return
			}
		}
		// Extra env beyond the manifest's own (LaunchManifest merges manifest env
		// + this slice, in that order, so manifest values still win last-write
		// conflicts the way the manifest's own EnvSlice() ordering intends).
		launchEnv := append([]string{}, req.Env...)
		// Allocate host port from pool
		hostPort, ok := portPool.Allocate(req.AppID)
		if !ok {
			writeErr(w, 503, "no ports available")
			return
		}
		// Generate app secret and inject into env
		appSecret := appGateway.GenerateAppSecret(req.AppID)
		launchEnv = append(launchEnv, "VULOS_APP_SECRET="+appSecret, "VULOS_API=http://localhost:8080")

		userID := r.Header.Get("X-User-ID")
		execAuditLog(r, "POST /api/apps/launch", fmt.Sprintf("app_id=%s cmd=%q", req.AppID, manifestCmd))
		// Use LaunchManifest (not Launch) so manifest.Concurrency is honored —
		// Launch always defaults to singleton run-lease gating, which silently
		// mis-gated replicated/collaborative-concurrency apps declared in their
		// manifest (that field was validated at install time but never read at
		// launch time). Manifest command/work-dir/port are still authoritative
		// (read inside LaunchManifest from m itself); appSecret+API env and
		// caller args still flow through as extraEnv/extraArgs.
		app, err := launcher.LaunchManifest(ctx, manifest, userID, "default", hostPort, manifestAppPort, req.Args, launchEnv)
		if err != nil {
			portPool.Release(req.AppID)
			appGateway.RemoveAppSecret(req.AppID)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"app_id":  app.ID,
			"url":     gateway.URLForApp(req.AppID),
			"running": app.Running,
		})
	})
	mux.HandleFunc("POST /api/apps/stop", func(w http.ResponseWriter, r *http.Request) {
		// SEC: stopping a running app process is a privileged mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID string `json:"app_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := launcher.Stop(ctx, req.AppID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		portPool.Release(req.AppID)
		trafficMon.Forget(req.AppID)
		appGateway.RemoveAppSecret(req.AppID)
		writeJSON(w, map[string]string{"status": "stopped"})
	})
	mux.HandleFunc("GET /api/apps/namespaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, netMgr.List())
	})
	mux.HandleFunc("GET /api/apps/ports", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"in_use":    portPool.InUse(),
			"available": portPool.Available(),
			"range":     "7070-7999",
		})
	})
	mux.HandleFunc("GET /api/apps/traffic", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, trafficMon.SampleAll(netMgr))
	})
	mux.HandleFunc("GET /api/apps/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, appGateway.HealthCheckAll())
	})

	// Energy management endpoints
	mux.HandleFunc("GET /api/energy/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, energyMgr.State())
	})
	mux.HandleFunc("POST /api/energy/mode", func(w http.ResponseWriter, r *http.Request) {
		// SEC: changing the host energy profile is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		switch energy.Mode(req.Mode) {
		case energy.ModePerformance, energy.ModeBalanced, energy.ModeSaver:
			energyMgr.SetMode(energy.Mode(req.Mode))
			writeJSON(w, energyMgr.State())
		default:
			writeErr(w, 400, "invalid mode: use performance, balanced, or saver")
		}
	})
	mux.HandleFunc("POST /api/energy/wake", func(w http.ResponseWriter, r *http.Request) {
		energyMgr.ResetIdle()
		writeJSON(w, map[string]string{"status": "awake"})
	})

	// PTY WebSocket — terminal access
	mux.Handle("/api/pty", ptySvc.Handler())
	mux.HandleFunc("/api/pty/sessions", ptySvc.SessionsHandler())

	// Sandbox — AI-generated Python scripts
	mux.HandleFunc("POST /api/sandbox/run", func(w http.ResponseWriter, r *http.Request) {
		// Kill-switch: operator can disable all privileged exec at runtime.
		if execDisabled() {
			writeErr(w, 503, "exec disabled by administrator")
			return
		}
		// Admin-only gate.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Code == "" {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.ID == "" {
			req.ID = fmt.Sprintf("script-%d", time.Now().UnixMilli())
		}
		execAuditLog(r, "POST /api/sandbox/run", fmt.Sprintf("script_id=%s code_len=%d", req.ID, len(req.Code)))
		script, err := sandboxSvc.Run(r.Context(), req.ID, req.Code)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"id": script.ID, "port": script.Port,
			"url": fmt.Sprintf("/api/sandbox/%s/", script.ID),
		})
	})
	mux.HandleFunc("POST /api/sandbox/stop", func(w http.ResponseWriter, r *http.Request) {
		// SEC: stopping a sandbox process is a privileged mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		sandboxSvc.Stop(req.ID)
		writeJSON(w, map[string]string{"status": "stopped"})
	})
	mux.HandleFunc("GET /api/sandbox/list", func(w http.ResponseWriter, r *http.Request) {
		// SEC: sandbox list leaks process isolation info — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		writeJSON(w, sandboxSvc.List())
	})
	// Sandbox proxy — /api/sandbox/{id}/* → localhost:{sandbox_port}/*
	mux.HandleFunc("/api/sandbox/", func(w http.ResponseWriter, r *http.Request) {
		// SEC: proxying to a sandbox subprocess is a privileged operation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/sandbox/")
		slashIdx := strings.Index(path, "/")
		if slashIdx == -1 {
			return
		}
		id := path[:slashIdx]
		rest := path[slashIdx:]
		port, ok := sandboxSvc.ProxyPort(id)
		if !ok {
			writeErr(w, 404, "sandbox not running")
			return
		}
		target := fmt.Sprintf("http://127.0.0.1:%d%s", port, rest)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		proxyReq, _ := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(proxyReq)
		if err != nil {
			writeErr(w, 502, "sandbox unreachable")
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	// One-shot command exec (for Portal /commands)
	mux.HandleFunc("POST /api/exec", func(w http.ResponseWriter, r *http.Request) {
		// Kill-switch: set VULOS_DISABLE_EXEC to any non-empty value to disable.
		if execDisabled() {
			writeErr(w, 503, "exec endpoint disabled by configuration")
			return
		}
		// Admin-only gate (mirrors the pattern used by /api/store/install et al.).
		userID := r.Header.Get("X-User-ID")
		if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Command == "" {
			writeErr(w, 400, "invalid request")
			return
		}
		execAuditLog(r, "POST /api/exec", req.Command)
		result := ptyservice.Exec(r.Context(), req.Command)
		logExecAudit(userID, req.Command, result.ExitCode)
		writeJSON(w, result)
	})

	// Telemetry WebSocket — live system stats
	mux.Handle("/api/telemetry", telemetry.Handler())

	// System info (one-shot, for About page)
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, telemetry.SystemInfo())
	})

	// Open-source licence notices, surfaced by Settings → About. Served from the
	// generated THIRD_PARTY_NOTICES.md that build.sh writes into the image at
	// /opt/vulos/legal/; falls back to the repo copy in development. The written
	// offer for GPL/LGPL source (WRITTEN-OFFER.md) is served the same way.
	mux.HandleFunc("GET /api/system/licenses", legalDocHandler(defaultLegalDirs, "THIRD_PARTY_NOTICES.md"))
	mux.HandleFunc("GET /api/system/written-offer", legalDocHandler(defaultLegalDirs, "WRITTEN-OFFER.md"))

	// System processes and network connections
	mux.HandleFunc("GET /api/system/processes", telemetry.ProcessHandler())
	mux.HandleFunc("GET /api/system/network", telemetry.NetworkHandler())

	// Remote access config
	mux.HandleFunc("GET /api/network/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, netSvc.Status())
	})
	mux.HandleFunc("GET /api/network/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, netSvc.Config())
	})
	mux.HandleFunc("POST /api/network/configure", func(w http.ResponseWriter, r *http.Request) {
		// SEC: network configuration is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var cfg network.Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&cfg); err != nil {
			writeErr(w, 400, "invalid config")
			return
		}
		execAuditLog(r, "POST /api/network/configure", "network config updated")
		netSvc.Configure(cfg)
		writeJSON(w, netSvc.Config())
	})

	// TURN/coturn settings routes (NET-10)
	// H2 fix: POST /api/turn/config and POST /api/turn/test are admin-only.
	registerTURNRoutes(mux, turnStore, authStore)
	registerRelayConfigRoutes(mux, authStore)
	registerNetModeRoutes(mux, netSvc, authStore)

	// --- System Settings ---

	// WiFi
	mux.HandleFunc("GET /api/wifi/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifiSvc.Status(r.Context()))
	})
	mux.HandleFunc("GET /api/wifi/scan", func(w http.ResponseWriter, r *http.Request) {
		networks, err := wifiSvc.Scan(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, networks)
	})
	mux.HandleFunc("POST /api/wifi/connect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: connecting to a network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		execAuditLog(r, "POST /api/wifi/connect", fmt.Sprintf("ssid=%q", req.SSID))
		if err := wifiSvc.Connect(r.Context(), req.SSID, req.Password); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connecting"})
	})
	mux.HandleFunc("POST /api/wifi/disconnect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: disconnecting from a network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		execAuditLog(r, "POST /api/wifi/disconnect", "wifi disconnect")
		wifiSvc.Disconnect(r.Context())
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("GET /api/wifi/saved", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifiSvc.SavedNetworks(r.Context()))
	})
	mux.HandleFunc("POST /api/wifi/forget", func(w http.ResponseWriter, r *http.Request) {
		// SEC: forgetting a saved network is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			SSID string `json:"ssid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/wifi/forget", fmt.Sprintf("ssid=%q", req.SSID))
		wifiSvc.ForgetNetwork(r.Context(), req.SSID)
		writeJSON(w, map[string]string{"status": "forgotten"})
	})

	// Ethernet
	mux.HandleFunc("GET /api/ethernet/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifi.ListEthernet(r.Context()))
	})
	mux.HandleFunc("POST /api/ethernet/dhcp", func(w http.ResponseWriter, r *http.Request) {
		// SEC: ethernet configuration is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Interface string `json:"interface"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !validIfaceName(req.Interface) {
			writeErr(w, 400, "invalid interface name")
			return
		}
		execAuditLog(r, "POST /api/ethernet/dhcp", fmt.Sprintf("iface=%q", req.Interface))
		if err := wifi.EnableDHCP(r.Context(), req.Interface); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "dhcp started"})
	})
	mux.HandleFunc("POST /api/ethernet/static", func(w http.ResponseWriter, r *http.Request) {
		// SEC: setting a static IP is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Interface string `json:"interface"`
			IP        string `json:"ip"`
			Gateway   string `json:"gateway"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !validIfaceName(req.Interface) {
			writeErr(w, 400, "invalid interface name")
			return
		}
		execAuditLog(r, "POST /api/ethernet/static", fmt.Sprintf("iface=%q ip=%q gw=%q", req.Interface, req.IP, req.Gateway))
		if err := wifi.SetStaticIP(r.Context(), req.Interface, req.IP, req.Gateway); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "configured"})
	})
	mux.HandleFunc("POST /api/ethernet/disable", func(w http.ResponseWriter, r *http.Request) {
		// SEC: disabling an interface is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Interface string `json:"interface"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !validIfaceName(req.Interface) {
			writeErr(w, 400, "invalid interface name")
			return
		}
		execAuditLog(r, "POST /api/ethernet/disable", fmt.Sprintf("iface=%q", req.Interface))
		wifi.DisableEthernet(r.Context(), req.Interface)
		writeJSON(w, map[string]string{"status": "disabled"})
	})

	// Bluetooth
	mux.HandleFunc("GET /api/bluetooth/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/power", func(w http.ResponseWriter, r *http.Request) {
		// SEC: toggling bluetooth power is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			On bool `json:"on"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/bluetooth/power", fmt.Sprintf("on=%v", req.On))
		if err := btSvc.SetPower(r.Context(), req.On); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/scan", func(w http.ResponseWriter, r *http.Request) {
		// SEC: starting bluetooth discovery is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			On bool `json:"on"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.On {
			btSvc.StartDiscovery(r.Context())
		} else {
			btSvc.StopDiscovery(r.Context())
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/bluetooth/pair", func(w http.ResponseWriter, r *http.Request) {
		// SEC: pairing a bluetooth device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/bluetooth/pair", fmt.Sprintf("addr=%q", req.Address))
		if err := btSvc.Pair(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "paired"})
	})
	mux.HandleFunc("POST /api/bluetooth/connect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: connecting a bluetooth device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/bluetooth/connect", fmt.Sprintf("addr=%q", req.Address))
		if err := btSvc.Connect(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connected"})
	})
	mux.HandleFunc("POST /api/bluetooth/disconnect", func(w http.ResponseWriter, r *http.Request) {
		// SEC: disconnecting a bluetooth device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		btSvc.Disconnect(r.Context(), req.Address)
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("POST /api/bluetooth/remove", func(w http.ResponseWriter, r *http.Request) {
		// SEC: removing a bluetooth device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		execAuditLog(r, "POST /api/bluetooth/remove", fmt.Sprintf("addr=%q", req.Address))
		btSvc.Remove(r.Context(), req.Address)
		writeJSON(w, map[string]string{"status": "removed"})
	})

	// Audio
	mux.HandleFunc("GET /api/audio/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, audioSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/audio/volume", func(w http.ResponseWriter, r *http.Request) {
		// SEC: changing audio device volume is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			DeviceID string `json:"device_id"`
			Type     string `json:"type"` // "output" or "input"
			Volume   int    `json:"volume"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := audioSvc.SetVolume(r.Context(), req.DeviceID, req.Type, req.Volume); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, audioSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/audio/mute", func(w http.ResponseWriter, r *http.Request) {
		// SEC: muting an audio device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			DeviceID string `json:"device_id"`
			Type     string `json:"type"`
			Muted    bool   `json:"muted"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := audioSvc.SetMute(r.Context(), req.DeviceID, req.Type, req.Muted); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, audioSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/audio/default", func(w http.ResponseWriter, r *http.Request) {
		// SEC: changing the default audio device is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			DeviceID string `json:"device_id"`
			Type     string `json:"type"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		audioSvc.SetDefault(r.Context(), req.DeviceID, req.Type)
		writeJSON(w, audioSvc.GetStatus(r.Context()))
	})

	// Display
	mux.HandleFunc("GET /api/display/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, displaySvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/display/brightness", func(w http.ResponseWriter, r *http.Request) {
		// SEC: writing to /sys/class/backlight is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Brightness int `json:"brightness"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := displaySvc.SetBrightness(r.Context(), req.Brightness); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, displaySvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/display/resolution", func(w http.ResponseWriter, r *http.Request) {
		// SEC: changing display resolution is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Output     string `json:"output"`
			Resolution string `json:"resolution"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !validDisplayOutput(req.Output) {
			writeErr(w, 400, "invalid output name")
			return
		}
		if !validDisplayMode(req.Resolution) {
			writeErr(w, 400, "invalid resolution format")
			return
		}
		execAuditLog(r, "POST /api/display/resolution", fmt.Sprintf("output=%q res=%q", req.Output, req.Resolution))
		if err := displaySvc.SetResolution(r.Context(), req.Output, req.Resolution); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, displaySvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/display/enable", func(w http.ResponseWriter, r *http.Request) {
		// SEC: enabling/disabling a display output is a privileged host mutation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Output string `json:"output"`
			Enable bool   `json:"enable"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !validDisplayOutput(req.Output) {
			writeErr(w, 400, "invalid output name")
			return
		}
		execAuditLog(r, "POST /api/display/enable", fmt.Sprintf("output=%q enable=%v", req.Output, req.Enable))
		if err := displaySvc.EnableOutput(r.Context(), req.Output, req.Enable); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, displaySvc.GetStatus(r.Context()))
	})

	// Peering multiplex WebSocket — GET /api/peering/stream
	peeringHub.RegisterHandlers(mux)
	// Boot-mode router — GET /api/setup/mode
	bootmode.RegisterHandlers(mux, home)

	// Generic app streaming (any X11 app via WebRTC).
	// launch + vnc endpoints require admin — pass the same gate used elsewhere.
	streamPool.RegisterHandlers(mux, func(r *http.Request) bool {
		p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
		return p != nil && p.Role == auth.RoleAdmin
	})
	// Stream toolbar endpoints (FPS selector, MangoHud toggle — GAME-08)
	registerStreamRoutes(mux, streamPool)

	// Streaming Chrome browser endpoints (POST /api/browser/launch|stop,
	// GET /api/browser/status, CDP tab management). WebRTC signaling reuses the
	// generic /api/stream/ws endpoint above with the per-user session ID.
	browserSvc.RegisterHandlers(mux)

	// AUTH-13: WebAuthn re-auth gate for input-injection sessions.
	//
	// The gate is now ARMED: when a stream session carries an input injector,
	// Pool.Launch starts it in the gated state (all injected mouse/keyboard input
	// dropped) and the client must POST a valid WebAuthn assertion to lift it.
	//
	// streamVerifier is non-nil when passkeys are available (set above in the
	// AUTH-12 block); nil when devicekey is unavailable (e.g. CI or containers
	// without TPM). To avoid PERMANENTLY bricking input, Pool.shouldGateInput only
	// arms the gate when a REAL verifier is wired (or the operator opts into strict
	// fail-closed gating via VULOS_STREAM_STRICT_INPUT_GATE=1). With no verifier and
	// strict gating off, input flows UNGATED and Launch logs a loud per-session
	// warning — an honest, non-crashing state rather than a silent permanent lock.
	//
	// AUTH-13 fail-closed default: an UNSET VULOS_STREAM_STRICT_INPUT_GATE must
	// not silently leave input ungated in production. If the operator hasn't
	// expressed an opinion (env var not present at all) and we're running with
	// --env prod (or VULOS_ENV=prod), default to strict so remote input injection
	// is gated even without a WebAuthn verifier wired. An operator who explicitly
	// sets the var (to "1" or anything else) always wins, in any environment.
	strictGate := os.Getenv("VULOS_STREAM_STRICT_INPUT_GATE") == "1"
	if rawGate, isSet := os.LookupEnv("VULOS_STREAM_STRICT_INPUT_GATE"); !isSet {
		if activeEnv.IsProd() {
			strictGate = true
		}
	} else {
		strictGate = rawGate == "1"
	}
	streamPool.SetStrictInputGate(strictGate)
	if streamPool.WebAuthnVerifier() == nil && !strictGate {
		if activeEnv.IsProd() {
			log.Printf("[stream/webauthn] WARNING: AUTH-13 NOT ENFORCED in prod — no WebAuthn verifier wired (devicekey/passkeys unavailable) and VULOS_STREAM_STRICT_INPUT_GATE unset. Remote input injection is UNGATED. Wire passkeys or set VULOS_STREAM_STRICT_INPUT_GATE=1 (fail-closed) to enforce.")
		} else {
			log.Printf("[stream/webauthn] WARNING: AUTH-13 not enforced — no WebAuthn verifier wired and strict gating off; input injection is ungated. Acceptable in dev/local.")
		}
	} else if streamPool.WebAuthnVerifier() == nil && strictGate {
		log.Printf("[stream/webauthn] AUTH-13 strict fail-closed: input injection is gated but no real verifier is wired — assertions will be REJECTED and input stays permanently locked. Wire passkeys to unlock.")
	}
	registerStreamWebAuthnRoutes(mux, streamPool, authStore, streamVerifier)

	// GAME-SESSION-01: wire the gaming-session HTTP surface (previously orphaned).
	// GamingManager enforces the one-active-gaming-session-per-user GPU-contention
	// policy the plain launch path lacks, and exposes GET /api/stream/gaming/
	// capability so the UI can read the box's NVENC/VA-API hardware-encode tier.
	// The endpoints are session-authed via X-User-ID like the base stream handlers.
	stream.NewGamingManager(streamPool).RegisterGamingHandlers(mux)

	// GAME-07: manifest-aware stream launch — detects gaming sessions automatically.
	// Sets LaunchOpts.Gaming=true when the manifest category=="gaming" OR the
	// command starts with wine/wine64/lutris/steam/steam-runtime.
	mux.HandleFunc("POST /api/stream/launch-app", func(w http.ResponseWriter, r *http.Request) {
		// SEC: launching a stream session spawns host processes — admin only.
		if execDisabled() {
			writeErr(w, 503, "exec disabled by administrator")
			return
		}
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID   string   `json:"app_id"`
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []string `json:"env"`
			Width   int      `json:"width"`
			Height  int      `json:"height"`
			FPS     int      `json:"fps"`
			Restart bool     `json:"restart"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.Command == "" {
			writeErr(w, 400, "command required")
			return
		}

		// Resolve user home (mirrors the resolver registered on the pool)
		userHome := ""
		if uid := r.Header.Get("X-User-ID"); uid != "" {
			if u, ok := authStore.GetUser(uid); ok {
				if su := sysUserSvc.Lookup(u.Username); su != nil {
					userHome = su.HomeDir
				}
			}
		}

		// GAME-07: auto-detect gaming mode (command launcher OR manifest category).
		g07Gaming := detectGamingLaunch(req.Command, req.AppID, appsDir)

		// BILLING GATE (surface 1: GPU/stream). A stream session is a billable
		// GPU/compute surface. Enforces: gpu_enabled, suspended, and the
		// gpu_session_cap concurrent-session limit (tracked locally via the stream
		// pool — no cp round-trip for the live count). A cold cp outage REFUSES
		// (cpbilling fails closed on an unverified account); a stale cached
		// entitlement still serves, so a cp blip does not black out a known
		// account. No-op when billing is disabled (standalone OS).
		gpuAccount := r.Header.Get("X-User-ID")
		if billingClient.Enabled() {
			activeSessions := len(streamPool.List())
			if d := billingClient.GateGPU(r.Context(), gpuAccount, activeSessions); !d.Allowed {
				// Distinguish "we could not verify you" (cp down → retryable 503)
				// from "you are not entitled" (authoritative 402).
				if d.Degraded {
					w.Header().Set("Retry-After", "30")
					writeErr(w, http.StatusServiceUnavailable, "entitlement check unavailable — try again shortly")
					return
				}
				writeErr(w, http.StatusPaymentRequired, "account not entitled: "+d.Reason)
				return
			}
		}

		execAuditLog(r, "POST /api/stream/launch-app", fmt.Sprintf("app_id=%q cmd=%q", req.AppID, req.Command))
		sess, err := streamPool.Launch(stream.LaunchOpts{
			ID:       req.AppID,
			Name:     req.Name,
			Command:  req.Command,
			Args:     req.Args,
			Env:      req.Env,
			Width:    req.Width,
			Height:   req.Height,
			FPS:      req.FPS,
			Restart:  req.Restart,
			UserHome: userHome,
			Gaming:   g07Gaming,
		})
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// METER (surface 4: GPU/stream). One GPU/stream session started.
		billingClient.MeterAsync(cpbilling.UsageEvent{
			Product:   cpbilling.ProductGPU,
			AccountID: gpuAccount,
			Kind:      cpbilling.KindGPUSession,
			Count:     1,
		})
		writeJSON(w, sess)
	})

	// Wine prefix management
	wineSvc.RegisterHandlers(mux, authStore)
	desktopSvc.RegisterHandlers(mux)
	gpu.RegisterGPUInfoHandlers(mux)

	// Peering — direct Vula-to-Vula communication
	peeringSvc.RegisterHandlers(mux)
	// PEER-20b: real bandwidth meter + handlers (replaces the removed
	// GET /api/peering/bandwidth stub; also serves /bandwidth/peer).
	bwMeter := peering.NewBandwidthMeter(peering.BandwidthConfig{})
	bwMeter.Start(context.Background())
	peering.RegisterBandwidthHandlers(mux, bwMeter)

	// ── PEER-42: wire the fully-implemented-but-previously-dead peering
	// handler sets. These were unit-tested but never called from
	// cmd/server, so the social core (contacts/messaging/calls/media/
	// groups/verify/relay/feeds/ice/discovery/drop/collab/endpoints/…)
	// returned HTTP 501 via the now-removed peering.go stubs.
	//
	// All handler sets are registered onto a dedicated peeringMux. The
	// /api/peering/inbound/ subtree is then mounted through
	// InboundMiddleware (signature + allow-list verification — every
	// inbound handler reads the verified envelope from the request
	// context and 401s without it). Everything else dispatches straight
	// to peeringMux. The handlers' specific patterns coexist with the
	// already-registered identity/wellknown/bandwidth/stream patterns on
	// the main mux because http.ServeMux always routes to the most
	// specific match.
	{
		peeringMux := http.NewServeMux()

		// Shared identity / transport reused from peeringSvc — no parallel
		// state. ContactStore is the single contacts.json under
		// ~/.vulos/peering/ that InboundMiddleware also gates on.
		pHome := peeringSvc.Home()
		pRoot := peeringSvc.Root()
		pPriv := peeringSvc.PrivateKey()
		pVulaID := peeringSvc.VulaID()
		myServer := "localhost:" + cfg.Port // best-effort self-address

		// Identity key lifecycle (rotation / revocation / recovery). The store
		// owns this node's transition chain + observed revocations and persists
		// them under the identity dir. Wiring the global revocation checker makes
		// every admission/verify point (InboundMiddleware, VerifyVulaSignatureChecked)
		// reject revoked identities; the lifecycle publisher exposes the chain +
		// revocations on /.well-known/vula-id so peers can follow rotations and
		// honor revocations. The recovery anchor (account-bound) is the account
		// recovery-kit anchor: its PUBLIC id is persisted under the identity dir at
		// recovery-kit generation/restore time (the only moments the recovery seed
		// is in hand) and re-loaded here on every boot so the well-known endpoint
		// publishes it and peers TOFU-pin it. The anchor PRIVATE key (recovery
		// signing power) is never stored on the box — it lives only in the off-box
		// recovery kit — so recovery is account-anchored, not box-anchored. When no
		// kit has been generated yet the anchor is empty: rotation + self-revocation
		// still work and recovery activates the moment a kit is generated/restored
		// (the live seed observer wired below calls SetAnchor without a restart).
		identityDir := filepath.Join(pRoot, "identity")
		persistedAnchor := peering.LoadRecoveryAnchorID(identityDir)
		if lcStore, lcErr := peering.NewLifecycleStore(identityDir, pVulaID, persistedAnchor); lcErr != nil {
			log.Printf("[peering] identity lifecycle store init: %v", lcErr)
		} else {
			peering.SetRevocationChecker(lcStore.IsRevoked)
			// Ingest peers' published lifecycle (revocations + rotation/recovery
			// chains) on every authenticated profile fetch/refresh, so a contact's
			// self-/anchor-revocation actually reaches IsRevoked end-to-end and a
			// rotated/recovered contact is followed to its current key.
			peering.SetLifecycleIngestor(func(lc *peering.WKLifecycle) {
				if _, err := lcStore.IngestPeerLifecycle(lc); err != nil {
					log.Printf("[peering] lifecycle ingest: %v", err)
				}
			})
			// Follow a rotated/recovered peer at admission: map a presented current
			// key back to its approved ROOT (only verified chains populate this).
			peering.SetIdentityRootResolver(lcStore.RootForResolvedKey)
			// Self-revocation endpoint (session-authed): POST /api/peering/identity/revoke.
			peering.RegisterIdentityLifecycleHandlers(peeringMux, lcStore, pPriv)
			peering.SetLifecyclePublisher(func() *peering.WKLifecycle {
				root, anchor, chain := lcStore.OwnChain()
				return &peering.WKLifecycle{
					RootVulaID:   root,
					AnchorVulaID: anchor,
					Chain:        chain,
					Revocations:  lcStore.RevocationList(),
				}
			})
			// Recovery-anchor seam (Contract C, box side): when a recovery kit is
			// generated or an account is restored, internal/auth hands us the 64-byte
			// recovery seed transiently. We derive the anchor, persist ONLY its public
			// id, and install it on the live lifecycle store so recovery turns on
			// immediately. The seed is not retained.
			internalauth.SetRecoverySeedObserver(func(seed []byte) {
				anchorID, err := peering.AnchorFromRecoverySeed(lcStore, identityDir, seed)
				if err != nil {
					log.Printf("[peering] recovery anchor install: %v", err)
					return
				}
				log.Printf("[peering] recovery anchor active: %s", anchorID)
			})
			if persistedAnchor != "" {
				log.Printf("[peering] recovery anchor loaded from disk: %s", persistedAnchor)
			}
		}

		// Forward secrecy: publish an X3DH prekey bundle (signed prekey + one-time
		// prekey pool) on /.well-known/vula-id so senders derive per-message keys
		// from an ephemeral + one-time prekey rather than static-static ECDH
		// (prekeys.go). The long-term identity key is used only to sign the bundle.
		if pkStore, pkErr := peering.NewPreKeyStore(filepath.Join(pRoot, "identity"), pVulaID, pPriv, 64); pkErr != nil {
			log.Printf("[peering] prekey store init: %v", pkErr)
		} else {
			// Publish ONLY the signed prekey on the cacheable, unauthenticated
			// well-known GET. One-time prekeys are NOT published there (a cached,
			// depletion-free bundle would let every sender reuse the same OPK,
			// defeating per-sender forward secrecy); they are handed out single-use
			// via the CLAIM endpoint below.
			peering.SetPreKeyPublisher(pkStore.PublicBundleSignedOnly)
			// Public-only directory for REMOTE (browser) peers: they generate their
			// X3DH prekeys client-side and PUBLISH the PUBLIC halves here so senders can
			// claim their one-time prekeys. CLOUD-BLIND — this stores no private key.
			publishedBundles := peering.NewPublishedBundleStore(0, 0)
			// Contract A: the OPK CLAIM endpoint hands out + deletes a single one-time
			// prekey per claim (per-sender forward secrecy), and PUBLISH lets browser
			// peers register their public bundles. Mounted on peeringMux with the same
			// public auth model as the well-known bundle. A background loop replenishes
			// this host's own pool so claims keep depleting fresh OPKs.
			peering.RegisterPreKeyHandlers(peeringMux, pkStore, publishedBundles)
			peering.StartPreKeyReplenish(ctx, pkStore, 64, time.Hour)
		}

		contactStore, csErr := peering.NewContactStore(pHome)
		if csErr != nil {
			log.Printf("[peering] PEER-42 contact store init: %v", csErr)
		}
		inboxStore, ibErr := peering.NewInboxStore(pHome)
		if ibErr != nil {
			log.Printf("[peering] PEER-42 inbox store init: %v", ibErr)
		}
		peerClient := peering.NewPeerClient()

		// callerVulaID extracts the caller's Vula ID from an authenticated
		// request; empty for unauthenticated callers (feeds public/link).
		callerVulaID := func(r *http.Request) string {
			if v := r.Header.Get("X-Vula-ID"); v != "" {
				return v
			}
			return r.Header.Get("X-User-ID")
		}

		if contactStore != nil {
			// Contacts (request/approve/block/remove/list + inbound/request).
			contactAPI := peering.NewContactAPI(contactStore, peerClient, peeringHub, pPriv, pVulaID, myServer)
			// Wire the local profile's display name into approval notifications.
			// We read the profile.json file lazily on each call so updates are
			// reflected without restarting the server.
			profileJSONPath := filepath.Join(pRoot, "profile", "profile.json")
			contactAPI.SelfDisplayName = func() string {
				var pd struct {
					DisplayName string `json:"display_name"`
				}
				if data, err := os.ReadFile(profileJSONPath); err == nil {
					_ = json.Unmarshal(data, &pd)
				}
				return pd.DisplayName
			}
			// PEER-12 AC: "approve triggers fetch" — immediately warm the
			// peer profile cache when a contact is approved.
			contactAPI.OnApprove = peering.FetchPeerProfileAsync
			contactAPI.RegisterContactHandlers(peeringMux)

			// PEER-12: periodic background refresh of approved peers' profiles.
			approvedPeers := func() []peering.WKApprovedPeer {
				contacts := contactStore.ListByState(peering.StateApproved)
				peers := make([]peering.WKApprovedPeer, 0, len(contacts))
				for _, c := range contacts {
					peers = append(peers, peering.WKApprovedPeer{VulaID: c.VulaID, ServerAddr: c.Server})
				}
				return peers
			}
			peering.StartPeerProfileSync(ctx, approvedPeers)

			// Lifecycle propagation: periodically re-ingest approved contacts'
			// revocation/rotation bundles (bypassing the profile cache) so a
			// revocation a contact publishes is honored within the refresh interval
			// even if no fresh profile fetch occurs.
			peering.StartLifecycleRefresh(ctx, approvedPeers)

			// Reachability ladder (direct → relay → contact.Server): keep the
			// resolvePeerBaseURL cache warm for every approved contact so box→box
			// delivery prefers a verified-direct or relay-tunnel endpoint over a
			// possibly-stale contact.Server, without adding a synchronous network
			// call to the hot delivery path. A no-op when VULOS_RELAY_BASE_URL is
			// unset (self-host boxes without a relay see unchanged B-0 behavior).
			peering.StartReachabilityRefresh(ctx, reachRelayBaseURLs(), approvedPeers)

			// Messaging (conversations + inbound/message).
			if inboxStore != nil {
				msgAPI := peering.NewMessageAPI(contactStore, inboxStore, peerClient, peeringHub, pPriv, pVulaID)
				msgAPI.RegisterMessageHandlers(peeringMux)

				// Groups (group def/members/send + inbound group-*).
				if groupStore, gErr := peering.NewGroupStore(pHome); gErr != nil {
					log.Printf("[peering] PEER-42 group store init: %v", gErr)
				} else {
					groupAPI := peering.NewGroupAPI(groupStore, contactStore, inboxStore, peerClient, peeringHub, pPriv, pVulaID)
					peering.RegisterGroupHandlers(peeringMux, groupAPI)
				}
			}

			// Relay store-and-forward (deposit/pickup/ack).
			if relayStore, rErr := peering.NewRelayStore(pHome, contactStore); rErr != nil {
				log.Printf("[peering] PEER-42 relay store init: %v", rErr)
			} else {
				relayStore.WithBilling(billingClient)
				peering.RegisterRelayHandlers(peeringMux, relayStore)
				// Launch the background blob reaper. Without this the store
				// grows without bound: expired blobs are only dropped lazily
				// on pickup, so a blob nobody ever picks up lives forever.
				// The goroutine exits when ctx is cancelled at shutdown.
				relayStore.Start(ctx)
			}

			// Feeds (own append-only feeds; peers-gating uses contacts).
			if feedStore, fErr := peering.NewFeedStore(pRoot, pPriv, pVulaID, contactStore); fErr != nil {
				log.Printf("[peering] PEER-42 feed store init: %v", fErr)
			} else {
				peering.RegisterFeedHandlers(peeringMux, feedStore, callerVulaID)
			}

			// Drop (LAN mDNS file drop). Wire a real media sender (DropTransfer)
			// so drop/send actually moves bytes over the shared media pipeline
			// (content-hash bucket + signed fetch URL) instead of returning
			// "media service unavailable". The sender uses its own MediaStore
			// over the same home/key, so it shares the on-disk content store and
			// URL-signing secret with the media handlers registered below.
			var dropSender peering.DropMediaSender
			if dms, dmErr := peering.NewMediaStore(pHome, pPriv, peerClient); dmErr != nil {
				log.Printf("[peering] drop media sender init: %v", dmErr)
			} else {
				// selfBaseURL is this node's externally reachable base URL used
				// to build signed fetch URLs the recipient pulls from. Prefer an
				// explicit public URL, then the relay base; empty ⇒ send fails
				// fast with a clear error rather than emitting an unfetchable URL.
				selfBaseURL := os.Getenv("VULOS_PUBLIC_BASE_URL")
				if selfBaseURL == "" {
					selfBaseURL = os.Getenv("VULOS_RELAY_BASE_URL")
				}
				downloadDir := filepath.Join(pHome, "Downloads")
				dropSender = peering.NewDropTransfer(dms, peerClient, pVulaID, "", selfBaseURL, downloadDir)
			}
			dropSvc := peering.NewDropService(pVulaID, "", contactStore, dropSender)
			dropSvc.Start(context.Background())
			peering.RegisterDropHandlers(peeringMux, dropSvc)
		}

		// Media (upload/fetch/thumb + inbound/media). HMAC URL signing key
		// is the node's Ed25519 private key seed.
		if mediaStore, mErr := peering.NewMediaStore(pHome, pPriv, peerClient); mErr != nil {
			log.Printf("[peering] PEER-42 media store init: %v", mErr)
		} else {
			mediaStore.RegisterMediaHandlers(peeringMux)
		}

		// Calls (initiate/answer/reject/signal/hangup + inbound/signal).
		if contactStore != nil {
			callRelay := peering.NewCallRelay(pVulaID, contactStore, peeringHub, peerClient, pPriv)
			peering.RegisterCallHandlers(peeringMux, callRelay)
		}

		// Mesh group call signaling (PEER-26): WebSocket room for 3–4 peer full-mesh.
		peering.RegisterMeshCallHandlers(peeringMux, peering.NewMeshSignalingHub())

		// Call history (list + record).
		peering.RegisterCallHistoryHandlers(peeringMux, pRoot)

		// Pre-call lobby: bandwidth table, SFU host selection, capacity estimate (PEER-25).
		lobbySvc := peering.NewLobbyService(bwMeter)
		peering.RegisterLobbyHandlers(peeringMux, lobbySvc)

		// Profile (get/put + avatar). contacts gates peer-visibility checks.
		var profileContacts interface {
			IsApproved(string) bool
		}
		if contactStore != nil {
			profileContacts = contactStore
		}
		profStore := peering.RegisterProfileHandlers(peeringMux, filepath.Join(pRoot, "profile"), pVulaID, profileContacts)

		// WAVE-7: internal content-key lookup the Vulos cell calls to enforce
		// recipient-targeting on content-blind shares (closes the F2 gap). Gated by
		// CP_SHARED_SECRET (X-Vulos-Internal-Auth), registered on the MAIN mux and
		// listed in auth.publicPaths so it is reached by the internal caller and gated
		// solely by the shared secret (the session middleware strips X-User-ID). When
		// CP_SHARED_SECRET is unset the endpoint returns 503, which the cell treats as
		// fail-closed. Cross-repo contract lives in peering/content_key_lookup.go.
		peering.RegisterContentKeyLookup(mux, profStore, os.Getenv("CP_SHARED_SECRET"))

		// Email verification (initiate/confirm/status against vulos.org).
		if vfySvc, vErr := peering.VerifyNewService(filepath.Join(pRoot, "identity")); vErr != nil {
			log.Printf("[peering] PEER-42 verify service init: %v", vErr)
		} else {
			peering.RegisterVerifyHandlers(peeringMux, vfySvc)
		}

		// Directory discovery (lookup/search). No hosted directory by default —
		// disabled unless the operator sets VULOS_VERIFY_URL; when unset, lookups
		// return "not found"/empty cleanly with no outbound call.
		peering.RegisterDiscoveryHandlers(peeringMux, peering.DiscoveryNewService(nil))

		// ICE / TURN config for WebRTC.
		peering.RegisterICEHandlers(peeringMux)

		// Sovereign-federation config profile: one coherent, self-reporting
		// status surface over VULOS_RELAY_BASE_URL / VULOS_VERIFY_URL /
		// VULOS_RENDEZVOUS_URL / TURN_SECRET+TURN_HOST / public-STUN opt-out.
		peering.RegisterFederationProfileHandler(peeringMux)

		// CONSOLIDATION B-3: the PEER-40 endpoint registry + its four REST routes
		// (RegisterEndpointHandlers) were deleted. They were built but never wired
		// into any live delivery path (delivery uses the single contact.Server
		// address via resolvePeerBaseURL + PeerClient.Post + the durable outbox).
		// Reachability is now the relay tunnel + verified-direct endpoint, not a
		// self-claimed in-memory host:port list.

		// Relay attestation document (public evidence endpoint).
		peering.RegisterAttestHandlers(peeringMux, peering.NewAttestStore())

		// Proximity drop codes (generate/redeem).
		peering.RegisterProximityHandlers(peeringMux, peering.NewProxService(pVulaID, myServer))

		// Realtime collaboration: CRDT transport + REST + inbound sync + time-travel.
		// NOTE: RegisterCollabShareHandlers (collab_share.go) is
		// deliberately NOT wired — it registers the same
		// /api/peering/collab/* and /api/peering/inbound/collab-* patterns
		// as RegisterCollabHandlers, which would panic the ServeMux. The
		// CRDT transport set (incl. the /sync WebSocket) is the superset.
		// The collab-invite inbound route (from collab_share/shares) does NOT
		// conflict and is wired separately via a lightweight SharesService below.
		// Single shared per-document share ACL (Contract 4): the SAME ShareStore
		// instance backs BOTH the collab inbound/WS authorization (CollabStore)
		// and the share invite/perms intake (SharesService below). An invite
		// received here registers the peer's permission, which the collab inbound
		// and WebSocket paths then enforce — closing the live hole where any
		// approved contact could persist/broadcast CRDT ops to any document.
		shareStore := peering.NewShareStore()
		if collabStore, cErr := peering.NewCollabStore(filepath.Join(pRoot, "collab")); cErr != nil {
			log.Printf("[peering] PEER-42 collab store init: %v", cErr)
		} else {
			collabStore.WithShareStore(shareStore)
			// Bind authenticated OS sessions to this box's VulaID so the collab WS
			// authorizer checks the share ACL against an un-spoofable identity rather
			// than the client-supplied X-Vula-ID header (Contract 4, multi-user box).
			collabStore.WithSelfVulaID(pVulaID)
			peering.RegisterCollabHandlers(peeringMux, collabStore)
			// Collab history (time-travel snapshots): GET /api/peering/collab/{doc_id}/history[/{seq}]
			// and GET /api/peering/collab-sync-v2. Routes are non-overlapping with
			// RegisterCollabHandlers so they coexist safely on the same mux.
			peering.RegisterCollabHistoryHandlers(peeringMux, collabStore)
		}

		// Presence awareness: WebSocket /api/peering/presence/{app_id},
		// GET /api/peering/presence/{app_id}/peers, and
		// POST /api/peering/inbound/presence. None of these routes overlap
		// with other registered patterns.
		peering.RegisterPresenceHandlers(peeringMux, peering.NewPresenceService())

		// Collab-invite inbound route: POST /api/peering/inbound/collab-invite.
		// This is the only route from shares.go / collab_share.go that does not
		// conflict with RegisterCollabHandlers. We wire it via a minimal
		// SharesService (in-memory ShareStore, no outbound deps needed for the
		// inbound-only path).
		if contactStore != nil {
			sharesSvc := peering.NewSharesService(
				contactStore,
				shareStore,
				peerClient,
				pPriv,
				pVulaID,
			)
			peeringMux.HandleFunc("POST /api/peering/inbound/collab-invite", sharesSvc.HandleInboundShare)
		}

		// Mount onto the main mux. Inbound goes through InboundMiddleware
		// (verifies the signed envelope, sets it on the request context),
		// then dispatches to peeringMux's inbound handlers. All other
		// peering + feeds routes dispatch straight to peeringMux.
		if contactStore != nil {
			mux.Handle("/api/peering/inbound/", peering.InboundMiddleware(contactStore, peeringMux))
		}
		mux.Handle("/api/peering/", peeringMux)
		mux.Handle("/api/feeds", peeringMux)
		mux.Handle("/api/feeds/", peeringMux)
	}

	// PEER-27: Pion SFU — host-side selective-forwarding unit.
	// Registers 5 routes under /api/sfu/:
	//   POST   /api/sfu/rooms
	//   DELETE /api/sfu/rooms/{room_id}
	//   POST   /api/sfu/rooms/{room_id}/join
	//   POST   /api/sfu/rooms/{room_id}/ice
	sfuSvc := sfu.New().WithBilling(billingClient)
	sfu.RegisterSFUHandlers(mux, sfuSvc)

	// App visibility (private|local|public).
	//
	// Finding 2 (HIGH): publishing an app (any non-private visibility) exposes it
	// beyond this device, so it is gated on RoleAdmin OR app ownership — not mere
	// authentication. Ownership = the user has a running namespace for the app
	// (the launcher records OwnerID). Publish events are audit-logged.
	publishAuthorizer := func(r *http.Request, appID, visibility string) bool {
		userID := r.Header.Get("X-User-ID") // stamped by auth middleware; never client-supplied
		if userID == "" {
			return false
		}
		isAdmin := false
		if p, _ := authStore.GetProfile(userID); p != nil && p.Role == auth.RoleAdmin {
			isAdmin = true
		}
		isOwner := false
		if !isAdmin {
			if ns, ok := netMgr.GetForUser(appID, userID); ok && ns.OwnerID == userID {
				isOwner = true
			}
		}
		if isAdmin || isOwner {
			execAuditLog(r, "PUBLISH /api/apps/"+appID+"/visibility",
				fmt.Sprintf("app_id=%s visibility=%s user=%s admin=%v owner=%v", appID, visibility, userID, isAdmin, isOwner))
			return true
		}
		return false
	}
	appnet.RegisterVisibilityHandlers(mux, appStore, visStore, publishAuthorizer)

	// PUBWEB anonymous public entrypoint (Finding 1, HIGH). The public-web edge
	// (Caddy / nginx) proxies published apps HERE, over loopback, instead of
	// straight at the app namespace. PublicHandler serves an app only when its
	// visibility is "public", strips all client X-Vulos-* headers, and injects no
	// identity. Adopted ports are never resolvable here (GetAnyForApp ignores the
	// external-upstream registry). The route is a public prefix (see
	// services/auth publicPrefixes) so the session middleware defers to it.
	appGateway.SetPublicVisibility(func(appID string) bool {
		return visStore.Get(appID) == appnet.VisibilityPublic
	})
	mux.HandleFunc(appnet.PubwebPathPrefix, appGateway.PublicHandler())

	// ADOPT-A-PORT: register + rehydrate adopted loopback upstreams. These are
	// reachable ONLY through the :8080 gateway (never the PUBWEB path above), so
	// they inherit session auth, entitlement gating, X-Vulos-* strip+inject and
	// rate limiting with no new trust path.
	if extStore, extErr := appnet.NewExternalUpstreamStore(); extErr != nil {
		log.Printf("[appnet/adopt] external-upstream store unavailable: %v — adopt-a-port disabled", extErr)
	} else {
		for _, u := range extStore.All() {
			if err := netMgr.RegisterExternalUpstream(u); err != nil {
				log.Printf("[appnet/adopt] skipping persisted upstream %s: %v", u.AppID, err)
				continue
			}
			if u.Product != "" {
				appGateway.AllowApp(u.AppID, u.Product)
			}
		}
		appnet.RegisterProxyAdoptHandlers(mux, appnet.ProxyAdoptDeps{
			Mgr:   netMgr,
			Store: extStore,
			// OS M1 (security): adopting a loopback port grants gateway-authenticated
			// reach to an arbitrary local service (Ollama, Postgres, Redis, a local
			// admin panel, …). On a multi-user box that must be restricted to the box
			// owner / RoleAdmin — not any authenticated user (incl. RoleGuest).
			AuthorizeAdopt: func(r *http.Request) bool {
				userID := r.Header.Get("X-User-ID") // stamped by auth middleware; never client-supplied
				if userID == "" {
					return false
				}
				if p, _ := authStore.GetProfile(userID); p != nil && p.Role == auth.RoleAdmin {
					return true
				}
				return false
			},
			OnRegister: func(appID, product string) {
				if product != "" {
					appGateway.AllowApp(appID, product)
				}
			},
			OnRemove: func(appID string) {
				appGateway.RemoveAppSecret(appID)
				appGateway.RemoveAppGrants(appID)
			},
		})
		log.Printf("[appnet/adopt] registered POST/GET /api/apps/proxy, DELETE /api/apps/proxy/{id}")
	}

	// New-feature routes: airouter, identity, multiinstance, appnet subdomain
	// provisioning, recovery handlers, cloud-sync, edge-cache.
	// Must be called AFTER RegisterVisibilityHandlers.
	fabricAppSync, lmNoteStore, sharedInstanceRegistry := registerNewFeatureRoutes(mux, newFeatureDeps{
		dbDir:              dbDir,
		netMgr:             netMgr,
		visStore:           visStore,
		authStore:          authStore,
		integrationsClient: integrationsClient,
		activeEnv:          activeEnv,
		webhooksDispatcher: webhooksDispatcher,
	}, ctx)
	if lmNoteStore != nil {
		defer lmNoteStore.Close()
	}

	// MinIO storage provisioning — H2 fix: admin-only + IsProvisioned guard
	storageprov.RegisterHandlers(mux, home, authStore)

	// Public API (vkl_ bearer-token developer keys)
	publicAPICloser := registerPublicAPIRoutes(mux, authStore, dbDir, sharedInstanceRegistry)
	defer publicAPICloser()

	// DEVICEKEY-ROTATE-01: device-key rotation/revocation HTTP API + admission gate.
	// Break-glass rotation/revocation is authorized ONLY by a fleetid.VerifyQuorum
	// of OTHER rostered boxes — a box can never self-authorize (see rotation.go).
	if deviceKSErr == nil {
		deviceRevStore, drErr := devicekey.NewRevocationStore(datadir.Join("auth", "tpm"))
		if drErr != nil {
			// FAIL CLOSED, not open. NewRevocationStore deliberately fails closed
			// on a corrupt store (refuses to open rather than start falsely-empty).
			// Merely logging and skipping would leave the process-wide checker nil,
			// so IsDeviceKeyRevoked would report NOTHING revoked for the whole
			// process lifetime — converting the store's LOCAL fail-closed into a
			// GLOBAL fail-OPEN. Instead install a checker that treats EVERY device
			// key as untrusted: Sign/Rotate refuse (ErrActiveKeyRevoked) and remote
			// device-signature checks reject, disabling device-key operations (the
			// same posture as the passkeys-disabled path when deviceKS itself fails)
			// until the operator repairs the store. Better a visible degradation
			// than a silent hole in revocation enforcement.
			log.Printf("[devicekey] ERROR: revocation store unavailable (%v) — FAILING CLOSED: device-key signing/verification disabled until %s is repaired", drErr, datadir.Join("auth", "tpm", "revocations.json"))
			devicekey.SetRevocationChecker(func(string) bool { return true })
		} else {
			devicekey.SetRevocationChecker(deviceRevStore.IsRevoked)
			registerDeviceKeyLifecycleRoutes(mux, dkLifecycleDeps{
				KeyStore:        deviceKS,
				RevocationStore: deviceRevStore,
				AuthStore:       authStore,
				Registry:        sharedInstanceRegistry,
			})
			log.Printf("[devicekey] rotation/revocation API registered (owner+step-up; break-glass via fleetid quorum)")

			// DEVICEKEY-ROTATE-02: fleet-wide revocation propagation. Periodically
			// pull every peer's GET /api/auth/device/revocations and merge what
			// re-verifies (MergeRevocationBatch is fail-closed per entry), so a
			// self- or quorum-revoked device key becomes known on boxes that never
			// issued the revocation. PeerSource is this box's own registry view —
			// non-self (skip the owner-role entry), non-revoked instances with a
			// reachable endpoint; pulling from self, if it ever happened, is a
			// harmless idempotent no-op. The loop is a safe no-op until peers exist.
			revSyncer := devicekey.NewRevSyncer(
				deviceRevStore,
				func(context.Context) ([]string, error) {
					if sharedInstanceRegistry == nil {
						return nil, nil
					}
					insts, lerr := sharedInstanceRegistry.List()
					if lerr != nil {
						return nil, lerr
					}
					var peers []string
					for _, in := range insts {
						if in.Role == multiinstance.RoleOwner || in.Revoked || in.EndpointURL == "" {
							continue
						}
						peers = append(peers, in.EndpointURL)
					}
					return peers, nil
				},
				func() (fleetid.Roster, int) {
					return registryRoster{sharedInstanceRegistry}, fleetid.MinThreshold
				},
				0,
			)
			go revSyncer.Run(ctx)
		}
	}

	// Web proxy (kept for API-level proxying)
	mux.HandleFunc("/api/proxy/ws/", proxySvc.WSRelayHandler())
	mux.HandleFunc("/api/proxy/", proxySvc.Handler())

	// Browser profiles
	mux.HandleFunc("GET /api/browser-profiles", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		browserProfiles.EnsureDefaults(userID)
		writeJSON(w, browserProfiles.ListForUser(userID))
	})
	mux.HandleFunc("POST /api/browser-profiles", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		var req struct {
			Name  string `json:"name"`
			Color string `json:"color"`
			Icon  string `json:"icon"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		p, err := browserProfiles.Create(userID, req.Name, req.Color, req.Icon)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		browserProfiles.Flush()
		writeJSON(w, p)
	})
	mux.HandleFunc("PUT /api/browser-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		// IDOR-BPROFILE-01: only the profile owner or an admin may mutate a browser profile.
		userID := r.Header.Get("X-User-ID")
		bp, ok := browserProfiles.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "browser profile not found")
			return
		}
		if bp.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 403, "cannot modify another user's browser profile")
				return
			}
		}
		var req struct {
			Name  string `json:"name"`
			Color string `json:"color"`
			Icon  string `json:"icon"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		browserProfiles.Update(r.PathValue("id"), req.Name, req.Color, req.Icon)
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "updated"})
	})
	mux.HandleFunc("DELETE /api/browser-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		// IDOR-BPROFILE-01: only the profile owner or an admin may delete a browser profile.
		userID := r.Header.Get("X-User-ID")
		bp, ok := browserProfiles.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "browser profile not found")
			return
		}
		if bp.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 403, "cannot delete another user's browser profile")
				return
			}
		}
		browserProfiles.Delete(r.PathValue("id"))
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "deleted"})
	})
	mux.HandleFunc("POST /api/browser-profiles/{id}/clear", func(w http.ResponseWriter, r *http.Request) {
		// IDOR-BPROFILE-01: only the profile owner or an admin may clear a browser profile's data.
		userID := r.Header.Get("X-User-ID")
		bp, ok := browserProfiles.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "browser profile not found")
			return
		}
		if bp.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 403, "cannot clear another user's browser profile")
				return
			}
		}
		browserProfiles.ClearData(r.PathValue("id"))
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "cleared"})
	})
	mux.HandleFunc("POST /api/browser-profiles/{id}/bind", func(w http.ResponseWriter, r *http.Request) {
		// IDOR-BPROFILE-01: only the profile owner or an admin may bind an app to a browser profile.
		userID := r.Header.Get("X-User-ID")
		bp, ok := browserProfiles.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "browser profile not found")
			return
		}
		if bp.UserID != userID {
			if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 403, "cannot bind app to another user's browser profile")
				return
			}
		}
		var req struct {
			AppID string `json:"app_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		browserProfiles.BindApp(r.PathValue("id"), req.AppID)
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "bound"})
	})

	// AI-generated apps gallery — hardened handlers in routes_aiapps_security.go (SEC-I).
	aiAppsDir := datadir.Join("ai-apps")
	registerAIAppsSecurityWrappers(mux, aiAppsDir, authStore)
	registerAIAppsRoutes(mux, aiAppsDir, authStore)

	// AI-07: version history + rollback endpoints
	registerAIAppsVersionsRoutes(mux, aiAppsDir, authStore)

	// Native window management — spawn Cog/WPE instances as real compositor windows
	// Cached at startup: detect if we're on baremetal (sole Cog instance) or native (compositor with multi-window)
	// D93: v2 opt-in required for native-launch / labwc path. Default (v1) = baremetal/cage.
	nativeModeV2 := os.Getenv("VULOS_NATIVE_MODE_V2") == "1"
	nativeMode := detectNativeMode(nativeModeV2)
	log.Printf("[shell] native mode: %s (v2_enabled=%v)", nativeMode, nativeModeV2)

	mux.HandleFunc("GET /api/shell/native-mode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"mode": nativeMode, "v2_enabled": nativeModeV2})
	})

	mux.HandleFunc("POST /api/shell/native-window", func(w http.ResponseWriter, r *http.Request) {
		// SEC: spawning a browser window is a privileged host operation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		if nativeMode != "native" {
			writeErr(w, 400, "native windows not supported in "+nativeMode+" mode")
			return
		}
		var req struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			AlwaysOnTop bool   `json:"always_on_top"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.URL == "" {
			writeErr(w, 400, "url required")
			return
		}

		// Validate scheme and host (SSRF prevention — mirrors /api/open validation).
		if err := validateNativeWindowURL(req.URL); err != nil {
			writeErr(w, 400, err.Error())
			return
		}

		if req.Width == 0 {
			req.Width = 720
		}
		if req.Height == 0 {
			req.Height = 500
		}
		if req.Title == "" {
			req.Title = "Vulos"
		}

		// Spawn a new Cog instance as a standalone Wayland window
		args := []string{}

		// Try cog first, fall back to wpe-launch
		cogBin := "cog"
		if _, err := exec.LookPath("cog"); err != nil {
			if _, err := exec.LookPath("wpe-launch"); err == nil {
				cogBin = "wpe-launch"
			} else {
				writeErr(w, 500, "no cog or wpe-launch binary found")
				return
			}
		}

		if cogBin == "cog" {
			// Cog supports --title and geometry hints
			args = append(args, fmt.Sprintf("--title=%s", req.Title))
			// Cog uses WPE_SHELL_GEOMETRY env for window size
		}
		args = append(args, req.URL)

		cmd := exec.CommandContext(ctx, cogBin, args...)
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("WPE_SHELL_GEOMETRY=%dx%d+60+32", req.Width, req.Height),
		)
		// Set always-on-top via wlr-layer-shell env hint if requested
		if req.AlwaysOnTop {
			cmd.Env = append(cmd.Env, "WLR_SCENE_ALWAYS_ON_TOP=1")
		}

		if err := cmd.Start(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}

		pid := cmd.Process.Pid
		// Track for cleanup
		go func() {
			cmd.Wait()
			log.Printf("[shell] native window pid=%d exited", pid)
		}()

		writeJSON(w, map[string]any{"pid": pid, "title": req.Title})
	})

	mux.HandleFunc("DELETE /api/shell/native-window", func(w http.ResponseWriter, r *http.Request) {
		// SEC: sending SIGTERM to a PID is a privileged host operation — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			PID int `json:"pid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID == 0 {
			writeErr(w, 400, "pid required")
			return
		}
		// Reject clearly invalid PIDs (kernel range on Linux is 1–4194304).
		if req.PID < 2 || req.PID > 4194304 {
			writeErr(w, 400, "invalid pid")
			return
		}
		execAuditLog(r, "DELETE /api/shell/native-window", fmt.Sprintf("pid=%d", req.PID))
		proc, err := os.FindProcess(req.PID)
		if err != nil {
			writeErr(w, 404, "process not found")
			return
		}
		proc.Signal(syscall.SIGTERM)
		writeJSON(w, map[string]string{"status": "killed"})
	})

	// BMINIT-12/13: installer + live-session endpoints (Try Vulos → Install flow, NETB-04).
	// Routes are always registered; destructive operations are self-guarded by
	// the install handler (requires confirm:true) and by installer.IsLiveSession
	// which reports mode:live only when the root is a squashfs+overlay.
	installerIsAdmin := func(r *http.Request) bool {
		p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
		return p != nil && p.Role == auth.RoleAdmin
	}
	installerSvc := installer.NewWithAdminGate(installerIsAdmin)
	installer.RegisterHandlers(mux, installerSvc)
	// NETB-03: netboot-to-install endpoints.  Share the SAME admin-gated Service
	// so the destructive netboot install is admin-gated and its squashfs is
	// signature-verified against the pinned trust anchor before being written.
	installer.RegisterNetbootHandlers(mux, installerSvc)

	// BMINIT-18: wlr-foreign-toplevel-management-v1 window enumeration + control.
	// Registers GET /api/shell/windows, POST /api/shell/windows/focus,
	// POST /api/shell/windows/minimize, POST /api/shell/windows/close.
	// Only meaningful when running under labwc (v2 native mode); when lswt/wlrctl
	// are absent every call degrades gracefully (empty list / 503).
	wltoplevel.New().RegisterHandlers(mux)

	// BMINIT-04: native-launch — spawn an arbitrary binary as a Wayland/X11 native window.
	// Admin-gated; only available when nativeMode == "native".
	mux.HandleFunc("POST /api/shell/native-launch", func(w http.ResponseWriter, r *http.Request) {
		// [1] Mode gate — must be native before any further work.
		if nativeMode != "native" {
			writeErr(w, 400, "native-launch not available in "+nativeMode+" mode")
			return
		}
		// [2] Admin gate.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		// [3] Parse request.
		var req struct {
			Binary string   `json:"binary"`
			Args   []string `json:"args"`
			AppID  string   `json:"app_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		// [4] If app_id is supplied, resolve binary from the installed manifest.
		if req.AppID != "" && req.Binary == "" {
			// SECAUDIT2 M-1: charset-validate app_id before the manifest path
			// join (parity with /api/apps/launch). ^[a-z0-9][a-z0-9-]{0,63}$
			validAppID := len(req.AppID) >= 1 && len(req.AppID) <= 64
			for i := 0; i < len(req.AppID) && validAppID; i++ {
				c := req.AppID[i]
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (c == '-' && i > 0)) {
					validAppID = false
				}
			}
			if !validAppID {
				writeErr(w, 400, "invalid app_id")
				return
			}
			if m, err := appStore.GetManifest(req.AppID); err == nil && m.Command != "" {
				req.Binary = m.Command
			}
		}
		if req.Binary == "" {
			writeErr(w, 400, "binary required")
			return
		}
		// [5] Audit log.
		log.Printf("[native-launch] admin=%s binary=%q args=%v app_id=%q",
			r.Header.Get("X-User-ID"), req.Binary, req.Args, req.AppID)
		// [6] Launch with scrubbed env.
		spec := appnet.NativeLaunchSpec{
			Binary:         req.Binary,
			Args:           req.Args,
			WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
			XDGRuntimeDir:  os.Getenv("XDG_RUNTIME_DIR"),
		}
		pid, err := appnet.LaunchNative(spec)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]any{"pid": pid})
	})

	// OS Control — AI and frontend can control the shell (admin only)
	mux.HandleFunc("POST /api/os/open-app", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		// Triggers app launch from backend (AI can call this)
		var req struct {
			AppID   string `json:"app_id"`
			AppPort int    `json:"app_port"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.AppPort == 0 {
			req.AppPort = 80
		}
		hostPort, ok := portPool.Allocate(req.AppID)
		if !ok {
			writeErr(w, 503, "no ports")
			return
		}
		appSecret := appGateway.GenerateAppSecret(req.AppID)
		userID := r.Header.Get("X-User-ID")
		_, err := launcher.Launch(ctx, req.AppID, userID, hostPort, req.AppPort, "", nil, "", []string{"VULOS_APP_SECRET=" + appSecret})
		if err != nil {
			portPool.Release(req.AppID)
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"app_id": req.AppID, "url": gateway.URLForApp(req.AppID)})
	})
	mux.HandleFunc("POST /api/os/close-app", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID string `json:"app_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		launcher.Stop(ctx, req.AppID)
		portPool.Release(req.AppID)
		appGateway.RemoveAppSecret(req.AppID)
		writeJSON(w, map[string]string{"status": "closed"})
	})
	mux.HandleFunc("POST /api/os/notify", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Level string `json:"level"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		level := notify.LevelInfo
		if req.Level == "warning" {
			level = notify.LevelWarning
		}
		if req.Level == "urgent" {
			level = notify.LevelUrgent
		}
		notifySvc.Send(req.Title, req.Body, level, "ai")
		writeJSON(w, map[string]string{"status": "sent"})
	})
	mux.HandleFunc("POST /api/os/energy-mode", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		energyMgr.SetMode(energy.Mode(req.Mode))
		writeJSON(w, energyMgr.State())
	})

	// OS update (OTA): OPT-IN, verify-only. The box polls the vulos.org release
	// channel to surface available updates in Settings -> OS Update, and fires a
	// PRIORITY notification to the owner on a new SECURITY update — but it NEVER
	// auto-stages. The user chooses to download + stage; the reboot/flip stays
	// manual. This replaces the old osdist auto-update loop (which auto-staged by
	// default, contradicting the opt-in model); the real A/B staging under the
	// hood still uses osdist.SlotManager via services/ota.
	otaClient := registerOTARoutes(mux, notifySvc, authStore)

	// Cluster heartbeat + peer discovery (services/cluster).
	// Conditional on S3 being configured and VULOS_CLUSTER_PASSPHRASE being set.
	clusterPassphrase := os.Getenv("VULOS_CLUSTER_PASSPHRASE")
	// UNIFIED-STORAGE: the OS routes its own user/app data (cluster + sync S3
	// paths) through the resolver under the "os/" prefix in the shared per-user
	// bucket. OSResolution preserves the legacy cluster defaults (localhost:9000,
	// vulos-cluster) so existing behaviour is unchanged when only VULOS_S3_* are
	// set; appfs/store remain the local-FS fallback used when no object store is
	// configured (empty Endpoint).
	osStore := storageResolver.OSResolution(ctx)
	osStoreHost, osStoreSSL := osStore.S3Host()
	clusterS3Cfg := cluster.S3Config{
		Endpoint:  osStoreHost,
		Bucket:    osStore.Bucket,
		AccessKey: osStore.AccessKey,
		SecretKey: osStore.SecretKey,
		UseSSL:    osStoreSSL,
	}
	log.Printf("[storage] OS storage namespace=%q bucket=%q object-store=%v",
		osStore.Prefix, osStore.Bucket, osStore.Configured())

	// OS SNAPSHOTS — point-in-time restore points for the box's bucket
	// (services/snapshot). Available whenever the box has an object store
	// configured (independent of cluster sync / passphrase). Snapshot artifacts
	// live INSIDE the box's own bucket under the OS data prefix, so they work
	// identically for provisioned and bring-your-own-bucket boxes.
	snapPolicy := snapshot.DefaultPolicy
	snapDeps := snapshotDeps{authStore: authStore, policy: snapPolicy, webhooksDispatcher: webhooksDispatcher}
	if osStore.Configured() {
		snapAccount := os.Getenv("VULOS_ACCOUNT_ID") // box billing account (metering); may be empty on self-host
		snapRes := osStore
		snapDeps.newSnapshotter = func() (*snapshot.Snapshotter, error) {
			st, err := snapshot.NewS3Store(snapRes)
			if err != nil {
				return nil, err
			}
			s := snapshot.New(st, snapshot.Config{DataPrefix: snapRes.Prefix, AccountID: snapAccount})
			if billingClient.Enabled() {
				s = s.WithMeter(snapshot.CPMeter{Client: billingClient})
			}
			return s, nil
		}
		// Optional scheduled snapshots + retention. Enable by setting
		// VULOS_SNAPSHOT_INTERVAL (e.g. "24h"). Disabled by default.
		if iv := os.Getenv("VULOS_SNAPSHOT_INTERVAL"); iv != "" {
			if d, perr := time.ParseDuration(iv); perr == nil && d > 0 {
				go func() {
					s, err := snapDeps.newSnapshotter()
					if err != nil {
						log.Printf("[snapshot] scheduler init failed: %v", err)
						return
					}
					sched := snapshot.NewScheduler(s, d, snapPolicy)
					sched.OnCreated = func(idx *snapshot.Index) {
						emitWebhookEvent(webhooksDispatcher, "snapshot.created", map[string]any{
							"id":           idx.ID,
							"kind":         idx.Kind,
							"object_count": idx.ObjectCount,
						})
					}
					sched.Start(ctx)
				}()
				log.Printf("[snapshot] scheduled snapshots enabled (interval=%s)", d)
			} else {
				log.Printf("[snapshot] invalid VULOS_SNAPSHOT_INTERVAL %q: %v", iv, perr)
			}
		}
	}
	registerSnapshotRoutes(mux, snapDeps)
	log.Printf("[snapshot] admin snapshot endpoints registered (available=%v)", snapDeps.newSnapshotter != nil)

	if clusterS3Cfg.Configured() && clusterPassphrase != "" {
		if clusterInst, clusterErr := cluster.New(clusterS3Cfg, clusterPassphrase); clusterErr == nil {
			go clusterInst.Start(ctx)
			log.Printf("[cluster] heartbeat loop started (node_id=%s, enabled=%v)",
				clusterInst.Health()["node_id"], clusterInst.Enabled())

			// CRDT sync engine (services/sync): watch data dirs, upload changes.
			if clusterInst.Enabled() {
				if s3c := clusterInst.S3Client(); s3c != nil {
					if syncer, syncErr := sync.NewFromCluster(sync.Config{}, s3c); syncErr == nil {
						clusterSyncer = syncer
						go func() {
							if err := syncer.Start(ctx); err != nil {
								log.Printf("[sync] syncer exited: %v", err)
							}
						}()
						log.Printf("[sync] file sync loop started")
					} else {
						log.Printf("[sync] init warning: %v", syncErr)
					}

					// DB snapshot backup/restore (services/sync Compactor/Restorer)
					// wired to admin HTTP endpoints + an optional periodic loop.
					backupNodeID, _ := clusterInst.Health()["node_id"].(string)
					backupLeaseCfg := lease.S3Config{
						Endpoint:  clusterS3Cfg.Endpoint,
						Bucket:    clusterS3Cfg.Bucket,
						AccessKey: clusterS3Cfg.AccessKey,
						SecretKey: clusterS3Cfg.SecretKey,
						UseSSL:    clusterS3Cfg.UseSSL,
					}
					backupDBPath := os.Getenv("VULOS_BACKUP_DB")
					if backupDBPath == "" {
						backupDBPath = filepath.Join(dbDir, "auth.db")
					}
					registerBackupRoutes(mux, clusterBackupDeps(authStore, s3c, backupLeaseCfg, backupNodeID, backupDBPath, clusterPassphrase, webhooksDispatcher))
					log.Printf("[backup] admin backup/restore endpoints registered (db=%s)", backupDBPath)

					// Optional config-gated periodic backup. Enable by setting
					// VULOS_BACKUP_INTERVAL (e.g. "1h", "30m"). Disabled by default.
					if iv := os.Getenv("VULOS_BACKUP_INTERVAL"); iv != "" {
						if d, perr := time.ParseDuration(iv); perr == nil && d > 0 {
							if compactor, cerr := sync.BuildCompactor(
								sync.BackupConfig{NodeID: backupNodeID, DBPath: backupDBPath},
								s3c, backupLeaseCfg, clusterPassphrase,
							); cerr == nil {
								go func() {
									ticker := time.NewTicker(d)
									defer ticker.Stop()
									for {
										select {
										case <-ctx.Done():
											return
										case <-ticker.C:
											if err := compactor.Run(ctx); err != nil {
												log.Printf("[backup] periodic backup failed: %v", err)
											} else {
												emitWebhookEvent(webhooksDispatcher, "backup.completed", map[string]any{
													"db":           backupDBPath,
													"kind":         "scheduled",
													"completed_at": time.Now().UTC().Format(time.RFC3339),
												})
											}
										}
									}
								}()
								log.Printf("[backup] periodic backup loop started (interval=%s)", d)
							} else {
								log.Printf("[backup] periodic backup init warning: %v", cerr)
							}
						} else {
							log.Printf("[backup] invalid VULOS_BACKUP_INTERVAL %q: %v", iv, perr)
						}
					}
				}
			}
		} else {
			log.Printf("[cluster] init warning: %v", clusterErr)
		}
	} else {
		log.Printf("[cluster] disabled (VULOS_S3_ACCESS_KEY or VULOS_CLUSTER_PASSPHRASE not set)")
	}

	// App store
	mux.HandleFunc("GET /api/store/catalog", func(w http.ResponseWriter, r *http.Request) {
		entries, err := appStore.Catalog(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, entries)
	})
	mux.HandleFunc("GET /api/store/installed", func(w http.ResponseWriter, r *http.Request) {
		apps, _ := appStore.Installed()
		writeJSON(w, apps)
	})
	mux.HandleFunc("POST /api/store/install", func(w http.ResponseWriter, r *http.Request) {
		// Admin only
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var entry appnet.StoreEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		// ENTITLE-01: a premium app (declares "product") can't be installed by
		// an account that isn't entitled to it, on cloud/os deployments. Open
		// (no-op) on self-host/standalone — SetEntitlementGating(false) there.
		if ok, reason := appGateway.ProductAllowed(r, entry.Product); !ok {
			writeErr(w, http.StatusPaymentRequired, "entitlement required: "+reason)
			return
		}
		if err := appStore.Install(r.Context(), entry); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// BUG FIX (2026-07-12): wire the gateway grants (storage/integration/
		// entitlement) for this app NOW rather than only at next process
		// restart — see wireAppGateway's doc comment. Read the manifest back
		// from disk (not the client-supplied `entry`) so a live install
		// grants exactly what the EXTRACTED app.json declares, matching what
		// a boot-time rescan would find.
		if m, err := appStore.GetManifest(entry.ID); err == nil {
			wireAppGateway(m)
		} else {
			log.Printf("[appstore] installed %q but failed to load its manifest for gateway wiring (will be picked up on next restart): %v", entry.ID, err)
		}
		writeJSON(w, map[string]string{"status": "installed"})
	})
	mux.HandleFunc("POST /api/store/uninstall", func(w http.ResponseWriter, r *http.Request) {
		// Admin only
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID string `json:"app_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		// Stop any running stream session for this app
		streamPool.Stop(req.AppID)
		// Stop any running app process
		launcher.Stop(ctx, req.AppID)
		portPool.Release(req.AppID)
		appGateway.RemoveAppSecret(req.AppID)
		// BUG FIX (2026-07-12): clear storage/integration/entitlement grants
		// too, not just the app secret — otherwise a DIFFERENT app installed
		// later under the same app_id would silently inherit this app's
		// stale grants (e.g. storage access it never declared) until the
		// next process restart.
		appGateway.RemoveAppGrants(req.AppID)
		// Uninstall (removes apt packages for desktop apps + app dir)
		if err := appStore.Uninstall(req.AppID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Rescan desktop entries
		desktopSvc.Scan()
		writeJSON(w, map[string]string{"status": "uninstalled"})
	})

	// Registry — vetted apps with versioned install recipes
	mux.HandleFunc("GET /api/store/registry", func(w http.ResponseWriter, r *http.Request) {
		reg := appStore.Registry()
		entries := reg.ListEntries(appStore.AppDir())
		writeJSON(w, entries)
	})
	mux.HandleFunc("GET /api/store/registry/{appId}", func(w http.ResponseWriter, r *http.Request) {
		reg := appStore.Registry()
		entry, ok := reg.Apps[r.PathValue("appId")]
		if !ok {
			writeErr(w, 404, "app not in registry")
			return
		}
		writeJSON(w, entry)
	})
	mux.HandleFunc("POST /api/store/registry/install", func(w http.ResponseWriter, r *http.Request) {
		// Admin only
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			AppID   string `json:"app_id"`
			Version string `json:"version"` // empty or "latest" = latest
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.AppID == "" {
			writeErr(w, 400, "app_id required")
			return
		}
		if err := appStore.InstallFromRegistry(r.Context(), req.AppID, req.Version); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// BUG FIX (2026-07-12): same live-wiring fix as POST /api/store/install
		// — a registry-installed app's manifest can still declare storage/
		// integration/product permissions and must not wait for a restart.
		if m, err := appStore.GetManifest(req.AppID); err == nil {
			wireAppGateway(m)
		} else {
			log.Printf("[appstore] installed %q from registry but failed to load its manifest for gateway wiring (will be picked up on next restart): %v", req.AppID, err)
		}
		desktopSvc.Scan()
		writeJSON(w, map[string]string{"status": "installed", "app_id": req.AppID})
	})
	mux.HandleFunc("GET /api/store/validate", func(w http.ResponseWriter, r *http.Request) {
		apps, errs := appStore.ValidateInstalled()
		type result struct {
			Valid  int      `json:"valid"`
			Errors []string `json:"errors"`
			Apps   []string `json:"apps"`
		}
		res := result{Valid: len(apps)}
		for _, a := range apps {
			res.Apps = append(res.Apps, a.ID)
		}
		for _, e := range errs {
			res.Errors = append(res.Errors, e.Error())
		}
		writeJSON(w, res)
	})

	// App filesystem persistence — sandboxed read/write under
	// ~/.vulos/<userID>/<appID>/ (APPFS-01: per-user AND per-app scoped).
	appfsBaseDir := datadir.Root()
	appfsSvc := appfs.New(appfsBaseDir)
	// One-time, single-user-only migration of data written under the OLD,
	// un-scoped ~/.vulos/<appID>/ layout (pre-APPFS-01). Only runs when
	// exactly one account exists on this box — see MigrateLegacySingleUser's
	// doc for why an ambiguous multi-user box is left untouched instead.
	if usernames := authStore.ListUsernames(); len(usernames) == 1 {
		if u := authStore.GetUserByUsername(usernames[0]); u != nil {
			if err := appfs.MigrateLegacySingleUser(appfsBaseDir, u.ID); err != nil {
				log.Printf("[appfs] legacy migration: %v", err)
			}
		}
	} else {
		_ = appfs.MigrateLegacySingleUser(appfsBaseDir, "") // marks as handled, no-op
	}
	appfsSvc.Register(mux)

	// TURN credentials (for WebRTC relay in remote mode). Routed through
	// relayconfig.EffectiveTURNConfig() — the SAME admin-store-authoritative,
	// env-fallback resolver /api/peering/ice uses — so this isn't a second,
	// env-only TURN producer split-brained against the admin's turn.json.
	mux.HandleFunc("GET /api/turn/credentials", func(w http.ResponseWriter, r *http.Request) {
		tc := relayconfig.EffectiveTURNConfig()
		if !tc.Enabled {
			writeErr(w, 503, "TURN not configured")
			return
		}
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, tc.GenerateCredentials(userID))
	})

	// Storage status (CLUSTER-06) — reads ~/.vulos/db/storage.json, no creds leaked
	registerStorageRoutes(mux, home)

	// BUNDLE-01: default-everything (batteries-included, opt-out) suite selection.
	// Persists the onboarding email/Workspace choice so the launcher can hide the
	// suite tiles a lean user opted out of. Absent selection ⇒ everything on.
	registerSuiteAppsRoutes(mux, authStore, home)

	// STORE-LOCAL-01: bundle storage-mode selector (central-tigris default vs
	// local-minio-sync opt-in). Coordinated with scripts/install-vulos.sh —
	// when MinIO is installed the installer writes storage.yaml and creates
	// /var/lib/vulos/minio/.minio_secret, and this endpoint flips the bundle
	// to consume it. The default path remains untouched.
	registerStorageModeRoutes(mux, home, authStore)

	// ANCHOR-01: per-account anchor inbox ENTITLEMENT RECORD (~1 GiB), tracked in
	// local SQLite. This creates no bucket and contacts no object store — see the
	// services/anchorinbox package doc; the hosted bucket, if any, is created
	// cloud-side for accounts that connect to a Vulos cloud account. It is
	// therefore unrelated to the storage-mode selector in either direction (it is
	// not moved by choosing local-fs, and it does not send anything to Tigris).
	// Routes: POST /api/anchor-inbox/provision, GET /api/anchor-inbox/status.
	if anchorStore, anchorErr := anchorinbox.Open(filepath.Join(dbDir, "anchorinbox.db")); anchorErr != nil {
		log.Printf("[anchorinbox] store unavailable: %v — endpoints will 500", anchorErr)
	} else {
		anchorinbox.RegisterAnchorHandlers(mux, anchorStore)
		log.Printf("[anchorinbox] registered POST /api/anchor-inbox/provision + GET /api/anchor-inbox/status")
	}

	// Disk usage
	mux.HandleFunc("GET /api/disks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, disks.GetStatus())
	})
	mux.HandleFunc("GET /api/disks/breakdown", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/"
		}
		if err := disks.ValidatePath(path); err != nil {
			writeErr(w, 400, "invalid path: path must be an absolute path under an allowed directory (e.g. /home, /var/data, /srv) without '..' components")
			return
		}
		writeJSON(w, disks.DirBreakdown(r.Context(), path))
	})

	// Drivers — hardware detection & kernel modules
	mux.HandleFunc("GET /api/drivers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, drivers.Detect(r.Context()))
	})
	mux.HandleFunc("POST /api/drivers/load", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Module string `json:"module"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Module == "" {
			writeErr(w, 400, "module required")
			return
		}
		if err := drivers.LoadModule(r.Context(), req.Module); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "loaded", "module": req.Module})
	})
	mux.HandleFunc("POST /api/drivers/unload", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Module string `json:"module"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Module == "" {
			writeErr(w, 400, "module required")
			return
		}
		if err := drivers.UnloadModule(r.Context(), req.Module); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "unloaded", "module": req.Module})
	})

	// Packages — OS package management (apk, apt, etc.)
	mux.HandleFunc("GET /api/packages/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, packages.GetStatus(r.Context()))
	})
	mux.HandleFunc("GET /api/packages/cache", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ready": packages.CacheReady(), "arch": runtime.GOARCH})
	})
	mux.HandleFunc("GET /api/packages/installed", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, packages.ListInstalled(r.Context()))
	})
	mux.HandleFunc("GET /api/packages/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			writeErr(w, 400, "q parameter required")
			return
		}
		writeJSON(w, packages.Search(r.Context(), q))
	})
	mux.HandleFunc("GET /api/packages/info", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			writeErr(w, 400, "name parameter required")
			return
		}
		writeJSON(w, packages.GetInfo(r.Context(), name))
	})
	mux.HandleFunc("POST /api/packages/install", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "name required")
			return
		}
		if err := packages.Install(r.Context(), req.Name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "installed", "name": req.Name})
	})
	mux.HandleFunc("POST /api/packages/remove", func(w http.ResponseWriter, r *http.Request) {
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "name required")
			return
		}
		if err := packages.Remove(r.Context(), req.Name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "removed", "name": req.Name})
	})
	mux.HandleFunc("POST /api/packages/update", func(w http.ResponseWriter, r *http.Request) {
		// SEC: apt-get update modifies package metadata as root — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		execAuditLog(r, "POST /api/packages/update", "apt-get update")
		if err := packages.Update(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "updated"})
	})
	mux.HandleFunc("POST /api/packages/upgrade", func(w http.ResponseWriter, r *http.Request) {
		// SEC: apt-get upgrade -y installs packages system-wide as root — admin only.
		if p, _ := authStore.GetProfile(r.Header.Get("X-User-ID")); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		execAuditLog(r, "POST /api/packages/upgrade", "apt-get upgrade -y")
		output, err := packages.Upgrade(r.Context())
		if err != nil {
			writeErr(w, 500, output)
			return
		}
		writeJSON(w, map[string]string{"status": "upgraded", "output": output})
	})

	// Recovery Kit re-download (admin-only)
	registerKitRoutes(mux, authStore, home)
	// Compliance (data export/audit records). MANAGED-TIER surface — its UI was
	// dropped in the management fold, so a self-host box must not expose orphan
	// endpoints. Off unless VULOS_MANAGED_TIER is set (greenfield 2026-07-23).
	if managedTierEnabled() {
		if complianceStore, err := compliance.OpenStore(dbDir); err != nil {
			log.Printf("[compliance] DISABLED: could not open store: %v", err)
		} else {
			registerComplianceRoutes(mux, complianceStore)
		}
	}
	// Webhooks (owner-gated outbound event delivery). Opened earlier (see the
	// AUTH-12 passkey section above) so webhooksDispatcher is ready in time to
	// be threaded into the real event sites (sign-in, snapshots, instance
	// enroll/remove) registered before this point.
	// CDN (owner-gated edge cache + firewall)
	registerCDNRoutes(mux, authStore, home)
	// Identity service (instance ULID + hostname)
	registerIdentityRoutes(mux, home)
	// Conflict resolver (CLUSTER-10)
	registerConflictRoutes(mux, dataDir, notifySvc)
	// Join codes — cross-device cluster joins via short-codes / QR (INIT-10)
	registerJoinCodeRoutes(mux, home, authStore)
	// Cluster join from a NEW device — validate S3+passphrase, begin sync (INIT-08)
	registerJoinRoutes(mux, home)
	// Persistent notification store + prune endpoint (NOTIF-02)
	registerNotifyPersistRoutes(mux, notifySvc, home)
	// Account security (login/session anomaly feed + emergency lock). acctSecSvc
	// was opened + wired to auth's sensitive-action hook earlier (see the
	// AUTH-12 passkey section above) so it's ready in time to also be threaded
	// into registerPasskeysRoutes.
	registerAccountSecurityRoutes(mux, acctSecSvc, authStore)
	// Support (help requests: records + classifies, no outbound delivery).
	// MANAGED-TIER surface (UI dropped in the fold) — off for self-host unless
	// VULOS_MANAGED_TIER is set (greenfield 2026-07-23).
	if managedTierEnabled() {
		registerSupportRoutes(mux, dbDir, authStore, notifySvc, billingClient)
	}

	// The OS desktop shell (served from "/" below) IS the shell, for both the local
	// and the remote/browser client of the box. There is no separate standalone
	// browser-shell app or /workspace front door.

	// Terminal /api/ handler — a real JSON 404/405 for anything the API routes
	// above did not claim. Registered BEFORE the SPA catch-all so an unmatched
	// API call can never be answered with index.html (200 text/html).
	registerAPIFallbackRoutes(mux)

	// Serve frontend static files (production build)
	webrootDir := ""
	for _, dir := range []string{"/opt/vulos/webroot", "./dist", "../dist", "../../dist"} {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			webrootDir = dir
			break
		}
	}
	if webrootDir != "" {
		fs := http.FileServer(http.Dir(webrootDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// SEC-CSP-01: richer Content-Security-Policy for the OS shell + login
			// HTML (and its same-origin assets) served from here. The baseline
			// `frame-ancestors 'self'` set in secHeadersMiddleware is overridden
			// with the fuller policy below. Notes on the (intentional) looseness:
			//   - script-src keeps 'unsafe-inline'/'unsafe-eval': srcdoc-sandboxed
			//     AI viewport iframes INHERIT the embedder CSP, and they run
			//     arbitrary inline/eval'd generated code (sandboxed to an opaque
			//     origin via the iframe sandbox attr, not via CSP).
			//   - connect-src allows https:/wss: because the shell fails over to
			//     cloud/LAN origins injected at runtime (window.__VULOS_ENDPOINTS__)
			//     that cannot be enumerated here.
			//   - frame-src allows 'self'/blob:/https: for same-origin /app/ apps,
			//     srcdoc/blob viewports, and https sandbox origins.
			// The structural protections — frame-ancestors 'self', object-src
			// 'none', base-uri 'self', form-action 'self' — are the load-bearing
			// hardening and are strict.
			w.Header().Set("Content-Security-Policy", shellCSPFor(appnet.BaseDomain()))
			filePath := filepath.Join(webrootDir, filepath.Clean(r.URL.Path))
			if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(webrootDir, "index.html"))
		})
		log.Printf("serving frontend from %s", webrootDir)
	} else {
		log.Printf("no frontend build found — API only mode (run npm run build)")
	}

	// Wrap mux with subdomain routing — if request comes on {appId}.host, route to gateway
	appHandler := appGateway.Handler()
	mainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PUBWEB anonymous entrypoint (Finding 1): the public-web edge rewrites to
		// this path. Route it to the mux (where PublicHandler is mounted) BEFORE
		// the host-subdomain check, so it works even when the box's own base domain
		// would otherwise capture the request into the authenticated appHandler.
		if strings.HasPrefix(r.URL.Path, appnet.PubwebPathPrefix) {
			mux.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		// Check for app subdomain: {appId}.lvh.me, {appId}.device.ts.net, etc.
		baseDomain := netSvc.Domain()
		if baseDomain != "" && baseDomain != "localhost" && strings.HasSuffix(host, "."+baseDomain) {
			appHandler(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	// Register debug endpoints when running in local mode.
	// These are never compiled out — they are gated purely by the env flag so
	// that a local developer can access them without a rebuild.
	if envDefaults.DebugEndpoints {
		log.Printf("[env] debug endpoints enabled at /debug/env and /debug/pprof/")
		mux.HandleFunc("GET /debug/env", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"env":                      activeEnv.String(),
				"bind_host":                envDefaults.BindHost,
				"skip_hardware_checks":     envDefaults.SkipHardwareChecks,
				"allow_self_signed_certs":  envDefaults.AllowSelfSignedCerts,
				"strict_cookies":           envDefaults.StrictCookies,
				"debug_endpoints":          envDefaults.DebugEndpoints,
				"allow_staging_broker_key": envDefaults.AllowStagingBrokerKey,
			})
		})
	}

	bindHost := envDefaults.BindHost
	addr := bindHost + ":" + cfg.Port
	// SEC: wrap with security headers for all responses served by this process.
	//
	// CSP (clickjacking — SEC-CSP-01): the gateway deletes X-Frame-Options so the
	// shell can embed gateway-proxied apps same-origin, which left every response
	// framable by arbitrary origins. We set `frame-ancestors 'self'` on EVERY
	// response (shell, login, API, and proxied apps): same-origin embedding (the
	// in-shell ProductFrame / /app/ iframes) is still permitted, but no external
	// site can frame a Vulos page. The richer default-src/script-src policy for
	// the shell+login HTML is set on the static "/" handler above (see shellCSP);
	// it is intentionally NOT applied here because this middleware also wraps the
	// gateway, and a strict script-src would break arbitrary proxied third-party
	// apps and srcdoc-sandboxed AI viewports.
	secHeadersMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
			next.ServeHTTP(w, r)
		})
	}
	// OS-ROUTE PROVENANCE (fail-closed when a cloud router sits in front):
	// when VULOS_ROUTER_SECRET is configured this box is behind the CP router, so
	// verify the X-Vulos-OS-Route handoff token the CP stamps. A present-but-forged/
	// expired/wrong-audience token is rejected 403 BEFORE it reaches the auth layer.
	// A self-hosted box has no secret configured → the verifier is disabled and this
	// wrap is a zero-overhead passthrough (the box is directly reachable). See
	// internal/osroute for the (deliberately non-locking) semantics.
	routeVerifier := osroute.VerifierFromEnv()
	if routeVerifier.Enabled() {
		log.Printf("[osroute] X-Vulos-OS-Route verification ENABLED (behind cloud router)")
	}
	// VULOS_APPS=off disables the apps platform (/api/apps) and the MCP
	// endpoint (/mcp) wholesale, as documented in docs/APPS.md. The gate sits
	// ahead of auth so a disabled surface is refused without doing any auth
	// work, and fails closed on an unrecognised value.
	handler := secHeadersMiddleware(appsgate.Middleware(routeVerifier.Middleware(authHandler.Middleware(mainHandler))))
	server := &http.Server{Addr: addr, Handler: handler}

	// lanSvc holds the opt-in OFFLINE-01 LAN reachability service (started below,
	// after TLS cert paths are resolved). Declared here so shutdown can stop it.
	var lanSvc *lan.Service
	// gpuHostSvc holds the opt-in STREAM-BYO-01 GPU streaming-host service
	// (FIX-GPUHOST-WIRE-01). Declared here so shutdown can stop it.
	var gpuHostSvc *gpuhost.Service
	// directSvc holds the opt-in DIRECT-IP public TLS listener (high-performance
	// mode). Declared here so shutdown can stop it.
	var directSvc *directlisten.Service

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		telephonySvc.StopPolling()
		if otaClient != nil {
			otaClient.StopPolling()
		}
		browserSvc.StopAll()
		streamPool.StopAll()
		sandboxSvc.StopAll()
		netSvc.Stop()
		ptySvc.DestroyAll()
		launcher.StopAll(context.Background())
		netMgr.DestroyAll(context.Background())
		if lanSvc != nil {
			lanSvc.Stop(context.Background())
		}
		if gpuHostSvc != nil {
			gpuHostSvc.Stop(context.Background())
		}
		if directSvc != nil {
			directSvc.Stop(context.Background())
		}
		// Force-drain hijacked notification WebSockets: http.Server.Shutdown does
		// not track hijacked conns, so their reader goroutines would linger. This
		// sends a close frame and closes each live WS so those goroutines exit.
		notifySvc.Shutdown()
		peeringHub.Shutdown()
		server.Shutdown(context.Background())
	}()

	// TLS: check for mkcert certs (dev) or production certs
	certPaths := []struct{ cert, key string }{
		{datadir.Join("localhost.pem"), datadir.Join("localhost-key.pem")},
		{"/etc/vulos/tls/cert.pem", "/etc/vulos/tls/key.pem"},
	}
	var tlsCert, tlsKey string
	for _, p := range certPaths {
		if _, err := os.Stat(p.cert); err == nil {
			if _, err := os.Stat(p.key); err == nil {
				tlsCert, tlsKey = p.cert, p.key
				break
			}
		}
	}

	// OFFLINE-01: opt-in box LAN reachability. When enabled, the box advertises
	// vulos.local over mDNS, runs a tiny DNS responder for box.<id>.lan.vulos.org,
	// and serves the OS over HTTPS on the LAN — all without any cloud round-trip,
	// so a co-located client keeps working with the internet/cloud down.
	// Disabled by default (NOT every install wants extra listeners); set
	// VULOS_LAN_ENABLE=1 to turn on. The HTTPS/DNS ports default to the
	// privileged 443/53 and can be overridden for non-root runs.
	if os.Getenv("VULOS_LAN_ENABLE") == "1" {
		lanHost := lan.BoxHostname(cfg.InstanceID)
		// Prefer the LANCERT-01 externally-issued cert at the well-known local
		// path (lan.DefaultCertPath / lan.DefaultKeyPath) — an owner's own ACME
		// client or other cert tooling can drop a cert there. LoadCertSource
		// silently falls back to the self-signed dev cert when the files are not
		// yet present, so the LAN HTTPS path always has TLS material even before
		// a real cert has been provisioned.
		lanCertPath, lanKeyPath := lan.DefaultCertPath, lan.DefaultKeyPath
		if v := os.Getenv("VULOS_LAN_CERT"); v != "" {
			lanCertPath = v
		}
		if v := os.Getenv("VULOS_LAN_KEY"); v != "" {
			lanKeyPath = v
		}
		certSrc := lan.LoadCertSource(lanCertPath, lanKeyPath, []string{"vulos.local", lanHost}, nil)
		httpsAddr := ":443"
		if v := os.Getenv("VULOS_LAN_HTTPS_ADDR"); v != "" {
			httpsAddr = v
		}
		dnsAddr := ":53"
		if v := os.Getenv("VULOS_LAN_DNS_ADDR"); v != "" {
			dnsAddr = v
		}
		// FABRIC-P2P-01: same-LAN peer-to-peer CRDT sync. The fabric handlers
		// (/api/fabric/changeset) are mounted on a LAN-ONLY mux so they ride the
		// LAN HTTPS listener (pinned to the LAN IP) and are never exposed on the
		// public cloud surface. The sync loop discovers sibling boxes over mDNS
		// and exchanges app-registry changesets directly — no cloud / no S3 —
		// so two boxes converge even with the internet down.
		//
		// Gated on a shared fabric secret (VULOS_FABRIC_SECRET). Without it the
		// handlers would have no peer auth, so fabric stays OFF (fail-closed)
		// rather than opening an unauthenticated exchange.
		lanHandler := handler
		var fabricSvc *fabric.Service
		fabricSecret := os.Getenv("VULOS_FABRIC_SECRET")
		if fabricSecret == "" {
			log.Printf("[fabric] disabled: VULOS_FABRIC_SECRET unset (set it on every sibling box to enable same-LAN P2P sync)")
		} else if fabricAppSync == nil {
			log.Printf("[fabric] disabled: app-registry sync (AppSync) unavailable")
		} else {
			// CRDT-QUORUM-01: install this box's PERSISTENT per-instance Ed25519
			// signing key and publish its public key into the registry roster.
			// Uninstall observations this box emits are then SIGNED, and peers
			// verify them against the rostered key — so a single shared-secret
			// holder can only ever validly sign as itself (one distinct verified
			// origin), defeating the multi-forged-origin quorum attack. The key
			// must persist across restarts (peers cached the public key).
			// FABRIC-KEY-01: encrypt the signing key AT REST. The seed is sealed
			// with AES-256-GCM under the OS-keyring root key (VULOS_FABRIC_KEY_HEX,
			// fail-closed in VULOS_ENV=prod). A legacy plaintext key file is
			// migrated in place on first sealed load. If no keyring key is
			// available (dev without the env), SealedKeyFromEnv derives a loud dev
			// key; LoadOrCreateSealedInstanceKey with a nil sealer would fall back
			// to the legacy unencrypted file.
			fabricKeyPath := filepath.Join(dataDir, "fabric_instance_key")
			fabricSealer, sealErr := multiinstance.SealedKeyFromEnv()
			if sealErr != nil {
				log.Printf("[fabric] WARNING: key-at-rest sealing unavailable (%v) — falling back to UNENCRYPTED signing key file", sealErr)
				fabricSealer = nil
			}
			var fabricSigner ed25519.PrivateKey
			if instKey, kerr := multiinstance.LoadOrCreateSealedInstanceKey(fabricKeyPath, fabricSealer); kerr != nil {
				log.Printf("[fabric] WARNING: could not load/create signing key (%v) — uninstall observations from this box will NOT count toward peer quorum", kerr)
			} else if serr := fabricAppSync.SetIdentity(cfg.InstanceID, instKey); serr != nil {
				log.Printf("[fabric] WARNING: could not set signing identity (%v) — uninstall observations from this box will NOT count toward peer quorum", serr)
			} else {
				fabricSigner = instKey
				log.Printf("[fabric] per-instance signing identity active (CRDT-QUORUM-01: signed uninstall observations)")

				// FLEETID-VOUCH-01: let OTHER boxes ask this box to vouch for
				// their break-glass identity recovery. The default policy NEVER
				// auto-approves — an operator must explicitly approve the exact
				// (action, subject, payload, request) tuple via the approve
				// endpoint (admin-gated) before any VouchCert is signed, and the
				// request handler refuses a self-vouch before the policy is even
				// consulted. The request endpoint is peer-facing (approval, not
				// caller auth, is the protection); the approve endpoint is
				// admin-only, matching devicekey.RegisterHandlers' gate.
				vouchPolicy := fleetid.NewManualApprovalPolicy()
				if voucherSvc, verr := fleetid.NewVoucherService(fabricSigner, vouchPolicy); verr != nil {
					log.Printf("[fleetid] WARNING: voucher service unavailable (%v) — this box cannot vouch for peers' break-glass requests", verr)
				} else {
					voucherSvc.RegisterHandlers(mux, func(r *http.Request) bool {
						p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
						return p != nil && p.Role == auth.RoleAdmin
					})
					log.Printf("[fleetid] voucher service registered (request: peer-facing; approve: admin-gated, default-deny policy)")
				}
			}

			// mDNS discoverer: advertise this box and resolve peers. Falls back
			// to a no-peer static discoverer if multicast cannot bind (CI), so
			// the service still constructs and serves inbound exchanges.
			var disc fabric.Discoverer
			if mdisc, derr := fabric.NewMDNSDiscoverer(fabric.MDNSConfig{
				SelfIP:     lan.DetectLANIP(),
				Port:       fabric.PortFromAddr(httpsAddr, 443),
				InstanceID: cfg.InstanceID,
				// Multi-peer (>2 box) discovery: resolve a per-instance qualified
				// name for every peer in the registry roster, so QueryAddr's
				// one-answer-per-name limit doesn't cap us at a single peer under
				// the shared mDNS name. Self is excluded.
				PeerNamesFunc: func() []string {
					return fabricAppSync.PeerInstanceIDs(cfg.InstanceID)
				},
			}); derr != nil {
				log.Printf("[fabric] mDNS discovery unavailable (%v) — inbound exchange still served; no auto peer discovery", derr)
				disc = fabric.NewStaticDiscoverer()
			} else {
				disc = mdisc
			}

			// Rendezvous discovery (FABRIC-WAN-01): mDNS only sees multicast, so
			// until now two of your own boxes in two different houses could never
			// find each other. Pointing VULOS_RENDEZVOUS_URL at any relay running
			// the open rendezvous role closes that — self-hosted relayd, Vulos's,
			// or none at all. Unset, behaviour is exactly as before.
			//
			// It composes with mDNS rather than replacing it: a peer in the same
			// house is still found by multicast with no round trip to anyone, and
			// the same peer moved behind a different NAT is found through the
			// relay. Either source failing does not cost you the other.
			//
			// VULOS_RENDEZVOUS_URL accepts a COMMA-SEPARATED LIST, and every
			// entry becomes its own discoverer merged into the same
			// MultiDiscoverer. That is the substrate spec's shape (KOTVA
			// 4.2.1(3): a home rendezvous set of >= 3 nodes under disjoint
			// operators): MultiDiscoverer skips a source that errors rather
			// than failing the set, so one node being down, seized, or lying
			// by omission costs you nothing as long as another still answers.
			// A single URL — the previous behaviour — is just a one-element
			// list and works exactly as before.
			if rdvURLs := reach.SplitList(os.Getenv("VULOS_RENDEZVOUS_URL")); len(rdvURLs) > 0 {
				if fabricSigner == nil {
					log.Printf("[fabric] rendezvous disabled: no per-instance signing identity — a peer could not be addressed by key")
				} else {
					discoverers := []fabric.Discoverer{disc}
					for _, rdvURL := range rdvURLs {
						rdv := &fabric.RendezvousDiscoverer{
							BaseURL:       rdvURL,
							Key:           fabricSigner,
							SelfEndpoints: fabricSelfEndpoints(httpsAddr),
							PeerKeys:      fabricAppSync.PeerPublicKeys(cfg.InstanceID),
							HTTPClient:    fabric.NewLANClient(10 * time.Second),
						}
						discoverers = append(discoverers, rdv)
						log.Printf("[fabric] rendezvous discovery active via %s (WAN peers; self key %s)", rdvURL, rdv.SelfKey())
					}
					disc = fabric.NewMultiDiscoverer(discoverers...)
					if len(rdvURLs) == 1 {
						log.Printf("[fabric] NOTE: one rendezvous node configured. Listing two or three under " +
							"different operators in VULOS_RENDEZVOUS_URL (comma-separated) removes it as a single point of failure.")
					}
				}
			}

			fs, ferr := fabric.New(fabric.Config{
				InstanceID: cfg.InstanceID,
				Secret:     fabricSecret,
				AppSync:    fabricAppSync,
				Discoverer: disc,
				HTTPClient: fabric.NewLANClient(10 * time.Second),
			})
			if ferr != nil {
				log.Printf("[fabric] disabled: %v", ferr)
			} else {
				fabricSvc = fs
				// Wire local app-registry changes to an immediate sync push: a
				// LocalInstall/LocalUninstall fires this hook, which Nudges the
				// fabric loop so a local change converges without waiting the
				// background tick (FABRIC-P2P-01 / wire-Nudge fix).
				fabricAppSync.SetOnLocalChange(fs.Nudge)
				// Mount fabric routes on a LAN-only mux that delegates everything
				// else to the shared handler.
				fabricMux := http.NewServeMux()
				fs.RegisterHandlers(fabricMux)
				fabricMux.Handle("/", handler)
				lanHandler = fabricMux
				go fs.Run(ctx)
				log.Printf("[fabric] same-LAN P2P sync active (instance=%s, /api/fabric/changeset on LAN listener)", cfg.InstanceID)
			}
		}

		lanCfg := lan.Config{
			InstanceID: cfg.InstanceID,
			CertSource: certSrc,
			Handler:    lanHandler,
			HTTPSAddr:  httpsAddr,
			DNSAddr:    dnsAddr,
		}
		if s, err := lan.New(lanCfg); err != nil {
			log.Printf("[lan] disabled: %v", err)
		} else if err := s.Start(ctx); err != nil {
			log.Printf("[lan] failed to start: %v", err)
		} else {
			lanSvc = s
			log.Printf("[lan] reachable at https://%s and %s (mDNS vulos.local)", lanHost, s.HTTPSAddr())
		}
		_ = fabricSvc

		// FIX-LANCERT-PULL-01: opt-in cloud LAN-cert puller. When enabled, this
		// background goroutine reports the box's LAN IP to the cloud control-plane
		// and pulls the ACME DNS-01 cert+key into lanCertPath/lanKeyPath — exactly
		// the paths LoadCertSource above mtime-watches, so a renewal is picked up
		// on the next handshake with no listener restart.
		if lan.PullerEnabled() {
			// SECURITY (audit P0-2): the puller refuses a plaintext CloudBaseURL
			// and pins the control-plane TLS chain. Pins are sourced from
			// VULOS_LANCERT_CA_PEM / VULOS_LANCERT_CA_FILE (CA bundle) and/or
			// VULOS_LANCERT_SPKI_PINS (SPKI pins) inside NewLANCertPuller. A
			// plaintext base URL is rejected unless VULOS_LANCERT_ALLOW_INSECURE=1
			// (local dev only).
			puller, err := lan.NewLANCertPuller(lan.PullerConfig{
				CloudBaseURL: os.Getenv("VULOS_CLOUD_BASE_URL"),
				SharedSecret: os.Getenv("CP_SHARED_SECRET"),
				BoxID:        cfg.InstanceID,
				CertPath:     lanCertPath,
				KeyPath:      lanKeyPath,
			})
			if err != nil {
				log.Printf("[lancert-puller] disabled: %v", err)
			} else {
				go puller.Run(ctx)
			}
		}
	}

	// DIRECT_IP_WIRE BEGIN — DIRECT-IP high-performance mode.
	//
	// When the box has a REACHABLE public endpoint (static IP / public hostname)
	// and the operator opts in (VULOS_DIRECT_ENABLE=1), serve the OS on a PUBLIC
	// TLS listener so clients can reach it DIRECTLY (near-native latency + full
	// bandwidth) instead of always tunneling through the relay. Clients try direct
	// first and fall back to the relay tunnel on failure (negotiated relay-side).
	//
	// SECURITY: the direct listener serves the EXACT SAME `handler` as the main /
	// relay-fronted path — secHeadersMiddleware(authHandler.Middleware(mainHandler))
	// — so the full auth/session/authz stack, CSRF checks, and security headers
	// apply identically. Direct is a faster TRANSPORT, NOT a security downgrade: an
	// unauthenticated request gets the same 401 it would through the relay. TLS is
	// required (ACME/Let's-Encrypt for a hostname, or an operator-provided cert).
	//
	// OFF BY DEFAULT: most boxes are NAT'd/CGNAT and stay purely on the relay path.
	// The box proves it controls the advertised endpoint by serving the relay's
	// probe path on this listener (ownership proof); the relay re-verifies it over
	// the internet before surfacing it to any client, so a box cannot advertise an
	// endpoint it does not actually serve.
	{
		// SEC: honor the LAN-only network mode — never bind a public listener when
		// the operator has set connection mode to "local" (external listeners
		// blocked), even if VULOS_DIRECT_ENABLE is set.
		directEnv := directlisten.FromEnv(datadir.Join("auth", "direct-acme"))
		if directEnv.Enabled && netSvc.ExternalListenerBlocked() {
			log.Printf("[direct] disabled: network mode is LAN-only (external listeners blocked) — set connection mode to fabric/direct/own to use the direct fast path")
		} else if directEnv.Enabled {
			ds, derr := directlisten.New(directEnv.BuildConfig(handler))
			if derr != nil {
				log.Printf("[direct] disabled: %v", derr)
			} else if err := ds.Start(ctx); err != nil {
				log.Printf("[direct] failed to start public listener: %v", err)
			} else {
				directSvc = ds
				log.Printf("[direct] public listener up on %s (endpoint=%s)", ds.Addr(), ds.Endpoint())
				// Advertise the direct endpoint to a co-located relay agent via the
				// env seam it reads (VULOS_RELAY_DIRECT_ENDPOINT). The relay agent
				// hands it to the relay in its Register frame; the relay verifies it
				// (reachable + ownership-proven) before surfacing it to clients. The
				// OS does not embed the relay agent, so this env is the box→agent seam.
				os.Setenv("VULOS_RELAY_DIRECT_ENDPOINT", ds.Endpoint())
				// Background self-reachability pre-check: confirm the endpoint answers
				// from outside before relying on it. This only ever probes the box's
				// OWN endpoint (never a caller-supplied URL). A failure is logged, not
				// fatal — the relay's authoritative probe still gates advertisement,
				// and clients fall back to the relay if direct is unreachable.
				go func(ep string) {
					pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer pcancel()
					if err := ds.CheckReachable(pctx, false); err != nil {
						log.Printf("[direct] self-reachability check for %s did not pass yet (%v) — the relay will re-verify; clients fall back to the relay until it does", ep, err)
					} else {
						log.Printf("[direct] self-reachability check passed: %s is reachable and ownership-provable", ep)
					}
				}(ds.Endpoint())
				// Expose the direct endpoint on a session-authed status route so the
				// shell/UX can show "direct fast-path active".
				mux.HandleFunc("GET /api/network/direct", func(w http.ResponseWriter, r *http.Request) {
					writeJSON(w, map[string]any{
						"enabled":  true,
						"endpoint": ds.Endpoint(),
						"addr":     ds.Addr(),
					})
				})
			}
		} else {
			// Not enabled: a well-defined status route so the UI renders the toggle.
			mux.HandleFunc("GET /api/network/direct", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, map[string]any{"enabled": false})
			})
		}
	}
	// DIRECT_IP_WIRE END

	// REACH_WIRE BEGIN — Vulos's OWN reverse tunnel (docs/REACH.md).
	//
	// Brings up one embedded tunnel agent per configured relay, each serving
	// this same `handler`. Ordering matters: this runs AFTER the direct
	// listener so that, when direct mode is up, its verified public origin is
	// offered to the relays — a relay that accepts it advertises it and
	// clients bypass the tunnel entirely, falling back to it on failure.
	//
	// A box with no relay endpoints configured gets a no-op agent and a
	// status route reporting exactly that.
	var reachDirectEndpoint string
	if directSvc != nil {
		reachDirectEndpoint = directSvc.Endpoint()
	}
	reachRT := startReachAgent(ctx, mux, handler, reachDirectEndpoint, Version)
	defer reachRT.Close()

	// GPUHOST_WIRE BEGIN — FIX-GPUHOST-WIRE-01
	//
	// On a box opted-in via VULOS_GPU_HOST, start the BYO GPU streaming-host
	// service (STREAM-BYO-01). The service runs the external WebRTC+NVENC
	// streamer under a supervisor and registers the box with the relay's
	// fabric so a remote client can discover + dial it.
	//
	// The fabric identity is the OS's existing peering identity: there is
	// exactly one Ed25519 keypair per box across all slices (peering / LAN
	// reachability / gpuhost), so the relay sees the same VulaID we already
	// advertise on the peering well-known endpoint.
	//
	// No-op when VULOS_GPU_HOST is unset; most boxes have no GPU and never
	// instantiate the service.
	if gpuhost.Enabled() {
		identity := gpuhost.FabricIdentity{
			HostID:       peeringSvc.VulaID(),
			PublicKeyB64: base64.StdEncoding.EncodeToString(peeringSvc.PublicKey()),
			Domain:       cfg.Domain,
		}
		gpuCfg := gpuhost.Config{
			Enabled:           true,
			Identity:          identity,
			RelayBaseURL:      os.Getenv("VULOS_RELAY_BASE_URL"),
			StreamerBinary:    os.Getenv("VULOS_STREAMER_BINARY"),
			AdvertiseHostname: os.Getenv("VULOS_GPU_ADVERTISE_HOST"),
			GPUVendor:         os.Getenv("VULOS_GPU_VENDOR"),
		}
		if svc, err := gpuhost.New(gpuCfg); err != nil {
			log.Printf("[gpuhost] disabled: %v", err)
		} else if err := svc.Start(ctx); err != nil {
			log.Printf("[gpuhost] start failed: %v", err)
		} else {
			gpuHostSvc = svc
			log.Printf("[gpuhost] streaming host registered (host_id=%s state=%s)",
				identity.HostID, svc.State())
			mux.Handle("/api/gpuhost/status", svc.StatusHandler())
		}
	}
	// GPUHOST_WIRE END

	// NOTE: First-party Vulos Meet is retired — comms (video/chat) are 3rd-party
	// app-store apps (Matrix/Element, Jitsi) reached as external services. The
	// former Meet-SFU host registry (internal/meethost + VULOS_SFU_HOST), the
	// self-host LiveKit join-token minter (/api/meet/token) and the per-room
	// Whisper transcription endpoints (/api/meet/transcribe/*) were removed with
	// that pivot. The sovereign P2P Messages builtin keeps its own in-process
	// Pion SFU (registered above at /api/sfu/*) for peer group calls.

	if tlsCert != "" {
		log.Printf("vulos server listening on %s with TLS (env=%s, cert=%s)", addr, activeEnv, tlsCert)
		if err := server.ListenAndServeTLS(tlsCert, tlsKey); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	} else {
		log.Printf("vulos server listening on %s (env=%s, no TLS)", addr, activeEnv)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}

// detectNativeMode checks if we're running on baremetal (sole fullscreen Cog/WPE)
// or under a Wayland compositor that supports multiple windows.
// Fast: just checks env vars and compositor presence, no subprocess.
//
// D93 window model: v2 opt-in (v2Enabled=true) is required before this function
// can return "native". Without the opt-in the result is always "baremetal",
// ensuring v1 bare-metal boots never activate the native-launch / labwc path.
func detectNativeMode(v2Enabled bool) string {
	// v1 default: skip compositor detection entirely and stay on the cage/stream path.
	if !v2Enabled {
		return "baremetal"
	}
	// Not on device at all (Docker dev, remote browser access)
	ua := os.Getenv("WPE_USER_AGENT")
	waylandDisplay := os.Getenv("WAYLAND_DISPLAY")
	xdgSession := os.Getenv("XDG_SESSION_TYPE")

	// Check if we're in a Wayland session with a compositor
	if waylandDisplay != "" {
		// We have a Wayland display — check if a multi-window compositor is running
		// Known compositors: sway, labwc, weston, cage (cage is single-window like baremetal)
		compositor := os.Getenv("XDG_CURRENT_DESKTOP")
		sessionDesktop := os.Getenv("DESKTOP_SESSION")

		// cage = single-app kiosk compositor (baremetal equivalent)
		if compositor == "cage" || sessionDesktop == "cage" {
			return "baremetal"
		}

		// Check for common multi-window compositors via their sockets/runtime
		// If WAYLAND_DISPLAY is set and it's not cage, we likely have multi-window support
		// Also verify by checking if wlr-foreign-toplevel or similar protocols are available
		// Quick check: if any known compositor name is in env
		for _, c := range []string{"sway", "labwc", "weston", "wayfire", "hyprland", "river"} {
			if strings.Contains(strings.ToLower(compositor), c) ||
				strings.Contains(strings.ToLower(sessionDesktop), c) {
				return "native"
			}
		}

		// WAYLAND_DISPLAY present but unknown compositor — try to detect via process list
		// This is still fast (single /proc read)
		if data, err := exec.Command("sh", "-c", "ps -eo comm 2>/dev/null | head -50").Output(); err == nil {
			ps := strings.ToLower(string(data))
			for _, c := range []string{"sway", "labwc", "weston", "wayfire", "hyprland"} {
				if strings.Contains(ps, c) {
					return "native"
				}
			}
		}

		// WAYLAND_DISPLAY set but can't identify compositor — assume multi-window capable
		// since baremetal typically uses cage or direct Cog without WAYLAND_DISPLAY gymnastics
		return "native"
	}

	// X11 session (unlikely on this project but handle it)
	if xdgSession == "x11" || os.Getenv("DISPLAY") != "" {
		return "native"
	}

	// No display server detected — or WPE is running as sole renderer.
	// ua (WPE_USER_AGENT) is not needed for mode detection; suppress the
	// unused-variable error without keeping a confusing blank identifier.
	_ = ua // reserved for future user-agent-based mode hinting
	return "baremetal"
}

// execDisabled returns true when the operator has set VULOS_DISABLE_EXEC to any
// non-empty value, disabling all privileged exec endpoints at runtime.
func execDisabled() bool {
	return os.Getenv("VULOS_DISABLE_EXEC") != ""
}

// validIfaceName validates that an ethernet/wifi interface name contains only
// safe characters (alphanumerics, dash, underscore, dot; max 15 chars per
// IFNAMSIZ). This prevents argument injection into ip/dhcpcd subcommands.
func validIfaceName(iface string) bool {
	if iface == "" || len(iface) > 15 {
		return false
	}
	for _, c := range iface {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// validDisplayOutput validates a wlr-randr/xrandr output name (e.g. "HDMI-A-1",
// "eDP-1", "DP-2"). Only alphanumerics, dash, and dot are accepted (max 32 chars).
func validDisplayOutput(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// validDisplayMode validates a resolution/mode string (e.g. "1920x1080",
// "1920x1080@60", "1280x720@60.00"). Accepted form: WxH or WxH@R.
func validDisplayMode(mode string) bool {
	if mode == "" || len(mode) > 24 {
		return false
	}
	for _, c := range mode {
		if !((c >= '0' && c <= '9') || c == 'x' || c == '@' || c == '.') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// defaultLegalDirs is where licence notices / the GPL source offer are looked
// for, in order: the location build.sh writes them to in a real image, then the
// repo tree for local development.
var defaultLegalDirs = []string{"/opt/vulos/legal", ".", "..", "../..", "/opt/vulos"}

// legalDocHandler serves the first of `names` found under one of `dirs`, as
// inline UTF-8 text. It backs Settings → About (GET /api/system/licenses and
// /api/system/written-offer): the OS must be able to show the third-party
// notices it is obliged to carry, and the GPL/LGPL written offer.
func legalDocHandler(dirs []string, names ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, d := range dirs {
			for _, n := range names {
				p := filepath.Join(d, n)
				if info, err := os.Stat(p); err == nil && !info.IsDir() {
					// text/plain so the browser renders it inline rather than
					// offering to download a .md file; nosniff so it stays text.
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					w.Header().Set("X-Content-Type-Options", "nosniff")
					http.ServeFile(w, r, p)
					return
				}
			}
		}
		http.Error(w, "licence notices not found on this system", http.StatusNotFound)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// fabricSelfEndpoints reports the URLs at which this box is reachable, most
// specific first, for a rendezvous announcement.
//
// A relay-tunnel URL is listed ahead of the LAN address when one is configured:
// a peer resolving us over the WAN cannot use our RFC1918 address, and the
// tunnel is the only endpoint that works from outside. The LAN address is kept
// as a second entry because it is strictly better when the peer turns out to be
// local, and the relay does not dial either — it just repeats what we claim.
func fabricSelfEndpoints(httpsAddr string) []string {
	var out []string
	if pub := strings.TrimSpace(os.Getenv("VULOS_PUBLIC_URL")); pub != "" {
		out = append(out, strings.TrimRight(pub, "/"))
	}
	if ip := lan.DetectLANIP(); ip != nil {
		out = append(out, fmt.Sprintf("https://%s:%d", ip.String(), fabric.PortFromAddr(httpsAddr, 443)))
	}
	return out
}
