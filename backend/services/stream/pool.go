package stream

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
	"vulos/backend/services/input"

	"github.com/pion/webrtc/v4"
)

// Pool manages multiple concurrent streaming sessions.
// Each session gets its own Xvfb display, GStreamer pipeline, and WebRTC tracks.
type Pool struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	nextDisplay int
	nextPort    int
}

// NewPool creates a streaming session pool.
// Display numbers start at 10 (:10, :11, ...) to avoid conflicting with :0 (real display) and :99 (browser).
func NewPool() *Pool {
	return &Pool{
		sessions:    make(map[string]*Session),
		nextDisplay: 10,
		nextPort:    6100,
	}
}

// LaunchOpts configures a streaming session.
type LaunchOpts struct {
	// ID is a unique identifier for this session. If empty, one is generated.
	ID string
	// Name is a human-readable label (e.g. "KiCad", "Wine: Notepad").
	Name string
	// Command is the binary to run (e.g. "/usr/bin/kicad").
	Command string
	// Args are command-line arguments.
	Args []string
	// Env are additional environment variables (on top of DISPLAY, etc.).
	Env []string
	// Width and Height of the virtual display (default 1280x720).
	Width, Height int
	// FPS for GStreamer capture (default 30).
	FPS int
	// Restart: if true, restart the app if it exits.
	Restart bool
}

