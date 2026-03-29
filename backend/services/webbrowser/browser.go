package webbrowser

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

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v4"
	"golang.org/x/net/websocket"
)

const (
	rtpVideoPort = 5004
	rtpAudioPort = 5006
	inputPort    = 5005
	display      = ":99"
	width        = 1280
	height       = 720
	fps          = 30
)

// Service manages Xvfb + Chromium + GStreamer, all launched at startup.
// By the time a user connects, the browser is already running and streaming.
type Service struct {
	mu         sync.Mutex
	xvfb       *exec.Cmd
	pulse      *exec.Cmd
	chrome     *exec.Cmd
	gstVideo   *exec.Cmd
	gstAudio   *exec.Cmd
	inputConn  net.Conn
	videoTrack *webrtc.TrackLocalStaticRTP
	audioTrack *webrtc.TrackLocalStaticRTP
	running    bool
	ctx        context.Context
	cancel     context.CancelFunc
}

func New() *Service {
	return &Service{}
}

// Start launches Xvfb, Chromium, GStreamer, and the RTP listener at boot.
// Everything stays running so WebRTC connections are instant.
func (s *Service) Start(parentCtx context.Context, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(parentCtx)

	// 0. Kill stale processes and clean up from previous runs
	exec.Command("pkill", "-9", "Xvfb").Run()
	exec.Command("pkill", "-9", "chromium").Run()
	exec.Command("pkill", "-9", "gst-launch").Run()
	exec.Command("pkill", "-9", "pulseaudio").Run()
	os.Remove("/tmp/.X11-unix/X99")
	os.Remove("/tmp/pulse-server")
	time.Sleep(500 * time.Millisecond)

	// 1. Xvfb
	if err := s.startXvfb(); err != nil {
		return fmt.Errorf("xvfb: %w", err)
	}

	// 2. PulseAudio (virtual sound card for Chromium audio)
	if err := s.startPulseAudio(); err != nil {
		log.Printf("[browser] pulseaudio warning: %v (audio disabled)", err)
	}

	// 3. Chromium
	if err := s.startChromium(); err != nil {
		return fmt.Errorf("chromium: %w", err)
	}

	// 4. Video track
	vTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video", "browser",
	)
	if err != nil {
		return fmt.Errorf("create video track: %w", err)
	}
	s.videoTrack = vTrack

	// 5. Audio track
	aTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "browser",
	)
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}
	s.audioTrack = aTrack

	// 6. RTP listeners
	go s.listenRTP(rtpVideoPort, s.videoTrack)
	go s.listenRTP(rtpAudioPort, s.audioTrack)

	// 7. Give Xvfb + PulseAudio a moment to stabilize before GStreamer connects
	time.Sleep(2 * time.Second)

	// 8. GStreamer pipelines (video + audio) — auto-restart on crash
	if err := s.startVideoGST(); err != nil {
		return fmt.Errorf("gstreamer video: %w", err)
	}
	if err := s.startAudioGST(); err != nil {
		log.Printf("[browser] gstreamer audio warning: %v (audio disabled)", err)
	}

	// 8. Input channel
	go s.startInputListener()
	go s.connectInput()

	s.running = true
	log.Printf("[browser] ready — Xvfb + PulseAudio + Chromium + GStreamer running")
	return nil
}

