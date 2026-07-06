// Package assistant implements the Vulos sovereign mail assistant — the wedge.
//
// It is a small RAG layer that reads the user's MAIL from the local mail
// service and answers questions about it (summarize, draft, triage, search)
// using a model reached through the existing services/ai seam. The core
// product promise is SOVEREIGNTY: mail content and prompts go ONLY to the
// instance-local model (Ollama-style) or an explicitly user-configured
// endpoint — never silently to a third-party API. See sovereign.go for the
// enforcement point.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is the normalized mail record the assistant reasons over. It is a
// provider-agnostic subset of the fields exposed by the local mail API.
type Message struct {
	UID       string    `json:"uid"`
	From      string    `json:"from"`
	FromName  string    `json:"from_name,omitempty"`
	To        string    `json:"to,omitempty"`
	Subject   string    `json:"subject"`
	Preview   string    `json:"preview,omitempty"`
	Body      string    `json:"body,omitempty"`
	Date      time.Time `json:"date"`
	Unread    bool      `json:"unread"`
	Folder    string    `json:"folder,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	InReplyTo string    `json:"in_reply_to,omitempty"`
	// Invite, when non-nil, is the calendar invitation (iTIP/iMIP) carried by this
	// message — parsed by the mail service (wave-37 email.invite) and surfaced on
	// the /v1 message. It is DATA the assistant reads; it is never trusted as
	// instructions (invite fields flow back through wrapUntrusted like any body).
	Invite *MessageInvite `json:"invite,omitempty"`
}

// MessageInvite is the calendar invitation (iTIP/iMIP) parsed from a message by
// the mail service. It is provider-agnostic: whatever CalDAV-JSON / text/calendar
// shape the mail service emits, the assistant reads this normalized subset. The
// assistant treats it as READ-ONLY content; it can surface an invite and PROPOSE
// an RSVP (ledger-gated), but the parse itself lives on the mail service.
type MessageInvite struct {
	// Method is the iTIP method (REQUEST for a new invite, CANCEL, REPLY, …). Only
	// REQUEST invites can be responded to; others are informational.
	Method string `json:"method,omitempty"`
	// Summary/Start/End/Location describe the proposed event.
	Summary  string `json:"summary,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Location string `json:"location,omitempty"`
	AllDay   bool   `json:"allDay,omitempty"`
	// Organizer is who sent the invite; UID is the event's iCalendar UID (the
	// stable id an RSVP replies against).
	Organizer string `json:"organizer,omitempty"`
	UID       string `json:"uid,omitempty"`
	// RSVP is this attendee's current participation status — the raw iCalendar
	// PARTSTAT the mail service emits as `myPartStat` (e.g. "NEEDS-ACTION",
	// "ACCEPTED", "DECLINED", "TENTATIVE"). AwaitsRSVP normalizes case/spacing;
	// "NEEDS-ACTION" (or empty on a REQUEST) means it awaits your response.
	// NOTE: the JSON tag MUST match lilmail's models.CalendarInvite wire field
	// (`myPartStat`) — a mismatch makes every REQUEST look unanswered.
	RSVP string `json:"myPartStat,omitempty"`
}

// AwaitsRSVP reports whether this invite is a live REQUEST still awaiting the
// user's response (partstat NEEDS-ACTION or unset). CANCEL/REPLY methods and
// already-answered invites (accepted/declined/tentative) are not pending.
func (iv *MessageInvite) AwaitsRSVP() bool {
	if iv == nil {
		return false
	}
	if m := strings.ToUpper(strings.TrimSpace(iv.Method)); m != "" && m != "REQUEST" {
		return false // CANCEL / REPLY / COUNTER etc. are informational, not RSVPable
	}
	switch strings.ToLower(strings.TrimSpace(iv.RSVP)) {
	case "", "needs-action", "needsaction":
		return true
	default:
		return false // accepted / declined / tentative — already answered
	}
}

// Sender returns a human-friendly sender label ("Name <addr>" or the address).
func (m Message) Sender() string {
	if m.FromName != "" && m.FromName != m.From {
		return fmt.Sprintf("%s <%s>", m.FromName, m.From)
	}
	return m.From
}

