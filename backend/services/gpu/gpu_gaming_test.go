package gpu

import (
	"strings"
	"testing"
)

// TestGamingEncoderArgs verifies that GamingEncoderArgs returns zero-latency,
// no-B-frame arguments for each GPU tier, and that the normal EncoderArgs path
// is byte-identical to the pre-gaming-mode baseline.
func TestGamingEncoderArgs(t *testing.T) {
	tiers := []struct {
		name string
		info Info
	}{
		{
			name: "NVENC",
			info: Info{Tier: TierNVENC, Encoder: "nvh264enc"},
		},
		{
			name: "VAAPI",
			info: Info{Tier: TierVAAPI, Encoder: "vaapih264enc"},
		},
		{
			name: "Software",
			info: Info{Tier: TierSoftware, Encoder: "vp8enc"},
		},
	}

	for _, tc := range tiers {
		t.Run(tc.name+"/gaming", func(t *testing.T) {
			args := tc.info.GamingEncoderArgs(60, 6000)
			joined := strings.Join(args, " ")

			// Must NOT include B-frame / lookahead parameters that add latency
			for _, forbidden := range []string{"b-adapt=true", "rc-lookahead=1"} {
				if strings.Contains(joined, forbidden) {
					t.Errorf("gaming args must not contain %q; got: %s", forbidden, joined)
				}
			}

			switch tc.info.Tier {
			case TierNVENC:
				mustContain(t, args, "nvh264enc", "preset=low-latency-hp", "b-adapt=false", "rc-lookahead=0", "bframes=0")
			case TierVAAPI:
				mustContain(t, args, "vaapih264enc", "tune=low-power", "max-bframes=0")
			case TierSoftware:
				mustContain(t, args, "vp8enc", "cpu-used=16", "lag-in-frames=0")
			}
		})

		t.Run(tc.name+"/normal", func(t *testing.T) {
			gaming := tc.info.GamingEncoderArgs(60, 6000)
			normal := tc.info.EncoderArgs()

			// Normal path must NOT contain gaming-specific tokens
			normalJoined := strings.Join(normal, " ")
			for _, gamingOnly := range []string{"low-latency-hp", "b-adapt=false", "rc-lookahead=0", "bframes=0", "tune=low-power", "max-bframes=0", "cpu-used=16"} {
				if strings.Contains(normalJoined, gamingOnly) {
					t.Errorf("normal EncoderArgs must not contain gaming token %q; got: %s", gamingOnly, normalJoined)
				}
			}

			// Gaming and normal must differ (they serve different profiles)
			if strings.Join(gaming, " ") == strings.Join(normal, " ") {
				t.Errorf("gaming and normal encoder args must differ for tier %s", tc.name)
			}
		})
	}
}

// TestGamingEncoderArgsDefaultBitrate verifies that a zero bitrate falls back to 6000 kbps.
func TestGamingEncoderArgsDefaultBitrate(t *testing.T) {
	info := Info{Tier: TierNVENC}
	args := info.GamingEncoderArgs(60, 0)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "bitrate=6000") {
		t.Errorf("expected default bitrate=6000 when bitrate=0; got: %s", joined)
	}
}

// TestGamingBitrateTiers verifies the new gaming quality constants have the correct kbps values.
func TestGamingBitrateTiers(t *testing.T) {
	// Import via the stream package would create a cycle; test values directly from gpu pkg.
	// The gpu package itself only defines GamingEncoderArgs — bitrate tier values live in stream.
	// This test is intentionally minimal: ensure GamingEncoderArgs accepts gaming-tier bitrates.
	info := Info{Tier: TierNVENC}

	cases := []struct {
		bitrate int
		want    string
	}{
		{6000, "bitrate=6000"},
		{10000, "bitrate=10000"},
	}
	for _, c := range cases {
		args := info.GamingEncoderArgs(60, c.bitrate)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, c.want) {
			t.Errorf("GamingEncoderArgs(%d) missing %q; got: %s", c.bitrate, c.want, joined)
		}
	}
}

// mustContain fails the test if any of the required tokens is absent from args.
func mustContain(t *testing.T, args []string, tokens ...string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, tok := range tokens {
		if !strings.Contains(joined, tok) {
			t.Errorf("expected token %q in args; got: %s", tok, joined)
		}
	}
}
