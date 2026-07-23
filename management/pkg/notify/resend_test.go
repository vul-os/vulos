package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── Header-injection regression (the wave-52 class) ────────────────────────────
//
// These assert the fail-closed guard: an attacker-controlled recipient handle or
// subject carrying CR / LF / NUL must be REJECTED before any network call. Revert
// the validateHeaderValue calls in resend.go and every case below flips to a nil
// error (the poisoned value would sail into the Resend payload), so this test is
// the regression anchor for the CP's slice of the header-injection class.
func TestDeliverSystemMessage_RejectsHeaderInjection(t *testing.T) {
	s := &ResendSender{apiKey: "re_test", fromAddr: "no-reply@vulos.org", httpClient: http.DefaultClient}

	cases := []struct {
		name    string
		handle  string
		subject string
	}{
		{"CRLF in handle (silent Bcc / second RCPT)", "victim\r\nBcc: evil@attacker.test", "Reset your Vulos password"},
		{"bare LF in handle", "victim\nBcc: evil@attacker.test", "hi"},
		{"bare CR in handle", "victim\rX-Injected: 1", "hi"},
		{"NUL in handle", "victim\x00", "hi"},
		{"CRLF in subject (subject splitting)", "alice", "Welcome\r\nBcc: evil@attacker.test"},
		{"LF in subject", "alice", "Welcome\nX-Injected: 1"},
		{"NUL in subject", "alice", "Welcome\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.DeliverSystemMessage(context.Background(), tc.handle, tc.subject, "body text")
			if err == nil {
				t.Fatalf("expected ErrHeaderInjection for %q/%q, got nil (poisoned value would reach Resend)", tc.handle, tc.subject)
			}
			if !errors.Is(err, ErrHeaderInjection) {
				t.Fatalf("expected ErrHeaderInjection, got %v", err)
			}
		})
	}
}

// A poisoned From address (should never happen from env, but defence-in-depth)
// must also fail closed.
func TestDeliverSystemMessage_RejectsPoisonedFrom(t *testing.T) {
	s := &ResendSender{apiKey: "re_test", fromAddr: "no-reply@vulos.org\r\nBcc: evil@attacker.test", httpClient: http.DefaultClient}
	err := s.DeliverSystemMessage(context.Background(), "alice", "hi", "body")
	if !errors.Is(err, ErrHeaderInjection) {
		t.Fatalf("expected ErrHeaderInjection for poisoned From, got %v", err)
	}
}

// Clean input must still be delivered (guard must not be over-eager). Drives the
// full JSON assembly against a local server and asserts the payload shape.
func TestDeliverSystemMessage_HappyPath(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("Authorization"); got != "Bearer re_test" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Reconstruct the send against the test server URL by reusing the exact
	// assembly logic through an inline client. Since resendAPIURL is a const we
	// exercise the assembly + guard via a thin re-implementation mirror is NOT
	// used; instead confirm the guard passes and delegate is invoked by pointing
	// a sender whose client rewrites the host.
	s := &ResendSender{
		apiKey:     "re_test",
		fromAddr:   "no-reply@vulos.org",
		httpClient: &http.Client{Timeout: 5 * time.Second, Transport: rewriteTransport{target: srv.URL}},
	}
	if err := s.DeliverSystemMessage(context.Background(), "alice", "Reset your Vulos password", "line1\nline2"); err != nil {
		t.Fatalf("clean input rejected: %v", err)
	}
	var payload struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
		Text    string   `json:"text"`
	}
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if len(payload.To) != 1 || !strings.HasSuffix(payload.To[0], "@"+strings.SplitN(payload.To[0], "@", 2)[1]) {
		t.Fatalf("unexpected To: %v", payload.To)
	}
	if payload.Subject != "Reset your Vulos password" {
		t.Fatalf("unexpected Subject: %q", payload.Subject)
	}
}

// ─── HTML body escaping (item 3: stored-XSS-in-email / content spoofing) ─────────
//
// The body is interpolated into an HTML <pre> block. Assert user-supplied markup
// is escaped, not interpolated raw, so a display name / message in the body can't
// inject <script> or forge trusted-looking content.
func TestHTMLBodyIsEscaped(t *testing.T) {
	raw := `<script>alert(1)</script> & "quote" 'apos' <b>bold</b>`
	got := htmlEscape(raw)
	for _, bad := range []string{"<script>", "</script>", "<b>", `"quote"`} {
		if strings.Contains(got, bad) {
			t.Fatalf("htmlEscape left raw markup %q in output: %q", bad, got)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "&#34;quote&#34;", "&#39;apos&#39;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("htmlEscape missing expected entity %q in output: %q", want, got)
		}
	}
}

// rewriteTransport redirects every request to target while preserving method,
// headers, and body — lets the happy-path test hit a local server despite the
// const api.resend.com URL.
type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := req.URL.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}