// Draft is an outgoing message the assistant asks the mail service to persist
// (or, once the user approves, to send).
type Draft struct {
	To        string `json:"to"`
	Cc        string `json:"cc,omitempty"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	InReplyTo string `json:"inReplyTo,omitempty"`
}

// CalendarEvent is a calendar entry the assistant proposes creating against the
// local mail service's /v1 calendar API. Times are passed through as the model /
// user supplied them (ISO-8601/RFC3339 preferred); the mail service does the
// authoritative parsing.
//
// ID/AllDay are populated only on the READ path (ListEvents) and are omitted on
// the create/propose path so the existing POST /v1/calendar/events payload is
// unchanged.
type CalendarEvent struct {
	ID        string `json:"id,omitempty"`
	Title     string `json:"title"`
	Start     string `json:"start"`
	End       string `json:"end,omitempty"`
	Location  string `json:"location,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Attendees string `json:"attendees,omitempty"`
	AllDay    bool   `json:"all_day,omitempty"`
}

// Contact is a contact-book entry. On the WRITE path it is what the assistant
// proposes adding (ledger-gated add_contact). On the READ path (FindContacts) it
// is a normalized address-book entry the assistant looked up — Email/Phone carry
// the PRIMARY (first) of each, and Notes carries the card's free-text note. The
// read path is provenance-untrusted: contact data is other people's text and is
// fed back through wrapUntrusted like any tool result.
type Contact struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone,omitempty"`
	Notes string `json:"notes,omitempty"`
}

// TriageAction is a mailbox state change (archive / snooze / label) the
// assistant proposes for a single message. Only the fields relevant to Action
// are populated.
type TriageAction struct {
	MessageID string `json:"message_id"`
	Action    string `json:"action"` // "archive" | "snooze" | "label"
	Until     string `json:"until,omitempty"`
	Label     string `json:"label,omitempty"`
	Folder    string `json:"folder,omitempty"`
}

// InviteRSVP is a response (accept/decline/tentative) to a calendar invite that
// arrived as a message. The mail service (which parsed the invite in wave-37)
// handles emitting the iTIP REPLY / updating the attendee partstat; the
// assistant only proposes it. Response is one of "accept"|"decline"|"tentative".
type InviteRSVP struct {
	MessageID string `json:"message_id"`
	Folder    string `json:"folder,omitempty"`
	Response  string `json:"response"`
}

// Auth carries the per-request credentials used to reach the local mail API.
// The assistant never stores mail credentials; it forwards whatever the caller
// provides. Cookie is the forwarded session cookie (session mode); Broker is
// the set of X-Vulos-Mail-* headers for the CP-brokered credential mode.
type Auth struct {
	Cookie string
	Broker map[string]string
	// UserID identifies the mailbox owner for this request. It is used ONLY to
	// scope the on-instance vector index per user (isolation) — never sent to
	// the mail service or the model. Empty is treated as a single "default"
	// scope (fixture/offline mode).
	UserID string
}

// MailSource is the read/write seam over the user's mailbox. The default
// implementation talks to the local LilMail /v1 JSON API; a fixture
// implementation backs offline demos and tests.
type MailSource interface {
	// Name identifies the backing source for status/telemetry ("lilmail",
	// "fixture", ...).
	Name() string
	// Recent returns the newest messages in a folder, newest first.
	Recent(ctx context.Context, auth Auth, folder string, limit int) ([]Message, error)
	// Get fetches a single message (with full body) by UID.
	Get(ctx context.Context, auth Auth, uid, folder string) (Message, error)
	// Search returns messages matching a query (server-side substring/FTS).
	Search(ctx context.Context, auth Auth, folder, query string, limit int) ([]Message, error)
	// SaveDraft persists a draft reply to the Drafts folder.
	SaveDraft(ctx context.Context, auth Auth, d Draft) error
	// ListEvents returns calendar events overlapping [fromISO, toISO] (RFC3339),
	// soonest first. It is a READ over the local /v1 calendar surface, used by the
	// Home agenda. Sources that cannot read events may return an empty slice.
	ListEvents(ctx context.Context, auth Auth, fromISO, toISO string) ([]CalendarEvent, error)
	// FindContacts searches the address book for entries matching query (a name /
	// email substring), returning up to limit normalized contacts. It is a READ
	// over the local /v1 contacts surface — no mutation, no model call. An empty
	// query lists recent contacts. Sources without a contacts backend return an
	// empty slice.
	FindContacts(ctx context.Context, auth Auth, query string, limit int) ([]Contact, error)

	// --- Mutating actions (only reached AFTER explicit user confirmation via
	// the assistant's proposal/execute round-trip; the model can PROPOSE these
	// but never triggers them directly). ---

	// SendEmail sends an outgoing message.
	SendEmail(ctx context.Context, auth Auth, d Draft) error
	// CreateEvent creates a calendar event.
	CreateEvent(ctx context.Context, auth Auth, ev CalendarEvent) error
	// AddContact adds a contact-book entry.
	AddContact(ctx context.Context, auth Auth, c Contact) error
	// Triage applies a mailbox state change (archive/snooze/label) to a message.
	Triage(ctx context.Context, auth Auth, action TriageAction) error
	// RSVPInvite responds (accept/decline/tentative) to a calendar invite carried
	// by a message. The mail service emits the iTIP REPLY / updates partstat.
	RSVPInvite(ctx context.Context, auth Auth, rsvp InviteRSVP) error
}

