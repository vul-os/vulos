package main

// routes_contacts.go — wires the box's UNIFIED address book (services/contacts)
// into the server and supplies its two external sources:
//
//	CardDAV/Vulos — an internal, credential-brokered read of lilmail's
//	                /v1/contacts/cards (same seam as routes_pim.go + the
//	                assistant), forwarding the caller's own session cookie.
//	Box SIM       — the telephony service's best-effort SIM phonebook (usually
//	                inactive; contributes only when opted in and a modem answers).
//
// The phone source needs no wiring here — the Android bridge PUSHES it to
// POST /api/contacts/ingest/device, which services/contacts holds per owner.
//
// Owner resolution matches routes_telephony.go: the box's SIM and personal
// address book are the ADMIN/owner's, resolved live so a later first-user setup
// is tracked.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"vulos/backend/services/auth"
	"vulos/backend/services/contacts"
	"vulos/backend/services/telephony"
)

func registerContactsRoutes(mux *http.ServeMux, authStore *auth.Store, telephonySvc *telephony.Service) {
	ownerID := func() string {
		if authStore == nil {
			return ""
		}
		return authStore.AdminUserID()
	}

	svc := contacts.New(ownerID)

	mailBase := mailBaseURLFromEnv()
	broker := assistantBrokerHeaders()
	svc.CardDAVSource = func(r *http.Request) ([]contacts.RawContact, bool) {
		return fetchCardDAVCards(r, mailBase, broker)
	}

	if telephonySvc != nil {
		svc.SIMSource = func() ([]contacts.RawContact, bool) {
			sim, ok := telephonySvc.SIMPhonebook()
			if !ok {
				return nil, false
			}
			out := make([]contacts.RawContact, 0, len(sim))
			for _, c := range sim {
				var phones []string
				if strings.TrimSpace(c.Number) != "" {
					phones = []string{c.Number}
				}
				out = append(out, contacts.RawContact{
					Name:   c.Name,
					Phones: phones,
					Source: contacts.SourceBoxSIM,
				})
			}
			return out, true
		}
	}

	svc.RegisterHandlers(mux)
}

// lilCard mirrors lilmail's GET /v1/contacts/cards wire shape (see
// services/assistant/mail.go). A drift here silently blanks a field.
type lilCard struct {
	UID    string   `json:"uid"`
	Name   string   `json:"name"`
	Org    string   `json:"org,omitempty"`
	Title  string   `json:"title,omitempty"`
	Note   string   `json:"note,omitempty"`
	Emails []string `json:"emails"`
	Phones []string `json:"phones,omitempty"`
}

// fetchCardDAVCards reads the caller's CardDAV/Vulos cards from lilmail, brokered.
// The caller's session cookie is forwarded (so lilmail returns only their data)
// and the CP-brokered X-Vulos-Mail-* headers are injected — the browser never
// sees either. Returns active=false (source omitted) when mail is unconfigured or
// unreachable, so the unified list degrades to whatever other sources are present.
func fetchCardDAVCards(r *http.Request, mailBase string, broker map[string]string) ([]contacts.RawContact, bool) {
	if strings.TrimSpace(mailBase) == "" {
		return nil, false
	}
	url := strings.TrimRight(mailBase, "/") + "/v1/contacts/cards?limit=1000"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	if c := r.Header.Get("Cookie"); c != "" {
		req.Header.Set("Cookie", c)
	}
	for k, v := range broker {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var out struct {
		Contacts []lilCard `json:"contacts"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, false
	}

	raws := make([]contacts.RawContact, 0, len(out.Contacts))
	for _, c := range out.Contacts {
		title := strings.TrimSpace(c.Title)
		org := strings.TrimSpace(c.Org)
		// lilmail carries title separately; fold it into Org for the unified
		// entry (the unified model has no title field), preferring "Title, Org".
		switch {
		case title != "" && org != "":
			org = title + ", " + org
		case title != "":
			org = title
		}
		raws = append(raws, contacts.RawContact{
			ID:     c.UID,
			Name:   c.Name,
			Emails: c.Emails,
			Phones: c.Phones,
			Org:    org,
			Note:   c.Note,
			Source: contacts.SourceVulos,
		})
	}
	return raws, true
}
