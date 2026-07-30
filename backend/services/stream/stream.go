// no-broker-dep:allow-file: comment states ICE servers come exclusively from
// relayconfig.ICEServers, 'the box's single, owner-configurable
// relay/TURN provider (ephor default; BYO...)' -- legacy shorthand for
// the built-in provider carried over from before the Vulos/Ephor
// provider split (providers.go's providerFor default case actually
// returns vulosProvider{}, not ephor). Stale-terminology finding reported
// separately, not fixed here.

// Package stream provides a generic X11 app streaming service.
// It manages Xvfb displays, GStreamer capture/encode pipelines, WebRTC
// transport, and input injection for any graphical application.
//
// Usage:
//
//	pool := stream.NewPool()
//	sess, _ := pool.Launch("kicad", "/usr/bin/kicad", nil, 1280, 720)
//	// sess.HandleSignaling(w, r) for WebRTC
//	// sess.Stop() to kill
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"vulos/backend/internal/wsutil"
	"vulos/backend/services/input"
	"vulos/backend/services/relayconfig"

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v4"
)

// pionICEServers converts the box's currently-configured relay/reachability
// ICE list (relayconfig.ICEServers) into Pion's webrtc.ICEServer shape. See
// stream.go's HandleSignaling for why this package never hardcodes STUN/TURN.
func pionICEServers(ctx context.Context, userID string) []webrtc.ICEServer {
	resolved := relayconfig.ICEServers(ctx, userID)
	servers := make([]webrtc.ICEServer, 0, len(resolved))
	for _, s := range resolved {
		servers = append(servers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}
	return servers
}

// inputInjector is the seam the input handlers drive. *input.Injector satisfies
// it structurally; tests substitute a recording fake to assert that every remote
// input event is CLAMPED/VALIDATED (GAME-INPUT-01) before it reaches injection.
// Keeping it an interface means the cage-safety boundary is unit-testable without
// a real /dev/uinput device or GPU hardware.
type inputInjector interface {
	MouseMove(x, y int)
	MouseMoveRel(dx, dy int)
	MouseButton(button int, pressed bool)
	Scroll(clicks int)
	SyncModifiers(clientMod int)
	KeyPress(jsKey, jsCode string, pressed bool)
	GamepadButton(index int, pressed bool)
	GamepadAxis(index int, value float64)
	GamepadTrigger(index int, value float64)
	RumbleChan() <-chan input.RumbleEvent
	Close()
}

// Session is a single streaming app: Xvfb + app process + GStreamer + WebRTC tracks.
type Session struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Display  string `json:"display"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FPS      int    `json:"fps"`
	Running  bool   `json:"running"`
	Encoder  string `json:"encoder"`
	Quality  string `json:"quality"`  // current adaptive quality level
	MangoHud bool   `json:"mangohud"` // whether MangoHud overlay is active
	// OwnerID is the authenticated user ID that launched this session.
	// Set from X-User-ID at launch time; used to enforce per-user session isolation.
	// Empty only for sessions launched without an authenticated context (e.g. CLI).
	OwnerID string `json:"owner_id,omitempty"`
	// Gaming reports whether this session runs the low-latency gaming-mode profile
	// (zero-latency encoder, gaming bitrate tiers, no idle/throttle/step-down).
	// Set from LaunchOpts.Gaming; exposed so the client can engage gaming input
	// behaviour (pointer-lock, split unreliable input channels) for real games only.
	// STREAMWIN-03: gaming=true makes all idle/throttle logic a no-op.
	Gaming bool `json:"gaming"`

	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	xvfb          *exec.Cmd
	wm            *exec.Cmd
	cage          *exec.Cmd // STREAM-08 headless wlroots compositor (GPU path)
	cageRTDir     string    // STREAM-08 per-session XDG_RUNTIME_DIR for cage socket
	app           *exec.Cmd
	gstVideo      *exec.Cmd
	gstAudio      *exec.Cmd
	videoTrack    *webrtc.TrackLocalStaticRTP
	audioTrack    *webrtc.TrackLocalStaticRTP
	videoPort     int
	audioPort     int
	displayNum    int
	bitrate       int              // current target bitrate in kbps
	bitrateC      chan int         // debounced bitrate change signals (SetBitrate → restart goroutine)
	buildVideoCmd func() *exec.Cmd // rebuilds video gst cmd with current bitrate
	injector      inputInjector
	fpsC          chan int  // GAME-08 FPS-change debounce signals
	mangoHudC     chan bool // GAME-08 MangoHud toggle signals
	// AUTH-13: WebAuthn re-auth gate.
	inputGated bool

	// STREAMWIN-01: connected-peer refcount.
	// On 0→1 the video encode pipeline is started; on 1→0 it is suspended (SIGSTOP).
	// All mutations are protected by mu.
	peerCount    int
	videoStartFn func() // called on 0→1 transition; assigned by Launch

	// STREAMWIN-03: idle lifecycle fields.
	lastInputAt time.Time // updated by noteInput on every input event
	normalFPS   int       // baseline FPS to restore after idle-fps throttle
	idleFPS     bool      // true while we are in the low-fps idle state
}

// Resize changes the Xvfb framebuffer resolution via xrandr.
// GStreamer ximagesrc auto-detects the new size.
func (s *Session) Resize(width, height int) error {
	if width < 320 || height < 200 || width > 3840 || height > 2160 {
		return fmt.Errorf("invalid resolution: %dx%d", width, height)
	}
	s.mu.Lock()
	display := s.Display
	s.Width = width
	s.Height = height
	s.mu.Unlock()

	// Add the new mode and apply it
	modeName := fmt.Sprintf("%dx%d", width, height)
	env := append(os.Environ(), "DISPLAY="+display)

	// Create new mode via xrandr
	cmd := exec.Command("xrandr", "--fb", modeName)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		// Fallback: try setting with --size
		cmd2 := exec.Command("xrandr", "-s", modeName)
		cmd2.Env = env
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("xrandr resize failed: %v", err2)
		}
	}

	log.Printf("[stream] resized %s to %dx%d", s.ID, width, height)
	return nil
}

// Stop kills the app process and all supporting processes.
func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Running {
		return
	}
	s.cancel()
	if s.injector != nil {
		s.injector.Close()
		saWebauthn_unregister(s.ID) // AUTH-13: clean up gate state
	}
	procs := []*exec.Cmd{s.gstAudio, s.gstVideo, s.app, s.wm, s.cage, s.xvfb}
	for _, cmd := range procs {
		if cmd != nil && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	time.Sleep(500 * time.Millisecond)
	for _, cmd := range procs {
		if cmd != nil && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	// Clean up X11 socket (Xvfb path)
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", s.displayNum))
	os.Remove(fmt.Sprintf("/tmp/.X%d-lock", s.displayNum))
	// Clean up cage per-session runtime dir (Wayland path)
	if s.cageRTDir != "" {
		os.RemoveAll(s.cageRTDir)
	}
	s.Running = false
	log.Printf("[stream] session %s (%s) stopped", s.ID, s.Name)
}

// SetFPS changes the capture framerate by signalling the video pipeline to restart.
// Calls are debounced — rapid calls coalesce and only the latest FPS wins.
// Valid range: 1–240. Values outside range are clamped.
func (s *Session) SetFPS(fps int) {
	if fps < 1 {
		fps = 1
	}
	if fps > 240 {
		fps = 240
	}
	s.mu.Lock()
	s.FPS = fps
	ch := s.fpsC
	s.mu.Unlock()
	if ch == nil {
		return
	}
	// Non-blocking send — if the channel already has a pending value, drain it
	// and replace with the latest so only one restart happens.
	select {
	case ch <- fps:
	default:
		// Drain old value and send new one (best-effort; if reader races, that's fine)
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- fps:
		default:
		}
	}
}

// SetMangoHud toggles the MangoHud overlay on the next GStreamer pipeline restart.
// Calls are debounced — only the latest value takes effect if called in rapid succession.
func (s *Session) SetMangoHud(on bool) {
	s.mu.Lock()
	s.MangoHud = on
	ch := s.mangoHudC
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- on:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- on:
		default:
		}
	}
}

// peerConnect increments the connected-peer refcount.
// On the 0→1 transition it resumes (SIGCONT) a suspended gstVideo process, or
// calls videoStartFn to kick off the video pipeline for the very first peer.
func (s *Session) peerConnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerCount++
	if s.peerCount == 1 {
		if s.gstVideo != nil && s.gstVideo.Process != nil {
			// Resume a SIGSTOP-suspended process (subsequent reconnects).
			syscall.Kill(-s.gstVideo.Process.Pid, syscall.SIGCONT)
			log.Printf("[stream] %s: video encode resumed (peer connected)", s.Name)
		} else if s.videoStartFn != nil {
			// Start for the very first peer (process was never launched yet).
			fn := s.videoStartFn
			go fn()
			log.Printf("[stream] %s: video encode started (first peer connected)", s.Name)
		}
	}
}

// peerDisconnect decrements the connected-peer refcount.
// On the 1→0 transition it suspends the gstVideo process with SIGSTOP so it
// consumes ~0 CPU while no viewer is watching.
func (s *Session) peerDisconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peerCount <= 0 {
		return
	}
	s.peerCount--
	if s.peerCount == 0 {
		if s.gstVideo != nil && s.gstVideo.Process != nil {
			syscall.Kill(-s.gstVideo.Process.Pid, syscall.SIGSTOP)
			log.Printf("[stream] %s: video encode suspended (no peers connected)", s.Name)
		}
	}
}

// HandleSignaling upgrades an HTTP request to WebSocket and runs WebRTC signaling.
// The client gets video + audio tracks and can send input via a data channel.
func (s *Session) HandleSignaling(w http.ResponseWriter, r *http.Request) {
	ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[stream] ws upgrade: %v", err)
		return
	}
	defer ws.Close()

	m := &webrtc.MediaEngine{}
	m.RegisterDefaultCodecs()
	ir := &interceptor.Registry{}
	pli, _ := intervalpli.NewReceiverInterceptor()
	ir.Add(pli)
	webrtc.RegisterDefaultInterceptors(m, ir)

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))
	// RELAY-01: ICE servers come EXCLUSIVELY from relayconfig.ICEServers — the
	// box's single, owner-configurable relay/TURN provider (ephor default;
	// BYO turn/libp2p/wireguard/none otherwise). This package must never
	// hardcode a STUN/TURN server itself.
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "guest"
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: pionICEServers(r.Context(), userID),
	})
	if err != nil {
		return
	}
	defer pc.Close()

	if s.videoTrack != nil {
		pc.AddTrack(s.videoTrack)
	}
	if s.audioTrack != nil {
		pc.AddTrack(s.audioTrack)
	}

	// Adaptive bitrate + resolution controller (STREAMWIN-04).
	// resizeFn is nil-safe — Resize only applies when gaming=false.
	var resizeFn func(w, h int)
	if !s.Gaming {
		resizeFn = func(w, h int) { s.Resize(w, h) }
	}
	bc := newBitrateControllerFull(pc, QualityMedium, func(q Quality) {
		s.mu.Lock()
		s.bitrate = q.Bitrate()
		s.Quality = q.String()
		s.mu.Unlock()
		s.SetBitrate(q.Bitrate())
	}, s.Gaming, resizeFn)
	defer bc.Close()

	var wsMu sync.Mutex
	wsWrite := func(data []byte) {
		wsMu.Lock()
		defer wsMu.Unlock()
		ws.WriteMessage(websocket.TextMessage, data)
	}

	// STREAMWIN-01: track connected peers so the video encode pipeline can be
	// started/suspended based on whether anyone is watching.
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.peerConnect()
			// AUTH-13: notify gated sessions that WebAuthn is required.
			if s.injector != nil {
				s.mu.Lock()
				isGated := s.inputGated
				s.mu.Unlock()
				if isGated {
					msg, _ := json.Marshal(map[string]string{"t": "need-webauthn"})
					wsWrite(msg)
				}
			}
		case webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			s.peerDisconnect()
		}
	})

	// Input data channels — split by priority for cloud-gaming grade input
	// Mouse: unreliable/unordered (latest-wins, high freq)
	// Keyboard: reliable/ordered (every event must arrive in sequence)
	// Gamepad: unreliable/unordered (full state snapshots)
	// Legacy "input" channel also supported for backwards compat
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "mouse":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleMouse(msg.Data)
			})
		case "keyboard":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleKeyboard(msg.Data)
			})
		case "input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleInput(msg.Data)
			})
		case "gamepad":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleGamepad(msg.Data)
			})
			// Forward FF_RUMBLE events from uinput back to the browser over
			// the same gamepad channel (server→client direction).
			dc.OnOpen(func() {
				if s.injector == nil || s.injector.RumbleChan() == nil {
					return
				}
				go rumbleForwardLoop(s.injector.RumbleChan(), dc, s.ctx)
			})
		}
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		b, _ := json.Marshal(map[string]any{"type": "candidate", "candidate": c.ToJSON()})
		wsWrite(b)
	})

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg["type"] {
		case "offer":
			sdp, _ := msg["sdp"].(string)
			pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp})
			answer, _ := pc.CreateAnswer(nil)
			pc.SetLocalDescription(answer)
			b, _ := json.Marshal(map[string]any{"type": "answer", "sdp": answer.SDP})
			wsWrite(b)

		case "candidate":
			c, ok := msg["candidate"].(map[string]any)
			if !ok {
				continue
			}
			candidate, _ := c["candidate"].(string)
			sdpMid, _ := c["sdpMid"].(string)
			sdpIdx, _ := c["sdpMLineIndex"].(float64)
			idx := uint16(sdpIdx)
			pc.AddICECandidate(webrtc.ICECandidateInit{
				Candidate: candidate, SDPMid: &sdpMid, SDPMLineIndex: &idx,
			})
		}
	}
}

// handleMouse processes mouse events from the dedicated mouse channel (unreliable/unordered).
// Message types:
//
//	mm  — absolute move (normal mode)
//	mr  — relative move delta (gaming / pointer-lock mode, no coalescing)
//	md  — mouse button down (includes absolute position)
//	mu  — mouse button up
//	sc  — scroll wheel
func (s *Session) handleMouse(data []byte) {
	if s.injector == nil {
		return
	}
	s.mu.Lock()
	gated := s.inputGated
	s.mu.Unlock()
	if gated {
		return // AUTH-13: block input until WebAuthn assertion is verified
	}
	var evt struct {
		T  string  `json:"t"`  // mm=move, mr=move-relative, md=down, mu=up, sc=scroll
		X  float64 `json:"x"`  // absolute X (mm/md) or delta X (mr)
		Y  float64 `json:"y"`  // absolute Y (mm/md) or delta Y (mr)
		DX float64 `json:"dx"` // alias delta X for mr (movementX)
		DY float64 `json:"dy"` // alias delta Y for mr (movementY)
		B  int     `json:"b"`  // button index
	}
	if json.Unmarshal(data, &evt) != nil {
		return
	}
	s.noteInput()
	// GAME-INPUT-01: clamp/validate every event against the session framebuffer
	// and the virtual-device envelope BEFORE it reaches the injector. This is the
	// cage-safety boundary — a remote peer cannot warp the pointer off-surface,
	// press an unadvertised button, or drive non-finite/huge values into uinput.
	s.mu.Lock()
	w, h := s.Width, s.Height
	s.mu.Unlock()
	switch evt.T {
	case "mm":
		x, y := boundAbs(int(finiteOrZero(evt.X)), int(finiteOrZero(evt.Y)), w, h)
		s.injector.MouseMove(x, y)
	case "mr":
		// Raw relative delta from pointer-lock movementX/movementY.
		// Frontend sends dx/dy; x/y are kept as aliases for forward compat.
		dx := finiteOrZero(evt.DX)
		dy := finiteOrZero(evt.DY)
		if dx == 0 && dy == 0 {
			dx, dy = finiteOrZero(evt.X), finiteOrZero(evt.Y)
		}
		rx, ry := boundRel(int(dx), int(dy))
		s.injector.MouseMoveRel(rx, ry)
	case "md":
		b, ok := boundMouseButton(evt.B)
		if !ok {
			return
		}
		x, y := boundAbs(int(finiteOrZero(evt.X)), int(finiteOrZero(evt.Y)), w, h)
		s.injector.MouseMove(x, y)
		s.injector.MouseButton(b, true)
	case "mu":
		b, ok := boundMouseButton(evt.B)
		if !ok {
			return
		}
		s.injector.MouseButton(b, false)
	case "sc":
		s.injector.Scroll(boundScroll(int(finiteOrZero(evt.Y))))
	}
}

// Modifier bitmask constants — must match frontend
const (
	modShift    = 1
	modCtrl     = 2
	modAlt      = 4
	modMeta     = 8
	modCapsLock = 16
)

// handleKeyboard processes keyboard events from the dedicated keyboard channel (reliable/ordered).
// Each event includes a modifier bitmask for state recovery if previous events were lost.
func (s *Session) handleKeyboard(data []byte) {
	if s.injector == nil {
		return
	}
	s.mu.Lock()
	gated := s.inputGated
	s.mu.Unlock()
	if gated {
		return // AUTH-13: block input until WebAuthn assertion is verified
	}
	var evt struct {
		T    string `json:"t"` // kd=keydown, ku=keyup
		Key  string `json:"key"`
		Code string `json:"code"`
		Mod  int    `json:"mod"` // modifier bitmask
	}
	if json.Unmarshal(data, &evt) != nil {
		return
	}
	// GAME-INPUT-01: drop oversized key/code strings and strip the modifier
	// bitmask to the defined bits before touching the injector.
	if !boundKeyString(evt.Key) || !boundKeyString(evt.Code) {
		return
	}

	s.noteInput()
	// Sync modifier state — reconcile held modifiers from bitmask (bounded).
	s.injector.SyncModifiers(boundMod(evt.Mod))

	switch evt.T {
	case "kd":
		s.injector.KeyPress(evt.Key, evt.Code, true)
	case "ku":
		s.injector.KeyPress(evt.Key, evt.Code, false)
	}
}

// handleInput processes legacy combined mouse/keyboard events (backwards compat).
func (s *Session) handleInput(data []byte) {
	if s.injector == nil {
		return
	}
	s.mu.Lock()
	gated := s.inputGated
	s.mu.Unlock()
	if gated {
		return // AUTH-13: block input until WebAuthn assertion is verified
	}
	var evt struct {
		Type   string  `json:"type"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Button int     `json:"button"`
		Key    string  `json:"key"`
		Code   string  `json:"code"`
	}
	if json.Unmarshal(data, &evt) != nil {
		return
	}

	s.noteInput()
	// GAME-INPUT-01: bound the legacy combined channel exactly like the split
	// channels — clamp coords, validate button, drop oversized key strings.
	s.mu.Lock()
	w, h := s.Width, s.Height
	s.mu.Unlock()
	x, y := boundAbs(int(finiteOrZero(evt.X)), int(finiteOrZero(evt.Y)), w, h)
	btn, btnOK := boundMouseButton(evt.Button)
	switch evt.Type {
	case "mousemove":
		s.injector.MouseMove(x, y)
	case "mousedown":
		if !btnOK {
			return
		}
		s.injector.MouseMove(x, y)
		s.injector.MouseButton(btn, true)
	case "mouseup":
		if !btnOK {
			return
		}
		s.injector.MouseButton(btn, false)
	case "click":
		if !btnOK {
			return
		}
		s.injector.MouseMove(x, y)
		s.injector.MouseButton(btn, true)
		s.injector.MouseButton(btn, false)
	case "scroll":
		s.injector.Scroll(boundScroll(int(finiteOrZero(evt.Y))))
	case "keydown":
		if !boundKeyString(evt.Key) || !boundKeyString(evt.Code) {
			return
		}
		s.injector.KeyPress(evt.Key, evt.Code, true)
	case "keyup":
		if !boundKeyString(evt.Key) || !boundKeyString(evt.Code) {
			return
		}
		s.injector.KeyPress(evt.Key, evt.Code, false)
	}
}