const defaultFolder = "INBOX"

// LilmailSource reads mail from the local LilMail JSON API (/v1/*). LilMail is
// the default Vulos mail service, embedded in the shell as the Mail app; it
// runs on the same instance, so this is a purely on-box read path.
type LilmailSource struct {
	baseURL string
	client  *http.Client
}

// NewLilmailSource builds a source targeting baseURL (e.g. http://localhost:3000).
func NewLilmailSource(baseURL string) *LilmailSource {
	return &LilmailSource{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *LilmailSource) Name() string { return "lilmail" }

// lilEmail mirrors the subset of lilmail models.Email returned by /v1.
type lilEmail struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`
	FromName  string         `json:"fromName"`
	To        string         `json:"to"`
	Subject   string         `json:"subject"`
	Preview   string         `json:"preview"`
	Body      string         `json:"body"`
	Date      time.Time      `json:"date"`
	Flags     []string       `json:"flags"`
	MessageID string         `json:"messageId"`
	InReplyTo string         `json:"inReplyTo"`
	Invite    *MessageInvite `json:"invite,omitempty"` // wave-37 iTIP/iMIP parse, when present
}

func (e lilEmail) toMessage(folder string) Message {
	seen := false
	for _, f := range e.Flags {
		if strings.EqualFold(f, "\\Seen") || strings.EqualFold(f, "Seen") {
			seen = true
		}
	}
	return Message{
		UID:       e.ID,
		From:      e.From,
		FromName:  e.FromName,
		To:        e.To,
		Subject:   e.Subject,
		Preview:   e.Preview,
		Body:      e.Body,
		Date:      e.Date,
		Unread:    !seen,
		Folder:    folder,
		MessageID: e.MessageID,
		InReplyTo: e.InReplyTo,
		Invite:    e.Invite,
	}
}

func (s *LilmailSource) apply(req *http.Request, auth Auth) {
	if auth.Cookie != "" {
		req.Header.Set("Cookie", auth.Cookie)
	}
	for k, v := range auth.Broker {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Accept", "application/json")
}

func (s *LilmailSource) Recent(ctx context.Context, auth Auth, folder string, limit int) ([]Message, error) {
	if folder == "" {
		folder = defaultFolder
	}
	q := url.Values{"folder": {folder}, "limit": {fmt.Sprint(limit)}}
	var out struct {
		Messages []lilEmail `json:"messages"`
	}
	if err := s.getJSON(ctx, auth, "/v1/messages?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return convert(out.Messages, folder), nil
}

func (s *LilmailSource) Get(ctx context.Context, auth Auth, uid, folder string) (Message, error) {
	if folder == "" {
		folder = defaultFolder
	}
	q := url.Values{"folder": {folder}}
	var e lilEmail
	if err := s.getJSON(ctx, auth, "/v1/messages/"+url.PathEscape(uid)+"?"+q.Encode(), &e); err != nil {
		return Message{}, err
	}
	return e.toMessage(folder), nil
}

func (s *LilmailSource) Search(ctx context.Context, auth Auth, folder, query string, limit int) ([]Message, error) {
	if folder == "" {
		folder = defaultFolder
	}
	q := url.Values{"folder": {folder}, "q": {query}, "limit": {fmt.Sprint(limit)}}
	var out struct {
		Messages []lilEmail `json:"messages"`
	}
	if err := s.getJSON(ctx, auth, "/v1/search?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return convert(out.Messages, folder), nil
}

func (s *LilmailSource) SaveDraft(ctx context.Context, auth Auth, d Draft) error {
	body, _ := json.Marshal(d)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v1/drafts", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.apply(req, auth)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("lilmail draft failed: %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// SendEmail sends an outgoing message via POST /v1/messages. The mail service
// runs on this instance, so this is an on-box write; it is only ever called
// after the user approves the assistant's proposal.
func (s *LilmailSource) SendEmail(ctx context.Context, auth Auth, d Draft) error {
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/messages", d, "send")
}

// CreateEvent creates a calendar event via POST /v1/calendar/events. lilmail's
// create payload uses {summary,start,end,location,description,allDay} — map the
// assistant's CalendarEvent (title/notes/all_day) onto that so the created event
// isn't left with an empty summary.
func (s *LilmailSource) CreateEvent(ctx context.Context, auth Auth, ev CalendarEvent) error {
	payload := map[string]any{
		"summary":  ev.Title,
		"start":    ev.Start,
		"end":      ev.End,
		"location": ev.Location,
		"allDay":   ev.AllDay,
	}
	if strings.TrimSpace(ev.Notes) != "" {
		payload["description"] = ev.Notes
	}
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/calendar/events", payload, "calendar create")
}

// lilEvent mirrors a lilmail /v1 calendar event, tolerating the common field
// spellings (title/summary, start/dtStart, ...) so the Home agenda reads cleanly
// regardless of the exact CalDAV-JSON shape the mail service emits.
type lilEvent struct {
	ID       string `json:"id"`
	UID      string `json:"uid"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Start    string `json:"start"`
	DTStart  string `json:"dtStart"`
	End      string `json:"end"`
	DTEnd    string `json:"dtEnd"`
	Location string `json:"location"`
	AllDay   bool   `json:"allDay"`
}

