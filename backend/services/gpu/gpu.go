// Package gpu detects GPU hardware and selects the best video encoder.
//
// Detection order:
//  1. NVIDIA (nvenc) — check for nvidia-smi + GStreamer nvh264enc
//  2. Intel/AMD VA-API — check for /dev/dri + vainfo + GStreamer vaapih264enc
//  3. Software fallback — VP8 via libvpx (always available)
package gpu

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Tier represents the GPU acceleration level.
type Tier int

const (
	TierSoftware Tier = 0 // No GPU — VP8 software encode
	TierVAAPI    Tier = 1 // Intel/AMD — VA-API hardware encode
	TierNVENC    Tier = 2 // NVIDIA — NVENC hardware encode
)

func (t Tier) String() string {
	switch t {
	case TierNVENC:
		return "nvenc"
	case TierVAAPI:
		return "vaapi"
	default:
		return "software"
	}
}

// Vendor identifies the GPU manufacturer.
type Vendor string

const (
	VendorNone   Vendor = "none"
	VendorIntel  Vendor = "intel"
	VendorAMD    Vendor = "amd"
	VendorNVIDIA Vendor = "nvidia"
)

// Info holds detected GPU capabilities.
type Info struct {
	Tier        Tier   `json:"tier"`
	TierName    string `json:"tier_name"`
	Vendor      Vendor `json:"vendor"`
	Device      string `json:"device"`       // GPU device name from lspci/nvidia-smi
	Encoder     string `json:"encoder"`      // GStreamer encoder element name
	Payloader   string `json:"payloader"`    // GStreamer RTP payloader element
	Codec       string `json:"codec"`        // WebRTC codec mime type
	HasDRI      bool   `json:"has_dri"`      // /dev/dri exists
	HasAV1      bool   `json:"has_av1"`      // AV1 hardware encode available
	HasPipeWire bool   `json:"has_pipewire"` // PipeWire screen capture available
}

// CaptureArgs returns the GStreamer capture source element + properties.
// When PipeWire is available, uses pipewiresrc for DMA-BUF zero-copy capture.
// Falls back to ximagesrc (SHM copy from X11).
//
// gaming controls the ximagesrc use-damage property:
//   - gaming=false (normal): use-damage=true  — dirty-region capture; static windows produce ~0 frames
//   - gaming=true:           use-damage=false — full-frame capture; required for constant high-FPS encode
func (g *Info) CaptureArgs(display string, fps int, gaming bool) []string {
	// PipeWire screen capture — DMA-BUF path, zero CPU copy
	if g.HasPipeWire && g.HasDRI && gstHasElement("pipewiresrc") {
		return []string{
			"pipewiresrc", "do-timestamp=true",
			"!", fmt.Sprintf("video/x-raw,framerate=%d/1", fps),
		}
	}
	// Dirty-region capture: enabled for normal sessions (static window → ~0 encoded frames),
	// disabled for gaming (full-frame capture needed for constant high-FPS encode).
	useDamage := "true"
	if gaming {
		useDamage = "false"
	}
	// Default: ximagesrc (X11 SHM capture)
	return []string{
		"ximagesrc", fmt.Sprintf("display-name=%s", display),
		"use-damage=" + useDamage, "show-pointer=true",
		"!", fmt.Sprintf("video/x-raw,framerate=%d/1", fps),
	}
}

// ConvertArgs returns the GStreamer color conversion pipeline segment.
// For GPU tiers, this uploads frames to GPU memory (DMA-BUF/CUDA) for zero-copy encoding.
// For software, this is plain videoconvert.
func (g *Info) ConvertArgs() []string {
	switch g.Tier {
	case TierNVENC:
		// CUDA upload path — frames go CPU → GPU memory → nvh264enc
		if gstHasElement("cudaupload") {
			return []string{"cudaupload", "!", "cudaconvert"}
		}
		return []string{"videoconvert"}
	case TierVAAPI:
		// VA-API postproc — uploads to VA surface for zero-copy encode
		if gstHasElement("vaapipostproc") {
			return []string{"vaapipostproc"}
		}
		return []string{"videoconvert"}
	default:
		return []string{"videoconvert"}
	}
}

