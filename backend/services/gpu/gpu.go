// Package gpu detects GPU hardware and selects the best video encoder.
//
// Detection order:
//  1. NVIDIA (nvenc) — check for nvidia-smi + GStreamer nvh264enc
//  2. Intel/AMD VA-API — check for /dev/dri + vainfo + a GStreamer VA H.264
//     encoder (vah264enc from the modern va plugin, or the deprecated
//     vaapih264enc)
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
		// VA-API postproc — uploads to VA surface for zero-copy encode.
		// vapostproc is the modern `va` plugin (gstreamer1.0-plugins-bad),
		// vaapipostproc the deprecated gstreamer1.0-vaapi one. Check both, in
		// that order: upstream removed the latter in GStreamer 1.28 and Debian
		// will drop the package with it.
		if gstHasElement("vapostproc") {
			return []string{"vapostproc"}
		}
		if gstHasElement("vaapipostproc") {
			return []string{"vaapipostproc"}
		}
		return []string{"videoconvert"}
	default:
		return []string{"videoconvert"}
	}
}

// ---------------------------------------------------------------------------
// VA-API element families
// ---------------------------------------------------------------------------
//
// There are TWO GStreamer VA-API plugins and their element properties do not
// overlap:
//
//	gstreamer1.0-vaapi      (deprecated, removed upstream in 1.28)
//	  vaapih264enc, vaapipostproc — keyframe-period, tune, max-bframes
//	gstreamer1.0-plugins-bad (the modern `va` plugin, already installed)
//	  vah264enc, vaav1enc, vapostproc — key-int-max, b-frames, target-usage
//
// That split is not a guess. Reading the two shipped shared objects out of the
// built image (arm64, 2026-08-10) and checking each property name for an exact
// match gives a clean positive/negative control:
//
//	property              libgstva.so   libgstvaapi.so
//	keyframe-period       no            yes
//	tune                  no            yes
//	max-bframes           no            yes
//	key-int-max           yes           no
//	b-frames              yes           no
//	target-usage          yes           no
//	rate-control          yes           yes
//
// gst-launch-1.0 fails hard on an unknown property, so sending a legacy
// property to a modern element does not degrade — the pipeline does not start.
// This is why the AV1 path was broken: it emitted `vaav1enc ... keyframe-period=30
// tune=low-power`, and vaav1enc comes from the plugin that has neither.
//
// NOT VERIFIED HERE: this machine has no /dev/dri, so no VA element can
// register and no pipeline built from these arguments has been run. The
// property NAMES are evidence-backed as above; that the resulting pipeline
// encodes is ASSUMED. To verify, on a box with /dev/dri/renderD128:
//
//	gst-inspect-1.0 vah264enc
//	gst-launch-1.0 videotestsrc num-buffers=60 ! vapostproc ! vah264enc ! fakesink -v

// isLegacyVAElement reports whether element comes from the deprecated
// gstreamer1.0-vaapi plugin (vaapi* prefix) rather than the modern va* one.
func isLegacyVAElement(element string) bool {
	return strings.HasPrefix(element, "vaapi")
}

// vaEncoderArgs builds the property list for a VA-API encoder element using the
// property names its own plugin actually defines. gopSize is the keyframe
// interval in frames; gaming disables B-frames for latency.
func vaEncoderArgs(element string, bitrateKbps, gopSize int, gaming bool) []string {
	args := []string{element}
	args = append(args, fmt.Sprintf("bitrate=%d", bitrateKbps), "rate-control=cbr")
	if isLegacyVAElement(element) {
		args = append(args, fmt.Sprintf("keyframe-period=%d", gopSize))
		if gaming {
			args = append(args, "tune=low-power", "max-bframes=0")
		} else {
			args = append(args, "tune=low-power")
		}
		return args
	}
	args = append(args, fmt.Sprintf("key-int-max=%d", gopSize))
	if gaming {
		args = append(args, "b-frames=0")
	}
	return args
}

// VAEncoderArgs is the exported form of vaEncoderArgs for callers that rebuild
// the encoder arguments with a session-specific bitrate (stream's adaptive
// bitrate). Passing Info.Encoder through here is what keeps the property names
// matched to the element's plugin.
func (g *Info) VAEncoderArgs(bitrateKbps, gopSize int, gaming bool) []string {
	return vaEncoderArgs(g.encoderElement("vaapih264enc"), bitrateKbps, gopSize, gaming)
}

