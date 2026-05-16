package main

import (
	"context"
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

	"vulos/backend/internal/config"
	"vulos/backend/internal/storage"
	"vulos/backend/services/ai"
	"vulos/backend/services/appfs"
	"vulos/backend/services/appnet"
	"vulos/backend/services/audio"
	"vulos/backend/services/auth"
	"vulos/backend/services/authvault"
	"vulos/backend/services/bluetooth"
	"vulos/backend/services/bootmode"
	"vulos/backend/services/credvault"
	"vulos/backend/services/desktop"
	"vulos/backend/services/disks"
	"vulos/backend/services/display"
	"vulos/backend/services/drivers"
	"vulos/backend/services/embeddings"
	"vulos/backend/services/energy"
	"vulos/backend/services/gateway"
	"vulos/backend/services/gpu"
	"vulos/backend/services/network"
	"vulos/backend/services/notify"
	"vulos/backend/services/packages"
	"vulos/backend/services/peering"
	bprofiles "vulos/backend/services/profiles"
	ptyservice "vulos/backend/services/pty"
	"vulos/backend/services/recall"
	"vulos/backend/services/sandbox"
	"vulos/backend/services/storageprov"
	"vulos/backend/services/stream"
	"vulos/backend/services/sysuser"
	"vulos/backend/services/telemetry"
	"vulos/backend/services/vault"
	"vulos/backend/services/webbrowser"
	"vulos/backend/services/webproxy"
	"vulos/backend/services/wifi"
	"vulos/backend/services/wine"
)

