package installer

import (
	"context"
	"encoding/json"
	"net/http"
)

// LiveSession describes the current live-RAM session state returned by
// GET /api/installer/live-session.
type LiveSession struct {
	// IsLive is true when the OS is booted from the --live squashfs+overlay
	// (RAM-only mode — no writes reach permanent storage).
	IsLive bool `json:"is_live"`
	// RootDev is the overlay/squashfs device when IsLive is true.
	RootDev string `json:"root_dev,omitempty"`
}

// LiveSessionInfo detects whether the OS is running in live-RAM mode
// (i.e. the root filesystem is a squashfs with a writable overlay, as set up
// by the --live boot flag in BMINIT-14).  It reuses the existing detectStatus
// logic so both endpoints stay consistent.
func (s *Service) LiveSessionInfo(ctx context.Context) LiveSession {
	sr := s.detectStatus(ctx)
	return LiveSession{
		IsLive:  sr.Mode == "live",
		RootDev: sr.RootDev,
	}
}

// handleLiveSession serves GET /api/installer/live-session.
// It reports whether the OS is running in live-RAM mode and, if so, signals
// that the user can choose between "Try Vulos" (stay in RAM) and "Install
// Vulos" (hand off to the NETB-03 installer pipeline).
func (s *Service) handleLiveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := s.LiveSessionInfo(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
