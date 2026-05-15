package stream

import "testing"

// TestGamingQualityConstants verifies the gaming bitrate tier values and their
// String() representations match the roadmap specification.
func TestGamingQualityConstants(t *testing.T) {
	cases := []struct {
		q       Quality
		bitrate int
		str     string
	}{
		{QualityLow, 500, "low"},
		{QualityMedium, 1500, "medium"},
		{QualityHigh, 2500, "high"},
		{QualityMax, 4000, "max"},
		{QualityGaming, 6000, "gaming"},
		{QualityGamingMax, 10000, "gaming-max"},
	}

	for _, c := range cases {
		if got := c.q.Bitrate(); got != c.bitrate {
			t.Errorf("Quality(%d).Bitrate() = %d, want %d", int(c.q), got, c.bitrate)
		}
		if got := c.q.String(); got != c.str {
			t.Errorf("Quality(%d).String() = %q, want %q", int(c.q), got, c.str)
		}
	}
}

// TestNormalBitratesUnchanged verifies existing non-gaming bitrates are byte-identical
// to their pre-GAME-02 values (regression guard).
func TestNormalBitratesUnchanged(t *testing.T) {
	baseline := map[Quality]int{
		QualityLow:    500,
		QualityMedium: 1500,
		QualityHigh:   2500,
		QualityMax:    4000,
	}
	for q, want := range baseline {
		if got := q.Bitrate(); got != want {
			t.Errorf("non-gaming Quality(%d).Bitrate() changed: got %d, want %d (regression)", int(q), got, want)
		}
	}
}
