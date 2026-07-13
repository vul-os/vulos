package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"vulos/backend/internal/safedial"
	"vulos/backend/services/gpu"
	"vulos/backend/services/input"

	"github.com/pion/webrtc/v4"
)

// EncoderElementName is the stable GStreamer pipeline name assigned to the video
// encoder element in every session pipeline (name=venc in EncoderArgs).
// It can be used to look up the element via gst_bin_get_by_name for property
// changes such as adaptive-bitrate updates.
const EncoderElementName = "venc"

// lookPath is exec.LookPath by default; swapped in tests to mock binary presence.
var lookPath = exec.LookPath

// Pool manages multiple concurrent streaming sessions.
// Each session gets its own Xvfb display, GStreamer pipeline, and WebRTC tracks.
type Pool struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	nextDisplay      int
	nextPort         int
	resolveUserHome  func(r *http.Request) string // resolves user home from request context
	webAuthnVerifier A13_WebAuthnVerifier         // AUTH-13: verifier for input-injection re-auth
	// strictInputGate (AUTH-13): when true, input-injection sessions are gated
	// even if no real WebAuthn verifier is wired — in that case the stub verifier
	// rejects every assertion, so input stays PERMANENTLY blocked (fail-closed).
	// Default false: without a real verifier we do NOT arm the gate, because doing
	// so would silently brick input with no unlock path. Operators who want
	// strict fail-closed behaviour set this explicitly (VULOS_STREAM_STRICT_INPUT_GATE=1).
	strictInputGate bool
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

// SetUserHomeResolver sets a callback to resolve user home directories from HTTP requests.
func (p *Pool) SetUserHomeResolver(fn func(r *http.Request) string) {
	p.resolveUserHome = fn
}

// launchCageSession starts a headless wlroots compositor (cage) for a GPU-accelerated
// session. It creates a per-session XDG_RUNTIME_DIR under os.TempDir(), launches cage
// with an isolated WAYLAND_DISPLAY, and stores the runtime dir on sess so Stop() can
// clean it up.
//
// The Wayland socket name is "wayland-<id>" and the socket file lives at:
//
//	<cageRTDir>/wayland-<id>
//
// Callers must set sess.cageRTDir before calling this function (it is done below in
// Launch). On success sess.cage is populated.
func launchCageSession(ctx context.Context, sess *Session, cageBin string, appEnv []string) error {
	// cage needs a dummy command to run — it renders that command's window on the
	// headless compositor. We pass a no-op sleep so that cage stays alive while the
	// real app (started separately with WAYLAND_DISPLAY set) connects via XWayland or
	// native Wayland. Using "sleep infinity" keeps cage resident without consuming CPU.
	cageCmd := exec.CommandContext(ctx, cageBin, "--", "sleep", "infinity")
	cageCmd.Env = appEnv
	cageCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cageCmd.Stdout = os.Stdout
	cageCmd.Stderr = os.Stderr
	if err := cageCmd.Start(); err != nil {
		return fmt.Errorf("cage: %w", err)
	}
	sess.cage = cageCmd

	// Wait up to 2 s for the Wayland socket to appear
	wayland := sess.cageRTDir + "/" + waylandDisplay(sess.ID)
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(wayland); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Socket didn't appear — kill cage and return an error so caller falls back.
	syscall.Kill(-cageCmd.Process.Pid, syscall.SIGKILL)
	sess.cage = nil
	return fmt.Errorf("cage: wayland socket %s not ready after 2s", wayland)
}

// waylandDisplay returns the per-session WAYLAND_DISPLAY name.
func waylandDisplay(id string) string {
	return "wayland-" + id
}

// SetWebAuthnVerifier configures the WebAuthn verifier used for input-injection
// re-auth (AUTH-13). Call this from main.go once the passkeys backend is wired.
// If never called, RequireAssertion uses the stub verifier that always rejects.
func (p *Pool) SetWebAuthnVerifier(v A13_WebAuthnVerifier) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.webAuthnVerifier = v
}

