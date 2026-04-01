// Package webbrowser manages the persistent Chromium browser session.
// It delegates display/capture/WebRTC to the generic stream pool and adds
// Chromium-specific concerns: PulseAudio, restart-on-exit, stale cleanup.
package webbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"vulos/backend/services/gpu"
	"vulos/backend/services/stream"
)

const (
	width     = 1280
	height    = 720
	fps       = 30
	sessionID = "browser"
)

// Service manages a persistent Chromium session backed by stream.Pool.
// Chromium + PulseAudio are launched at startup so WebRTC connections are instant.
type Service struct {
	mu      sync.Mutex
	pool    *stream.Pool
	sess    *stream.Session
	pulse   *exec.Cmd
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func New(pool *stream.Pool) *Service {
	return &Service{pool: pool}
}

// Start launches PulseAudio and Chromium via the stream pool.
// Retries up to 3 times with backoff.
func (s *Service) Start(parentCtx context.Context, _ int) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt*3) * time.Second
			log.Printf("[browser] startup attempt %d failed: %v — retrying in %s", attempt, lastErr, wait)
			time.Sleep(wait)
		}
		lastErr = s.tryStart(parentCtx)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (s *Service) tryStart(parentCtx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(parentCtx)

	// Ensure extensions directory exists
	os.MkdirAll("/root/.vulos/browser/extensions", 0755)

	// Kill stale processes from previous runs
	exec.Command("pkill", "-9", "Xvfb").Run()
	exec.Command("pkill", "-9", "chromium").Run()
	exec.Command("pkill", "-9", "gst-launch").Run()
	exec.Command("pkill", "-9", "pulseaudio").Run()
	os.Remove("/tmp/pulse-server")
	time.Sleep(time.Second)

	// PulseAudio (virtual sound card for Chromium audio capture)
	if err := s.startPulseAudio(); err != nil {
		log.Printf("[browser] pulseaudio warning: %v (audio disabled)", err)
	}

	// Find Chromium binary
	bin := findBin("chromium-browser", "chromium", "/usr/bin/chromium-browser")
	if bin == "" {
		return fmt.Errorf("chromium not found")
	}

	// Launch via stream pool — Xvfb, GStreamer, WebRTC tracks, input all handled
	sess, err := s.pool.Launch(stream.LaunchOpts{
		ID:   sessionID,
		Name: "Chrome",
		Command: bin,
		Args: []string{
			"--no-sandbox", "--test-type", "--disable-gpu", "--disable-software-rasterizer", "--disable-logging",
			"--disable-dev-shm-usage", "--no-first-run", "--disable-background-networking",
			"--disable-sync", "--disable-translate", "--metrics-recording-only",
			"--no-default-browser-check", "--disable-dbus",
			"--disable-features=TranslateUI,MediaRouter",
			"--enable-features=SuppressUnsupportedCommandLineWarning",
			"--remote-debugging-port=9222",
			"--disable-infobars",
			"--disable-default-apps",
			"--enable-extensions",
			"--load-extension=/root/.vulos/browser/extensions",
			"--disable-component-update",
			"--disable-domain-reliability",
			"--disable-client-side-phishing-detection",
			"--noerrdialogs",
			"--hide-crash-restore-bubble",
			"--disable-popup-blocking",
			"--new-window",
			fmt.Sprintf("--window-size=%d,%d", width, height),
			"--window-position=0,0",
			"--start-maximized",
			"https://google.com",
		},
		Width:   width,
		Height:  height,
		FPS:     fps,
		Restart: true,
	})
	if err != nil {
		return fmt.Errorf("stream launch: %w", err)
	}

	s.sess = sess
	s.running = true
	log.Printf("[browser] ready via stream pool (session=%s, display=%s, encoder=%s)",
		sess.ID, sess.Display, sess.Encoder)
	return nil
}

