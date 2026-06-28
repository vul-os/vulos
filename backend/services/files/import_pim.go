package files

// import_pim.go — CONTACTS + CALENDAR import sources and runner.
//
// Architecture overview
// ─────────────────────
// Contacts and calendar live in vulos-mail's CardDAV/CalDAV stores, not in the
// OS Drive. Their import flow therefore diverges from the file import flow
// (importWalk → importWriteFile → Drive bucket) after the provider API call:
// instead of writing bytes into the Drive, the PIM runner POSTs bulk vCard /
// iCal payloads to vulos-mail's broker-gated /api/admin/import/contacts and
// /api/admin/import/events endpoints.
//
// PERSIST-AFTER-DISCONNECT guarantee: once a contact or event is written to
// vulos-mail, it is the user's owned copy in the CardDAV/CalDAV store.
// Disconnecting the Google or Microsoft integration does not remove it. The OS
// removes only the import job metadata (files_import_jobs), not the data.
//
// Additive-only sync (mode=sync): the OS tracks (ownerID, provider, externalID)
// in files_import_items. A re-pull only imports items not yet in that table — it
// NEVER deletes the vulos-mail copy when the upstream entry is removed. This is
// the same dedup/additive guarantee the files import engine has.
//
// SSRF safety: both sources talk ONLY to fixed Google API hosts
// (people.googleapis.com, www.googleapis.com/calendar). No caller-controlled
// URL, scheme, or host is ever used.
//
// Implemented sources:
//   GoogleContactsSource   — Google Contacts via People API v1
//   GoogleCalendarSource   — Google Calendar via Calendar API v3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Fixed API hosts (SSRF guard) ─────────────────────────────────────────────

const (
	peopleAPIBase   = "https://people.googleapis.com/v1"
	calendarAPIBase = "https://www.googleapis.com/calendar/v3"
	pimMaxPageSize  = 1000
	pimBatchSize    = 100
)

// ── Google Contacts source ────────────────────────────────────────────────────

// GoogleContactsSource implements ImportSource for Google Contacts via the
// People API v1. Each Google contact is pulled as a structured person resource,
// mapped to a vCard 4.0 string, and posted to vulos-mail's
// /api/admin/import/contacts endpoint by runPIMImport.
type GoogleContactsSource struct {
	peopleBase string       // overridable in tests; production: peopleAPIBase
	httpClient *http.Client // bounded; only ever contacts people.googleapis.com
}