func (s *Service) startXvfb() error {
	s.xvfb = exec.CommandContext(s.ctx, "Xvfb", display,
		"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
		"-ac", "+render", "-noreset")
	s.xvfb.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.xvfb.Stdout = os.Stdout
	s.xvfb.Stderr = os.Stderr
	if err := s.xvfb.Start(); err != nil {
		return err
	}
	// Wait for display to be available
	for i := 0; i < 20; i++ {
		if _, err := os.Stat("/tmp/.X11-unix/X99"); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("display %s not ready", display)
}

func (s *Service) startChromium() error {
	bin := findBin("chromium-browser", "chromium", "/usr/bin/chromium-browser")
	if bin == "" {
		return fmt.Errorf("chromium not found")
	}
	s.chrome = exec.CommandContext(s.ctx, bin,
		"--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--no-first-run", "--disable-background-networking",
		"--disable-sync", "--disable-translate", "--metrics-recording-only",
		"--no-default-browser-check", "--disable-dbus",
		"--disable-features=TranslateUI,MediaRouter",
		"--disable-infobars",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--noerrdialogs",
		"--hide-crash-restore-bubble",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--window-position=0,0",
		"--start-maximized",
		"https://google.com",
	)
	s.chrome.Env = append(os.Environ(), "DISPLAY="+display)
	s.chrome.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.chrome.Stdout = os.Stdout
	s.chrome.Stderr = os.Stderr
	if err := s.chrome.Start(); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return nil
}

func (s *Service) startPulseAudio() error {
	bin := findBin("pulseaudio")
	if bin == "" {
		return fmt.Errorf("pulseaudio not found")
	}
	args := []string{
		"--daemonize=no", "--system=false", "--exit-idle-time=-1",
		"--load=module-null-sink sink_name=virtual_speaker sink_properties=device.description=VirtualSpeaker",
		"--load=module-always-sink",
	}
	env := append(os.Environ(), "DISPLAY="+display)

	cmd := exec.CommandContext(s.ctx, bin, args...)
	cmd.Env = env
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
			c.Env = env
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
			backoff = 3 * time.Second // reset on successful start
			start := time.Now()
			c.Wait()
			// If it ran for >10s, reset backoff (it was a real crash, not a startup failure)
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

func (s *Service) startVideoGST() error {
	bin := findBin("gst-launch-1.0")
	if bin == "" {
		return fmt.Errorf("gst-launch-1.0 not found")
	}
	args := []string{"-q",
		"ximagesrc", fmt.Sprintf("display-name=%s", display),
		"use-damage=false", "show-pointer=true",
		"!", fmt.Sprintf("video/x-raw,framerate=%d/1", fps),
		"!", "videoconvert",
		"!", "queue", "max-size-buffers=1", "leaky=downstream",
		"!", "vp8enc",
		"target-bitrate=2000000", "cpu-used=8", "deadline=1",
		"keyframe-max-dist=30", "threads=4", "end-usage=cbr",
		"undershoot=95", "buffer-size=6000", "buffer-initial-size=4000",
		"lag-in-frames=0", "error-resilient=1",
		"!", "rtpvp8pay", "pt=96", "mtu=1200",
		"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", rtpVideoPort),
		"sync=false", "async=false",
	}
	env := append(os.Environ(), "DISPLAY="+display)

	// Auto-restart loop with backoff
	go func() {
		backoff := 3 * time.Second
		for {
			if s.ctx.Err() != nil {
				return
			}
			cmd := exec.CommandContext(s.ctx, bin, args...)
			cmd.Env = env
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				log.Printf("[browser] gstreamer video start failed: %v", err)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				time.Sleep(backoff)
				continue
			}
			s.gstVideo = cmd
			backoff = 3 * time.Second
			start := time.Now()
			cmd.Wait()
			if s.ctx.Err() != nil {
				return
			}
			if time.Since(start) > 10*time.Second {
				backoff = 3 * time.Second
			} else if backoff < 30*time.Second {
				backoff *= 2
			}
			log.Printf("[browser] gstreamer video exited, restarting in %s...", backoff)
			time.Sleep(backoff)
		}
	}()
	return nil
}

