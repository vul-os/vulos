package osdist

// handlers.go — HTTP handlers for OS-update status and apply (OSDIST-05).
//
// Routes (registered externally):
//
//	GET  /api/os/update/status — current version, available version, last check,
//	                              last error, and slot state.
//	POST /api/os/update/apply  — flip an already-staged+verified pending slot to
//	                              active (202); refuses with 409 when no pending
//	                              slot is set.  Does NOT download or verify — that
//	                              is OSDIST-04's responsibility.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ─── UpdateStatus ─────────────────────────────────────────────────────────────

// UpdateStatus is the JSON body returned by GET /api/os/update/status.
type UpdateStatus struct {
	// RunningVersion is the version string of the slot the OS booted from
	// (e.g. "v08").  Sourced from UpdaterConfig.RunningVersion.
	RunningVersion string `json:"running_version"`

	// AvailableVersion is the latest version seen in the manifest, if it
	// differs from RunningVersion.  Empty when the system is up to date or
	// no check has completed yet.
	AvailableVersion string `json:"available_version,omitempty"`

	// LastCheck is the UTC timestamp of the most recent completed update check
	// (whether it found an update or not).  Zero value is omitted.
	LastCheck *time.Time `json:"last_check,omitempty"`

	// LastError is the error message from the most recent failed update check.
	// Empty when the last check succeeded.
	LastError string `json:"last_error,omitempty"`

	// SlotState summarises the current BootState fields relevant to the UI.
	SlotState SlotSummary `json:"slot_state"`
}

// SlotSummary is an embedded sub-object carrying the slot fields the UI needs.
type SlotSummary struct {
	Active        string `json:"active"`
	Pending       string `json:"pending,omitempty"`
	LastKnownGood string `json:"last_known_good,omitempty"`
}

// ─── StatusStore ──────────────────────────────────────────────────────────────

// StatusStore holds the mutable update-check state that handlers read and the
// Updater writes.  It is safe for concurrent use.
//
// Wire it between NewStatusStore, Updater.SetStatusStore, and
// NewUpdateHandlers so that the periodic loop's results are visible to the
// HTTP handlers without direct coupling.
type StatusStore struct {
	mu               sync.RWMutex
	availableVersion string
	lastCheck        *time.Time
	lastError        string
}

// NewStatusStore returns an initialised, empty StatusStore.
func NewStatusStore() *StatusStore { return &StatusStore{} }

// RecordSuccess records a successful update check.  availableVersion is the
// manifest's Latest field; pass "" when the system is already up to date.
func (s *StatusStore) RecordSuccess(availableVersion string) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.availableVersion = availableVersion
	s.lastCheck = &now
	s.lastError = ""
	s.mu.Unlock()
}

// RecordError records a failed update check.
func (s *StatusStore) RecordError(err error) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.lastCheck = &now
	s.lastError = err.Error()
	s.mu.Unlock()
}

// snapshot returns a point-in-time copy of the store's fields.
func (s *StatusStore) snapshot() (available string, lastCheck *time.Time, lastErr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var lc *time.Time
	if s.lastCheck != nil {
		t := *s.lastCheck
		lc = &t
	}
	return s.availableVersion, lc, s.lastError
}

// ─── UpdateHandlers ───────────────────────────────────────────────────────────

// UpdateHandlers exposes the OS-update HTTP endpoints.
type UpdateHandlers struct {
	slotMgr        *SlotManager
	runningVersion string
	store          *StatusStore
}

// NewUpdateHandlers creates UpdateHandlers.
//
//   - slotMgr        — the shared SlotManager (OSDIST-02).
//   - runningVersion — version string of the currently-running OS slot
//     (e.g. "v08"); passed through UpdaterConfig.RunningVersion.
//   - store          — the StatusStore that the Updater writes to; may be nil,
//     in which case last_check and last_error are always absent.
func NewUpdateHandlers(slotMgr *SlotManager, runningVersion string, store *StatusStore) *UpdateHandlers {
	return &UpdateHandlers{
		slotMgr:        slotMgr,
		runningVersion: runningVersion,
		store:          store,
	}
}

// RegisterHandlers mounts the two routes onto mux.
func (h *UpdateHandlers) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/os/update/status", h.handleStatus)
	mux.HandleFunc("POST /api/os/update/apply", h.handleApply)
}

// ─── GET /api/os/update/status ────────────────────────────────────────────────

func (h *UpdateHandlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	bs, err := h.slotMgr.Load()
	if err != nil {
		writeUpdateErr(w, 500, fmt.Sprintf("load boot state: %v", err))
		return
	}

	status := UpdateStatus{
		RunningVersion: h.runningVersion,
		SlotState: SlotSummary{
			Active:        string(bs.Active),
			Pending:       string(bs.Pending),
			LastKnownGood: string(bs.LastKnownGood),
		},
	}

	if h.store != nil {
		available, lastCheck, lastErr := h.store.snapshot()
		if available != "" && available != h.runningVersion {
			status.AvailableVersion = available
		}
		status.LastCheck = lastCheck
		status.LastError = lastErr
	}

	writeUpdateJSON(w, status)
}

// ─── POST /api/os/update/apply ────────────────────────────────────────────────

func (h *UpdateHandlers) handleApply(w http.ResponseWriter, r *http.Request) {
	bs, err := h.slotMgr.Load()
	if err != nil {
		writeUpdateErr(w, 500, fmt.Sprintf("load boot state: %v", err))
		return
	}

	// Refuse if no pending slot — the frontend must not call this without a
	// staged+verified update (OSDIST-04 sets pending after verification).
	if bs.Pending == SlotNone {
		writeUpdateErr(w, 409, "no pending update; stage and verify an update first (OSDIST-04)")
		return
	}

	next, err := h.slotMgr.Flip(bs)
	if err != nil {
		writeUpdateErr(w, 500, fmt.Sprintf("flip slot: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted) // 202
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "reboot to apply",
		"new_active": string(next.Active),
	})
}

// ─── local JSON helpers ───────────────────────────────────────────────────────

func writeUpdateJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeUpdateErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