// noteInput records the current time as the most-recent input event.
// Called on every mouse/keyboard/gamepad message so the idle timer resets.
func (s *Session) noteInput() {
	s.mu.Lock()
	s.lastInputAt = time.Now()
	// If we are in idle-FPS mode, ramp back to normal immediately.
	if s.idleFPS && !s.Gaming {
		s.idleFPS = false
		fps := s.normalFPS
		s.mu.Unlock()
		log.Printf("[stream] %s: idle FPS ramp-up → %d fps (input received)", s.Name, fps)
		s.SetFPS(fps)
		return
	}
	s.mu.Unlock()
}

// idleThresholdFPS is the FPS to drop to when the session has been idle
// (no input events) for idleStaticThreshold. Not applied when Gaming=true.
const idleThresholdFPS = 2

// idleStaticThreshold is how long the session must be without input before
// the FPS is throttled to idleThresholdFPS.
const idleStaticThreshold = 10 * time.Second

// idleSuspendDuration is how long the session must be without input AND
// without any connected peer before Xvfb/app processes are suspended (SIGSTOP).
const idleSuspendDuration = 5 * time.Minute

// startIdleWatcher launches the background goroutine that monitors input-event
// recency and applies idle-FPS throttle and idle-suspend logic.
// Must be called once during Launch (with gaming flag already set on sess).
func (s *Session) startIdleWatcher() {
	if s.Gaming {
		return // STREAMWIN-03 guardrail: no-op for gaming sessions
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				if s.Gaming {
					// Safety re-check — if gaming flag was toggled on, bail.
					s.mu.Unlock()
					return
				}
				since := time.Since(s.lastInputAt)
				peers := s.peerCount
				idleAlready := s.idleFPS
				normalFPS := s.normalFPS
				s.mu.Unlock()

				switch {
				case since >= idleSuspendDuration && peers == 0:
					// No input AND no viewers for idleSuspendDuration → suspend.
					s.mu.Lock()
					proc := s.gstVideo
					s.mu.Unlock()
					if proc != nil && proc.Process != nil {
						log.Printf("[stream] %s: idle suspend (no input for %.0fs, no peers)",
							s.Name, since.Seconds())
						syscall.Kill(-proc.Process.Pid, syscall.SIGSTOP)
					}

				case since >= idleStaticThreshold && !idleAlready:
					// Static for idleStaticThreshold → drop FPS.
					s.mu.Lock()
					s.idleFPS = true
					s.mu.Unlock()
					log.Printf("[stream] %s: idle FPS drop → %d fps (no input for %.0fs)",
						s.Name, idleThresholdFPS, since.Seconds())
					s.SetFPS(idleThresholdFPS)

				case since < idleStaticThreshold && idleAlready:
					// Input resumed but noteInput ramp-up beat us here — still
					// clear the flag in case it didn't fire.
					s.mu.Lock()
					s.idleFPS = false
					s.mu.Unlock()
					log.Printf("[stream] %s: idle FPS ramp-up → %d fps (ticker)", s.Name, normalFPS)
					s.SetFPS(normalFPS)
				}
			}
		}
	}()
}

