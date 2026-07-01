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

// Draft is an outgoing message the assistant asks the mail service to persist.
type Draft struct {
	To        string `json:"to"`
	Cc        string `json:"cc,omitempty"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	InReplyTo string `json:"inReplyTo,omitempty"`
}

// Auth carries the per-request credentials used to reach the local mail API.
// The assistant never stores mail credentials; it forwards whatever the caller
// provides. Cookie is the forwarded session cookie (session mode); Broker is
// the set of X-Vulos-Mail-* headers for the CP-brokered credential mode.
type Auth struct {
	Cookie string
	Broker map[string]string
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

func (s *LilmailSource) getJSON(ctx context.Context, auth Auth, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return err
	}
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
		return fmt.Errorf("mail service error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func convert(in []lilEmail, folder string) []Message {
	out := make([]Message, 0, len(in))
	for _, e := range in {
		out = append(out, e.toMessage(folder))
	}
	return out
}