// NewGoogleContactsSource returns a GoogleContactsSource with a bounded HTTP
// client. The client only ever contacts people.googleapis.com (SSRF-safe).
func NewGoogleContactsSource() *GoogleContactsSource {
	return &GoogleContactsSource{
		peopleBase: peopleAPIBase,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GoogleContactsSource) Kind() string                { return "google-contacts" }
func (g *GoogleContactsSource) IntegrationProvider() string { return "google" }
func (g *GoogleContactsSource) DisplayName() string         { return "Google Contacts" }

// DataKind implements the optional DataKind interface so importDataKind() sets
// job.Kind = "contacts" automatically when this source is selected.
func (g *GoogleContactsSource) DataKind() string { return ImportKindContacts }

// List returns all of the authenticated user's Google contacts as flat Nodes
// (contacts have no directory hierarchy). Each Node.ID is the People API
// resourceName (e.g. "people/c1234567890"), used as the dedup key in
// files_import_items.
func (g *GoogleContactsSource) List(ctx context.Context, call ProviderCall, _ string) ([]*Node, error) {
	var out []*Node
	pageToken := ""
	for {
		u := g.peopleBase + "/people/me/connections" +
			"?personFields=names,emailAddresses,phoneNumbers,organizations" +
			"&pageSize=" + fmt.Sprint(pimMaxPageSize)
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("google-contacts: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+call.Token)
		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("google-contacts: list: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("google-contacts: list %d: %s", resp.StatusCode, trimBody(body))
		}
		var page struct {
			Connections []personResource `json:"connections"`
			NextPage    string           `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("google-contacts: parse list: %w", err)
		}
		for i := range page.Connections {
			p := &page.Connections[i]
			out = append(out, &Node{
				ID:          p.ResourceName,
				Name:        p.displayName(),
				IsDir:       false,
				ContentType: "text/vcard",
			})
		}
		if page.NextPage == "" {
			break
		}
		pageToken = page.NextPage
	}
	return out, nil
}

// OpenForImport re-fetches a single Google contact by resourceName and returns
// its vCard 4.0 bytes as an io.ReadCloser. Used by runPIMImport's per-item
// fetch step; the node.ID is the resourceName for the API call.
func (g *GoogleContactsSource) OpenForImport(ctx context.Context, call ProviderCall, node *Node) (io.ReadCloser, string, string, int64, error) {
	u := g.peopleBase + "/" + url.PathEscape(node.ID) +
		"?personFields=names,emailAddresses,phoneNumbers,organizations"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", "", -1, fmt.Errorf("google-contacts: open %s: %w", node.ID, err)
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, "", "", -1, fmt.Errorf("google-contacts: fetch %s: %w", node.ID, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", -1, fmt.Errorf("google-contacts: fetch %d: %s", resp.StatusCode, trimBody(body))
	}
	var p personResource
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "", "", -1, fmt.Errorf("google-contacts: parse person: %w", err)
	}
	vc := p.toVCard()
	data := []byte(vc)
	return io.NopCloser(bytes.NewReader(data)), node.ID + ".vcf", "text/vcard", int64(len(data)), nil
}

// personResource is the subset of a People API person resource we need.
type personResource struct {
	ResourceName   string `json:"resourceName"`
	EmailAddresses []struct {
		Value string `json:"value"`
	} `json:"emailAddresses"`
	Names []struct {
		DisplayName string `json:"displayName"`
		GivenName   string `json:"givenName"`
		FamilyName  string `json:"familyName"`
	} `json:"names"`
	PhoneNumbers []struct {
		Value string `json:"value"`
	} `json:"phoneNumbers"`
	Organizations []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"organizations"`
}

func (p *personResource) displayName() string {
	if len(p.Names) > 0 && p.Names[0].DisplayName != "" {
		return p.Names[0].DisplayName
	}
	if len(p.EmailAddresses) > 0 {
		return p.EmailAddresses[0].Value
	}
	return p.ResourceName
}

// toVCard builds a minimal vCard 4.0 string. UID = resourceName for consistent
// dedup between the OS files_import_items table and vulos-mail's store href.
func (p *personResource) toVCard() string {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:4.0\r\n")
	b.WriteString("UID:" + vcfEscape(p.ResourceName) + "\r\n")
	b.WriteString("FN:" + vcfEscape(p.displayName()) + "\r\n")
	if len(p.Names) > 0 {
		n := p.Names[0]
		b.WriteString("N:" + vcfEscape(n.FamilyName) + ";" + vcfEscape(n.GivenName) + ";;;\r\n")
	}
	for _, em := range p.EmailAddresses {
		if em.Value != "" {
			b.WriteString("EMAIL;TYPE=INTERNET:" + vcfEscape(em.Value) + "\r\n")
		}
	}
	for _, ph := range p.PhoneNumbers {
		if ph.Value != "" {
			b.WriteString("TEL:" + vcfEscape(ph.Value) + "\r\n")
		}
	}
	for _, org := range p.Organizations {
		if org.Name != "" {
			b.WriteString("ORG:" + vcfEscape(org.Name) + "\r\n")
		}
		if org.Title != "" {
			b.WriteString("TITLE:" + vcfEscape(org.Title) + "\r\n")
		}
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

// vcfEscape escapes special characters in a vCard property value (RFC 6350 §3.4).
func vcfEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, ";", `\;`)
	return s
}

// ── Google Calendar source ────────────────────────────────────────────────────

// GoogleCalendarSource implements ImportSource for Google Calendar via the
// Calendar API v3. Each event from the primary calendar is mapped to an
// iCalendar VCALENDAR string and posted to vulos-mail's /api/admin/import/events.
type GoogleCalendarSource struct {
	calBase    string       // overridable in tests; production: calendarAPIBase
	httpClient *http.Client // bounded; only ever contacts www.googleapis.com
}

// NewGoogleCalendarSource returns a GoogleCalendarSource with a bounded HTTP
// client. The client only ever contacts www.googleapis.com (SSRF-safe).
func NewGoogleCalendarSource() *GoogleCalendarSource {
	return &GoogleCalendarSource{
		calBase:    calendarAPIBase,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GoogleCalendarSource) Kind() string                { return "google-calendar" }
func (g *GoogleCalendarSource) IntegrationProvider() string { return "google" }
func (g *GoogleCalendarSource) DisplayName() string         { return "Google Calendar" }

// DataKind implements the optional DataKind interface so importDataKind() sets
// job.Kind = "calendar" automatically when this source is selected.
func (g *GoogleCalendarSource) DataKind() string { return ImportKindCalendar }

// List returns all events from the user's primary Google Calendar as flat Nodes.
// Each Node.ID is the Calendar API event ID used as the dedup key.
func (g *GoogleCalendarSource) List(ctx context.Context, call ProviderCall, _ string) ([]*Node, error) {
	var out []*Node
	pageToken := ""
	for {
		u := g.calBase + "/calendars/primary/events" +
			"?maxResults=" + fmt.Sprint(pimMaxPageSize) +
			"&singleEvents=true&orderBy=startTime"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("google-calendar: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+call.Token)
		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("google-calendar: list: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("google-calendar: list %d: %s", resp.StatusCode, trimBody(body))
		}
		var page struct {
			Items    []calEventResource `json:"items"`
			NextPage string             `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("google-calendar: parse list: %w", err)
		}
		for i := range page.Items {
			ev := &page.Items[i]
			out = append(out, &Node{
				ID:          ev.ID,
				Name:        ev.Summary,
				IsDir:       false,
				ContentType: "text/calendar",
			})
		}
		if page.NextPage == "" {
			break
		}
		pageToken = page.NextPage
	}
	return out, nil
}

// OpenForImport re-fetches a single Calendar event by ID and returns its
// iCalendar VCALENDAR bytes as an io.ReadCloser.
func (g *GoogleCalendarSource) OpenForImport(ctx context.Context, call ProviderCall, node *Node) (io.ReadCloser, string, string, int64, error) {
	u := g.calBase + "/calendars/primary/events/" + url.PathEscape(node.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", "", -1, fmt.Errorf("google-calendar: build fetch: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, "", "", -1, fmt.Errorf("google-calendar: fetch %s: %w", node.ID, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", -1, fmt.Errorf("google-calendar: fetch %d: %s", resp.StatusCode, trimBody(body))
	}
	var ev calEventResource
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, "", "", -1, fmt.Errorf("google-calendar: parse event: %w", err)
	}
	ics := ev.toICS()
	data := []byte(ics)
	return io.NopCloser(bytes.NewReader(data)), node.ID + ".ics", "text/calendar", int64(len(data)), nil
}

// calEventResource is the subset of a Calendar API event resource we map to iCal.
type calEventResource struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Start   struct {
		DateTime string `json:"dateTime"` // RFC3339; empty for all-day events
		Date     string `json:"date"`     // YYYY-MM-DD for all-day events
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	} `json:"end"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

// toICS builds a minimal iCalendar VCALENDAR wrapping one VEVENT. UID = event
// ID for dedup consistency with files_import_items.
func (e *calEventResource) toICS() string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//vulos//import//EN\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + icsEscape(e.ID) + "\r\n")
	b.WriteString("DTSTAMP:" + icsNow() + "\r\n")
	if e.Summary != "" {
		b.WriteString("SUMMARY:" + icsEscape(e.Summary) + "\r\n")
	}
	if e.Description != "" {
		b.WriteString("DESCRIPTION:" + icsEscape(e.Description) + "\r\n")
	}
	if e.Location != "" {
		b.WriteString("LOCATION:" + icsEscape(e.Location) + "\r\n")
	}
	if e.Start.DateTime != "" {
		if t, err := time.Parse(time.RFC3339, e.Start.DateTime); err == nil {
			b.WriteString("DTSTART:" + t.UTC().Format("20060102T150405Z") + "\r\n")
		}
	} else if e.Start.Date != "" {
		b.WriteString("DTSTART;VALUE=DATE:" + strings.ReplaceAll(e.Start.Date, "-", "") + "\r\n")
	}
	if e.End.DateTime != "" {
		if t, err := time.Parse(time.RFC3339, e.End.DateTime); err == nil {
			b.WriteString("DTEND:" + t.UTC().Format("20060102T150405Z") + "\r\n")
		}
	} else if e.End.Date != "" {
		b.WriteString("DTEND;VALUE=DATE:" + strings.ReplaceAll(e.End.Date, "-", "") + "\r\n")
	}
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// icsEscape escapes text-value characters per RFC 5545 §3.3.11.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	return s
}

func icsNow() string {
	return time.Now().UTC().Format("20060102T150405Z")
}

// ── PIM import runner ─────────────────────────────────────────────────────────

// pimItem is a fetched provider item (contact or event) ready for bulk posting.
type pimItem struct {
	node *Node  // provider metadata; node.ID is the dedup key
	data string // raw vCard or iCal string
}

// runPIMImport is the runner for kind=contacts|calendar import jobs. It replaces
// importWalk (which writes to Drive). It:
//  1. Lists all items from the provider.
//  2. Skips already-imported items (dedup via files_import_items).
//  3. Fetches each new item's bytes (OpenForImport → vCard/iCal string).
//  4. POSTs in batches to vulos-mail's /api/admin/import/{contacts,events}.
//  5. Records each imported item in files_import_items.
//
// PERSIST: once POSTed to vulos-mail, the item is the user's owned copy. The OS
// never sends a delete to vulos-mail; Vulos-owned copies persist after the
// integration is disconnected or the upstream entry is removed.
//
// ADDITIVE: a mode=sync re-run adds new items (absent from files_import_items)
// and skips existing ones. It never removes vulos-mail copies.
func (s *Service) runPIMImport(ctx context.Context, src ImportSource, job *ImportJob) (importCounts, error) {
	if s.pimMailURL == "" || s.pimMailSecret == "" {
		return importCounts{}, fmt.Errorf("files: PIM import not configured (set VULOS_MAIL_URL and VULOS_MAIL_BROKER_SECRET)")
	}
	mailAccount := ""
	if s.pimAccountResolver != nil {
		mailAccount = s.pimAccountResolver(job.OwnerID)
	}
	if mailAccount == "" {
		return importCounts{}, fmt.Errorf("files: PIM import: no mail account found for owner %s", job.OwnerID)
	}

	listCall, err := s.importCall(ctx, src)
	if err != nil {
		return importCounts{}, fmt.Errorf("files: PIM import: mint token: %w", err)
	}
	nodes, err := src.List(ctx, listCall, "")
	if err != nil {
		return importCounts{}, fmt.Errorf("files: PIM import: list: %w", err)
	}

	var counts importCounts
	var batch []pimItem

	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		payloads := make([]string, len(batch))
		for i, it := range batch {
			payloads[i] = it.data
		}
		var postErr error
		switch job.Kind {
		case ImportKindContacts:
			postErr = s.pimPost(ctx, s.pimMailURL+"/api/admin/import/contacts", map[string]any{
				"account": mailAccount,
				"vcards":  payloads,
			})
		case ImportKindCalendar:
			postErr = s.pimPost(ctx, s.pimMailURL+"/api/admin/import/events", map[string]any{
				"account": mailAccount,
				"events":  payloads,
			})
		}
		if postErr != nil {
			counts.errors += len(batch)
			log.Printf("[files-pim-import] batch POST failed job=%s kind=%s: %v", job.ID, job.Kind, postErr)
		} else {
			for _, it := range batch {
				// node.ID is the external ID (resourceName / eventId); use it as
				// both the external key and the "vulos node" key (PIM has no Drive node).
				if err := s.putImportItem(job.OwnerID, job.Provider, it.node.ID, it.node.ID, false, job.ID); err != nil {
					log.Printf("[files-pim-import] dedup record failed %s: %v", it.node.ID, err)
				}
				counts.imported++
			}
		}
		batch = batch[:0]
	}

	for _, node := range nodes {
		if node == nil || node.ID == "" {
			continue
		}
		// Dedup: skip items already imported in a previous run (additive-only).
		if _, _, ok := s.getImportItem(job.OwnerID, job.Provider, node.ID); ok {
			counts.skipped++
			continue
		}
		// Mint a fresh token per item (same pattern as importFile).
		call, err := s.importCall(ctx, src)
		if err != nil {
			counts.errors++
			log.Printf("[files-pim-import] token mint failed %s: %v", node.ID, err)
			continue
		}
		rc, _, _, _, err := src.OpenForImport(ctx, call, node)
		if err != nil {
			counts.errors++
			log.Printf("[files-pim-import] fetch %s failed: %v", node.ID, err)
			continue
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			counts.errors++
			log.Printf("[files-pim-import] read %s failed: %v", node.ID, err)
			continue
		}
		batch = append(batch, pimItem{node: node, data: string(raw)})
		if len(batch) >= pimBatchSize {
			flushBatch()
		}
	}
	flushBatch() // remaining partial batch
	return counts, nil
}

// pimPost sends a broker-authenticated JSON POST to a vulos-mail admin endpoint.
// A non-2xx response is returned as an error.
func (s *Service) pimPost(ctx context.Context, endpoint string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pim post: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pim post: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vulos-Broker-Auth", s.pimMailSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pim post: %w", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pim post: mail returned %d: %s", resp.StatusCode, trimBody(rb))
	}
	return nil
}

// trimBody returns at most 256 bytes of a body for error messages.
func trimBody(b []byte) string {
	if len(b) > 256 {
		return string(b[:256]) + "..."
	}
	return string(b)
}
