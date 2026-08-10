package gpu

import (
	"strings"
	"testing"
)

// The two VA-API GStreamer plugins define DISJOINT property names. Reading the
// shipped shared objects out of the built image (arm64, 2026-08-10) and testing
// each name for an exact match gives a clean positive/negative control:
//
//	property          libgstva.so   libgstvaapi.so
//	keyframe-period   no            yes
//	tune              no            yes
//	max-bframes       no            yes
//	key-int-max       yes           no
//	b-frames          yes           no
//	target-usage      yes           no
//
// gst-launch-1.0 fails hard on an unknown property, so mixing them does not
// degrade quality — the pipeline never starts. These tests pin each element to
// its own plugin's vocabulary.
//
// What they do NOT prove: that the resulting pipeline encodes. That needs
// /dev/dri, which this machine does not have.
var (
	legacyOnlyProps = []string{"keyframe-period", "tune", "max-bframes"}
	modernOnlyProps = []string{"key-int-max", "b-frames", "target-usage"}
)

func assertNoProps(t *testing.T, args []string, banned []string, why string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, p := range banned {
		for _, a := range args {
			if a == p || strings.HasPrefix(a, p+"=") {
				t.Errorf("%s: argument %q is not a property of this element's plugin; full args: %s", why, a, joined)
			}
		}
	}
}

func assertHasProp(t *testing.T, args []string, prop string) {
	t.Helper()
	for _, a := range args {
		if a == prop || strings.HasPrefix(a, prop+"=") {
			return
		}
	}
	t.Errorf("expected property %q in args: %s", prop, strings.Join(args, " "))
}

// The modern va plugin's H.264 encoder must never receive gstreamer1.0-vaapi
// property names.
func TestEncoderArgs_ModernH264UsesModernProperties(t *testing.T) {
	info := Info{Tier: TierVAAPI, Encoder: "vah264enc"}

	args := info.EncoderArgs()
	if args[0] != "vah264enc" {
		t.Fatalf("element = %q, want vah264enc", args[0])
	}
	if args[1] != "name=venc" {
		t.Errorf("args[1] = %q, want name=venc immediately after the element", args[1])
	}
	assertHasProp(t, args, "key-int-max")
	assertNoProps(t, args, legacyOnlyProps, "vah264enc EncoderArgs")

	gaming := info.GamingEncoderArgs(60, 6000)
	assertHasProp(t, gaming, "key-int-max")
	assertHasProp(t, gaming, "b-frames")
	assertNoProps(t, gaming, legacyOnlyProps, "vah264enc GamingEncoderArgs")

	abr := info.VAEncoderArgs(3000, 30, false)
	assertHasProp(t, abr, "key-int-max")
	assertNoProps(t, abr, legacyOnlyProps, "vah264enc VAEncoderArgs")
}

// The deprecated element keeps its own vocabulary for as long as it exists.
func TestEncoderArgs_LegacyH264UsesLegacyProperties(t *testing.T) {
	info := Info{Tier: TierVAAPI, Encoder: "vaapih264enc"}

	args := info.EncoderArgs()
	if args[0] != "vaapih264enc" {
		t.Fatalf("element = %q, want vaapih264enc", args[0])
	}
	assertHasProp(t, args, "keyframe-period")
	assertNoProps(t, args, modernOnlyProps, "vaapih264enc EncoderArgs")

	gaming := info.GamingEncoderArgs(60, 6000)
	assertHasProp(t, gaming, "keyframe-period")
	assertHasProp(t, gaming, "max-bframes")
	assertNoProps(t, gaming, modernOnlyProps, "vaapih264enc GamingEncoderArgs")
}

// THE BUG THIS FILE WAS WRITTEN FOR: the AV1 path emitted
// "vaav1enc ... keyframe-period=30 tune=low-power". vaav1enc only exists in the
// modern plugin, which defines neither property — so on exactly the hardware
// that has AV1 encode (Intel Arc, AMD RX 7000+), the pipeline could not start.
func TestEncoderArgs_AV1NeverGetsLegacyProperties(t *testing.T) {
	info := Info{Tier: TierVAAPI, Encoder: "vaav1enc", HasAV1: true}

	args := info.EncoderArgs()
	if args[0] != "vaav1enc" {
		t.Fatalf("element = %q, want vaav1enc", args[0])
	}
	assertHasProp(t, args, "key-int-max")
	assertNoProps(t, args, legacyOnlyProps, "vaav1enc EncoderArgs")
}