// vaH264Element returns the H.264 VA-API encoder element available on this
// system, preferring the modern plugin, or "" if neither is present.
func vaH264Element() string {
	if gstHasElement("vah264enc") {
		return "vah264enc"
	}
	if gstHasElement("vaapih264enc") {
		return "vaapih264enc"
	}
	return ""
}

// vaAV1Element returns the AV1 VA-API encoder element, or "" if absent.
// Only the modern plugin has one; gstreamer1.0-vaapi never shipped an AV1
// encoder, so there is no legacy fallback to check.
func vaAV1Element() string {
	if gstHasElement("vaav1enc") {
		return "vaav1enc"
	}
	return ""
}

// encoderElement returns the VA-API element this Info was probed with, falling
// back to fallback when Info was constructed by hand (as several tests do).
func (g *Info) encoderElement(fallback string) string {
	if strings.HasPrefix(g.Encoder, "va") {
		return g.Encoder
	}
	return fallback
}

// insertName puts name=venc immediately after the element, which is where the
// pipeline expects a stable element name (EncoderElementName).
func insertName(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[0], "name=venc")
	return append(out, args[1:]...)
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
			return insertName(vaEncoderArgs(g.encoderElement("vaav1enc"), 1500, 30, false))
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
		return insertName(vaEncoderArgs(g.encoderElement("vaapih264enc"), 2000, 30, false))
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
		return vaEncoderArgs(g.encoderElement("vaapih264enc"), bitrate, gopSize, true)
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
		if av1 := vaAV1Element(); av1 != "" {
			info.HasAV1 = true
			info.Encoder = av1
			info.Payloader = "rtpav1pay"
			info.Codec = "video/AV1"
			log.Printf("[gpu] VA-API AV1 hardware encode available (%s)", av1)
		} else {
			info.Encoder = va.h264Element
			info.Payloader = "rtph264pay"
			info.Codec = "video/H264"
			log.Printf("[gpu] VA-API H.264 hardware encode available (%s)", va.h264Element)
		}
		return info
	}

	return info
}

type probeResult struct {
	vendor Vendor
	device string
	// h264Element is the VA-API H.264 encoder element this box actually has —
	// "vah264enc" or "vaapih264enc". They take different property names, so the
	// pipeline builder needs to know which one it got, not just that it got one.
	h264Element string
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

	// Verify GStreamer can actually encode with it. Either plugin family will
	// do; prefer the modern one.
	//
	// This used to test gstHasElement("vaapih264enc") alone — the DEPRECATED
	// element, removed upstream in GStreamer 1.28. The modern `va` plugin has
	// shipped in the already-installed gstreamer1.0-plugins-bad the whole time.
	// So the day Debian retires gstreamer1.0-vaapi, this probe would have
	// returned nil on a perfectly capable GPU and every session would have
	// dropped to software VP8 — with one log line, on a box that had just been
	// bought for its hardware encoder.
	encoder := vaH264Element()
	if encoder == "" {
		// LOUD, because the cost is invisible otherwise: the stream still
		// works, it is just software-encoded, and nobody looks at the tier.
		log.Printf("[gpu] ################################################################")
		log.Printf("[gpu] HARDWARE ENCODE DISABLED: VA-API is present and reports an")
		log.Printf("[gpu] encode entrypoint, but GStreamer has NEITHER vah264enc")
		log.Printf("[gpu] (gstreamer1.0-plugins-bad) NOR vaapih264enc (gstreamer1.0-vaapi).")
		log.Printf("[gpu] Every stream on this box will fall back to SOFTWARE VP8.")
		log.Printf("[gpu] Fix: install gstreamer1.0-plugins-bad, then check")
		log.Printf("[gpu]   gst-inspect-1.0 vah264enc")
		log.Printf("[gpu] ################################################################")
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

	return &probeResult{vendor: vendor, device: device, h264Element: encoder}
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

// gstHasElement reports whether GStreamer can find an element. It is a var so
// tests can decide which elements "exist" — without that seam the element
// SELECTION logic (which VA-API plugin do we pick?) is unreachable from a test,
// and selecting the wrong one is the defect this indirection exists to catch.
var gstHasElement = func(element string) bool {
	err := exec.Command("gst-inspect-1.0", element).Run()
	return err == nil
}