func main() {
	env := flag.String("env", "local", "Environment: local, dev, main")
	flag.Parse()

	cfg := config.Load(*env)

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

	// Remote browser (Chromium via stream pool — one persistent instance, always ready)
	browserSvc := webbrowser.New(streamPool)
	if err := browserSvc.Start(ctx, 0); err != nil {
		log.Printf("[browser] start warning: %v", err)
	} else {
		browserSvc.WaitReady(30 * time.Second)
	}

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

	// NET-07: cluster health (data-dir writable, disk space, sync lag) — public
	mux.HandleFunc("GET /api/health", handleClusterHealth(filepath.Join(home, ".vulos")))

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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// TOTP vault routes (/api/auth/totp/*)
	totpHandler := authvault.NewHandler()
	totpHandler.RegisterHandlers(mux)

	// Credential vault HTTP API (password manager, per-user, AES-256-GCM)
	credVaultHandler := credvault.NewHandler(func(userID string) string {
		return filepath.Join(home, ".vulos", "auth", "vault", userID)
	})
	credVaultHandler.RegisterHandlers(mux)
	// SSH key management (host key + authorized_keys)
	registerSSHKeyRoutes(mux, authStore, home)

	// App gateway — /app/{appId}/* proxied with auth
	mux.HandleFunc("/app/", appGateway.Handler())

	// AI chat
	mux.HandleFunc("POST /api/ai/chat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []ai.Message `json:"messages"`
			Stream   bool         `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		// Use per-user config if available, else default
		userCfg := aiCfg
		userID := r.Header.Get("X-User-ID")
		if userID != "" {
			if profile, ok := authStore.GetProfile(userID); ok && profile.AIAPIKey != "" {
				userCfg.Provider = ai.Provider(profile.AIProvider)
				userCfg.APIKey = profile.AIAPIKey
				if profile.AIModel != "" {
					userCfg.Model = profile.AIModel
				}
			}
		}

		// Enrich with Recall context if available
		if recallSvc != nil && len(req.Messages) > 0 {
			lastMsg := req.Messages[len(req.Messages)-1].Content
			if results, err := recallSvc.Search(r.Context(), lastMsg, 3); err == nil && len(results) > 0 {
				var ctx string
				for _, res := range results {
					if res.Score > 0.5 {
						path := res.Metadata["path"]
						ctx += fmt.Sprintf("[File: %s]\n%s\n\n", path, res.Content)
					}
				}
				if ctx != "" {
					// Prepend context as a system message
					req.Messages = append([]ai.Message{
						{Role: "system", Content: "Relevant files from the user's system:\n" + ctx},
					}, req.Messages...)
				}
			}
		}

		cr := ai.CompletionRequest{Messages: req.Messages, Stream: req.Stream}

		// Save user messages to history
		if userID != "" {
			chatHistory.Save(userID, req.Messages)
		}

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				writeErr(w, 500, "streaming not supported")
				return
			}
			var fullResp string
			aiSvc.Stream(r.Context(), userCfg, cr, func(chunk ai.StreamChunk) {
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				fullResp += chunk.Content
			})
			// Save assistant response
			if userID != "" && fullResp != "" {
				chatHistory.Save(userID, []ai.Message{{Role: "assistant", Content: fullResp}})
				chatHistory.Flush()
			}
			return
		}

		resp, err := aiSvc.Complete(r.Context(), userCfg, cr)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Save assistant response
		if userID != "" {
			chatHistory.Save(userID, []ai.Message{{Role: "assistant", Content: resp}})
			chatHistory.Flush()
		}
		writeJSON(w, map[string]string{"content": resp})
	})
	mux.HandleFunc("GET /api/ai/status", func(w http.ResponseWriter, r *http.Request) {
		err := aiSvc.HealthCheck(r.Context(), aiCfg)
		writeJSON(w, map[string]any{
			"provider":  aiCfg.Provider,
			"model":     aiCfg.Model,
			"available": err == nil,
			"error":     errStr(err),
		})
	})

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
		m := missionStore.Get(r.PathValue("id"))
		if m == nil {
			writeErr(w, 404, "not found")
			return
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

	// xdg-open handler — opens URL in the OS browser via CDP and signals frontend.
	// Requires authentication (not in publicPaths). Hardened against SSRF (H6).
	registerOpenRoutes(mux, browserSvc, notifySvc)

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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if os.Getenv("VULOS_DISABLE_EXEC") != "" {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if os.Getenv("VULOS_DISABLE_EXEC") != "" {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
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
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		sandboxSvc.Stop(req.ID)
		writeJSON(w, map[string]string{"status": "stopped"})
	})
	mux.HandleFunc("GET /api/sandbox/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, sandboxSvc.List())
	})
	// Sandbox proxy — /api/sandbox/{id}/* → localhost:{sandbox_port}/*
	mux.HandleFunc("/api/sandbox/", func(w http.ResponseWriter, r *http.Request) {
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
		// Kill-switch: set VULOS_DISABLE_EXEC=1 to disable entirely.
		if os.Getenv("VULOS_DISABLE_EXEC") == "1" {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" {
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
		var cfg network.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeErr(w, 400, "invalid config")
			return
		}
		netSvc.Configure(cfg)
		writeJSON(w, netSvc.Config())
	})

	// TURN/coturn settings routes (NET-10)
	registerTURNRoutes(mux, turnStore)

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
		var req struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := wifiSvc.Connect(r.Context(), req.SSID, req.Password); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connecting"})
	})
	mux.HandleFunc("POST /api/wifi/disconnect", func(w http.ResponseWriter, r *http.Request) {
		wifiSvc.Disconnect(r.Context())
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("GET /api/wifi/saved", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifiSvc.SavedNetworks(r.Context()))
	})
	mux.HandleFunc("POST /api/wifi/forget", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SSID string `json:"ssid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		wifiSvc.ForgetNetwork(r.Context(), req.SSID)
		writeJSON(w, map[string]string{"status": "forgotten"})
	})

	// Ethernet
	mux.HandleFunc("GET /api/ethernet/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifi.ListEthernet(r.Context()))
	})
	mux.HandleFunc("POST /api/ethernet/dhcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Interface string `json:"interface"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := wifi.EnableDHCP(r.Context(), req.Interface); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "dhcp started"})
	})
	mux.HandleFunc("POST /api/ethernet/static", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Interface string `json:"interface"`
			IP        string `json:"ip"`
			Gateway   string `json:"gateway"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := wifi.SetStaticIP(r.Context(), req.Interface, req.IP, req.Gateway); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "configured"})
	})
	mux.HandleFunc("POST /api/ethernet/disable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Interface string `json:"interface"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		wifi.DisableEthernet(r.Context(), req.Interface)
		writeJSON(w, map[string]string{"status": "disabled"})
	})

	// Bluetooth
	mux.HandleFunc("GET /api/bluetooth/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/power", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.SetPower(r.Context(), req.On); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/scan", func(w http.ResponseWriter, r *http.Request) {
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
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.Pair(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "paired"})
	})
	mux.HandleFunc("POST /api/bluetooth/connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.Connect(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connected"})
	})
	mux.HandleFunc("POST /api/bluetooth/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		btSvc.Disconnect(r.Context(), req.Address)
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("POST /api/bluetooth/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address string `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		btSvc.Remove(r.Context(), req.Address)
		writeJSON(w, map[string]string{"status": "removed"})
	})

	// Audio
	mux.HandleFunc("GET /api/audio/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, audioSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/audio/volume", func(w http.ResponseWriter, r *http.Request) {
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
		var req struct {
			Output     string `json:"output"`
			Resolution string `json:"resolution"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := displaySvc.SetResolution(r.Context(), req.Output, req.Resolution); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, displaySvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/display/enable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Output string `json:"output"`
			Enable bool   `json:"enable"`
		}
		json.NewDecoder(r.Body).Decode(&req)
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

	// Remote browser — WebRTC (delegates to stream pool)
	browserSvc.RegisterHandlers(mux)

	// Generic app streaming (any X11 app via WebRTC)
	streamPool.RegisterHandlers(mux)
	// Stream toolbar endpoints (FPS selector, MangoHud toggle — GAME-08)
	registerStreamRoutes(mux, streamPool)

	// AUTH-13: WebAuthn re-auth gate for input-injection sessions
	registerStreamWebAuthnRoutes(mux, streamPool, authStore)

	// Wine prefix management
	wineSvc.RegisterHandlers(mux)
	desktopSvc.RegisterHandlers(mux)
	gpu.RegisterGPUInfoHandlers(mux)

	// Peering — direct Vula-to-Vula communication
	peeringSvc.RegisterHandlers(mux)
	// App visibility (private|local|public)
	appnet.RegisterVisibilityHandlers(mux, appStore, visStore)

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
		browserProfiles.Delete(r.PathValue("id"))
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "deleted"})
	})
	mux.HandleFunc("POST /api/browser-profiles/{id}/clear", func(w http.ResponseWriter, r *http.Request) {
		browserProfiles.ClearData(r.PathValue("id"))
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "cleared"})
	})
	mux.HandleFunc("POST /api/browser-profiles/{id}/bind", func(w http.ResponseWriter, r *http.Request) {
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
	nativeMode := detectNativeMode()
	log.Printf("[shell] native mode: %s", nativeMode)

	mux.HandleFunc("GET /api/shell/native-mode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"mode": nativeMode})
	})

	mux.HandleFunc("POST /api/shell/native-window", func(w http.ResponseWriter, r *http.Request) {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if req.URL == "" {
			writeErr(w, 400, "url required")
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
		var req struct {
			PID int `json:"pid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID == 0 {
			writeErr(w, 400, "pid required")
			return
		}
		proc, err := os.FindProcess(req.PID)
		if err != nil {
			writeErr(w, 404, "process not found")
			return
		}
		proc.Signal(syscall.SIGTERM)
		writeJSON(w, map[string]string{"status": "killed"})
	})

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

	// OS Control — AI and frontend can control the shell
	mux.HandleFunc("POST /api/os/open-app", func(w http.ResponseWriter, r *http.Request) {
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
		var req struct {
			Mode string `json:"mode"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		energyMgr.SetMode(energy.Mode(req.Mode))
		writeJSON(w, energyMgr.State())
	})

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

	// Disk usage
	mux.HandleFunc("GET /api/disks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, disks.GetStatus())
	})
	mux.HandleFunc("GET /api/disks/breakdown", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/"
		}
		writeJSON(w, disks.DirBreakdown(r.Context(), path))
	})

	// Drivers — hardware detection & kernel modules
	mux.HandleFunc("GET /api/drivers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, drivers.Detect(r.Context()))
	})
	mux.HandleFunc("POST /api/drivers/load", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Module string `json:"module"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Module == "" {
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
		var req struct {
			Module string `json:"module"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Module == "" {
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
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
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
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
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
		if err := packages.Update(r.Context()); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "updated"})
	})
	mux.HandleFunc("POST /api/packages/upgrade", func(w http.ResponseWriter, r *http.Request) {
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

	addr := ":" + cfg.Port
	handler := authHandler.Middleware(mainHandler)
	server := &http.Server{Addr: addr, Handler: handler}

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		browserSvc.StopAll()
		streamPool.StopAll()
		sandboxSvc.StopAll()
		netSvc.Stop()
		ptySvc.DestroyAll()
		launcher.StopAll(context.Background())
		netMgr.DestroyAll(context.Background())
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

	if tlsCert != "" {
		log.Printf("vulos server listening on %s with TLS (env=%s, cert=%s)", addr, *env, tlsCert)
		if err := server.ListenAndServeTLS(tlsCert, tlsKey); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	} else {
		log.Printf("vulos server listening on %s (env=%s, no TLS)", addr, *env)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}

// detectNativeMode checks if we're running on baremetal (sole fullscreen Cog/WPE)
// or under a Wayland compositor that supports multiple windows.
// Fast: just checks env vars and compositor presence, no subprocess.
func detectNativeMode() string {
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

	// No display server detected — or WPE is running as sole renderer
	_ = ua
	return "baremetal"
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