func (s *Service) startAudioGST() error {
	bin := findBin("gst-launch-1.0")
	if bin == "" {
		return fmt.Errorf("gst-launch-1.0 not found")
	}
	args := []string{"-q",
		"pulsesrc", "device=virtual_speaker.monitor",
		"!", "audio/x-raw,rate=48000,channels=2",
		"!", "queue", "max-size-buffers=1", "leaky=downstream",
		"!", "opusenc", "bitrate=128000", "frame-size=20",
		"!", "rtpopuspay", "pt=111",
		"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", rtpAudioPort),
		"sync=false", "async=false",
	}
	env := append(os.Environ(), "DISPLAY="+display)

	// Auto-restart loop with backoff
	go func() {
		backoff := 3 * time.Second
		for {
			if s.ctx.Err() != nil {
				return
			}
			cmd := exec.CommandContext(s.ctx, bin, args...)
			cmd.Env = env
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				log.Printf("[browser] gstreamer audio start failed: %v", err)
				if backoff < 30*time.Second {
					backoff *= 2
				}
				time.Sleep(backoff)
				continue
			}
			s.gstAudio = cmd
			backoff = 3 * time.Second
			start := time.Now()
			cmd.Wait()
			if s.ctx.Err() != nil {
				return
			}
			if time.Since(start) > 10*time.Second {
				backoff = 3 * time.Second
			} else if backoff < 30*time.Second {
				backoff *= 2
			}
			log.Printf("[browser] gstreamer audio exited, restarting in %s...", backoff)
			time.Sleep(backoff)
		}
	}()
	return nil
}

func (s *Service) startInputListener() {
	bin := findBin("socat")
	if bin == "" {
		log.Printf("[browser] socat not found, input disabled")
		return
	}
	cmd := exec.CommandContext(s.ctx, bin,
		fmt.Sprintf("TCP-LISTEN:%d,reuseaddr,fork", inputPort),
		"EXEC:/bin/sh")
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Run()
}

func (s *Service) connectInput() {
	time.Sleep(3 * time.Second)
	for i := 0; i < 15; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inputPort), time.Second)
		if err == nil {
			s.mu.Lock()
			s.inputConn = conn
			s.mu.Unlock()
			log.Printf("[browser] input channel connected")
			return
		}
		time.Sleep(time.Second)
	}
	log.Printf("[browser] warning: input channel not available")
}

// listenRTP receives VP8 RTP packets and writes them to the shared video track.
func (s *Service) listenRTP(port int, track *webrtc.TrackLocalStaticRTP) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Printf("[browser] rtp resolve %d: %v", port, err)
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("[browser] rtp listen %d: %v", port, err)
		return
	}
	defer conn.Close()

	log.Printf("[browser] RTP listener on :%d", port)
	buf := make([]byte, 1500)
	for {
		select {
		case <-s.ctx.Done():
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

// HandleWebSocket handles WebRTC signaling for a browser viewer.
func (s *Service) HandleWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		log.Printf("[browser] codecs: %v", err)
		return
	}

	ir := &interceptor.Registry{}
	pli, _ := intervalpli.NewReceiverInterceptor()
	ir.Add(pli)
	webrtc.RegisterDefaultInterceptors(m, ir)

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		log.Printf("[browser] peer: %v", err)
		return
	}
	defer pc.Close()

	// Add video + audio tracks
	if s.videoTrack != nil {
		if _, err := pc.AddTrack(s.videoTrack); err != nil {
			log.Printf("[browser] add video track: %v", err)
			return
		}
	}
	if s.audioTrack != nil {
		if _, err := pc.AddTrack(s.audioTrack); err != nil {
			log.Printf("[browser] add audio track: %v", err)
		}
	}

	// Data channel for mouse/keyboard input
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "input" {
			return
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			s.handleInput(msg.Data)
		})
	})

	// Send ICE candidates to client
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		b, _ := json.Marshal(map[string]any{"type": "candidate", "candidate": c.ToJSON()})
		websocket.Message.Send(ws, string(b))
	})

	// Signaling loop
	for {
		var raw string
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		switch msg["type"] {
		case "offer":
			sdp, _ := msg["sdp"].(string)
			pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp})
			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				return
			}
			pc.SetLocalDescription(answer)
			b, _ := json.Marshal(map[string]any{"type": "answer", "sdp": answer.SDP})
			websocket.Message.Send(ws, string(b))

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
				Candidate:     candidate,
				SDPMid:        &sdpMid,
				SDPMLineIndex: &idx,
			})
		}
	}
}