// EncoderArgs returns the GStreamer encoder element + properties as args.
// Prefers AV1 when available (better quality/bitrate), falls back to H.264/VP8.
// All encoder elements carry name=venc so the pipeline element can be looked up
// by a stable name (e.g. for adaptive-bitrate property changes).
func (g *Info) EncoderArgs() []string {
	if g.HasAV1 {
		switch g.Tier {
		case TierNVENC:
			return []string{
				"nvav1enc", "name=venc",
				"bitrate=1500", "preset=low-latency-hq", "rc-mode=cbr",
				"gop-size=30",
				"zerolatency=true", "b-adapt=false", "rc-lookahead=0", "aud=true",
			}
		case TierVAAPI:
			return []string{
				"vaav1enc", "name=venc",
				"bitrate=1500", "rate-control=cbr",
				"keyframe-period=30",
				"tune=low-power",
			}
		}
	}
	switch g.Tier {
	case TierNVENC:
		return []string{
			"nvh264enc", "name=venc",
			"bitrate=2000", "preset=low-latency-hq", "rc-mode=cbr",
			"gop-size=30",
			"zerolatency=true", "b-adapt=false", "rc-lookahead=0", "aud=true",
		}
	case TierVAAPI:
		return []string{
			"vaapih264enc", "name=venc",
			"bitrate=2000", "rate-control=cbr",
			"keyframe-period=30",
			"tune=low-power", "cabac-entropy-coding=true",
		}
	default:
		return []string{
			"vp8enc", "name=venc",
			"target-bitrate=2000000", "cpu-used=8", "deadline=1",
			"keyframe-max-dist=30", "threads=4", "end-usage=cbr",
			"undershoot=95", "buffer-size=6000", "buffer-initial-size=4000",
			"lag-in-frames=0", "error-resilient=1",
		}
	}
}

// GamingEncoderArgs returns the GStreamer encoder element + properties tuned for
// gaming: zero-latency preset, no B-frames, no lookahead, high bitrate.
// The fps and bitrate parameters let the caller pass session-specific values
// (bitrate is in kbps for GPU tiers, converted to bps for VP8).
//
// Keyframe-recovery note: the ideal cloud-gaming pattern is on-demand IDR
// (client reports loss → RTCP PLI/FIR → encoder emits a fresh keyframe). That
// path is NOT wired here because the encoder runs in a separate gst-launch-1.0
// subprocess fed over a UDP relay: gst-client/gstd can set properties (used for
// live bitrate) but cannot inject a GstForceKeyUnit event, so there is no
// external hook to force an IDR. A real on-demand-IDR path needs an in-process
// GStreamer (cgo go-gst appsrc/appsink sending force-key-unit to the encoder
// pad). As the safe, bounded mitigation we use a 1-second GOP for gaming (down
// from 2s): under CBR (all gaming tiers use CBR) shorter GOPs are bandwidth-
// neutral, and this halves the worst-case wait for a clean keyframe after loss.
func (g *Info) GamingEncoderArgs(fps, bitrate int) []string {
	if bitrate <= 0 {
		bitrate = 6000
	}
	gopSize := fps // 1-second keyframe interval for gaming (bounds keyframe-recovery latency under CBR)

	switch g.Tier {
	case TierNVENC:
		return []string{
			"nvh264enc",
			fmt.Sprintf("bitrate=%d", bitrate),
			"preset=low-latency-hp",
			"rc-mode=cbr",
			fmt.Sprintf("gop-size=%d", gopSize),
			"b-adapt=false",
			"rc-lookahead=0",
			"bframes=0",
		}
	case TierVAAPI:
		return []string{
			"vaapih264enc",
			fmt.Sprintf("bitrate=%d", bitrate),
			"rate-control=cbr",
			fmt.Sprintf("keyframe-period=%d", gopSize),
			"tune=low-power",
			"max-bframes=0",
		}
	default:
		// Software VP8: cpu-used=16 for minimum encode latency
		return []string{
			"vp8enc",
			fmt.Sprintf("target-bitrate=%d", bitrate*1000),
			"cpu-used=16",
			"deadline=1",
			fmt.Sprintf("keyframe-max-dist=%d", gopSize),
			"threads=8",
			"end-usage=cbr",
			"lag-in-frames=0",
		}
	}
}

// PayloaderArgs returns the RTP payloader element + properties.
func (g *Info) PayloaderArgs() []string {
	if g.HasAV1 && (g.Tier == TierNVENC || g.Tier == TierVAAPI) {
		return []string{"rtpav1pay", "pt=96", "mtu=1200"}
	}
	switch g.Tier {
	case TierNVENC, TierVAAPI:
		return []string{"rtph264pay", "pt=96", "mtu=1200", "config-interval=-1"}
	default:
		return []string{"rtpvp8pay", "pt=96", "mtu=1200"}
	}
}

// WebRTCCodec returns the mime type for the WebRTC track.
func (g *Info) WebRTCCodec() string {
	if g.HasAV1 && (g.Tier == TierNVENC || g.Tier == TierVAAPI) {
		return "video/AV1"
	}
	switch g.Tier {
	case TierNVENC, TierVAAPI:
		return "video/H264"
	default:
		return "video/VP8"
	}
}

var (
	detectOnce sync.Once
	detected   Info
)

