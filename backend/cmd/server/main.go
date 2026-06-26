package main

import (
	"context"
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

	"vulos/backend/internal/airouter"
	"vulos/backend/internal/config"
	"vulos/backend/internal/cpbilling"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/gpuhost"
	"vulos/backend/internal/lan"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/storage"
	"vulos/backend/services/ai"
	"vulos/backend/services/anchorinbox"
	"vulos/backend/services/appfs"
	"vulos/backend/services/appnet"
	"vulos/backend/services/audio"
	"vulos/backend/services/auth"
	"vulos/backend/services/authvault"
	"vulos/backend/services/bluetooth"
	"vulos/backend/services/bootmode"
	"vulos/backend/services/cloudenroll"
	"vulos/backend/services/cluster"
	"vulos/backend/services/credvault"
	"vulos/backend/services/desktop"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/disks"
	"vulos/backend/services/display"
	"vulos/backend/services/drivers"
	"vulos/backend/services/embeddings"
	"vulos/backend/services/energy"
	vulenv "vulos/backend/services/env"
	"vulos/backend/services/gateway"
	"vulos/backend/services/gpu"
	"vulos/backend/services/installer"
	"vulos/backend/services/integrations"
	"vulos/backend/services/lease"
	"vulos/backend/services/network"
	"vulos/backend/services/notify"
	"vulos/backend/services/osdist"
	"vulos/backend/services/packages"
	"vulos/backend/services/passkeys"
	"vulos/backend/services/peering"
	"vulos/backend/services/peering/sfu"
	bprofiles "vulos/backend/services/profiles"
	ptyservice "vulos/backend/services/pty"
	"vulos/backend/services/recall"
	"vulos/backend/services/sandbox"
	"vulos/backend/services/signing"
	"vulos/backend/services/storageprov"
	"vulos/backend/services/stream"
	"vulos/backend/services/sync"
	"vulos/backend/services/sysuser"
	"vulos/backend/services/telemetry"
	"vulos/backend/services/vault"
	"vulos/backend/services/webproxy"
	"vulos/backend/services/wifi"
	"vulos/backend/services/wine"
	"vulos/backend/services/wltoplevel"

	"vulos/backend/internal/obs"
)