// rumbleForwardLoop reads FF_RUMBLE events from the uinput channel and sends
// them to the browser over the existing gamepad WebRTC data channel.
// Message format: {"t":"rumble","strong":<0–65535>,"weak":<0–65535>}
// The browser maps strong→leftActuator, weak→rightActuator for playEffect.
func rumbleForwardLoop(ch <-chan input.RumbleEvent, dc *webrtc.DataChannel, ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if dc.ReadyState() != webrtc.DataChannelStateOpen {
				continue
			}
			b, err := json.Marshal(map[string]any{
				"t":      "rumble",
				"strong": evt.Strong,
				"weak":   evt.Weak,
			})
			if err != nil {
				continue
			}
			dc.SendText(string(b))
		}
	}
}

// handleGamepad processes gamepad state updates.
func (s *Session) handleGamepad(data []byte) {
	if s.injector == nil {
		return
	}
	s.mu.Lock()
	gated := s.inputGated
	s.mu.Unlock()
	if gated {
		return // AUTH-13: block input until WebAuthn assertion is verified
	}
	var state struct {
		Buttons  []bool    `json:"buttons"`
		Axes     []float64 `json:"axes"`
		Triggers []float64 `json:"triggers"`
	}
	if json.Unmarshal(data, &state) != nil {
		return
	}
	s.noteInput()
	// GAME-INPUT-01: bound button/axis/trigger indices and clamp analog values.
	// Full-state snapshots from a hostile client could carry thousands of
	// entries or NaN/Inf axis values; validate each before injection and drop
	// anything out of the virtual gamepad's advertised envelope.
	for i, pressed := range state.Buttons {
		if idx, ok := boundGamepadButton(i); ok {
			s.injector.GamepadButton(idx, pressed)
		}
	}
	for i, value := range state.Axes {
		if idx, v, ok := boundGamepadAxis(i, value); ok {
			s.injector.GamepadAxis(idx, v)
		}
	}
	for i, value := range state.Triggers {
		if idx, v, ok := boundGamepadTrigger(i, value); ok {
			s.injector.GamepadTrigger(idx, v)
		}
	}
}

// listenRTP receives RTP packets on a UDP port and writes them to a WebRTC track.
func listenRTP(ctx context.Context, port int, track *webrtc.TrackLocalStaticRTP) {
	addr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("[stream] rtp listen %d: %v", port, err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			continue
		}
		if track != nil {
			track.Write(buf[:n])
		}
	}
}

// runWithBackoff runs a command in a restart loop with exponential backoff.
func runWithBackoff(ctx context.Context, name string, makeFn func() *exec.Cmd, store **exec.Cmd) {
	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := makeFn()
		if err := cmd.Start(); err != nil {
			log.Printf("[stream] %s start failed: %v", name, err)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			time.Sleep(backoff)
			continue
		}
		if store != nil {
			*store = cmd
		}
		backoff = 3 * time.Second
		start := time.Now()
		cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 10*time.Second {
			backoff = 3 * time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		log.Printf("[stream] %s exited, restarting in %s...", name, backoff)
		time.Sleep(backoff)
	}
}
