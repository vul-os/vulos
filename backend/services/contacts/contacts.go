// Package contacts is the box-side UNIFIED address book: it aggregates every
// contact source the box can see into a single, de-duplicated list, and records
// on each merged entry WHICH sources it came from so the UI can show provenance.
//
// Three sources feed in, all OPTIONAL — the unified list is only ever as rich as
// what is actually present:
//
//	vulos    — the user's CardDAV / Vulos contacts, read from lilmail's
//	           GET /v1/contacts/cards (the same surface the existing
//	           /api/pim/contacts proxy and the assistant already use).
//	phone    — the Android app's device + SIM contacts, PUSHED up to the box by
//	           the native ContactsBridge via POST /api/contacts/ingest/device.
//	box-sim  — the contacts stored on a SIM plugged into the box itself, read by
//	           services/telephony's best-effort SIM phonebook seam.
//
// If the phone has never pushed and the box has no SIM, the unified list is just
// the CardDAV contacts. Nothing here is required for the box to run.
//
// SECURITY: the whole surface is OWNER-GATED and fail-closed. The box's SIM and
// the pushed phone book are the OWNER's personal address book; another account on
// a multi-user box must never see them, so every endpoint is denied unless the
// caller is the resolved box owner (see handlers.go). The CardDAV read forwards
// the caller's OWN session cookie to lilmail, so it can only ever return the
// caller's own CardDAV data.
package contacts

// Source identifies where a contact (or one of its merged constituents) came from.
type Source string

const (
	SourceVulos  Source = "vulos"   // CardDAV / lilmail contacts
	SourcePhone  Source = "phone"   // pushed from the Android app (device + phone SIM)
	SourceBoxSIM Source = "box-sim" // read from a SIM plugged into the box
)

// sourcePriority orders sources for "which field wins" decisions during a merge
// (lower = more authoritative). CardDAV is the user's curated book, so it wins
// ties; the pushed phone book next; the raw SIM phonebook last.
var sourcePriority = map[Source]int{
	SourceVulos:  0,
	SourcePhone:  1,
	SourceBoxSIM: 2,
}

func (s Source) priority() int {
	if p, ok := sourcePriority[s]; ok {
		return p
	}
	return 99
}

// RawContact is one contact as read from a single source, before merging. It is
// the common shape every source normalises onto.
type RawContact struct {
	// ID is a source-local stable id (device raw-contact id, CardDAV uid, ...).
	// Optional; used only for diagnostics, never trusted for merging.
	ID     string
	Name   string
	Emails []string
	Phones []string
	Org    string
	Note   string
	Source Source
}

// UnifiedContact is a merged address-book entry — the wire shape returned by
// GET /api/contacts/unified. Phones/Emails are the de-duplicated union across the
// merged constituents; Sources is the sorted set of sources that contributed.
type UnifiedContact struct {
	// ID is a STABLE, content-derived id (a hash of the entry's match keys), so a
	// UI can key on it across refreshes even though nothing persists it.
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Phones  []string `json:"phones,omitempty"`
	Emails  []string `json:"emails,omitempty"`
	Org     string   `json:"org,omitempty"`
	Note    string   `json:"note,omitempty"`
	Sources []string `json:"sources"`
}