func (e lilEvent) toEvent() CalendarEvent {
	return CalendarEvent{
		ID:       strEmpty(e.ID, e.UID),
		Title:    strEmpty(e.Title, e.Summary),
		Start:    strEmpty(e.Start, e.DTStart),
		End:      strEmpty(e.End, e.DTEnd),
		Location: e.Location,
		AllDay:   e.AllDay,
	}
}

// ListEvents reads calendar events overlapping [fromISO, toISO] from
// GET /v1/calendar/events. The response is decoded tolerantly: either a
// {"events":[...]} envelope or a bare array. Best-effort — an unreadable
// calendar surfaces as an empty agenda, never a crash.
func (s *LilmailSource) ListEvents(ctx context.Context, auth Auth, fromISO, toISO string) ([]CalendarEvent, error) {
	q := url.Values{}
	if fromISO != "" {
		q.Set("from", fromISO)
		q.Set("start", fromISO)
	}
	if toISO != "" {
		q.Set("to", toISO)
		q.Set("end", toISO)
	}
	path := "/v1/calendar/events"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	raw, err := s.getRaw(ctx, auth, path)
	if err != nil {
		return nil, err
	}
	var env struct {
		Events []lilEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Events != nil {
		return convertEvents(env.Events), nil
	}
	var arr []lilEvent
	if err := json.Unmarshal(raw, &arr); err == nil {
		return convertEvents(arr), nil
	}
	return nil, fmt.Errorf("calendar: unrecognized events response")
}

func convertEvents(in []lilEvent) []CalendarEvent {
	out := make([]CalendarEvent, 0, len(in))
	for _, e := range in {
		ev := e.toEvent()
		if strings.TrimSpace(ev.Title) == "" && strings.TrimSpace(ev.Start) == "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// AddContact adds a contact via POST /v1/contacts.
func (s *LilmailSource) AddContact(ctx context.Context, auth Auth, c Contact) error {
	// lilmail's POST /v1/contacts expects {name, emails[], phones[], note} — map
	// the assistant's singular email/phone/notes onto the array/field shape so the
	// contact isn't saved with a name but no address.
	payload := map[string]any{"name": c.Name}
	if strings.TrimSpace(c.Email) != "" {
		payload["emails"] = []string{c.Email}
	}
	if strings.TrimSpace(c.Phone) != "" {
		payload["phones"] = []string{c.Phone}
	}
	if strings.TrimSpace(c.Notes) != "" {
		payload["note"] = c.Notes
	}
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/contacts", payload, "add contact")
}

// lilContact mirrors the subset of lilmail models.Contact returned by
// GET /v1/contacts/cards. The JSON tags MUST match lilmail's wire shape
// (emails[]/phones[]/note) — a drift here silently blanks the field the
// assistant reads. Guarded by TestContactCardWireSeam.
type lilContact struct {
	UID    string   `json:"uid"`
	Name   string   `json:"name"`
	Org    string   `json:"org,omitempty"`
	Title  string   `json:"title,omitempty"`
	Note   string   `json:"note,omitempty"`
	Emails []string `json:"emails"`
	Phones []string `json:"phones,omitempty"`
}

// toContact normalizes a lilmail card onto the assistant's Contact, taking the
// PRIMARY (first) email/phone (lilmail treats index 0 as primary) and the note.
func (c lilContact) toContact() Contact {
	out := Contact{Name: c.Name, Notes: c.Note}
	if len(c.Emails) > 0 {
		out.Email = c.Emails[0]
	}
	if len(c.Phones) > 0 {
		out.Phone = c.Phones[0]
	}
	return out
}

// FindContacts reads matching address-book cards from GET /v1/contacts/cards
// (the FULL-card surface: {contacts:[{name,emails[],phones[],note,...}]}) — NOT
// the lean /v1/contacts autocomplete form, which omits phone/note. It is a pure
// on-box READ: no model call, no mutation. An account without a CardDAV backend
// returns an empty list (lilmail degrades gracefully), which surfaces as "no
// matching contacts" rather than an error.
func (s *LilmailSource) FindContacts(ctx context.Context, auth Auth, query string, limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 20
	}
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if strings.TrimSpace(query) != "" {
		q.Set("q", query)
	}
	var out struct {
		Contacts []lilContact `json:"contacts"`
	}
	if err := s.getJSON(ctx, auth, "/v1/contacts/cards?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	res := make([]Contact, 0, len(out.Contacts))
	for _, c := range out.Contacts {
		res = append(res, c.toContact())
	}
	return res, nil
}

// Triage maps archive/snooze/label onto lilmail's REAL /v1 surface:
//   - archive → POST  /v1/messages/{id}/move  {"toFolder":"Archive"}
//   - snooze  → POST  /v1/messages/{id}/move  {"toFolder":"Snoozed"}
//     (lilmail has no timed-snooze primitive, so "snooze" = park it in a Snoozed
//     folder; a timed un-snooze would be a control-plane scheduler job later.)
//   - label   → PATCH /v1/messages/{id}/flags {"flags":["<label>"],"add":true}
//     (lilmail's flags endpoint: `add` is a boolean, `flags` is the keyword list.)
//
// Triage runs only after the user approves the proposal.
func (s *LilmailSource) Triage(ctx context.Context, auth Auth, a TriageAction) error {
	id := url.PathEscape(a.MessageID)
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "snooze":
		return s.writeJSON(ctx, auth, http.MethodPost, "/v1/messages/"+id+"/move",
			map[string]string{"toFolder": "Snoozed"}, "snooze")
	case "label":
		return s.writeJSON(ctx, auth, http.MethodPatch, "/v1/messages/"+id+"/flags",
			map[string]any{"flags": []string{a.Label}, "add": true}, "label")
	case "archive":
		return s.writeJSON(ctx, auth, http.MethodPost, "/v1/messages/"+id+"/move",
			map[string]string{"toFolder": "Archive"}, "archive")
	default:
		return fmt.Errorf("unknown triage action %q", a.Action)
	}
}

// RSVPInvite responds to a calendar invite via
// POST /v1/messages/{id}/invite/rsvp {"response":"accept|decline|tentative"}.
// The mail service owns the iTIP REPLY / partstat update (it parsed the invite);
// this only forwards the user's approved response. Runs only after approval.
func (s *LilmailSource) RSVPInvite(ctx context.Context, auth Auth, rsvp InviteRSVP) error {
	id := url.PathEscape(rsvp.MessageID)
	payload := map[string]string{"response": rsvp.Response}
	if strings.TrimSpace(rsvp.Folder) != "" {
		payload["folder"] = rsvp.Folder
	}
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/messages/"+id+"/invite/rsvp", payload, "invite rsvp")
}

// writeJSON is the shared mutating-request helper (send/calendar/contacts/triage).
func (s *LilmailSource) writeJSON(ctx context.Context, auth Auth, method, path string, payload any, what string) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.apply(req, auth)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail service unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("mail not authenticated")
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("lilmail %s failed: %d: %s", what, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *LilmailSource) getJSON(ctx context.Context, auth Auth, path string, v any) error {
	raw, err := s.getRaw(ctx, auth, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// getRaw performs an authed GET and returns the (size-capped) response body.
func (s *LilmailSource) getRaw(ctx context.Context, auth Auth, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	s.apply(req, auth)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mail service unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("mail not authenticated")
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mail service error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func convert(in []lilEmail, folder string) []Message {
	out := make([]Message, 0, len(in))
	for _, e := range in {
		out = append(out, e.toMessage(folder))
	}
	return out
}
