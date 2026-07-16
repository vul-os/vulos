// Package notify provides a Resend-backed transactional email sender.
//
// The ResendSender implements auth.InboxSender by posting to the Resend
// REST API (https://api.resend.com/emails). It uses plain net/http with no
// third-party SDK so the dependency footprint stays minimal and CGO_ENABLED=0.
//
// Configuration:
//
//	RESEND_API_KEY  — Resend API key (required in production; prefix "re_")
//	RESEND_FROM     — sender address (e.g. "no-reply@vulos.org")
//
// The sender satisfies auth.InboxSender (DeliverSystemMessage) by treating
// recipientHandle as the bare handle and constructing the destination address
// as "<handle>@vulos.org". HTML is a simple <pre>-wrapped body for now;
// the text field carries the same content.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

const (
	resendAPIURL      = "https://api.resend.com/emails"
	resendHTTPTimeout = 10 * time.Second
)

// ErrHeaderInjection is returned when a recipient address, sender address, or
// subject destined for an outbound email contains a CR, LF, or NUL byte. Such
// bytes are the email header-injection class: a single CRLF in an untrusted
// To/From/Subject can forge additional headers (a silent Bcc), split the
// message, or inject a second recipient once the value reaches an RFC822 sink.
//
// The CP delegates the actual RFC822 assembly to the Resend HTTP API (values
// travel as JSON, not as hand-built headers), so Resend is the primary guard.
// This guard is defence-in-depth: it fails CLOSED at the CP boundary so a
// poisoned value (e.g. an attacker-controlled login "email" that reaches the
// step-up sender without canonicalisation) never leaves the process, mirroring
// the validateHeaderValue pattern used by lilmail/vulos-mail/vulos-deliver.
var ErrHeaderInjection = fmt.Errorf("notify: address/subject contains a forbidden control byte (CR, LF, or NUL)")

// validateHeaderValue returns ErrHeaderInjection if v contains a CR, LF, or NUL
// byte. Shared fail-closed guard applied to every caller-supplied value that
// reaches an email address field or the Subject before the send.
func validateHeaderValue(field, v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("%w: %s=%q", ErrHeaderInjection, field, v)
	}
	return nil
}

// ResendSender delivers transactional email via the Resend HTTP API.
// It satisfies auth.InboxSender.
type ResendSender struct {
	apiKey     string
	fromAddr   string
	httpClient *http.Client
}

// NewResendSenderFromEnv constructs a ResendSender from RESEND_API_KEY and
// RESEND_FROM environment variables. Returns nil if RESEND_API_KEY is unset
// (caller should fall back to NopInboxSender and log a warning).
func NewResendSenderFromEnv() *ResendSender {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		return nil
	}
	from := os.Getenv("RESEND_FROM")
	if from == "" {
		from = "no-reply@" + env.Domain()
	}
	return &ResendSender{
		apiKey:   apiKey,
		fromAddr: from,
		httpClient: &http.Client{
			Timeout: resendHTTPTimeout,
		},
	}
}

// FromAddr returns the configured sender address (for logging).
func (s *ResendSender) FromAddr() string { return s.fromAddr }

// DeliverSystemMessage sends a plain-text message from the configured FROM
// address to "<recipientHandle>@vulos.org". Implements auth.InboxSender.
func (s *ResendSender) DeliverSystemMessage(ctx context.Context, recipientHandle, subject, body string) error {
	return s.send(ctx, recipientHandle+"@"+env.Domain(), subject, body)
}

// DeliverExternalMessage sends a plain-text message to an address OUTSIDE the
// Vulos network. Implements auth.ExternalSender — the ONLY off-network mail the
// CP sends on an account's behalf is the copy of a recovery message to the
// out-of-network recovery address the account holder opted in to.
func (s *ResendSender) DeliverExternalMessage(ctx context.Context, toAddr, subject, body string) error {
	return s.send(ctx, toAddr, subject, body)
}

func (s *ResendSender) send(ctx context.Context, toAddr, subject, body string) error {
	// Fail closed on the email header-injection class BEFORE the value reaches
	// Resend. Validate every field that lands in an address slot or the Subject:
	// a CR/LF/NUL in any of them is a header-injection attempt. The body is NOT
	// a header (it becomes the message payload, HTML-escaped below), so control
	// bytes there are harmless and are left intact.
	if err := validateHeaderValue("to", toAddr); err != nil {
		return err
	}
	if err := validateHeaderValue("from", s.fromAddr); err != nil {
		return err
	}
	if err := validateHeaderValue("subject", subject); err != nil {
		return err
	}

	// Build a minimal HTML body: wrap the plain-text body in <pre> for
	// readability in email clients. In a future iteration this can be a
	// proper template.
	htmlBody := "<html><body><pre style=\"font-family:monospace;white-space:pre-wrap;\">" +
		htmlEscape(body) +
		"</pre></body></html>"

	payload := struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
		Text    string   `json:"text"`
	}{
		From:    s.fromAddr,
		To:      []string{toAddr},
		Subject: subject,
		HTML:    htmlBody,
		Text:    body,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("resend: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendAPIURL, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("resend: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend: POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend: unexpected status %d: %s", resp.StatusCode, string(raw))
	}

	log.Printf("[notify/resend] delivered to=%s subj=%q", toAddr, subject)
	return nil
}

// htmlEscape escapes the five XML/HTML special characters so the message body
// is safe to embed inside <pre>...</pre> without an html/template import.
func htmlEscape(s string) string {
	var buf bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&#34;")
		case '\'':
			buf.WriteString("&#39;")
		default:
			buf.WriteRune(r)
		}
	}
	return buf.String()
}