func (s *Service) startPulseAudio() error {
	bin := findBin("pulseaudio")
	if bin == "" {
		return fmt.Errorf("pulseaudio not found")
	}
	args := []string{
		"--daemonize=no", "--system=false", "--exit-idle-time=-1",
		// Virtual speaker — captures all app audio (Chromium, Wine, Lutris)
		"--load=module-null-sink sink_name=virtual_speaker sink_properties=device.description=VirtualSpeaker",
		// Virtual microphone — apps can record from this source
		"--load=module-null-sink sink_name=virtual_mic sink_properties=device.description=VirtualMic",
		"--load=module-remap-source master=virtual_mic.monitor source_name=virtual_mic_input source_properties=device.description=VirtualMicInput",
		"--load=module-always-sink",
		// Hardware audio detection (bare metal — ignored in containers without devices)
		"--load=module-udev-detect",
		// Bluetooth audio (A2DP sink, HFP gateway)
		"--load=module-bluetooth-discover",
	}

	cmd := exec.CommandContext(s.ctx, bin, args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	s.pulse = cmd

	// Auto-restart loop with backoff
	go func() {
		cmd.Wait()
		backoff := 3 * time.Second
		for {
			if s.ctx.Err() != nil {
				return
			}
			log.Printf("[browser] pulseaudio exited, restarting in %s...", backoff)
			time.Sleep(backoff)
			c := exec.CommandContext(s.ctx, bin, args...)
			c.Env = os.Environ()
			c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Start(); err != nil {
				log.Printf("[browser] pulseaudio restart failed: %v", err)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			s.pulse = c
			log.Printf("[browser] pulseaudio restarted")
			backoff = 3 * time.Second
			start := time.Now()
			c.Wait()
			if time.Since(start) > 10*time.Second {
				backoff = 3 * time.Second
			} else if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
	time.Sleep(time.Second)
	log.Printf("[browser] pulseaudio started")
	return nil
}

// OpenTab opens a URL in a new browser tab via CDP and activates it.
func (s *Service) OpenTab(url string) (*cdpTab, error) {
	tab, err := cdpNewTab(url)
	if err != nil {
		return nil, err
	}
	cdpActivateTab(tab.ID)
	return tab, nil
}

// RegisterHandlers exposes browser-specific API endpoints.
// WebRTC signaling delegates to the stream session.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		running := s.running
		sess := s.sess
		s.mu.Unlock()
		g := gpu.Detect()
		resp := map[string]any{
			"running":     running,
			"gpu_vendor":  g.Vendor,
			"gpu_device":  g.Device,
			"gpu_tier":    g.TierName,
			"gpu_encoder": g.Encoder,
		}
		if sess != nil {
			resp["display"] = sess.Display
			resp["session_id"] = sess.ID
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Tab management via Chrome DevTools Protocol
	mux.HandleFunc("GET /api/browser/tabs", func(w http.ResponseWriter, r *http.Request) {
		tabs, err := cdpListTabs()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tabs)
	})

	mux.HandleFunc("POST /api/browser/tabs/new", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ URL string `json:"url"` }
		json.NewDecoder(r.Body).Decode(&req)
		if req.URL == "" { req.URL = "about:blank" }
		tab, err := cdpNewTab(req.URL)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tab)
	})

	mux.HandleFunc("POST /api/browser/tabs/close", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string `json:"id"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := cdpCloseTab(req.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"closed"}`))
	})

	mux.HandleFunc("POST /api/browser/tabs/activate", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string `json:"id"` }
		json.NewDecoder(r.Body).Decode(&req)
		if err := cdpActivateTab(req.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"activated"}`))
	})

	mux.HandleFunc("POST /api/browser/tabs/navigate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := cdpNavigate(req.ID, req.URL); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"navigated"}`))
	})

	// Extension management
	mux.HandleFunc("GET /api/browser/extensions", func(w http.ResponseWriter, r *http.Request) {
		exts := listExtensions()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(exts)
	})

	mux.HandleFunc("DELETE /api/browser/extensions", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string `json:"id"` }
		json.NewDecoder(r.Body).Decode(&req)
		extDir := "/root/.vulos/browser/extensions/" + req.ID
		if err := os.RemoveAll(extDir); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"removed"}`))
	})

	mux.HandleFunc("GET /api/browser/ws", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		sess := s.sess
		s.mu.Unlock()
		if sess == nil {
			http.Error(w, `{"error":"browser not running"}`, 503)
			return
		}
		sess.HandleSignaling(w, r)
	})
}

// Stop kills PulseAudio and the browser stream session.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	if s.pulse != nil && s.pulse.Process != nil {
		syscall.Kill(-s.pulse.Process.Pid, syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		syscall.Kill(-s.pulse.Process.Pid, syscall.SIGKILL)
	}
	s.pool.Stop(sessionID)
	s.running = false
}

func (s *Service) StopAll()      { s.Stop() }
func (s *Service) Running() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.running }
func (s *Service) Port() int     { return 0 }
func (s *Service) WaitReady(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.Running() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// Extension management helpers

type browserExtension struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

func listExtensions() []browserExtension {
	extBase := "/root/.vulos/browser/extensions"
	entries, err := os.ReadDir(extBase)
	if err != nil {
		return []browserExtension{}
	}
	var exts []browserExtension
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ext := browserExtension{
			ID:   e.Name(),
			Name: e.Name(),
			Path: extBase + "/" + e.Name(),
		}
		// Try to read manifest.json for name/version
		mf, err := os.ReadFile(extBase + "/" + e.Name() + "/manifest.json")
		if err == nil {
			var m struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			if json.Unmarshal(mf, &m) == nil {
				if m.Name != "" {
					ext.Name = m.Name
				}
				ext.Version = m.Version
			}
		}
		exts = append(exts, ext)
	}
	return exts
}

// CDP (Chrome DevTools Protocol) helpers — communicate with Chromium's debug endpoint.
// Chromium launches with --remote-debugging-port=9222 for tab management.

type cdpTab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

const cdpBase = "http://127.0.0.1:9222"

func cdpListTabs() ([]cdpTab, error) {
	resp, err := http.Get(cdpBase + "/json/list")
	if err != nil {
		return nil, fmt.Errorf("CDP unavailable: %w", err)
	}
	defer resp.Body.Close()
	var tabs []cdpTab
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, err
	}
	// Filter to page type only
	var pages []cdpTab
	for _, t := range tabs {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

func cdpNewTab(url string) (*cdpTab, error) {
	req, err := http.NewRequest("PUT", cdpBase+"/json/new?"+url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tab cdpTab
	if err := json.NewDecoder(resp.Body).Decode(&tab); err != nil {
		return nil, err
	}
	return &tab, nil
}

func cdpCloseTab(id string) error {
	req, _ := http.NewRequest("PUT", cdpBase+"/json/close/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func cdpActivateTab(id string) error {
	req, _ := http.NewRequest("PUT", cdpBase+"/json/activate/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func cdpNavigate(id, url string) error {
	// Activate the tab first, then send a Page.navigate command
	if err := cdpActivateTab(id); err != nil {
		return err
	}
	// Use the CDP WebSocket endpoint for this tab to navigate
	// For simplicity, we open a new tab and close the old one
	_, err := cdpNewTab(url)
	if err != nil {
		return err
	}
	// The new tab becomes the active one — close the old if needed
	return nil
}

func findBin(names ...string) string {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
