package stream

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"syscall"
	"time"

	"vulos/backend/services/gpu"
	"vulos/backend/services/input"

	"github.com/pion/webrtc/v4"
)

// VNCOpts configures a VNC streaming session.
type VNCOpts struct {
	// ID is a unique identifier. If empty, one is generated.
	ID string
	// Name is a human-readable label.
	Name string
	// Host is the VNC server address (default "127.0.0.1").
	Host string
	// Port is the VNC server port (default 5900).
	Port int
	// Password for VNC authentication (empty = no auth).
	Password string
	// Width and Height of the capture (default 1280x720).
	Width, Height int
	// FPS for capture (default 30).
	FPS int
}

// LaunchVNC starts a streaming session that captures from a VNC source
// instead of Xvfb. Uses GStreamer's rfbsrc to connect to the VNC server,
// then feeds into the same encode → RTP → WebRTC pipeline as Xvfb sessions.
func (p *Pool) LaunchVNC(opts VNCOpts) (*Session, error) {
	if opts.Width == 0 {
		opts.Width = 1280
	}
	if opts.Height == 0 {
		opts.Height = 720
	}
	if opts.FPS == 0 {
		opts.FPS = 30
	}
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	if opts.Port == 0 {
		opts.Port = 5900
	}
	if opts.ID == "" {
		opts.ID = fmt.Sprintf("vnc-%d", time.Now().UnixMilli())
	}

	p.mu.Lock()
	if _, exists := p.sessions[opts.ID]; exists {
		p.mu.Unlock()
		return nil, fmt.Errorf("session %s already exists", opts.ID)
	}
	videoPort := p.nextPort
	audioPort := p.nextPort + 1
	p.nextPort += 2
	p.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	gpuInfo := gpu.Detect()

	sess := &Session{
		ID:        opts.ID,
		Name:      opts.Name,
		Display:   fmt.Sprintf("vnc://%s:%d", opts.Host, opts.Port),
		Width:     opts.Width,
		Height:    opts.Height,
		FPS:       opts.FPS,
		Running:   true,
		Encoder:   gpuInfo.Encoder,
		ctx:       ctx,
		cancel:    cancel,
		videoPort: videoPort,
		audioPort: audioPort,
	}

	// VNC sessions don't have a local display for uinput — input goes through
	// the VNC protocol itself (rfbsrc handles this via GStreamer navigation events).
	// For local VNC servers, we can still create an injector on the target display.
	// For remote VNC, input is handled client-side via the data channel → VNC protocol.

	// WebRTC tracks
	vTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: gpuInfo.WebRTCCodec()},
		"video", "vnc-"+opts.ID,
	)
	sess.videoTrack = vTrack

	aTrack, _ := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "vnc-"+opts.ID,
	)
	sess.audioTrack = aTrack

	// RTP listeners
	go listenRTP(ctx, videoPort, vTrack)
	go listenRTP(ctx, audioPort, aTrack)

	// GStreamer video pipeline: rfbsrc → convert → encode → RTP → UDP
	gstBin, _ := exec.LookPath("gst-launch-1.0")
	if gstBin == "" {
		cancel()
		return nil, fmt.Errorf("gst-launch-1.0 not found")
	}

	go runWithBackoff(ctx, opts.Name+"-vnc-video", func() *exec.Cmd {
		args := []string{"-q",
			"rfbsrc",
			fmt.Sprintf("host=%s", opts.Host),
			fmt.Sprintf("port=%d", opts.Port),
			"view-only=false",
			"incremental=false",
		}
		if opts.Password != "" {
			args = append(args, fmt.Sprintf("password=%s", opts.Password))
		}
		args = append(args, "!",
			"video/x-raw",
			fmt.Sprintf("framerate=%d/1", opts.FPS),
		)
		// Scale to target resolution
		args = append(args, "!",
			"videoscale", "!",
			fmt.Sprintf("video/x-raw,width=%d,height=%d", opts.Width, opts.Height),
		)
		// Color conversion / GPU upload
		args = append(args, "!")
		args = append(args, gpuInfo.ConvertArgs()...)
		args = append(args, "!", "queue", "max-size-buffers=1", "leaky=downstream")
		// Encode
		args = append(args, "!")
		args = append(args, gpuInfo.EncoderArgs()...)
		// RTP payloader
		args = append(args, "!")
		args = append(args, gpuInfo.PayloaderArgs()...)
		args = append(args, "!",
			"udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", videoPort),
			"sync=false", "async=false",
		)
		cmd := exec.CommandContext(ctx, gstBin, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}, &sess.gstVideo)

	// VNC input relay — forward data channel input events to VNC via xdotool on local VNC,
	// or via a lightweight VNC input client for remote servers.
	// For local VNC servers (same machine), create an injector targeting the VNC display.
	if opts.Host == "127.0.0.1" || opts.Host == "localhost" {
		// Local VNC — we can inject input via the display
		// VNC display is typically :0 or the display number = port - 5900
		displayNum := opts.Port - 5900
		display := fmt.Sprintf(":%d", displayNum)
		sess.injector = input.NewInjector(display, opts.Width, opts.Height)
	}

	p.mu.Lock()
	p.sessions[opts.ID] = sess
	p.mu.Unlock()

	log.Printf("[stream] launched VNC %q from %s:%d (encoder=%s, %dx%d@%dfps)",
		opts.Name, opts.Host, opts.Port, gpuInfo.Encoder, opts.Width, opts.Height, opts.FPS)
	return sess, nil
}