func (s *Service) handleInput(data []byte) {
	s.mu.Lock()
	conn := s.inputConn
	s.mu.Unlock()
	if conn == nil {
		return
	}

	var evt struct {
		Type   string  `json:"type"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Button int     `json:"button"`
		Key    string  `json:"key"`
		Code   string  `json:"code"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return
	}

	var cmd string
	switch evt.Type {
	case "mousemove":
		cmd = fmt.Sprintf("xdotool mousemove --screen 0 %d %d\n", int(evt.X), int(evt.Y))
	case "mousedown":
		cmd = fmt.Sprintf("xdotool mousemove --screen 0 %d %d mousedown %d\n", int(evt.X), int(evt.Y), xButton(evt.Button))
	case "mouseup":
		cmd = fmt.Sprintf("xdotool mouseup %d\n", xButton(evt.Button))
	case "click":
		cmd = fmt.Sprintf("xdotool mousemove --screen 0 %d %d click %d\n", int(evt.X), int(evt.Y), xButton(evt.Button))
	case "scroll":
		btn := 5
		clicks := int(evt.Y)
		if clicks < 0 {
			btn = 4
			clicks = -clicks
		}
		if clicks < 1 {
			clicks = 1
		}
		if clicks > 5 {
			clicks = 5
		}
		cmd = fmt.Sprintf("xdotool click --repeat %d --delay 10 %d\n", clicks, btn)
	case "keydown":
		if xk := jsKeyToX(evt.Key, evt.Code); xk != "" {
			cmd = fmt.Sprintf("xdotool keydown %s\n", xk)
		}
	case "keyup":
		if xk := jsKeyToX(evt.Key, evt.Code); xk != "" {
			cmd = fmt.Sprintf("xdotool keyup %s\n", xk)
		}
	}
	if cmd != "" {
		conn.Write([]byte(cmd))
	}
}

func xButton(jsButton int) int {
	switch jsButton {
	case 1:
		return 2
	case 2:
		return 3
	default:
		return 1
	}
}

func jsKeyToX(key, code string) string {
	m := map[string]string{
		"Enter": "Return", "Backspace": "BackSpace", "Tab": "Tab",
		"Escape": "Escape", "Delete": "Delete", "Insert": "Insert",
		"Home": "Home", "End": "End", "PageUp": "Prior", "PageDown": "Next",
		"ArrowUp": "Up", "ArrowDown": "Down", "ArrowLeft": "Left", "ArrowRight": "Right",
		"Shift": "Shift_L", "Control": "Control_L", "Alt": "Alt_L", "Meta": "Super_L",
		"CapsLock": "Caps_Lock", " ": "space",
		"F1": "F1", "F2": "F2", "F3": "F3", "F4": "F4", "F5": "F5", "F6": "F6",
		"F7": "F7", "F8": "F8", "F9": "F9", "F10": "F10", "F11": "F11", "F12": "F12",
	}
	if x, ok := m[key]; ok {
		return x
	}
	if len(key) == 1 {
		return key
	}
	return ""
}

// RegisterHandlers registers the browser WebRTC endpoints.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"running": running})
	})
	mux.Handle("GET /api/browser/ws", websocket.Handler(s.HandleWebSocket))
}

// Stop kills all browser processes.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	if s.inputConn != nil {
		s.inputConn.Close()
	}
	for _, cmd := range []*exec.Cmd{s.gstAudio, s.gstVideo, s.chrome, s.pulse, s.xvfb} {
		if cmd != nil && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	time.Sleep(500 * time.Millisecond)
	for _, cmd := range []*exec.Cmd{s.gstAudio, s.gstVideo, s.chrome, s.pulse, s.xvfb} {
		if cmd != nil && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	s.running = false
}

func (s *Service) StopAll()            { s.Stop() }
func (s *Service) Running() bool       { s.mu.Lock(); defer s.mu.Unlock(); return s.running }
func (s *Service) Port() int           { return 0 }
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

func findBin(names ...string) string {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