// WebAuthnVerifier returns the configured verifier (may be nil — caller should
// fall back to the stub).
func (p *Pool) WebAuthnVerifier() A13_WebAuthnVerifier {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.webAuthnVerifier
}

// SetStrictInputGate enables fail-closed AUTH-13 gating even when no real
// WebAuthn verifier is configured. See the strictInputGate field for the
// safety trade-off. Default is off.
func (p *Pool) SetStrictInputGate(strict bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strictInputGate = strict
}

// shouldGateInput reports whether a newly launched input-injection session
// should start in the gated state (AUTH-13). We ONLY arm the gate when it can
// actually be lifted — i.e. a real WebAuthn verifier is wired — unless the
// operator has explicitly opted into strict fail-closed gating. Arming with only
// the reject-all stub verifier would permanently block legitimate input.
func (p *Pool) shouldGateInput() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.strictInputGate {
		return true
	}
	return p.webAuthnVerifier != nil
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
	// FPS for GStreamer capture (default 60). Clamped to {30,60,90,120,144}.
	FPS int
	// Restart: if true, restart the app if it exits.
	Restart bool
	// UserHome is the home directory of the requesting user (e.g. /home/alice).
	UserHome string
	// Gaming enables gaming-mode encoder profile and bitrate tiers.
	// When true: zero-latency encoder args, no B-frames, no lookahead,
	// Opus 10ms frames, and gaming bitrate tiers (6000–10000 kbps).
	// When false (default): byte-identical to the pre-gaming-mode path.
	Gaming bool
	// UserID is the authenticated Vulos user ID. Used for WebAuthn re-auth (AUTH-13).
	UserID string
}