// An Info built by hand without an encoder element must still produce a
// coherent pipeline rather than an element named "".
func TestEncoderArgs_NoEncoderElementFallsBackCoherently(t *testing.T) {
	info := Info{Tier: TierVAAPI}
	args := info.EncoderArgs()
	if args[0] != "vaapih264enc" {
		t.Fatalf("element = %q, want the vaapih264enc fallback", args[0])
	}
	assertNoProps(t, args, modernOnlyProps, "fallback EncoderArgs")
}

// isLegacyVAElement is what routes the two vocabularies; pin it directly.
func TestIsLegacyVAElement(t *testing.T) {
	for _, tc := range []struct {
		element string
		legacy  bool
	}{
		{"vaapih264enc", true},
		{"vaapipostproc", true},
		{"vah264enc", false},
		{"vaav1enc", false},
		{"vapostproc", false},
	} {
		if got := isLegacyVAElement(tc.element); got != tc.legacy {
			t.Errorf("isLegacyVAElement(%q) = %v, want %v", tc.element, got, tc.legacy)
		}
	}
}

// ---------------------------------------------------------------------------
// Element selection
// ---------------------------------------------------------------------------

// withElements swaps the GStreamer element probe for a fixed set.
func withElements(t *testing.T, present ...string) {
	t.Helper()
	set := make(map[string]bool, len(present))
	for _, e := range present {
		set[e] = true
	}
	orig := gstHasElement
	gstHasElement = func(e string) bool { return set[e] }
	t.Cleanup(func() { gstHasElement = orig })
}

// THE LATENT REGRESSION: the probe used to test for vaapih264enc alone. When
// Debian retires gstreamer1.0-vaapi — upstream removed it in GStreamer 1.28 —
// a box with the modern plugin and a perfectly good encoder would have been
// reported as having no VA-API encoder at all, and every stream would have
// silently dropped to software VP8.
func TestVAH264Element_PrefersModernAndAcceptsEither(t *testing.T) {
	t.Run("both present prefers modern", func(t *testing.T) {
		withElements(t, "vah264enc", "vaapih264enc")
		if got := vaH264Element(); got != "vah264enc" {
			t.Errorf("got %q, want vah264enc", got)
		}
	})
	t.Run("modern only", func(t *testing.T) {
		withElements(t, "vah264enc")
		if got := vaH264Element(); got != "vah264enc" {
			t.Errorf("got %q, want vah264enc — this is the case that used to "+
				"report no hardware encoder at all", got)
		}
	})
	t.Run("deprecated only", func(t *testing.T) {
		withElements(t, "vaapih264enc")
		if got := vaH264Element(); got != "vaapih264enc" {
			t.Errorf("got %q, want vaapih264enc", got)
		}
	})
	t.Run("neither", func(t *testing.T) {
		withElements(t)
		if got := vaH264Element(); got != "" {
			t.Errorf("got %q, want empty so the caller degrades loudly", got)
		}
	})
}

// The AV1 encoder only exists in the modern plugin; there is no legacy
// equivalent to fall back to.
func TestVAAV1Element(t *testing.T) {
	withElements(t, "vaav1enc")
	if got := vaAV1Element(); got != "vaav1enc" {
		t.Errorf("got %q, want vaav1enc", got)
	}
	withElements(t, "vaapih264enc")
	if got := vaAV1Element(); got != "" {
		t.Errorf("got %q, want empty — gstreamer1.0-vaapi never had an AV1 encoder", got)
	}
}

// ConvertArgs must prefer vapostproc and still accept the deprecated one.
func TestConvertArgs_VAPostprocPreference(t *testing.T) {
	info := Info{Tier: TierVAAPI}

	withElements(t, "vapostproc", "vaapipostproc")
	if got := info.ConvertArgs(); got[0] != "vapostproc" {
		t.Errorf("got %v, want vapostproc first", got)
	}
	withElements(t, "vaapipostproc")
	if got := info.ConvertArgs(); got[0] != "vaapipostproc" {
		t.Errorf("got %v, want vaapipostproc", got)
	}
	withElements(t)
	if got := info.ConvertArgs(); got[0] != "videoconvert" {
		t.Errorf("got %v, want the videoconvert fallback", got)
	}
}