// Version is the server version string. The default is "dev"; it is overridden
// at build time via -ldflags "-X main.Version=vX.Y.Z" in the release pipeline.
var Version = "dev"

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

	// Ensure system state directory exists
	os.MkdirAll("/var/lib/vulos", 0755)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Data directory
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".vulos", "data")
	os.MkdirAll(dataDir, 0755)
	dbDir := filepath.Join(home, ".vulos", "db")
	os.MkdirAll(dbDir, 0755)

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

	// App store
	appsDir := filepath.Join(home, ".vulos", "apps")
	appStore := appnet.NewAppStore(appsDir)

	// App visibility store (private|local|public per app)
	visStore, err := appnet.NewVisibilityStoreAt(filepath.Join(dbDir, "visibility.json"))
	if err != nil {
		log.Printf("[visibility] init warning: %v", err)
	}

	// TURN server (WebRTC relay for remote mode)
	turnCfg := network.LoadTURNConfig()
	if turnCfg.Enabled {
		if cmd, err := turnCfg.StartCoturn(ctx, filepath.Join(home, ".vulos", "tunnel")); err != nil {
			log.Printf("[turn] start warning: %v", err)
		} else {
			go func() { cmd.Wait(); log.Printf("[turn] coturn exited") }()
		}
	}

	// Sandbox (AI-generated Python scripts)
	sandboxSvc := sandbox.New(filepath.Join(home, ".vulos"))

	// Browser profiles (isolated cookie jars / contexts)
	browserProfiles := bprofiles.NewStore(filepath.Join(home, ".vulos", "db"))

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
	wineSvc := wine.New(filepath.Join(home, ".vulos", "wine"))

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
	appGateway.SetIntegrationTokenFunc(func(ctx context.Context, provider string) (string, error) {
		tok, err := integrationsClient.MintToken(ctx, provider)
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

	// C1/C3: when an STS endpoint is configured, mint SHORT-LIVED, PREFIX-SCOPED
	// credentials per app instead of handing out the box's long-lived full-bucket
	// creds. Falls back to static per-user-bucket creds when STS is unavailable.
	if stsEndpoint := os.Getenv("VULOS_STORAGE_STS_ENDPOINT"); stsEndpoint != "" {
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
		log.Printf("[storage] STS not configured (VULOS_STORAGE_STS_ENDPOINT unset) — apps receive static per-user-bucket creds; cross-app isolation within a user relies on gateway-mediated per-user/app prefixes")
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
	if manifests, err := appnet.ScanApps(appsDir); err == nil {
		for _, m := range manifests {
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

	// Peering: well-known identity endpoint + peer profile fetch (PEER-12).
	peering.RegisterWellKnownHandlers(mux)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /metrics", obs.Handler())

	// Version — public, no auth. Returns the server build version.
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": Version})
	})

	// NET-07: cluster health (data-dir writable, disk space, sync lag) — public
	// syncer is wired below after cluster init; use a pointer so the handler
	// always reads the current value. nilSyncer is replaced once cluster is ready.
	var clusterSyncer *sync.Syncer
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handleClusterHealth(filepath.Join(home, ".vulos"), clusterSyncer)(w, r)
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

	// Credential vault HTTP API (password manager, per-user, AES-256-GCM)
	credVaultHandler := credvault.NewHandler(func(userID string) string {
		return filepath.Join(home, ".vulos", "auth", "vault", userID)
	})
	credVaultHandler.RegisterHandlers(mux)

	// Mail: URL of the embedded LilMail service (built-in Mail app).
	registerMailRoutes(mux)

	// SSH key management (host key + authorized_keys)
	registerSSHKeyRoutes(mux, authStore, home)

	// AUTH-12: server-side passkey (FIDO2/WebAuthn) authenticator. Credentials
	// are sealed at rest per-user via the device KeyStore (TPM or software).
	// streamVerifier is set here when passkeys are available; used below for
	// AUTH-13 (input-injection re-auth gate).
	var streamVerifier *passkeys.StreamVerifier
	deviceKS, deviceKSErr := devicekey.Open(filepath.Join(home, ".vulos", "auth", "tpm"))
	if deviceKSErr != nil {
		log.Printf("passkeys: devicekey unavailable, server-side passkeys disabled: %v", deviceKSErr)
	} else {
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
		// enrollment, present its CA-signed device cert on mint (preferred over the
		// TOFU device-key path — no first-use race).
		enroller := cloudenroll.New(os.Getenv("VULOS_CLOUD_URL"), filepath.Join(home, ".vulos", "auth", "integrations"), deviceKS)
		if ident, err := enroller.Load(); err != nil {
			log.Printf("[integrations] cloud enrollment load: %v", err)
		} else if ident != nil {
			integrationsClient.SetCertProvider(ident)
			log.Printf("[integrations] using owner-attested device cert (ulid=%s)", ident.ULID)
		}

		passkeysSvc := passkeys.New(filepath.Join(home, ".vulos", "auth", "passkeys"), deviceKS)
		// TASK-2 (P0): RP ID prod safety — reject insecure defaults in prod.
		if activeEnv.IsProd() {
			if err := passkeysSvc.ValidateConfig(); err != nil {
				log.Fatalf("[passkeys] %v", err)
			}
		}
		registerPasskeysRoutes(mux, passkeysSvc, authStore)
		// LOGINISO-01: promote WebAuthn from re-auth gate to full login flow.
		// LOGINISO-02: QR / phone-approval kiosk login.
		loginSvc := passkeys.NewLoginService(passkeysSvc, authStore)
		qrSvc := passkeys.NewQRLoginService(authStore)
		registerPasskeyLoginRoutes(mux, loginSvc, qrSvc)
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

	// Notifications
	mux.Handle("/api/notifications/stream", notifySvc.Handler())
	mux.HandleFunc("GET /api/notifications", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, notifySvc.List(50))
	})
	mux.HandleFunc("GET /api/notifications/unread", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]int{"unread": notifySvc.UnreadCount()})
	})
	mux.HandleFunc("POST /api/notifications/read", func(w http.ResponseWriter, r *http.Request) {
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
		notifySvc.Clear()
		writeJSON(w, map[string]string{"status": "cleared"})
	})
	registerNotifyExtRoutes(mux, notifySvc, home) // NOTIF-05+06: DND + inline actions

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
		writeJSON(w, v.Status())
	})
	mux.HandleFunc("POST /api/vault/backup", func(w http.ResponseWriter, r *http.Request) {
		if err := v.Backup(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/vault/snapshots", func(w http.ResponseWriter, r *http.Request) {
		snaps, err := v.Snapshots(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, snaps)
	})

	mux.HandleFunc("GET /api/vault/sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, v.SyncStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/vault/sync", func(w http.ResponseWriter, r *http.Request) {
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
		if r.Header.Get("X-Burst-Secret") != burstSecret {
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
		manifestWorkDir := manifest.WorkDir
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
		// Merge manifest env (authoritative) with caller-supplied extra env
		launchEnv := manifest.EnvSlice()
		launchEnv = append(launchEnv, req.Env...)
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
		app, err := launcher.Launch(ctx, req.AppID, userID, hostPort, manifestAppPort, manifestCmd, req.Args, manifestWorkDir, launchEnv)
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
	registerTURNRoutes(mux, turnStore)
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

	// AUTH-13: WebAuthn re-auth gate for input-injection sessions.
	// streamVerifier is non-nil when passkeys are available (set above in the
	// AUTH-12 block); nil when devicekey is unavailable (e.g. CI or containers
	// without TPM). In the nil case the stub verifier rejects all assertions
	// (safe failure mode: input injection cannot be unlocked). Prod requires
	// passkeys to be available or the gate is permanently locked, so surface a
	// clear log rather than crash — the operator can disable input injection
	// if passkeys are intentionally not deployed.
	if streamPool.WebAuthnVerifier() == nil {
		if activeEnv.IsProd() {
			log.Printf("[stream/webauthn] WARNING: devicekey unavailable in prod — input-injection re-auth permanently gated (stub rejects all assertions). Set up a TPM or software keystore to enable passkey-gated input injection.")
		} else {
			log.Printf("[stream/webauthn] WARNING: no WebAuthn verifier wired — input injection will remain gated (stub rejects all assertions). Acceptable in dev/local.")
		}
	}
	registerStreamWebAuthnRoutes(mux, streamPool, authStore, streamVerifier)

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

		// GAME-07: auto-detect gaming mode.
		// Check manifest category first (if app_id provided and manifest exists).
		g07Gaming := wine.IsGamingCommand(req.Command)
		if !g07Gaming && req.AppID != "" {
			manifestPath := filepath.Join(appsDir, req.AppID, "app.json")
			if m, err := appnet.LoadManifest(manifestPath); err == nil {
				g07Gaming = (m.Category == "gaming")
			}
		}

		// BILLING GATE (surface 1: GPU/stream). A stream session is a billable
		// GPU/compute surface. Enforces: gpu_enabled, suspended, and the
		// gpu_session_cap concurrent-session limit (tracked locally via the stream
		// pool — no cp round-trip for the live count). Fail-open/degraded on a
		// cold cp outage. No-op when billing is disabled (standalone OS).
		gpuAccount := r.Header.Get("X-User-ID")
		if billingClient.Enabled() {
			activeSessions := len(streamPool.List())
			if d := billingClient.GateGPU(r.Context(), gpuAccount, activeSessions); !d.Allowed {
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
			peering.StartPeerProfileSync(ctx, func() []peering.WKApprovedPeer {
				contacts := contactStore.ListByState(peering.StateApproved)
				peers := make([]peering.WKApprovedPeer, 0, len(contacts))
				for _, c := range contacts {
					peers = append(peers, peering.WKApprovedPeer{VulaID: c.VulaID, ServerAddr: c.Server})
				}
				return peers
			})

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
			}

			// Feeds (own append-only feeds; peers-gating uses contacts).
			if feedStore, fErr := peering.NewFeedStore(pRoot, pPriv, pVulaID, contactStore); fErr != nil {
				log.Printf("[peering] PEER-42 feed store init: %v", fErr)
			} else {
				peering.RegisterFeedHandlers(peeringMux, feedStore, callerVulaID)
			}

			// Drop (LAN mDNS file drop). media nil → drop send returns a
			// graceful error (drop.go handles nil media sender).
			dropSvc := peering.NewDropService(pVulaID, "", contactStore, nil)
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
		peering.RegisterProfileHandlers(peeringMux, filepath.Join(pRoot, "profile"), pVulaID, profileContacts)

		// Email verification (initiate/confirm/status against vulos.org).
		if vfySvc, vErr := peering.VerifyNewService(filepath.Join(pRoot, "identity")); vErr != nil {
			log.Printf("[peering] PEER-42 verify service init: %v", vErr)
		} else {
			peering.RegisterVerifyHandlers(peeringMux, vfySvc)
		}

		// Directory discovery (lookup/search via vulos.org).
		peering.RegisterDiscoveryHandlers(peeringMux, peering.DiscoveryNewService(nil))

		// ICE / TURN config for WebRTC.
		peering.RegisterICEHandlers(peeringMux)

		// Endpoint registry (multi-address routing).
		peering.RegisterEndpointHandlers(peeringMux)

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
		if collabStore, cErr := peering.NewCollabStore(filepath.Join(pRoot, "collab")); cErr != nil {
			log.Printf("[peering] PEER-42 collab store init: %v", cErr)
		} else {
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
				peering.NewShareStore(),
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

	// App visibility (private|local|public)
	appnet.RegisterVisibilityHandlers(mux, appStore, visStore)

	// New-feature routes: airouter, identity, multiinstance, appnet subdomain
	// provisioning, recovery handlers, cloud-sync, edge-cache.
	// Must be called AFTER RegisterVisibilityHandlers.
	fabricAppSync := registerNewFeatureRoutes(mux, newFeatureDeps{
		dbDir:              dbDir,
		netMgr:             netMgr,
		visStore:           visStore,
		authStore:          authStore,
		integrationsClient: integrationsClient,
	}, ctx)

	// MinIO storage provisioning
	storageprov.RegisterHandlers(mux, home)

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
	aiAppsDir := filepath.Join(home, ".vulos", "ai-apps")
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
			req.Title = "Vula"
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
	installer.RegisterHandlers(mux, installer.NewWithAdminGate(installerIsAdmin))

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

	// OS update status + apply (OSDIST-04 + OSDIST-05)
	if osdistSlotMgr, osdistErr := osdist.NewSlotManager(filepath.Join(home, ".vulos", "os-cache")); osdistErr == nil {
		osdistStore := osdist.NewStatusStore()
		osdistIsAdmin := func(r *http.Request) bool {
			p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
			return p != nil && p.Role == auth.RoleAdmin
		}
		osdist.NewUpdateHandlers(osdistSlotMgr, os.Getenv("VULOS_OS_VERSION"), osdistStore, osdistIsAdmin).RegisterHandlers(mux)

		// OSDIST-04: start the 4-hour background update-fetch loop.
		// Trust anchor is optional — skip updater when key is absent (dev/CI).
		if anchorPub, anchorErr := signing.LoadAnchor(signing.DefaultAnchorPath); anchorErr == nil {
			osdistSrc := osdist.NewSource()
			updaterCfg := osdist.UpdaterConfig{
				Source:         osdistSrc,
				SlotManager:    osdistSlotMgr,
				AnchorPub:      anchorPub,
				RunningVersion: os.Getenv("VULOS_OS_VERSION"),
			}
			if updater, updErr := osdist.NewUpdater(updaterCfg); updErr == nil {
				go updater.Run(ctx)
				log.Printf("[osdist] update loop started (version=%s)", os.Getenv("VULOS_OS_VERSION"))
			} else {
				log.Printf("[osdist] updater init warning: %v", updErr)
			}
		} else {
			log.Printf("[osdist] trust anchor not found (%v) — update loop disabled", anchorErr)
		}
	} else {
		log.Printf("[osdist] slot manager init warning: %v", osdistErr)
	}

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
					registerBackupRoutes(mux, clusterBackupDeps(authStore, s3c, backupLeaseCfg, backupNodeID, backupDBPath))
					log.Printf("[backup] admin backup/restore endpoints registered (db=%s)", backupDBPath)

					// Optional config-gated periodic backup. Enable by setting
					// VULOS_BACKUP_INTERVAL (e.g. "1h", "30m"). Disabled by default.
					if iv := os.Getenv("VULOS_BACKUP_INTERVAL"); iv != "" {
						if d, perr := time.ParseDuration(iv); perr == nil && d > 0 {
							if compactor, cerr := sync.BuildCompactor(
								sync.BackupConfig{NodeID: backupNodeID, DBPath: backupDBPath},
								s3c, backupLeaseCfg,
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
		if err := appStore.Install(r.Context(), entry); err != nil {
			writeErr(w, 500, err.Error())
			return
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

	// App filesystem persistence — sandboxed read/write under ~/.vulos/<app>/
	appfsBaseDir := filepath.Join(home, ".vulos")
	appfsSvc := appfs.New(appfsBaseDir)
	appfsSvc.Register(mux)

	// TURN credentials (for WebRTC relay in remote mode)
	mux.HandleFunc("GET /api/turn/credentials", func(w http.ResponseWriter, r *http.Request) {
		if !turnCfg.Enabled {
			writeErr(w, 503, "TURN not configured")
			return
		}
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, turnCfg.GenerateCredentials(userID))
	})

	// Storage status (CLUSTER-06) — reads ~/.vulos/db/storage.json, no creds leaked
	registerStorageRoutes(mux, home)

	// STORE-LOCAL-01: bundle storage-mode selector (central-tigris default vs
	// local-minio-sync opt-in). Coordinated with scripts/install-vulos.sh —
	// when MinIO is installed the installer writes storage.yaml and creates
	// /var/lib/vulos/minio/.minio_secret, and this endpoint flips the bundle
	// to consume it. The default path remains untouched.
	registerStorageModeRoutes(mux, home, authStore)

	// ANCHOR-01: per-account anchor inbox provisioning (always-on ~1 GiB Tigris
	// inbox). Routes: POST /api/anchor-inbox/provision, GET /api/anchor-inbox/status.
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
	secHeadersMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
	handler := secHeadersMiddleware(authHandler.Middleware(mainHandler))
	server := &http.Server{Addr: addr, Handler: handler}

	// lanSvc holds the opt-in OFFLINE-01 LAN reachability service (started below,
	// after TLS cert paths are resolved). Declared here so shutdown can stop it.
	var lanSvc *lan.Service
	// gpuHostSvc holds the opt-in STREAM-BYO-01 GPU streaming-host service
	// (FIX-GPUHOST-WIRE-01). Declared here so shutdown can stop it.
	var gpuHostSvc *gpuhost.Service

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
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
		server.Shutdown(context.Background())
	}()

	// TLS: check for mkcert certs (dev) or production certs
	certPaths := []struct{ cert, key string }{
		{filepath.Join(home, ".vulos", "localhost.pem"), filepath.Join(home, ".vulos", "localhost-key.pem")},
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
		// Prefer the LANCERT-01 externally-issued cert at the documented cross-repo
		// path (lan.DefaultCertPath / lan.DefaultKeyPath — kept in sync with
		// vulos-cloud's lancert/contract.go). LoadCertSource silently falls back to
		// the self-signed dev cert when the files are not yet present, so the LAN
		// HTTPS path always has TLS material even before the puller has run.
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
			if instKey, kerr := multiinstance.LoadOrCreateSealedInstanceKey(fabricKeyPath, fabricSealer); kerr != nil {
				log.Printf("[fabric] WARNING: could not load/create signing key (%v) — uninstall observations from this box will NOT count toward peer quorum", kerr)
			} else if serr := fabricAppSync.SetIdentity(cfg.InstanceID, instKey); serr != nil {
				log.Printf("[fabric] WARNING: could not set signing identity (%v) — uninstall observations from this box will NOT count toward peer quorum", serr)
			} else {
				log.Printf("[fabric] per-instance signing identity active (CRDT-QUORUM-01: signed uninstall observations)")
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

	// MEET_TRANSCRIPT_WIRE BEGIN — MEET-TRANSCRIPT-01
	//
	// Per-room Whisper transcription endpoints (POST /api/meet/transcribe/start,
	// /audio, /stop and GET /api/meet/transcribe/stream/{room_id}). The same
	// WhisperProvider wraps a hosted Whisper API (VULOS_WHISPER_URL pointed at
	// api.openai.com or similar) AND a self-hosted whisper.cpp HTTP server
	// (VULOS_WHISPER_URL pointed at localhost). Both speak the OpenAI-compatible
	// /v1/audio/transcriptions multipart shape.
	//
	// Privacy gate:
	//   - cloud / Pro tier: VULOS_MEET_TRANSCRIBE_ENABLE flipped on by tier hint
	//     (see meetTranscribeEnvFromTier).
	//   - self-host / Free: default-off unless the operator sets
	//     VULOS_MEET_TRANSCRIBE_ENABLE=1 explicitly.
	//
	// Routes are still registered when the provider/env is absent — they reply
	// 503 so the office UI can render the toggle as disabled cleanly rather
	// than getting 404s.
	meetTranscribeEnvFromTier(os.Getenv("VULOS_TIER"))
	if whisper, werr := airouter.NewWhisperProviderFromEnv(); werr == nil {
		registerMeetTranscriptRoutes(mux, whisper, authStore)
		log.Printf("[meet/transcribe] registered POST /api/meet/transcribe/{start,audio,stop}, GET /api/meet/transcribe/stream/{room_id} (provider=%s)", whisper.Name())
	} else {
		// Register with a nil provider so the endpoints exist and return 503;
		// keeps the office UI well-defined when transcription is off.
		registerMeetTranscriptRoutes(mux, nil, authStore)
		log.Printf("[meet/transcribe] provider unavailable: %v — routes return 503 (set VULOS_WHISPER_URL to enable)", werr)
	}
	// MEET_TRANSCRIPT_WIRE END

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

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
