package contacts

// handlers.go — the OWNER-GATED HTTP surface of the unified address book.
//
//	GET  /api/contacts/unified        → the merged, de-duplicated list (+ which
//	                                     sources are currently active).
//	POST /api/contacts/ingest/device  → the Android ContactsBridge pushes the
//	                                     phone's device + SIM contacts here.
//
// Every route is denied unless the caller is the resolved box owner (fail-closed:
// an unresolved owner denies everyone). The box SIM and the pushed phone book are
// the owner's personal address book — another account on a multi-user box must
// never see them. The CardDAV read forwards the caller's OWN session cookie to
// lilmail, so it can only ever return the caller's own cards.

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

// DeviceContact is one contact as PUSHED by the Android ContactsBridge (the
// phone's device + phone-SIM address book). Sparse entries are fine — the merge
// layer copes.
type DeviceContact struct {
	Name   string   `json:"name"`
	Emails []string `json:"emails,omitempty"`
	Phones []string `json:"phones,omitempty"`
	Org    string   `json:"org,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// Service aggregates the box's contact sources into the unified list. The two
// external sources are injected as seams so this package stays free of the mail
// and telephony packages (and is trivially testable):
//
//	CardDAVSource — reads the caller's CardDAV/Vulos cards (forwards their cookie).
//	SIMSource     — reads the box SIM phonebook (best-effort, usually inactive).
//
// The phone source is not a seam: it is state the phone PUSHES, held here in
// memory per owner (it re-pushes on every sync, so a restart just re-populates).
type Service struct {
	ownerID func() string

	// CardDAVSource returns the caller's cards and whether the source is active
	// (false ⇒ mail unconfigured/unreachable, so the vulos source is omitted).
	CardDAVSource func(r *http.Request) ([]RawContact, bool)
	// SIMSource returns the box SIM phonebook and whether the source is active.
	SIMSource func() ([]RawContact, bool)

	mu     sync.Mutex
	device map[string][]RawContact // owner id → last device push
	pushed map[string]bool         // owner id → has ever pushed (active even if empty)
}

// New builds a Service. ownerID resolves the box owner live (nil ⇒ deny-all).
func New(ownerID func() string) *Service {
	if ownerID == nil {
		ownerID = func() string { return "" }
	}
	return &Service{ownerID: ownerID, device: map[string][]RawContact{}}
}

func (s *Service) isOwner(userID string) bool {
	owner := s.ownerID()
	return owner != "" && userID == owner
}

// requireOwner gates a handler to the resolved box owner. Fail-closed.
func (s *Service) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwner(r.Header.Get("X-User-ID")) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// RegisterHandlers wires the unified-contacts routes onto mux.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/contacts/unified", s.requireOwner(s.handleUnified))
	mux.HandleFunc("POST /api/contacts/ingest/device", s.requireOwner(s.handleIngestDevice))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleUnified aggregates every active source, merges, and returns the unified
// list plus the set of sources that actually contributed (so the UI can render
// an honest "no SIM / phone not linked" affordance).
func (s *Service) handleUnified(w http.ResponseWriter, r *http.Request) {
	var raws []RawContact
	activeSet := map[string]bool{}

	if s.CardDAVSource != nil {
		if cs, ok := s.CardDAVSource(r); ok {
			activeSet[string(SourceVulos)] = true
			raws = append(raws, cs...)
		}
	}

	s.mu.Lock()
	dev := s.device[s.ownerID()]
	pushed := s.hasPushed(s.ownerID())
	s.mu.Unlock()
	if pushed {
		activeSet[string(SourcePhone)] = true
		raws = append(raws, dev...)
	}

	if s.SIMSource != nil {
		if sc, ok := s.SIMSource(); ok {
			activeSet[string(SourceBoxSIM)] = true
			raws = append(raws, sc...)
		}
	}

	active := make([]string, 0, len(activeSet))
	for src := range activeSet {
		active = append(active, src)
	}
	sort.Slice(active, func(i, j int) bool {
		return Source(active[i]).priority() < Source(active[j]).priority()
	})

	writeJSON(w, map[string]any{
		"contacts":       Merge(raws),
		"sources_active": active,
	})
}

// handleIngestDevice stores the phone's pushed device/SIM contacts for the owner,
// replacing any previous push (the phone sends its full book each sync).
func (s *Service) handleIngestDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Contacts []DeviceContact `json:"contacts"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	raws := make([]RawContact, 0, len(body.Contacts))
	for _, c := range body.Contacts {
		raws = append(raws, RawContact{
			Name:   c.Name,
			Emails: c.Emails,
			Phones: c.Phones,
			Org:    c.Org,
			Note:   c.Note,
			Source: SourcePhone,
		})
	}
	owner := s.ownerID()
	s.mu.Lock()
	s.device[owner] = raws
	s.pushedMark(owner)
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "count": len(raws)})
}

// hasPushed reports whether an owner has ever pushed, so an owner who pushed an
// EMPTY book still counts the phone as an active (linked) source. Guarded by mu.
func (s *Service) hasPushed(owner string) bool {
	return s.pushed[owner]
}

func (s *Service) pushedMark(owner string) {
	if s.pushed == nil {
		s.pushed = map[string]bool{}
	}
	s.pushed[owner] = true
}