// Launch starts a new streaming session: Xvfb + app + GStreamer + WebRTC.
func (p *Pool) Launch(opts LaunchOpts) (*Session, error) {
	if opts.Width == 0 {
		opts.Width = 1280
	}
	if opts.Height == 0 {
		opts.Height = 720
	}
	if opts.FPS == 0 {
		opts.FPS = 30
	}
	if opts.ID == "" {
		opts.ID = fmt.Sprintf("stream-%d", time.Now().UnixMilli())
	}

	p.mu.Lock()
	if existing, exists := p.sessions[opts.ID]; exists {
		p.mu.Unlock()
		return existing, nil // Return existing session instead of erroring
	}
	displayNum := p.nextDisplay
	videoPort := p.nextPort
	audioPort := p.nextPort + 1
	p.nextDisplay++
	p.nextPort += 2
	p.mu.Unlock()

	display := fmt.Sprintf(":%d", displayNum)
	ctx, cancel := context.WithCancel(context.Background())
	gpuInfo := gpu.Detect()

	sess := &Session{
		ID:         opts.ID,
		Name:       opts.Name,
		Display:    display,
		Width:      opts.Width,
		Height:     opts.Height,
		FPS:        opts.FPS,
		Running:    true,
		Encoder:    gpuInfo.Encoder,
		ctx:        ctx,
		cancel:     cancel,
		videoPort:  videoPort,
		audioPort:  audioPort,
		displayNum: displayNum,
	}

	// 1. Start Xvfb
	// Start Xvfb with large max screen size so xrandr can resize freely
	sess.xvfb = exec.CommandContext(ctx, "Xvfb", display,
		"-screen", "0", "3840x2160x24",
		"-ac", "+render", "+extension", "RANDR", "-noreset")
	sess.xvfb.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sess.xvfb.Stdout = os.Stdout
	sess.xvfb.Stderr = os.Stderr
	if err := sess.xvfb.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("xvfb: %w", err)
	}

	// Wait for display
	xSock := fmt.Sprintf("/tmp/.X11-unix/X%d", displayNum)
	ready := false
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(xSock); err == nil {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		sess.Stop()
		return nil, fmt.Errorf("display %s not ready", display)
	}

	// 1b. Resize Xvfb from max (3840x2160) to the actual requested resolution
	resizeCmd := exec.CommandContext(ctx, "xrandr", "--fb", fmt.Sprintf("%dx%d", opts.Width, opts.Height))
	resizeCmd.Env = append(os.Environ(), "DISPLAY="+display)
	if err := resizeCmd.Run(); err != nil {
		log.Printf("[stream] initial xrandr resize warning: %v", err)
	}

	// 1c. Create input injector (uinput or xdotool fallback)
	sess.injector = input.NewInjector(display, opts.Width, opts.Height)

	// Each session gets isolated runtime/config dirs so apps don't see each other's lock files
	sessionHome := fmt.Sprintf("/tmp/stream-%s", opts.ID)
	os.MkdirAll(sessionHome, 0755)
	appEnv := append(os.Environ(),
		"DISPLAY="+display,
		"XDG_RUNTIME_DIR="+sessionHome,
		"XDG_CONFIG_HOME="+sessionHome+"/.config",
		"XDG_DATA_HOME="+sessionHome+"/.local/share",
		"XDG_CACHE_HOME="+sessionHome+"/.cache",
		"TMPDIR="+sessionHome+"/tmp",
	)
	os.MkdirAll(sessionHome+"/tmp", 0755)
	os.MkdirAll(sessionHome+"/.config", 0755)
	os.MkdirAll(sessionHome+"/.local/share", 0755)
	os.MkdirAll(sessionHome+"/.cache", 0755)
	appEnv = append(appEnv, opts.Env...)

	// 1c. Launch matchbox window manager (auto-maximizes all windows, constrains dialogs)
	if wmBin, err := exec.LookPath("matchbox-window-manager"); err == nil {
		sess.wm = exec.CommandContext(ctx, wmBin, "-use_titlebar", "no", "-use_desktop", "no")
		sess.wm.Env = appEnv
		sess.wm.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := sess.wm.Start(); err != nil {
			log.Printf("[stream] matchbox-wm start warning: %v", err)
		} else {
			time.Sleep(300 * time.Millisecond) // Let WM initialize
		}
	}

	// 2. Launch the app
	sess.app = exec.CommandContext(ctx, opts.Command, opts.Args...)
	sess.app.Env = appEnv
	sess.app.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sess.app.Stdout = os.Stdout
	sess.app.Stderr = os.Stderr
	if err := sess.app.Start(); err != nil {
		sess.Stop()
		return nil, fmt.Errorf("app %q: %w", opts.Command, err)
	}

	// 3. WebRTC tracks
	vTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: gpuInfo.WebRTCCodec()},
		"video", "stream-"+opts.ID,
	)
	sess.videoTrack = vTrack

	aTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "stream-"+opts.ID,
	)
	sess.audioTrack = aTrack

	// 4. RTP listeners
	go listenRTP(ctx, videoPort, vTrack)
	go listenRTP(ctx, audioPort, aTrack)

	// 5. Wait for app to paint, then start GStreamer capture
	time.Sleep(2 * time.Second)

	gstBin, _ := exec.LookPath("gst-launch-1.0")
	if gstBin != "" {
		// Video pipeline
		go runWithBackoff(ctx, sess.Name+"-video", func() *exec.Cmd {
			args := []string{"-q"}
			// Capture source: PipeWire DMA-BUF when available, ximagesrc fallback
			args = append(args, gpuInfo.CaptureArgs(display, opts.FPS)...)
			// Color conversion / GPU upload (DMA-BUF for VA-API, CUDA for NVENC, CPU for software)
			args = append(args, "!")
			args = append(args, gpuInfo.ConvertArgs()...)
			args = append(args, "!", "queue", "max-size-buffers=1", "leaky=downstream")
			args = append(args, "!")
			args = append(args, gpuInfo.EncoderArgs()...)
			args = append(args, "!")
			args = append(args, gpuInfo.PayloaderArgs()...)
			args = append(args, "!",
				"udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", videoPort),
				"sync=false", "async=false",
			)
			cmd := exec.CommandContext(ctx, gstBin, args...)
			cmd.Env = append(os.Environ(), "DISPLAY="+display)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd
		}, &sess.gstVideo)

		// Audio pipeline — captures from virtual speaker monitor (all app audio)
		go runWithBackoff(ctx, sess.Name+"-audio", func() *exec.Cmd {
			args := []string{"-q",
				"pulsesrc", "device=virtual_speaker.monitor",
				"!", "audio/x-raw,rate=48000,channels=2",
				"!", "queue", "max-size-buffers=1", "leaky=downstream",
				"!", "opusenc", "bitrate=128000", "frame-size=20",
				"!", "rtpopuspay", "pt=111",
				"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", audioPort),
				"sync=false", "async=false",
			}
			cmd := exec.CommandContext(ctx, gstBin, args...)
			cmd.Env = append(os.Environ(), "DISPLAY="+display)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd
		}, &sess.gstAudio)
	}

	// 6. Monitor app exit
	go func() {
		sess.app.Wait()
		if opts.Restart && ctx.Err() == nil {
			log.Printf("[stream] %s exited, restarting...", opts.Name)
			time.Sleep(time.Second)
			newApp := exec.CommandContext(ctx, opts.Command, opts.Args...)
			newApp.Env = appEnv
			newApp.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			newApp.Stdout = os.Stdout
			newApp.Stderr = os.Stderr
			if err := newApp.Start(); err != nil {
				log.Printf("[stream] %s restart failed: %v", opts.Name, err)
				sess.Stop()
				p.mu.Lock()
				delete(p.sessions, opts.ID)
				p.mu.Unlock()
				return
			}
			sess.mu.Lock()
			sess.app = newApp
			sess.mu.Unlock()
			return
		}
		log.Printf("[stream] %s exited", opts.Name)
		sess.Stop()
		p.mu.Lock()
		delete(p.sessions, opts.ID)
		p.mu.Unlock()
	}()

	p.mu.Lock()
	p.sessions[opts.ID] = sess
	p.mu.Unlock()

	log.Printf("[stream] launched %q on %s (encoder=%s, %dx%d@%dfps)",
		opts.Name, display, gpuInfo.Encoder, opts.Width, opts.Height, opts.FPS)
	return sess, nil
}

