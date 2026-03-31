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

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v4"
)

// Session is a single streaming app: Xvfb + app process + GStreamer + WebRTC tracks.
type Session struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Display string  `json:"display"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     int     `json:"fps"`
	Running bool    `json:"running"`
	Encoder string  `json:"encoder"`
	Quality string  `json:"quality"` // current adaptive quality level

	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	xvfb       *exec.Cmd
	wm         *exec.Cmd
	app        *exec.Cmd
	gstVideo   *exec.Cmd
	gstAudio   *exec.Cmd
	videoTrack *webrtc.TrackLocalStaticRTP
	audioTrack *webrtc.TrackLocalStaticRTP
	videoPort  int
	audioPort  int
	displayNum int
	bitrate    int // current target bitrate in kbps
	injector   *input.Injector
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
	}
	procs := []*exec.Cmd{s.gstAudio, s.gstVideo, s.app, s.wm, s.xvfb}
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
	// Clean up X11 socket
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", s.displayNum))
	os.Remove(fmt.Sprintf("/tmp/.X%d-lock", s.displayNum))
	s.Running = false
	log.Printf("[stream] session %s (%s) stopped", s.ID, s.Name)
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
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
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

	// Adaptive bitrate controller — monitors RTCP stats and adjusts quality
	bc := newBitrateController(pc, QualityMedium, func(q Quality) {
		s.mu.Lock()
		s.bitrate = q.Bitrate()
		s.Quality = q.String()
		s.mu.Unlock()
	})
	defer bc.Close()

	// Input data channels — mouse/keyboard/gamepad events
	// Uses uinput (zero-overhead) when available, xdotool fallback otherwise
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleInput(msg.Data)
			})
		case "gamepad":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				s.handleGamepad(msg.Data)
			})
		}
	})

	var wsMu sync.Mutex
	wsWrite := func(data []byte) {
		wsMu.Lock()
		defer wsMu.Unlock()
		ws.WriteMessage(websocket.TextMessage, data)
	}

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

// handleInput processes mouse/keyboard events via the session's injector.
func (s *Session) handleInput(data []byte) {
	if s.injector == nil {
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
	if json.Unmarshal(data, &evt) != nil {
		return
	}

	switch evt.Type {
	case "mousemove":
		s.injector.MouseMove(int(evt.X), int(evt.Y))
	case "mousedown":
		s.injector.MouseMove(int(evt.X), int(evt.Y))
		s.injector.MouseButton(evt.Button, true)
	case "mouseup":
		s.injector.MouseButton(evt.Button, false)
	case "click":
		s.injector.MouseMove(int(evt.X), int(evt.Y))
		s.injector.MouseButton(evt.Button, true)
		s.injector.MouseButton(evt.Button, false)
	case "scroll":
		s.injector.Scroll(int(evt.Y))
	case "keydown":
		s.injector.KeyPress(evt.Key, evt.Code, true)
	case "keyup":
		s.injector.KeyPress(evt.Key, evt.Code, false)
	}
}

// handleGamepad processes gamepad state updates.
func (s *Session) handleGamepad(data []byte) {
	if s.injector == nil {
		return
	}
	var state struct {
		Buttons  []bool    `json:"buttons"`
		Axes     []float64 `json:"axes"`
		Triggers []float64 `json:"triggers"`
	}
	if json.Unmarshal(data, &state) != nil {
		return
	}
	for i, pressed := range state.Buttons {
		s.injector.GamepadButton(i, pressed)
	}
	for i, value := range state.Axes {
		s.injector.GamepadAxis(i, value)
	}
	for i, value := range state.Triggers {
		s.injector.GamepadTrigger(i, value)
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
