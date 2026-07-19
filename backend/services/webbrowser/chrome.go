// Package webbrowser launches a streaming Chromium/Chrome browser as a
// first-class app via the generic stream pool (Xvfb + GStreamer HW encode +
// WebRTC). Unlike the client-side "Smart Browser" (an iframe/host-browser web
// app), this streams a REAL Chromium instance running on the box.
//
// It adds Chromium-specific concerns on top of the generic pool:
//   - a PER-USER persistent profile directory (cookies/history/logins), derived
//     from the authenticated user id + that user's home, so every user gets an
//     isolated, persistent Chrome profile;
//   - a virtual PulseAudio sound card for audio capture;
//   - Chrome DevTools Protocol (CDP) tab management.
//
// History: this package was deleted in commit 12e7507 ("rip-out dead GPU
// code") when the browser was replaced by a pure iframe app. It is restored
// here ALONGSIDE the iframe browser, adapted from a single boot-time persistent
// session to an on-demand, per-user launcher against the current stream.Pool
// API (Launch(LaunchOpts)).
package webbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"vulos/backend/services/gpu"
	"vulos/backend/services/stream"
)

const (
	width            = 1280
	height           = 720
	fps              = 30
	defaultSessionID = "browser"
)

// Service launches per-user streaming Chromium sessions on the shared
// stream.Pool. It owns the (single, shared) virtual PulseAudio sound card;
// the browser sessions themselves live in the pool, keyed by a per-user
// session ID, so the pool handles their lifecycle, ownership and WebRTC.
type Service struct {
	mu           sync.Mutex
	pool         *stream.Pool
	pulse        *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	pulseStarted bool
	// homeResolver resolves the requesting user's home directory from the HTTP
	// request (the per-user profile lives under it). Set via SetHomeResolver.
	homeResolver func(r *http.Request) string
}

// New creates a streaming-browser service backed by the given stream pool.
func New(pool *stream.Pool) *Service {
	return &Service{pool: pool}
}

// SetHomeResolver wires the callback that maps an HTTP request to the
// authenticated user's home directory. Without it, profiles fall back to /root.
func (s *Service) SetHomeResolver(fn func(r *http.Request) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.homeResolver = fn
}

// profileSlug sanitises a user ID into a filesystem-safe directory segment.
// The empty user ID (system / single-user context) maps to "default".
func profileSlug(userID string) string {
	if userID == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range userID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if s == "" {
		return "default"
	}
	return s
}

// sessionIDForUser returns the stream-pool session ID for a user's browser.
// A stable per-user ID means the pool dedupes repeat launches onto the same
// live session (one browser per user) instead of spawning duplicates.
func sessionIDForUser(userID string) string {
	if userID == "" {
		return defaultSessionID
	}
	return defaultSessionID + "-" + profileSlug(userID)
}

// profileDir returns the PER-USER persistent Chrome profile directory. It is
// derived from BOTH the user's home (already per-user) and a slug of the user
// id, so profiles are isolated and persistent across launches — this is what
// gives each user their own cookies/history/logins.
func profileDir(home, userID string) string {
	if home == "" {
		home = "/root"
	}
	return filepath.Join(home, "browser", "profiles", profileSlug(userID))
}

// chromeArgs builds the Chromium command-line for a given per-user profile.
// The profile directory (and its extensions subdir) make the session
// persistent and isolated.
func chromeArgs(profileDir string) []string {
	return []string{
		"--no-sandbox", "--test-type", "--disable-gpu", "--disable-software-rasterizer", "--disable-logging",
		"--disable-dev-shm-usage", "--no-first-run", "--disable-background-networking",
		"--disable-sync", "--disable-translate", "--metrics-recording-only",
		"--no-default-browser-check", "--disable-dbus",
		"--disable-features=TranslateUI,MediaRouter",
		"--enable-features=SuppressUnsupportedCommandLineWarning",
		"--remote-debugging-port=9222",
		"--user-data-dir=" + profileDir,
		"--disable-infobars",
		"--disable-default-apps",
		"--enable-extensions",
		"--load-extension=" + filepath.Join(profileDir, "extensions"),
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
	}
}

// chromeLaunchOpts assembles the stream.LaunchOpts for a per-user Chromium
// session. Kept pure (no I/O) so it is unit-testable.
func chromeLaunchOpts(bin, sessionID, profileDir, userID string) stream.LaunchOpts {
	return stream.LaunchOpts{
		ID:      sessionID,
		Name:    "Chrome",
		Command: bin,
		Args:    chromeArgs(profileDir),
		Width:   width,
		Height:  height,
		FPS:     fps,
		Restart: true,
		// UserHome is set by the caller (LaunchForUser) so this helper stays pure.
		UserID: userID,
	}
}