// Detect probes the system for GPU hardware and returns the best encoder config.
// Result is cached after the first call.
func Detect() Info {
	detectOnce.Do(func() {
		detected = detect()
		log.Printf("[gpu] detected: tier=%s vendor=%s device=%q encoder=%s",
			detected.TierName, detected.Vendor, detected.Device, detected.Encoder)
	})
	return detected
}

func detect() Info {
	info := Info{
		Tier:        TierSoftware,
		TierName:    TierSoftware.String(),
		Vendor:      VendorNone,
		Encoder:     "vp8enc",
		Payloader:   "rtpvp8pay",
		Codec:       "video/VP8",
		HasDRI:      hasDRI(),
		HasPipeWire: hasPipeWire(),
	}

	// Check NVIDIA first (highest priority)
	if nv := probeNVIDIA(); nv != nil {
		info.Tier = TierNVENC
		info.TierName = TierNVENC.String()
		info.Vendor = VendorNVIDIA
		info.Device = nv.device
		// Prefer AV1 (RTX 4000+) over H.264
		if gstHasElement("nvav1enc") {
			info.HasAV1 = true
			info.Encoder = "nvav1enc"
			info.Payloader = "rtpav1pay"
			info.Codec = "video/AV1"
			log.Printf("[gpu] NVIDIA AV1 hardware encode available")
		} else {
			info.Encoder = "nvh264enc"
			info.Payloader = "rtph264pay"
			info.Codec = "video/H264"
		}
		return info
	}

	// Check VA-API (Intel/AMD)
	if va := probeVAAPI(); va != nil {
		info.Tier = TierVAAPI
		info.TierName = TierVAAPI.String()
		info.Vendor = va.vendor
		info.Device = va.device
		// Prefer AV1 (Intel Arc, AMD RX 7000+) over H.264
		if gstHasElement("vaav1enc") {
			info.HasAV1 = true
			info.Encoder = "vaav1enc"
			info.Payloader = "rtpav1pay"
			info.Codec = "video/AV1"
			log.Printf("[gpu] VA-API AV1 hardware encode available")
		} else {
			info.Encoder = "vaapih264enc"
			info.Payloader = "rtph264pay"
			info.Codec = "video/H264"
		}
		return info
	}

	return info
}

type probeResult struct {
	vendor Vendor
	device string
}

func probeNVIDIA() *probeResult {
	// Check nvidia-smi exists and responds
	out, err := exec.Command("nvidia-smi", "--query-gpu=gpu_name", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	device := strings.TrimSpace(string(out))
	if device == "" {
		return nil
	}

	// Verify GStreamer has the nvenc plugin
	if !gstHasElement("nvh264enc") {
		log.Printf("[gpu] NVIDIA GPU found (%s) but nvh264enc GStreamer plugin missing", device)
		return nil
	}

	return &probeResult{vendor: VendorNVIDIA, device: device}
}

func probeVAAPI() *probeResult {
	// Need /dev/dri to exist
	if !hasDRI() {
		return nil
	}

	// Run vainfo to check VA-API support
	out, err := exec.Command("vainfo").CombinedOutput()
	if err != nil {
		return nil
	}
	vainfo := string(out)

	// Must support H.264 encode
	if !strings.Contains(vainfo, "VAEntrypointEncSlice") {
		return nil
	}

	// Verify GStreamer has the vaapi plugin
	if !gstHasElement("vaapih264enc") {
		log.Printf("[gpu] VA-API available but vaapih264enc GStreamer plugin missing")
		return nil
	}

	// Determine vendor from DRI device
	vendor := VendorIntel // default assumption
	device := "VA-API GPU"

	// Try to read the driver name from vainfo output
	for _, line := range strings.Split(vainfo, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "driver") {
			if strings.Contains(lower, "intel") || strings.Contains(lower, "iHD") || strings.Contains(lower, "i965") {
				vendor = VendorIntel
				device = "Intel GPU (VA-API)"
			} else if strings.Contains(lower, "amd") || strings.Contains(lower, "radeon") || strings.Contains(lower, "radeonsi") {
				vendor = VendorAMD
				device = "AMD GPU (VA-API)"
			}
			break
		}
	}

	return &probeResult{vendor: vendor, device: device}
}

func hasDRI() bool {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") || strings.HasPrefix(e.Name(), "card") {
			return true
		}
	}
	return false
}

func hasPipeWire() bool {
	// Check if PipeWire daemon is running and the GStreamer plugin exists
	if err := exec.Command("pw-cli", "info", "0").Run(); err != nil {
		return false
	}
	return gstHasElement("pipewiresrc")
}

func gstHasElement(element string) bool {
	err := exec.Command("gst-inspect-1.0", element).Run()
	return err == nil
}