// Launch starts a new streaming session: Xvfb + app + GStreamer + WebRTC.
func (p *Pool) Launch(opts LaunchOpts) (*Session, error) {
	if opts.Width == 0 {
		opts.Width = 1280
	}
	if opts.Height == 0 {
		opts.Height = 720
	}
	// allowedFPS is the set of supported frame rates; 0/unset defaults to 60.
	allowedFPS := []int{30, 60, 90, 120, 144}
	if opts.FPS == 0 {
		opts.FPS = 60
	} else {
		// Clamp to the nearest allowed value.
		clamped := allowedFPS[0]
		for _, f := range allowedFPS {
			if opts.FPS >= f {
				clamped = f
			}
		}
		opts.FPS = clamped
	}
	if opts.ID == "" {
		opts.ID = fmt.Sprintf("stream-%d", time.Now().UnixMilli())
	}

	// Atomically check-and-reserve the ID under one lock region to close the
	// check-then-insert TOCTOU: two concurrent launches of the same explicit ID
	// used to both pass the existence check (which was in a separate lock region
	// from the final insert), each build a full session, and the second insert
	// would overwrite — and thereby orphan — the first session's processes.
	//
	// We reserve the slot by inserting a placeholder Session now. A concurrent
	// duplicate then observes the placeholder and returns it (dedupe, as before),
	// rather than launching a second orphaning session. On any launch failure
	// below we release the reservation (see releaseReservation) so the ID can be
	// retried.
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

	display := fmt.Sprintf(":%d", displayNum)
	ctx, cancel := context.WithCancel(context.Background())

	sess := &Session{
		ID:         opts.ID,
		Name:       opts.Name,
		Display:    display,
		Width:      opts.Width,
		Height:     opts.Height,
		FPS:        opts.FPS,
		Running:    true,
		OwnerID:    opts.UserID,
		ctx:        ctx,
		cancel:     cancel,
		videoPort:  videoPort,
		audioPort:  audioPort,
		displayNum: displayNum,
		bitrate:    1500, // default medium quality in kbps
		bitrateC:   make(chan int, 1),
		// Debounce channels for FPS and MangoHud changes (capacity 1).
		fpsC:      make(chan int, 1),
		mangoHudC: make(chan bool, 1),
		// STREAMWIN-03: idle lifecycle.
		Gaming:      opts.Gaming,
		normalFPS:   opts.FPS,
		lastInputAt: time.Now(),
	}
	// Reserve the ID slot immediately so a concurrent same-ID Launch dedupes
	// against this placeholder instead of building a second session. The
	// Encoder field is filled in below once gpu.Detect() runs.
	p.sessions[opts.ID] = sess
	p.mu.Unlock()

	// releaseReservation removes the placeholder if a later launch step fails,
	// so a failed Launch does not permanently squat the ID. It only deletes the
	// slot when it still points at *this* session (never clobbering a session a
	// concurrent caller may have since installed under the same ID).
	releaseReservation := func() {
		p.mu.Lock()
		if p.sessions[opts.ID] == sess {
			delete(p.sessions, opts.ID)
		}
		p.mu.Unlock()
	}

	gpuInfo := gpu.Detect()
	sess.Encoder = gpuInfo.Encoder

	// Determine whether to use cage (headless Wayland) or fall back to Xvfb.
	// We use cage only when:
	//   (a) GPU tier is not software (VA-API or NVENC), AND
	//   (b) the cage binary is present on PATH.
	cageBin, cageErr := lookPath("cage")
	useCage := gpuInfo.Tier != gpu.TierSoftware && cageErr == nil

	// Resolve user home — use provided UserHome, fall back to $HOME, then /root
	home := opts.UserHome
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = "/root"
	}

	// Each session gets isolated runtime/config dirs so apps don't see each other's lock files.
	// For the cage path the runtime dir also hosts the Wayland socket.
	sessionHome := fmt.Sprintf("/tmp/stream-%s", opts.ID)
	os.MkdirAll(sessionHome, 0700)
	os.MkdirAll(sessionHome+"/tmp", 0755)
	os.MkdirAll(sessionHome+"/.config", 0755)
	os.MkdirAll(sessionHome+"/.local/share", 0755)
	os.MkdirAll(sessionHome+"/.cache", 0755)

	var appEnv []string

	if useCage {
		// ── Cage / headless Wayland path ─────────────────────────────────────
		// Per-session runtime dir that also holds the Wayland socket.
		cageRTDir := fmt.Sprintf("%s/vulos-stream-%s", os.TempDir(), opts.ID)
		os.MkdirAll(cageRTDir, 0700)
		sess.cageRTDir = cageRTDir

		wlDisp := waylandDisplay(opts.ID)
		appEnv = append(os.Environ(),
			"HOME="+home,
			"XDG_RUNTIME_DIR="+cageRTDir,
			"WAYLAND_DISPLAY="+wlDisp,
			"XDG_CONFIG_HOME="+sessionHome+"/.config",
			"XDG_DATA_HOME="+sessionHome+"/.local/share",
			"XDG_CACHE_HOME="+sessionHome+"/.cache",
			"TMPDIR="+sessionHome+"/tmp",
		)
		appEnv = append(appEnv, opts.Env...)

		if err := launchCageSession(ctx, sess, cageBin, appEnv); err != nil {
			log.Printf("[stream] cage launch failed (%v); falling back to Xvfb", err)
			useCage = false
		}
	}

	if !useCage {
		// ── Xvfb / X11 path (unchanged) ──────────────────────────────────────
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
			releaseReservation()
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
			releaseReservation()
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

		appEnv = append(os.Environ(),
			"DISPLAY="+display,
			"HOME="+home,
			"XDG_RUNTIME_DIR="+sessionHome,
			"XDG_CONFIG_HOME="+sessionHome+"/.config",
			"XDG_DATA_HOME="+sessionHome+"/.local/share",
			"XDG_CACHE_HOME="+sessionHome+"/.cache",
			"TMPDIR="+sessionHome+"/tmp",
		)
		appEnv = append(appEnv, opts.Env...)

		// 1c. Launch matchbox window manager (auto-maximizes all windows, constrains dialogs)
		if wmBin, err := lookPath("matchbox-window-manager"); err == nil {
			sess.wm = exec.CommandContext(ctx, wmBin, "-use_titlebar", "no", "-use_desktop", "no")
			sess.wm.Env = appEnv
			sess.wm.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := sess.wm.Start(); err != nil {
				log.Printf("[stream] matchbox-wm start warning: %v", err)
			} else {
				time.Sleep(300 * time.Millisecond) // Let WM initialize
			}
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
		releaseReservation()
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

	gstBin, _ := lookPath("gst-launch-1.0")
	if gstBin != "" {
		// buildVideoCmd constructs a gst-launch command with the current session bitrate.
		// Called on initial launch and on every adaptive-bitrate restart.
		// gstClientBin holds the path to `gst-client` if available.
		// gst-client can set element properties on a running gst-launch-1.0
		// pipeline via the GStreamer debug/control TCP socket.
		// STREAMWIN-05: used for live bitrate updates without pipeline restart.
		gstClientBin, _ := lookPath("gst-client")

		sess.buildVideoCmd = func() *exec.Cmd {
			sess.mu.Lock()
			kbps := sess.bitrate
			currentFPS := sess.FPS
			mangoHud := sess.MangoHud
			sess.mu.Unlock()
			args := []string{"-q"}
			// Capture source: PipeWire DMA-BUF when available, ximagesrc fallback
			args = append(args, gpuInfo.CaptureArgs(display, currentFPS, opts.Gaming)...)
			// Color conversion / GPU upload (DMA-BUF for VA-API, CUDA for NVENC, CPU for software)
			args = append(args, "!")
			args = append(args, gpuInfo.ConvertArgs()...)
			args = append(args, "!", "queue", "max-size-buffers=1", "leaky=downstream")
			args = append(args, "!")
			// STREAMWIN-05: insert videorate (name=vrate) so FPS changes can be applied
			// live by adjusting the caps negotiation rate without a full pipeline restart.
			args = append(args, "videorate", "name=vrate", "!")
			args = append(args, fmt.Sprintf("video/x-raw,framerate=%d/1", currentFPS), "!")
			// Encoder: gaming profile (zero-latency, no B-frames) or standard profile
			if opts.Gaming {
				args = append(args, gpuInfo.GamingEncoderArgs(opts.FPS, QualityGaming.Bitrate())...)
			} else {
				args = append(args, encoderArgsWithBitrate(gpuInfo, kbps)...)
			}
			args = append(args, "!")
			args = append(args, gpuInfo.PayloaderArgs()...)
			args = append(args, "!",
				"udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", videoPort),
				"sync=false", "async=false",
			)
			cmd := exec.CommandContext(ctx, gstBin, args...)
			if useCage {
				// PipeWire captures from the Wayland compositor: supply the session env.
				cmd.Env = append(appEnv, "GST_DEBUG=1")
				if mangoHud {
					cmd.Env = append(cmd.Env, "MANGOHUD=1")
				}
			} else {
				gstEnv := append(os.Environ(), "DISPLAY="+display)
				if mangoHud {
					gstEnv = append(gstEnv, "MANGOHUD=1")
				}
				cmd.Env = gstEnv
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd
		}

		// tryLiveBitrateUpdate attempts to set the encoder bitrate live via gst-client
		// without restarting the pipeline (STREAMWIN-05).
		// Returns true if the live update succeeded; false means the caller must fall
		// back to the restart path.
		tryLiveBitrateUpdate := func(kbps int) bool {
			if gstClientBin == "" {
				return false
			}
			// Determine property name and value for each encoder.
			// vp8enc uses target-bitrate in bps; nvenc/vaapi use bitrate in kbps.
			var prop, val string
			switch gpuInfo.Encoder {
			case "nvh264enc", "nvav1enc":
				prop = "bitrate"
				val = fmt.Sprintf("%d", kbps)
			case "vaapih264enc", "vaav1enc":
				prop = "bitrate"
				val = fmt.Sprintf("%d", kbps)
			default: // vp8enc
				prop = "target-bitrate"
				val = fmt.Sprintf("%d", kbps*1000)
			}
			// gst-client pipeline_name element_name property_name property_value
			// Pipeline name defaults to "pipeline0" for gst-launch-1.0.
			cmd := exec.Command(gstClientBin,
				"set_property", "pipeline0", EncoderElementName, prop, val)
			if err := cmd.Run(); err != nil {
				return false
			}
			log.Printf("[stream] %s: live bitrate update → %dkbps (no restart)", sess.Name, kbps)
			return true
		}

		// Video pipeline — adaptive-bitrate restarts via runWithBackoff (STREAM-04).
		// FPS / MangoHud change controller — watches debounce channels from SetFPS and
		// SetMangoHud, then kills the current gstVideo process so runWithBackoff restarts
		// it with updated parameters.  Mirrors the STREAM-04 ABR channel pattern.
		go func() {
			const debounce = 300 * time.Millisecond
			var debounceTimer *time.Timer
			triggerRestart := func() {
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(debounce, func() {
					sess.mu.Lock()
					cur := sess.gstVideo
					sess.mu.Unlock()
					if cur != nil && cur.Process != nil {
						log.Printf("[stream] %s: restarting video pipeline (fps=%d mangohud=%v)",
							sess.Name, sess.FPS, sess.MangoHud)
						syscall.Kill(-cur.Process.Pid, syscall.SIGTERM)
					}
				})
			}
			for {
				select {
				case <-ctx.Done():
					return
				case fps, ok := <-sess.fpsC:
					if !ok {
						return
					}
					log.Printf("[stream] %s: FPS change request → %d", sess.Name, fps)
					triggerRestart()
				case on, ok := <-sess.mangoHudC:
					if !ok {
						return
					}
					log.Printf("[stream] %s: MangoHud change request → %v", sess.Name, on)
					triggerRestart()
				}
			}
		}()

		// STREAMWIN-01: Assign videoStartFn so peerConnect() can launch the video
		// pipeline on the first peer connection. The pipeline is NOT started here;
		// it will be started on the 0→1 peer transition.
		sess.videoStartFn = func() {
			runWithBackoff(ctx, sess.Name+"-video", sess.buildVideoCmd, &sess.gstVideo)
		}

		// STREAMWIN-05: Debounced adaptive-bitrate change handler.
		// On each bitrateC signal:
		//   1. Try a live property set via gst-client (no restart, no black frame).
		//   2. If gst-client is unavailable or fails, fall back to the old SIGTERM restart.
		// The debounce window is kept at 5 s so rapid ABR oscillations don't thrash
		// the encoder; only the live path avoids the visible restart artefact.
		go func() {
			const debounce = 5 * time.Second
			timer := time.NewTimer(debounce)
			timer.Stop()
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-sess.bitrateC:
					// Reset/restart the debounce window on every new signal.
					timer.Stop()
					timer.Reset(debounce)
				case <-timer.C:
					sess.mu.Lock()
					kbps := sess.bitrate
					cmd := sess.gstVideo
					sess.mu.Unlock()
					// Attempt live update first (STREAMWIN-05 happy path).
					if tryLiveBitrateUpdate(kbps) {
						// Live update succeeded — no restart needed.
						continue
					}
					// Fallback: restart the pipeline (legacy path).
					log.Printf("[stream] adaptive bitrate restart (fallback): encoder=%s bitrate=%dkbps",
						sess.Encoder, kbps)
					if cmd != nil && cmd.Process != nil {
						syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
					}
				}
			}
		}()

		// Audio pipeline — captures from virtual speaker monitor (all app audio).
		// Uses pipewiresrc when PipeWire is available; falls back to pulsesrc.
		go runWithBackoff(ctx, sess.Name+"-audio", func() *exec.Cmd {
			// Gaming mode: 10ms Opus frames for lower audio latency.
			// Normal mode: 20ms frames (standard quality/CPU trade-off).
			opusFrameSize := "20"
			if opts.Gaming {
				opusFrameSize = "10"
			}
			args := []string{"-q"}
			args = append(args, audioSourceArgs(gpuInfo)...)
			args = append(args,
				"!", "audio/x-raw,rate=48000,channels=2",
				"!", "queue", "max-size-buffers=1", "leaky=downstream",
				"!", "opusenc", "bitrate=128000", "frame-size="+opusFrameSize,
				"!", "rtpopuspay", "pt=111",
				"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", audioPort),
				"sync=false", "async=false",
			)
			cmd := exec.CommandContext(ctx, gstBin, args...)
			if useCage {
				cmd.Env = appEnv
			} else {
				cmd.Env = append(os.Environ(), "DISPLAY="+display)
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd
		}, &sess.gstAudio)
	}

	// 6. Monitor app exit — detect stale processes and auto-cleanup
	appStartTime := time.Now()
	go func() {
		sess.app.Wait()
		if ctx.Err() != nil {
			// Context cancelled — clean shutdown
			sess.Stop()
			p.mu.Lock()
			delete(p.sessions, opts.ID)
			p.mu.Unlock()
			return
		}

		elapsed := time.Since(appStartTime)

		// Quick exit (<3s) likely means stale process holding a lock
		if elapsed < 3*time.Second {
			log.Printf("[stream] %s exited too quickly (%.1fs) — killing stale %s processes and retrying",
				opts.Name, elapsed.Seconds(), opts.Command)
			// Kill stale processes by command name
			binName := filepath.Base(opts.Command)
			exec.Command("pkill", "-9", binName).Run()
			// Clean up stale lock files
			os.RemoveAll(sessionHome)
			os.MkdirAll(sessionHome, 0755)
			os.MkdirAll(sessionHome+"/tmp", 0755)
			os.MkdirAll(sessionHome+"/.config", 0755)
			os.MkdirAll(sessionHome+"/.local/share", 0755)
			os.MkdirAll(sessionHome+"/.cache", 0755)
			time.Sleep(time.Second)
		}

		if opts.Restart {
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

	// AUTH-13: arm the input-injection re-auth gate. When this session carries an
	// input injector, remote mouse/keyboard/gamepad events can drive the host, so
	// we start GATED — all injected input is dropped until the client completes a
	// WebAuthn assertion (POST /api/stream/webauthn-assert), which flips
	// inputGated=false. We only arm when the gate can actually be lifted (a real
	// verifier is wired) OR the operator opted into strict fail-closed gating;
	// otherwise the reject-all stub would block input permanently. See
	// shouldGateInput.
	if sess.injector != nil && p.shouldGateInput() {
		sess.mu.Lock()
		sess.inputGated = true
		sess.mu.Unlock()
		saWebauthn_register(sess.ID, opts.UserID)
		log.Printf("[stream] AUTH-13: input injection gated for session %s until WebAuthn assertion", opts.ID)
	} else if sess.injector != nil {
		log.Printf("[stream] AUTH-13 WARNING: input injection is NOT gated for session %s "+
			"(no WebAuthn verifier wired and strict gating off) — remote input flows unauthenticated. "+
			"Wire passkeys or set VULOS_STREAM_STRICT_INPUT_GATE=1 to enforce.", opts.ID)
	}

	// STREAMWIN-03: start idle lifecycle watcher (no-op when gaming=true).
	sess.startIdleWatcher()

	// The session was already registered under opts.ID as the reservation
	// placeholder at the top of Launch (this is the same *Session pointer), so
	// no final insert is needed. Re-inserting unconditionally would risk
	// resurrecting the entry if the app-exit monitor goroutine already deleted
	// it on a fast app exit; the reservation model makes that write redundant.

	log.Printf("[stream] launched %q on %s (encoder=%s, %dx%d@%dfps, gaming=%v)",
		opts.Name, display, gpuInfo.Encoder, opts.Width, opts.Height, opts.FPS, opts.Gaming)
	return sess, nil
}

// audioSourceArgs returns GStreamer audio source arguments for the capture pipeline.
// When PipeWire is available (detected by gpu.Info.HasPipeWire), it uses pipewiresrc
// targeting the virtual_speaker monitor node. Otherwise it falls back to the original
// pulsesrc path (byte-identical to the pre-STREAM-06 pipeline).
func audioSourceArgs(g gpu.Info) []string {
	if g.HasPipeWire {
		return []string{"pipewiresrc", `target-object=virtual_speaker.monitor`}
	}
	// PulseAudio fallback — unchanged from original pipeline
	return []string{"pulsesrc", "device=virtual_speaker.monitor"}
}

// Get returns a session by ID.
func (p *Pool) Get(id string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// GetForUser returns a session by ID only if it is owned by userID.
// Returns nil when the session does not exist OR when the caller is not the
// owner.  Use this instead of Get on any endpoint that lets a user mutate
// or observe a session (resize, stop, fps, mangohud, WebRTC signaling).
//
// Exception: admin callers (adminUserID == "") are NOT exempt — use the
// adminGate callback in RegisterHandlers for privilege escalation.
// Sessions launched without an owner (OwnerID == "") are accessible by any
// authenticated user (e.g. system-level sessions launched from the CLI).
func (p *Pool) GetForUser(id, userID string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[id]
	if !ok {
		return nil
	}
	// Sessions without an owner are accessible to any authenticated caller.
	if sess.OwnerID == "" {
		return sess
	}
	if sess.OwnerID != userID {
		return nil
	}
	return sess
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

// isBlockedVNCHost returns true when the given host resolves to a loopback,
// private, link-local, CGNAT, multicast, or unspecified address, preventing
// SSRF via the VNC host parameter.  Fail-closed on DNS resolution errors.
//
// Delegates to safedial.ValidateHost so the canonical deny-list (including
// hex/decimal/octal obfuscated IP literals and CGNAT ranges) is applied
// consistently across all services.
func isBlockedVNCHost(host string) bool {
	_, err := safedial.ValidateHost(host, false)
	return err != nil
}

// blockedEnvPrefixes lists env-var prefixes that must not be forwarded to
// exec'd processes to prevent library-injection attacks (VULN-04).
var blockedEnvPrefixes = []string{
	"ld_preload=",
	"ld_library_path=",
	"ld_audit=",
	"ld_debug=",
	"dyld_insert_libraries=",
	"python_path=",
}

// validateLaunchEnv returns an error if any element of env is a blocked var.
func validateLaunchEnv(env []string) error {
	for _, e := range env {
		lower := strings.ToLower(e)
		for _, prefix := range blockedEnvPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return fmt.Errorf("forbidden env var: %s", strings.SplitN(e, "=", 2)[0])
			}
		}
	}
	return nil
}

// RegisterHandlers registers streaming API endpoints.
// isAdmin is a callback that returns true when the request comes from an admin user.
// Pass nil to skip the admin gate (not recommended for production).
func (p *Pool) RegisterHandlers(mux *http.ServeMux, isAdmin ...func(*http.Request) bool) {
	adminGate := func(r *http.Request) bool { return true }
	if len(isAdmin) > 0 && isAdmin[0] != nil {
		adminGate = isAdmin[0]
	}

	// List streaming sessions — returns only sessions owned by the caller.
	// Admin users see all sessions (adminGate).
	mux.HandleFunc("GET /api/stream/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		userID := r.Header.Get("X-User-ID")
		if adminGate(r) && userID == "" {
			// Admin bearer token with no user ID (system context) — list all.
			json.NewEncoder(w).Encode(p.List())
			return
		}
		// Filter to sessions owned by this user (plus unowned system sessions).
		all := p.List()
		visible := make([]*Session, 0, len(all))
		for _, s := range all {
			if s.OwnerID == "" || s.OwnerID == userID {
				visible = append(visible, s)
			}
		}
		json.NewEncoder(w).Encode(visible)
	})

	// Launch a new streaming session (admin only — arbitrary command execution)
	mux.HandleFunc("POST /api/stream/launch", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(r) {
			http.Error(w, `{"error":"admin only"}`, 403)
			return
		}
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
			Gaming  bool     `json:"gaming"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if req.Command == "" {
			http.Error(w, `{"error":"command required"}`, 400)
			return
		}
		// Block dangerous env vars (VULN-04 / env-var injection prevention).
		if err := validateLaunchEnv(req.Env); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 400)
			return
		}
		// Resolve user home from request
		userHome := ""
		if p.resolveUserHome != nil {
			userHome = p.resolveUserHome(r)
		}
		sess, err := p.Launch(LaunchOpts{
			ID: req.ID, Name: req.Name, Command: req.Command,
			Args: req.Args, Env: req.Env,
			Width: req.Width, Height: req.Height, FPS: req.FPS,
			Restart: req.Restart, UserHome: userHome,
			Gaming: req.Gaming,
			UserID: r.Header.Get("X-User-ID"),
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
		sess := p.GetForUser(req.ID, r.Header.Get("X-User-ID"))
		if sess == nil {
			// Return 404 to avoid revealing whether the session exists.
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

	// Stop a session — caller must own the session (or it must be unowned).
	mux.HandleFunc("POST /api/stream/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		// Ownership check before stopping.
		if p.GetForUser(id, r.Header.Get("X-User-ID")) == nil {
			http.Error(w, `{"error":"session not found"}`, 404)
			return
		}
		if err := p.Stop(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"stopped"}`))
	})

	// Launch a VNC streaming session (admin only — SSRF risk via host parameter)
	mux.HandleFunc("POST /api/stream/vnc", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(r) {
			http.Error(w, `{"error":"admin only"}`, 403)
			return
		}
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
		// Validate host to prevent SSRF to loopback / metadata addresses.
		if req.Host != "" && isBlockedVNCHost(req.Host) {
			http.Error(w, `{"error":"vnc host resolves to a restricted address"}`, 403)
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

	// WebRTC signaling for a session — caller must own the session.
	mux.HandleFunc("GET /api/stream/ws", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		sess := p.GetForUser(id, r.Header.Get("X-User-ID"))
		if sess == nil {
			http.Error(w, `{"error":"session not found"}`, 404)
			return
		}
		sess.HandleSignaling(w, r)
	})
}