// LaunchForUser starts (or returns the existing) streaming Chromium session for
// the given user, with a persistent per-user profile under home. The generic
// stream pool handles Xvfb, GStreamer capture, WebRTC tracks and input.
func (s *Service) LaunchForUser(userID, home string) (*stream.Session, error) {
	bin := findBin("chromium-browser", "chromium", "google-chrome", "google-chrome-stable", "/usr/bin/chromium-browser")
	if bin == "" {
		return nil, fmt.Errorf("chromium not found")
	}
	sessionID := sessionIDForUser(userID)
	profDir := profileDir(home, userID)
	// Ensure the profile + extensions dirs exist so Chromium can persist state.
	if err := os.MkdirAll(filepath.Join(profDir, "extensions"), 0755); err != nil {
		return nil, fmt.Errorf("create profile dir: %w", err)
	}

	// Best-effort virtual sound card for audio capture (never fatal).
	s.ensurePulseAudio()

	opts := chromeLaunchOpts(bin, sessionID, profDir, userID)
	opts.UserHome = home
	sess, err := s.pool.Launch(opts)
	if err != nil {
		return nil, fmt.Errorf("stream launch: %w", err)
	}
	log.Printf("[browser] streaming Chrome ready (user=%q session=%s display=%s encoder=%s profile=%s)",
		userID, sess.ID, sess.Display, sess.Encoder, profDir)
	return sess, nil
}

// ensurePulseAudio starts the shared virtual PulseAudio sound card once,
// idempotently. Failures are logged and ignored (audio simply stays disabled).
func (s *Service) ensurePulseAudio() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pulseStarted {
		return
	}
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	if err := s.startPulseAudioLocked(); err != nil {
		log.Printf("[browser] pulseaudio warning: %v (audio disabled)", err)
		return
	}
	s.pulseStarted = true
}

// startPulseAudioLocked launches PulseAudio with virtual sink/source modules.
// Caller must hold s.mu.
func (s *Service) startPulseAudioLocked() error {
	bin := findBin("pulseaudio")
	if bin == "" {
		return fmt.Errorf("pulseaudio not found")
	}
	args := []string{
		"--daemonize=no", "--system=false", "--exit-idle-time=-1",
		"--load=module-null-sink sink_name=virtual_speaker sink_properties=device.description=VirtualSpeaker",
		"--load=module-null-sink sink_name=virtual_mic sink_properties=device.description=VirtualMic",
		"--load=module-remap-source master=virtual_mic.monitor source_name=virtual_mic_input source_properties=device.description=VirtualMicInput",
		"--load=module-always-sink",
		"--load=module-udev-detect",
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
	log.Printf("[browser] pulseaudio started")
	return nil
}

// OpenTab opens a URL in a new browser tab via CDP and activates it. Requires a
// running streaming session with --remote-debugging-port=9222 (the default
// session). Returns an error if CDP is unavailable.
func (s *Service) OpenTab(url string) (*cdpTab, error) {
	tab, err := cdpNewTab(url)
	if err != nil {
		return nil, err
	}
	cdpActivateTab(tab.ID)
	return tab, nil
}

// RegisterHandlers exposes browser-specific API endpoints. WebRTC signaling is
// NOT here — the streaming browser reuses the generic /api/stream/ws endpoint
// (the frontend StreamViewer connects with the per-user session ID).
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	// Launch (or return) the caller's streaming Chrome session.
	mux.HandleFunc("POST /api/browser/launch", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		home := "/root"
		s.mu.Lock()
		resolver := s.homeResolver
		s.mu.Unlock()
		if resolver != nil {
			if h := resolver(r); h != "" {
				home = h
			}
		}
		sess, err := s.LaunchForUser(userID, home)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      sess.ID,
			"display": sess.Display,
			"encoder": sess.Encoder,
		})
	})

	mux.HandleFunc("GET /api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		sess := s.pool.GetForUser(sessionIDForUser(userID), userID)
		g := gpu.Detect()
		resp := map[string]any{
			"running":     sess != nil,
			"session_id":  sessionIDForUser(userID),
			"gpu_vendor":  g.Vendor,
			"gpu_device":  g.Device,
			"gpu_tier":    g.TierName,
			"gpu_encoder": g.Encoder,
		}
		if sess != nil {
			resp["display"] = sess.Display
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// Tab management via Chrome DevTools Protocol (default session only).
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
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.URL == "" {
			req.URL = "about:blank"
		}
		tab, err := cdpNewTab(req.URL)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tab)
	})

	mux.HandleFunc("POST /api/browser/tabs/close", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := cdpCloseTab(req.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"closed"}`))
	})

	mux.HandleFunc("POST /api/browser/tabs/activate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := cdpActivateTab(req.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"activated"}`))
	})

	// Stop the caller's streaming session.
	mux.HandleFunc("POST /api/browser/stop", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		id := sessionIDForUser(userID)
		if s.pool.GetForUser(id, userID) == nil {
			http.Error(w, `{"error":"session not found"}`, 404)
			return
		}
		if err := s.pool.Stop(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"stopped"}`))
	})
}

// StopAll kills the shared PulseAudio daemon. The per-user browser sessions are
// owned by the stream pool and are stopped by streamPool.StopAll().
func (s *Service) StopAll() {
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
	s.pulseStarted = false
}

// CDP (Chrome DevTools Protocol) helpers — communicate with Chromium's debug
// endpoint. Chromium launches with --remote-debugging-port=9222.

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
	var pages []cdpTab
	for _, t := range tabs {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

func cdpNewTab(url string) (*cdpTab, error) {
	req, err := http.NewRequest("PUT", cdpBase+"/json/new?"+neturl.Values{"url": {url}}.Encode(), nil)
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

// findBin returns the first of names found on PATH (absolute paths are returned
// as-is if they exist), or "" if none are present.
func findBin(names ...string) string {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
