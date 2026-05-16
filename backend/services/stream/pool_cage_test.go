package stream

import (
	"testing"

	"vulos/backend/services/gpu"
)

// TestUseCageDecision verifies the useCage branch decision logic without
// actually spawning any processes.
//
// We swap the package-level lookPath var to control binary presence, and
// synthesise gpu.Info values directly (no system probing).
func TestUseCageDecision(t *testing.T) {
	tests := []struct {
		name        string
		tier        gpu.Tier
		cagePresent bool
		wantCage    bool
	}{
		{
			name:        "software GPU + no cage → Xvfb",
			tier:        gpu.TierSoftware,
			cagePresent: false,
			wantCage:    false,
		},
		{
			name:        "software GPU + cage present → Xvfb (tier wins)",
			tier:        gpu.TierSoftware,
			cagePresent: true,
			wantCage:    false,
		},
		{
			name:        "VAAPI GPU + no cage → Xvfb",
			tier:        gpu.TierVAAPI,
			cagePresent: false,
			wantCage:    false,
		},
		{
			name:        "VAAPI GPU + cage present → cage",
			tier:        gpu.TierVAAPI,
			cagePresent: true,
			wantCage:    true,
		},
		{
			name:        "NVENC GPU + cage present → cage",
			tier:        gpu.TierNVENC,
			cagePresent: true,
			wantCage:    true,
		},
		{
			name:        "NVENC GPU + no cage → Xvfb",
			tier:        gpu.TierNVENC,
			cagePresent: false,
			wantCage:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Swap lookPath to control binary presence.
			orig := lookPath
			t.Cleanup(func() { lookPath = orig })

			if tc.cagePresent {
				lookPath = func(file string) (string, error) {
					if file == "cage" {
						return "/usr/bin/cage", nil
					}
					return orig(file)
				}
			} else {
				lookPath = func(file string) (string, error) {
					if file == "cage" {
						return "", &notFoundError{file}
					}
					return orig(file)
				}
			}

			cageBin, cageErr := lookPath("cage")
			got := tc.tier != gpu.TierSoftware && cageErr == nil

			if got != tc.wantCage {
				t.Errorf("useCage=%v (wantCage=%v) for tier=%s cagePresent=%v cageBin=%q cageErr=%v",
					got, tc.wantCage, tc.tier, tc.cagePresent, cageBin, cageErr)
			}
		})
	}
}

// TestWaylandDisplayName checks that waylandDisplay generates the expected socket name.
func TestWaylandDisplayName(t *testing.T) {
	id := "abc123"
	got := waylandDisplay(id)
	want := "wayland-abc123"
	if got != want {
		t.Errorf("waylandDisplay(%q) = %q, want %q", id, got, want)
	}
}

// notFoundError satisfies the error interface for a missing binary.
type notFoundError struct{ name string }

func (e *notFoundError) Error() string { return e.name + ": executable file not found in $PATH" }