// SetBitrate signals the session to restart the video encoder at the given kbps.
// The restart is debounced by ≥5 s (handled by the goroutine started in Launch).
// Safe to call from any goroutine; non-blocking.
func (s *Session) SetBitrate(kbps int) {
	if s.bitrateC == nil {
		return // session launched without GStreamer (e.g. unit tests)
	}
	// Drain any pending signal so the channel never blocks.
	select {
	case <-s.bitrateC:
	default:
	}
	// Send the new target; the debounce goroutine picks it up.
	select {
	case s.bitrateC <- kbps:
	default:
	}
}

// encoderArgsWithBitrate builds GStreamer encoder args using an explicit bitrate (kbps).
// Units: vp8enc expects bps (target-bitrate), nvenc/vaapi expect kbps (bitrate).
func encoderArgsWithBitrate(g gpu.Info, kbps int) []string {
	if g.HasAV1 {
		switch g.Tier {
		case gpu.TierNVENC:
			return []string{
				"nvav1enc",
				fmt.Sprintf("bitrate=%d", kbps), "preset=low-latency-hq", "rc-mode=cbr",
				"gop-size=30",
			}
		case gpu.TierVAAPI:
			return []string{
				"vaav1enc",
				fmt.Sprintf("bitrate=%d", kbps), "rate-control=cbr",
				"keyframe-period=30",
			}
		}
	}
	switch g.Tier {
	case gpu.TierNVENC:
		return []string{
			"nvh264enc",
			fmt.Sprintf("bitrate=%d", kbps), "preset=low-latency-hq", "rc-mode=cbr",
			"gop-size=30",
		}
	case gpu.TierVAAPI:
		return []string{
			"vaapih264enc",
			fmt.Sprintf("bitrate=%d", kbps), "rate-control=cbr",
			"keyframe-period=30",
		}
	default:
		// vp8enc: target-bitrate is in bps
		return []string{
			"vp8enc",
			fmt.Sprintf("target-bitrate=%d", kbps*1000), "cpu-used=8", "deadline=1",
			"keyframe-max-dist=30", "threads=4", "end-usage=cbr",
			"undershoot=95", "buffer-size=6000", "buffer-initial-size=4000",
			"lag-in-frames=0", "error-resilient=1",
		}
	}
}
