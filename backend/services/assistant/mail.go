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

// Contact is a contact-book entry the assistant proposes adding.
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
	ID        string    `json:"id"`
	From      string    `json:"from"`
	FromName  string    `json:"fromName"`
	To        string    `json:"to"`
	Subject   string    `json:"subject"`
	Preview   string    `json:"preview"`
	Body      string    `json:"body"`
	Date      time.Time `json:"date"`
	Flags     []string  `json:"flags"`
	MessageID string    `json:"messageId"`
	InReplyTo string    `json:"inReplyTo"`
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

// CreateEvent creates a calendar event via POST /v1/calendar/events.
func (s *LilmailSource) CreateEvent(ctx context.Context, auth Auth, ev CalendarEvent) error {
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/calendar/events", ev, "calendar create")
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
	return s.writeJSON(ctx, auth, http.MethodPost, "/v1/contacts", c, "add contact")
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