// Get returns a session by ID.
func (p *Pool) Get(id string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// Stop kills a session by ID.
func (p *Pool) Stop(id string) error {
	p.mu.Lock()
	sess, ok := p.sessions[id]
	if ok {
		delete(p.sessions, id)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	sess.Stop()
	return nil
}

// StopAll kills all sessions.
func (p *Pool) StopAll() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.sessions))
	for id := range p.sessions {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.Stop(id)
	}
}

// List returns all active sessions.
func (p *Pool) List() []*Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := make([]*Session, 0, len(p.sessions))
	for _, s := range p.sessions {
		list = append(list, s)
	}
	return list
}

// RegisterHandlers registers streaming API endpoints.
func (p *Pool) RegisterHandlers(mux *http.ServeMux) {
	// List all streaming sessions
	mux.HandleFunc("GET /api/stream/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.List())
	})

	// Launch a new streaming session
	mux.HandleFunc("POST /api/stream/launch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []string `json:"env"`
			Width   int      `json:"width"`
			Height  int      `json:"height"`
			FPS     int      `json:"fps"`
			Restart bool     `json:"restart"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if req.Command == "" {
			http.Error(w, `{"error":"command required"}`, 400)
			return
		}
		sess, err := p.Launch(LaunchOpts{
			ID: req.ID, Name: req.Name, Command: req.Command,
			Args: req.Args, Env: req.Env,
			Width: req.Width, Height: req.Height, FPS: req.FPS,
			Restart: req.Restart,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sess)
	})

	// Resize a session's virtual display
	mux.HandleFunc("POST /api/stream/resize", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string `json:"id"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		sess := p.Get(req.ID)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, 404)
			return
		}
		if err := sess.Resize(req.Width, req.Height); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"resized"}`))
	})

	// Stop a session
	mux.HandleFunc("POST /api/stream/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if err := p.Stop(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"stopped"}`))
	})

	// Launch a VNC streaming session
	mux.HandleFunc("POST /api/stream/vnc", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Password string `json:"password"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			FPS      int    `json:"fps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		sess, err := p.LaunchVNC(VNCOpts{
			ID: req.ID, Name: req.Name,
			Host: req.Host, Port: req.Port, Password: req.Password,
			Width: req.Width, Height: req.Height, FPS: req.FPS,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sess)
	})

	// WebRTC signaling for a session
	mux.HandleFunc("GET /api/stream/ws", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		sess := p.Get(id)
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, 404)
			return
		}
		sess.HandleSignaling(w, r)
	})
}
