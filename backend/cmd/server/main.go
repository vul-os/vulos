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
	"strings"
	"syscall"
	"time"

	"vulos/backend/internal/config"
	"vulos/backend/internal/storage"
	"vulos/backend/services/ai"
	"vulos/backend/services/appnet"
	bprofiles "vulos/backend/services/profiles"
	"vulos/backend/services/audio"
	"vulos/backend/services/auth"
	"vulos/backend/services/bluetooth"
	"vulos/backend/services/display"
	"vulos/backend/services/embeddings"
	"vulos/backend/services/energy"
	"vulos/backend/services/gateway"
	"vulos/backend/services/notify"
	ptyservice "vulos/backend/services/pty"
	"vulos/backend/services/sysuser"
	"vulos/backend/services/recall"
	"vulos/backend/services/sandbox"
	"vulos/backend/services/webbrowser"
	"vulos/backend/services/webproxy"
	"vulos/backend/services/disks"
	"vulos/backend/services/drivers"
	"vulos/backend/services/packages"
	"vulos/backend/services/telemetry"
	"vulos/backend/services/tunnel"
	"vulos/backend/services/vault"
	"vulos/backend/services/wifi"
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

	// Tunnel (cloudflared / frp)
	tunnelCfg := tunnel.LoadConfig()
	tunnelDir := filepath.Join(home, ".vulos", "tunnel")
	tunnelSvc := tunnel.New(tunnelCfg, tunnelDir)
	if tunnelSvc.Configured() {
		if err := tunnelSvc.Start(ctx); err != nil {
			log.Printf("[tunnel] start warning: %v", err)
		}
	} else {
		log.Printf("[tunnel] skipped — set TUNNEL_PROVIDER + credentials to enable")
	}

	// AI service
	aiSvc := ai.New()
	aiCfg := ai.DefaultConfig()
	chatHistory := ai.NewHistoryStore(dbDir)
	missionStore := ai.NewMissionStore(dbDir)

	// Notifications
	notifySvc := notify.New()

	// Proactive AI agent
	proactiveAgent := ai.NewProactiveAgent(aiSvc, aiCfg, notifySvc)
	// Register system checks
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
	go proactiveAgent.Run(ctx, 60*time.Second)

	// App store
	appsDir := filepath.Join(home, ".vulos", "apps")
	appStore := appnet.NewAppStore(appsDir)

	// TURN server (WebRTC relay for remote mode)
	turnCfg := tunnel.LoadTURNConfig()
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

	// Remote browser (Xvfb + Chromium + GStreamer → WebRTC, starts at boot)
	browserSvc := webbrowser.New()
	if err := browserSvc.Start(ctx, 0); err != nil {
		log.Printf("[browser] start warning: %v", err)
	} else {
		browserSvc.WaitReady(30 * time.Second)
	}

	// Web proxy (for remote mode — kept for API proxy use)
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
	authHandler.OnUserCreated = func(username, password string) {
		if err := sysUserSvc.EnsureUser(username, password); err != nil {
			log.Printf("[sysuser] failed to create Linux user %q: %v", username, err)
		}
	}

	authHandler.OnUserLogin = func(username, password string) {
		if err := sysUserSvc.EnsureUser(username, password); err != nil {
			log.Printf("[sysuser] login sync failed for %q: %v", username, err)
		}
		if err := sysUserSvc.SetPassword(username, password); err != nil {
			log.Printf("[sysuser] password sync failed for %q: %v", username, err)
		}
	}

	// Set hostname to "vula" (Docker defaults to container ID)
	if os.Getuid() == 0 {
		os.WriteFile("/etc/hostname", []byte("vula\n"), 0644)
		exec.Command("hostname", "vula").Run()
	}

	// Ensure Linux users exist for all registered accounts (survives container rebuilds)
	if os.Getuid() == 0 {
		for _, username := range authStore.ListUsernames() {
			if err := sysUserSvc.EnsureUser(username, ""); err != nil {
				log.Printf("[sysuser] failed to reconcile user %q: %v", username, err)
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

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})

	// Setup status — public, no auth needed
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) {
		_, err := os.Stat("/var/lib/vulos/.setup-complete")
		writeJSON(w, map[string]bool{"setup_complete": err == nil})
	})

	// Auth routes
	authHandler.Register(mux)

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
		var req struct{ ID string `json:"id"` }
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
		if req.Level == "" { req.Level = notify.LevelInfo }
		if req.Source == "" { req.Source = "system" }
		n := notifySvc.Send(req.Title, req.Body, req.Level, req.Source)
		writeJSON(w, n)
	})
	mux.HandleFunc("POST /api/notifications/clear", func(w http.ResponseWriter, r *http.Request) {
		notifySvc.Clear()
		writeJSON(w, map[string]string{"status": "cleared"})
	})

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
		var req struct {
			AppID   string   `json:"app_id"`
			AppPort int      `json:"app_port"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			WorkDir string   `json:"work_dir"`
			Env     []string `json:"env"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		// Validate: app ID must be alphanumeric
		for _, c := range req.AppID {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				writeErr(w, 400, "invalid app_id")
				return
			}
		}
		// Validate: block dangerous env vars
		for _, e := range req.Env {
			lower := strings.ToLower(e)
			if strings.HasPrefix(lower, "ld_preload=") || strings.HasPrefix(lower, "ld_library_path=") {
				writeErr(w, 400, "forbidden env var")
				return
			}
		}
		// Validate: workdir must not escape
		if strings.Contains(req.WorkDir, "..") {
			writeErr(w, 400, "invalid work_dir")
			return
		}
		if req.AppPort == 0 {
			req.AppPort = 80
		}
		// Allocate host port from pool
		hostPort, ok := portPool.Allocate(req.AppID)
		if !ok {
			writeErr(w, 503, "no ports available")
			return
		}
		// Generate app secret and inject into env
		appSecret := appGateway.GenerateAppSecret(req.AppID)
		req.Env = append(req.Env, "VULOS_APP_SECRET="+appSecret, "VULOS_API=http://localhost:8080")

		app, err := launcher.Launch(ctx, req.AppID, hostPort, req.AppPort, req.Command, req.Args, req.WorkDir, req.Env)
		if err != nil {
			portPool.Release(req.AppID)
			appGateway.RemoveAppSecret(req.AppID)
			writeErr(w, 500, err.Error())
			return
		}
		// Register tunnel route for this app
		tunnelSvc.AddRoute(req.AppID, hostPort)

		resp := map[string]any{
			"app_id":  app.ID,
			"url":     gateway.URLForApp(req.AppID), // /app/{appId}/ — goes through auth gateway
			"running": app.Running,
		}
		// Include public URL if tunnel is active
		st := tunnelSvc.Status()
		if url, ok := st.PublicURLs[req.AppID]; ok {
			resp["public_url"] = url
		}
		writeJSON(w, resp)
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
		tunnelSvc.RemoveRoute(req.AppID)
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
		var req struct{ ID string `json:"id"` }
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
		var req struct{ Command string `json:"command"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" {
			writeErr(w, 400, "invalid request")
			return
		}
		result := ptyservice.Exec(r.Context(), req.Command)
		writeJSON(w, result)
	})

	// Telemetry WebSocket — live system stats
	mux.Handle("/api/telemetry", telemetry.Handler())

	// System info (one-shot, for About page)
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, telemetry.SystemInfo())
	})

	// Tunnel management
	mux.HandleFunc("GET /api/tunnel/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, tunnelSvc.Status())
	})
	mux.HandleFunc("POST /api/tunnel/start", func(w http.ResponseWriter, r *http.Request) {
		if err := tunnelSvc.Start(ctx); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, tunnelSvc.Status())
	})
	mux.HandleFunc("POST /api/tunnel/stop", func(w http.ResponseWriter, r *http.Request) {
		tunnelSvc.Stop()
		writeJSON(w, map[string]string{"status": "stopped"})
	})
	mux.HandleFunc("POST /api/tunnel/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := tunnelSvc.Reload(ctx); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, tunnelSvc.Status())
	})

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
		var req struct{ SSID string `json:"ssid"` }
		json.NewDecoder(r.Body).Decode(&req)
		wifiSvc.ForgetNetwork(r.Context(), req.SSID)
		writeJSON(w, map[string]string{"status": "forgotten"})
	})

	// Ethernet
	mux.HandleFunc("GET /api/ethernet/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wifi.ListEthernet(r.Context()))
	})
	mux.HandleFunc("POST /api/ethernet/dhcp", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Interface string `json:"interface"` }
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
		var req struct{ Interface string `json:"interface"` }
		json.NewDecoder(r.Body).Decode(&req)
		wifi.DisableEthernet(r.Context(), req.Interface)
		writeJSON(w, map[string]string{"status": "disabled"})
	})

	// Bluetooth
	mux.HandleFunc("GET /api/bluetooth/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/power", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ On bool `json:"on"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.SetPower(r.Context(), req.On); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, btSvc.GetStatus(r.Context()))
	})
	mux.HandleFunc("POST /api/bluetooth/scan", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ On bool `json:"on"` }
		json.NewDecoder(r.Body).Decode(&req)
		if req.On {
			btSvc.StartDiscovery(r.Context())
		} else {
			btSvc.StopDiscovery(r.Context())
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/bluetooth/pair", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Address string `json:"address"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.Pair(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "paired"})
	})
	mux.HandleFunc("POST /api/bluetooth/connect", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Address string `json:"address"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := btSvc.Connect(r.Context(), req.Address); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"status": "connected"})
	})
	mux.HandleFunc("POST /api/bluetooth/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Address string `json:"address"` }
		json.NewDecoder(r.Body).Decode(&req)
		btSvc.Disconnect(r.Context(), req.Address)
		writeJSON(w, map[string]string{"status": "disconnected"})
	})
	mux.HandleFunc("POST /api/bluetooth/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Address string `json:"address"` }
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
		var req struct{ Brightness int `json:"brightness"` }
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

	// Remote browser — WebRTC
	browserSvc.RegisterHandlers(mux)

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
		p := browserProfiles.Create(userID, req.Name, req.Color, req.Icon)
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
		var req struct{ AppID string `json:"app_id"` }
		json.NewDecoder(r.Body).Decode(&req)
		browserProfiles.BindApp(r.PathValue("id"), req.AppID)
		browserProfiles.Flush()
		writeJSON(w, map[string]string{"status": "bound"})
	})

	// AI-generated apps gallery — save, list, search, launch saved viewports
	aiAppsDir := filepath.Join(home, ".vulos", "ai-apps")
	os.MkdirAll(aiAppsDir, 0755)
	mux.HandleFunc("POST /api/ai-apps/save", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title  string `json:"title"`
			HTML   string `json:"html"`
			Python string `json:"python"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		id := fmt.Sprintf("ai-%d", time.Now().UnixMilli())
		appDir := filepath.Join(aiAppsDir, id)
		os.MkdirAll(appDir, 0755)
		meta := map[string]string{"title": req.Title, "id": id, "created": time.Now().Format(time.RFC3339)}
		metaData, _ := json.MarshalIndent(meta, "", "  ")
		os.WriteFile(filepath.Join(appDir, "meta.json"), metaData, 0644)
		if req.HTML != "" {
			os.WriteFile(filepath.Join(appDir, "index.html"), []byte(req.HTML), 0644)
		}
		if req.Python != "" {
			os.WriteFile(filepath.Join(appDir, "server.py"), []byte(req.Python), 0644)
		}
		writeJSON(w, map[string]string{"id": id, "status": "saved"})
	})
	mux.HandleFunc("GET /api/ai-apps", func(w http.ResponseWriter, r *http.Request) {
		entries, _ := os.ReadDir(aiAppsDir)
		var apps []map[string]string
		for _, e := range entries {
			if !e.IsDir() { continue }
			metaPath := filepath.Join(aiAppsDir, e.Name(), "meta.json")
			if data, err := os.ReadFile(metaPath); err == nil {
				var meta map[string]string
				json.Unmarshal(data, &meta)
				// Check what files exist
				if _, err := os.Stat(filepath.Join(aiAppsDir, e.Name(), "server.py")); err == nil {
					meta["has_python"] = "true"
				}
				if _, err := os.Stat(filepath.Join(aiAppsDir, e.Name(), "index.html")); err == nil {
					meta["has_html"] = "true"
				}
				apps = append(apps, meta)
			}
		}
		writeJSON(w, apps)
	})
	mux.HandleFunc("GET /api/ai-apps/{id}/html", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		data, err := os.ReadFile(filepath.Join(aiAppsDir, id, "index.html"))
		if err != nil { writeErr(w, 404, "not found"); return }
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})
	mux.HandleFunc("GET /api/ai-apps/{id}/python", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		data, err := os.ReadFile(filepath.Join(aiAppsDir, id, "server.py"))
		if err != nil { writeErr(w, 404, "not found"); return }
		w.Header().Set("Content-Type", "text/plain")
		w.Write(data)
	})
	mux.HandleFunc("DELETE /api/ai-apps/{id}", func(w http.ResponseWriter, r *http.Request) {
		os.RemoveAll(filepath.Join(aiAppsDir, r.PathValue("id")))
		writeJSON(w, map[string]string{"status": "deleted"})
	})

	// OS Control — AI and frontend can control the shell
	mux.HandleFunc("POST /api/os/open-app", func(w http.ResponseWriter, r *http.Request) {
		// Triggers app launch from backend (AI can call this)
		var req struct {
			AppID string `json:"app_id"`
			AppPort int  `json:"app_port"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.AppPort == 0 { req.AppPort = 80 }
		hostPort, ok := portPool.Allocate(req.AppID)
		if !ok { writeErr(w, 503, "no ports"); return }
		appSecret := appGateway.GenerateAppSecret(req.AppID)
		_, err := launcher.Launch(ctx, req.AppID, hostPort, req.AppPort, "", nil, "", []string{"VULOS_APP_SECRET=" + appSecret})
		if err != nil { portPool.Release(req.AppID); writeErr(w, 500, err.Error()); return }
		writeJSON(w, map[string]any{"app_id": req.AppID, "url": gateway.URLForApp(req.AppID)})
	})
	mux.HandleFunc("POST /api/os/close-app", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ AppID string `json:"app_id"` }
		json.NewDecoder(r.Body).Decode(&req)
		launcher.Stop(ctx, req.AppID)
		portPool.Release(req.AppID)
		appGateway.RemoveAppSecret(req.AppID)
		writeJSON(w, map[string]string{"status": "closed"})
	})
	mux.HandleFunc("POST /api/os/notify", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title  string `json:"title"`
			Body   string `json:"body"`
			Level  string `json:"level"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		level := notify.LevelInfo
		if req.Level == "warning" { level = notify.LevelWarning }
		if req.Level == "urgent" { level = notify.LevelUrgent }
		notifySvc.Send(req.Title, req.Body, level, "ai")
		writeJSON(w, map[string]string{"status": "sent"})
	})
	mux.HandleFunc("POST /api/os/energy-mode", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Mode string `json:"mode"` }
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
		var req struct{ AppID string `json:"app_id"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := appStore.Uninstall(req.AppID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
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

	// TURN credentials (for WebRTC relay in remote mode)
	mux.HandleFunc("GET /api/turn/credentials", func(w http.ResponseWriter, r *http.Request) {
		if !turnCfg.Enabled {
			writeErr(w, 503, "TURN not configured")
			return
		}
		userID := r.Header.Get("X-User-ID")
		writeJSON(w, turnCfg.GenerateCredentials(userID))
	})

	// S3 health
	mux.HandleFunc("GET /api/storage/status", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{
			"configured": s3cfg.Configured(),
			"endpoint":   s3cfg.Endpoint,
			"bucket":     s3cfg.Bucket,
		}
		if s3cfg.Configured() {
			if err := s3cfg.HealthCheck(r.Context()); err != nil {
				status["reachable"] = false
				status["error"] = err.Error()
			} else {
				status["reachable"] = true
			}
		}
		writeJSON(w, status)
	})

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
		var req struct{ Module string `json:"module"` }
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
		var req struct{ Module string `json:"module"` }
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

	// Packages — Alpine apk package management
	mux.HandleFunc("GET /api/packages/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, packages.GetStatus(r.Context()))
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
		var req struct{ Name string `json:"name"` }
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
		var req struct{ Name string `json:"name"` }
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

	// Landing page — separate server on LANDING_PORT (default off)
	if cfg.LandingPort != "" {
		landingDir := ""
		for _, dir := range []string{"/opt/vulos/landing", "./landing", "../landing"} {
			if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
				landingDir = dir
				break
			}
		}
		if landingDir != "" {
			landingMux := http.NewServeMux()
			landingFS := http.FileServer(http.Dir(landingDir))
			landingMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				// Serve file if exists, otherwise index.html
				filePath := filepath.Join(landingDir, filepath.Clean(r.URL.Path))
				if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
					landingFS.ServeHTTP(w, r)
					return
				}
				if r.URL.Path == "/docs" || r.URL.Path == "/docs/" {
					http.ServeFile(w, r, filepath.Join(landingDir, "docs.html"))
					return
				}
				http.ServeFile(w, r, filepath.Join(landingDir, "index.html"))
			})
			landingAddr := ":" + cfg.LandingPort
			go func() {
				log.Printf("serving landing page from %s on %s", landingDir, landingAddr)
				if err := http.ListenAndServe(landingAddr, landingMux); err != nil {
					log.Printf("[landing] server error: %v", err)
				}
			}()
		}
	}

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

	addr := ":" + cfg.Port
	log.Printf("vulos server listening on %s (env=%s)", addr, *env)
	server := &http.Server{Addr: addr, Handler: authHandler.Middleware(mux)}

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		browserSvc.StopAll()
		sandboxSvc.StopAll()
		tunnelSvc.Stop()
		ptySvc.DestroyAll()
		launcher.StopAll(context.Background())
		netMgr.DestroyAll(context.Background())
		server.Shutdown(context.Background())
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
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
	if err == nil { return "" }
	return err.Error()
}
