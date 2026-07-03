package assistant

// mail_http_test.go — transport-level tests for LilmailSource (mail.go), the
// REAL /v1 HTTP source the assistant uses against the local LilMail service.
//
// Until now only the in-memory FixtureSource was exercised, so the actual wire
// mapping (lilEmail→Message, the mutating payload shapes for send / calendar /
// contacts / triage, tolerant event decoding, and non-200 error handling) was
// completely unguarded. These tests stand up a small httptest.Server that speaks
// the lilmail /v1 contract, point a LilmailSource at it, and assert the bytes
// that actually go over the wire — this is the assistant↔lilmail payload seam.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// recordedReq captures what the fake server received, so a test can assert the
// exact method, path, query and decoded JSON body the source emitted.
type recordedReq struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Header http.Header
}

// fakeLilmail is a scriptable httptest server speaking the /v1 contract. Each
// handler records the request and returns whatever the test configured.
type fakeLilmail struct {
	t      *testing.T
	srv    *httptest.Server
	mu     chan struct{} // 1-slot lock without importing sync
	got    []recordedReq
	// responder is keyed by "METHOD /path" (path without query); it returns the
	// status and body to write. A nil responder for a route yields 200 "{}".
	responder map[string]func(r recordedReq) (int, string)
}

func newFakeLilmail(t *testing.T) *fakeLilmail {
	t.Helper()
	f := &fakeLilmail{
		t:         t,
		mu:        make(chan struct{}, 1),
		responder: map[string]func(recordedReq) (int, string){},
	}
	f.mu <- struct{}{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLilmail) on(methodPath string, fn func(recordedReq) (int, string)) {
	f.responder[methodPath] = fn
}

func (f *fakeLilmail) handle(w http.ResponseWriter, r *http.Request) {
	// Use EscapedPath so path-escaping (e.g. "a/b" → "a%2Fb") is observable at the
	// transport level — decoding it would hide exactly the escaping we assert on.
	rec := recordedReq{
		Method: r.Method,
		Path:   r.URL.EscapedPath(),
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
	}
	if b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)); len(b) > 0 {
		rec.Body = map[string]any{}
		_ = json.Unmarshal(b, &rec.Body)
	}
	<-f.mu
	f.got = append(f.got, rec)
	f.mu <- struct{}{}

	key := r.Method + " " + r.URL.EscapedPath()
	if fn := f.responder[key]; fn != nil {
		status, body := fn(rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, "{}")
}

// last returns the most recently recorded request (fatal if none).
func (f *fakeLilmail) last() recordedReq {
	f.t.Helper()
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	if len(f.got) == 0 {
		f.t.Fatal("no request was recorded")
	}
	return f.got[len(f.got)-1]
}

func (f *fakeLilmail) source() *LilmailSource { return NewLilmailSource(f.srv.URL) }

// bg is a short-lived context for the request-level tests.
func bg() context.Context { return context.Background() }

// --- Recent / Search: wire → Message mapping --------------------------------

func TestLilmailRecentMapsWireToMessage(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/messages", func(r recordedReq) (int, string) {
		// Assert the source built the right query.
		if got := r.Query.Get("folder"); got != "INBOX" {
			t.Errorf("folder query = %q, want INBOX", got)
		}
		if got := r.Query.Get("limit"); got != "5" {
			t.Errorf("limit query = %q, want 5", got)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept header not set")
		}
		return 200, `{"messages":[
			{"id":"101","from":"a@ex.com","fromName":"Alice","to":"me@ex.com",
			 "subject":"Hi","preview":"prev","body":"full body",
			 "date":"2026-01-02T03:04:05Z","flags":["\\Seen"],
			 "messageId":"<101@ex.com>","inReplyTo":"<prev@ex.com>"},
			{"id":"102","from":"b@ex.com","subject":"Unseen one","flags":[]}
		]}`
	})
	msgs, err := f.source().Recent(bg(), Auth{}, "INBOX", 5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	m := msgs[0]
	if m.UID != "101" || m.From != "a@ex.com" || m.FromName != "Alice" ||
		m.To != "me@ex.com" || m.Subject != "Hi" || m.Preview != "prev" ||
		m.Body != "full body" || m.MessageID != "<101@ex.com>" || m.InReplyTo != "<prev@ex.com>" {
		t.Errorf("field mapping wrong: %+v", m)
	}
	if m.Folder != "INBOX" {
		t.Errorf("folder not stamped: %q", m.Folder)
	}
	if !m.Date.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("date parse wrong: %v", m.Date)
	}
	// \Seen flag ⇒ read; empty flags ⇒ unread.
	if m.Unread {
		t.Errorf("msg with \\Seen should be read")
	}
	if !msgs[1].Unread {
		t.Errorf("msg with no flags should be unread")
	}
}

func TestLilmailRecentDefaultsFolder(t *testing.T) {
	f := newFakeLilmail(t)
	var seen string
	f.on("GET /v1/messages", func(r recordedReq) (int, string) {
		seen = r.Query.Get("folder")
		return 200, `{"messages":[]}`
	})
	if _, err := f.source().Recent(bg(), Auth{}, "", 10); err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if seen != "INBOX" {
		t.Errorf("empty folder should default to INBOX, got %q", seen)
	}
}

func TestLilmailSearchMapsAndQueries(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/search", func(r recordedReq) (int, string) {
		if r.Query.Get("q") != "invoice" {
			t.Errorf("q = %q, want invoice", r.Query.Get("q"))
		}
		if r.Query.Get("folder") != "INBOX" {
			t.Errorf("folder = %q, want INBOX", r.Query.Get("folder"))
		}
		if r.Query.Get("limit") != "3" {
			t.Errorf("limit = %q, want 3", r.Query.Get("limit"))
		}
		return 200, `{"messages":[{"id":"9","from":"billing@x.com","subject":"Invoice"}]}`
	})
	msgs, err := f.source().Search(bg(), Auth{}, "", "invoice", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(msgs) != 1 || msgs[0].UID != "9" || msgs[0].Subject != "Invoice" {
		t.Fatalf("search mapping wrong: %+v", msgs)
	}
}

func TestLilmailGetMapsSingleMessage(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/messages/abc%20123", func(r recordedReq) (int, string) {
		if r.Query.Get("folder") != "Archive" {
			t.Errorf("folder = %q", r.Query.Get("folder"))
		}
		return 200, `{"id":"abc 123","from":"x@y.com","subject":"S","body":"B","flags":["Seen"]}`
	})
	m, err := f.source().Get(bg(), Auth{}, "abc 123", "Archive")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.UID != "abc 123" || m.Body != "B" || m.Unread {
		t.Errorf("get mapping wrong: %+v", m)
	}
}

// --- Auth header forwarding --------------------------------------------------

func TestLilmailForwardsAuth(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/messages", func(r recordedReq) (int, string) {
		if r.Header.Get("Cookie") != "vulos_session=tok" {
			t.Errorf("cookie not forwarded: %q", r.Header.Get("Cookie"))
		}
		if r.Header.Get("X-Vulos-Mail-Secret") != "s3cr3t" {
			t.Errorf("broker header not forwarded: %q", r.Header.Get("X-Vulos-Mail-Secret"))
		}
		return 200, `{"messages":[]}`
	})
	auth := Auth{Cookie: "vulos_session=tok", Broker: map[string]string{"X-Vulos-Mail-Secret": "s3cr3t", "X-Empty": ""}}
	if _, err := f.source().Recent(bg(), auth, "INBOX", 5); err != nil {
		t.Fatalf("Recent: %v", err)
	}
}

// --- SendEmail: {to,cc,subject,text,inReplyTo} to POST /v1/messages ----------

func TestLilmailSendEmailPayload(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/messages", func(r recordedReq) (int, string) { return 200, `{}` })
	d := Draft{To: "dana@acme.io", Cc: "cc@x.com", Subject: "Signed", Text: "Done.", InReplyTo: "<r@acme.io>"}
	if err := f.source().SendEmail(bg(), Auth{}, d); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	req := f.last()
	if req.Method != "POST" || req.Path != "/v1/messages" {
		t.Fatalf("wrong route: %s %s", req.Method, req.Path)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("missing json content-type")
	}
	if req.Body["to"] != "dana@acme.io" || req.Body["cc"] != "cc@x.com" ||
		req.Body["subject"] != "Signed" || req.Body["text"] != "Done." ||
		req.Body["inReplyTo"] != "<r@acme.io>" {
		t.Errorf("send payload wrong: %v", req.Body)
	}
}

func TestLilmailSaveDraftPayload(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/drafts", func(r recordedReq) (int, string) { return 201, `{}` })
	if err := f.source().SaveDraft(bg(), Auth{}, Draft{To: "x@y.com", Subject: "S", Text: "hi"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	req := f.last()
	if req.Path != "/v1/drafts" || req.Body["to"] != "x@y.com" || req.Body["subject"] != "S" {
		t.Errorf("draft payload wrong: %s %v", req.Path, req.Body)
	}
}

// --- Triage: archive/snooze → move; label → PATCH flags {add:true} ----------

func TestLilmailTriageArchive(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/messages/m1/move", func(r recordedReq) (int, string) { return 200, `{}` })
	if err := f.source().Triage(bg(), Auth{}, TriageAction{MessageID: "m1", Action: "archive"}); err != nil {
		t.Fatalf("Triage archive: %v", err)
	}
	req := f.last()
	if req.Path != "/v1/messages/m1/move" || req.Body["toFolder"] != "Archive" {
		t.Errorf("archive → move Archive expected, got %s %v", req.Path, req.Body)
	}
}

func TestLilmailTriageSnooze(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/messages/m2/move", func(r recordedReq) (int, string) { return 200, `{}` })
	if err := f.source().Triage(bg(), Auth{}, TriageAction{MessageID: "m2", Action: "SNOOZE"}); err != nil {
		t.Fatalf("Triage snooze: %v", err)
	}
	req := f.last()
	if req.Path != "/v1/messages/m2/move" || req.Body["toFolder"] != "Snoozed" {
		t.Errorf("snooze → move Snoozed expected, got %s %v", req.Path, req.Body)
	}
}

func TestLilmailTriageLabelFlagsPatch(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("PATCH /v1/messages/m3/flags", func(r recordedReq) (int, string) { return 200, `{}` })
	if err := f.source().Triage(bg(), Auth{}, TriageAction{MessageID: "m3", Action: "label", Label: "Important"}); err != nil {
		t.Fatalf("Triage label: %v", err)
	}
	req := f.last()
	if req.Method != "PATCH" || req.Path != "/v1/messages/m3/flags" {
		t.Fatalf("label should PATCH flags, got %s %s", req.Method, req.Path)
	}
	if req.Body["add"] != true {
		t.Errorf("label flags payload must carry add:true, got %v", req.Body)
	}
	flags, _ := req.Body["flags"].([]any)
	if len(flags) != 1 || flags[0] != "Important" {
		t.Errorf("label flags list wrong: %v", req.Body["flags"])
	}
}

func TestLilmailTriageMessageIDEscaped(t *testing.T) {
	f := newFakeLilmail(t)
	hit := false
	f.on("POST /v1/messages/a%2Fb/move", func(r recordedReq) (int, string) { hit = true; return 200, `{}` })
	// A slash-bearing message id must be path-escaped, not treated as a subpath.
	if err := f.source().Triage(bg(), Auth{}, TriageAction{MessageID: "a/b", Action: "archive"}); err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if !hit {
		t.Errorf("message id with '/' was not path-escaped; got path %s", f.last().Path)
	}
}

func TestLilmailTriageUnknownAction(t *testing.T) {
	f := newFakeLilmail(t)
	err := f.source().Triage(bg(), Auth{}, TriageAction{MessageID: "m", Action: "delete"})
	if err == nil || !strings.Contains(err.Error(), "unknown triage action") {
		t.Fatalf("expected unknown-action error, got %v", err)
	}
}

// --- CreateEvent: title→summary, notes→description, allDay -------------------

func TestLilmailCreateEventPayload(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/calendar/events", func(r recordedReq) (int, string) { return 201, `{}` })
	ev := CalendarEvent{Title: "Sync", Start: "2026-07-01T09:00:00Z", End: "2026-07-01T09:30:00Z",
		Location: "Meet", Notes: "agenda", AllDay: false}
	if err := f.source().CreateEvent(bg(), Auth{}, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	req := f.last()
	if req.Path != "/v1/calendar/events" {
		t.Fatalf("wrong route %s", req.Path)
	}
	if req.Body["summary"] != "Sync" || req.Body["start"] != "2026-07-01T09:00:00Z" ||
		req.Body["end"] != "2026-07-01T09:30:00Z" || req.Body["location"] != "Meet" ||
		req.Body["description"] != "agenda" || req.Body["allDay"] != false {
		t.Errorf("event payload wrong: %v", req.Body)
	}
}

func TestLilmailCreateEventOmitsEmptyNotes(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/calendar/events", func(r recordedReq) (int, string) { return 200, `{}` })
	if err := f.source().CreateEvent(bg(), Auth{}, CalendarEvent{Title: "T", Start: "s"}); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if _, ok := f.last().Body["description"]; ok {
		t.Errorf("empty notes should omit description, got %v", f.last().Body)
	}
}

// --- AddContact: emails[]/phones[]/note -------------------------------------

func TestLilmailAddContactPayload(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/contacts", func(r recordedReq) (int, string) { return 201, `{}` })
	c := Contact{Name: "Dana", Email: "dana@acme.io", Phone: "+15551234", Notes: "client"}
	if err := f.source().AddContact(bg(), Auth{}, c); err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	req := f.last()
	if req.Path != "/v1/contacts" || req.Body["name"] != "Dana" || req.Body["note"] != "client" {
		t.Errorf("contact payload wrong: %v", req.Body)
	}
	emails, _ := req.Body["emails"].([]any)
	phones, _ := req.Body["phones"].([]any)
	if len(emails) != 1 || emails[0] != "dana@acme.io" {
		t.Errorf("emails[] wrong: %v", req.Body["emails"])
	}
	if len(phones) != 1 || phones[0] != "+15551234" {
		t.Errorf("phones[] wrong: %v", req.Body["phones"])
	}
}

func TestLilmailAddContactOmitsEmptyArrays(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/contacts", func(r recordedReq) (int, string) { return 200, `{}` })
	if err := f.source().AddContact(bg(), Auth{}, Contact{Name: "NoAddr"}); err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	b := f.last().Body
	if _, ok := b["emails"]; ok {
		t.Errorf("empty email should omit emails[], got %v", b)
	}
	if _, ok := b["phones"]; ok {
		t.Errorf("empty phone should omit phones[], got %v", b)
	}
	if _, ok := b["note"]; ok {
		t.Errorf("empty notes should omit note, got %v", b)
	}
}

// --- ListEvents: tolerant decoding + toEvent field spellings ----------------

func TestLilmailListEventsEnvelope(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/calendar/events", func(r recordedReq) (int, string) {
		// The source should send both from/start and to/end for compatibility.
		if r.Query.Get("from") != "2026-07-01T00:00:00Z" || r.Query.Get("start") != "2026-07-01T00:00:00Z" {
			t.Errorf("from/start query wrong: %v", r.Query)
		}
		if r.Query.Get("to") == "" || r.Query.Get("end") == "" {
			t.Errorf("to/end query missing: %v", r.Query)
		}
		return 200, `{"events":[
			{"id":"e1","title":"Standup","start":"2026-07-01T09:30:00Z","end":"2026-07-01T09:45:00Z","location":"Meet"},
			{"uid":"e2","summary":"1:1","dtStart":"2026-07-01T14:00:00Z","dtEnd":"2026-07-01T14:30:00Z","allDay":false}
		]}`
	})
	evs, err := f.source().ListEvents(bg(), Auth{}, "2026-07-01T00:00:00Z", "2026-07-08T00:00:00Z")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	if evs[0].ID != "e1" || evs[0].Title != "Standup" || evs[0].Start != "2026-07-01T09:30:00Z" {
		t.Errorf("primary spelling mapping wrong: %+v", evs[0])
	}
	// Second event uses the alternate spellings (uid/summary/dtStart/dtEnd).
	if evs[1].ID != "e2" || evs[1].Title != "1:1" || evs[1].Start != "2026-07-01T14:00:00Z" || evs[1].End != "2026-07-01T14:30:00Z" {
		t.Errorf("alternate spelling mapping wrong: %+v", evs[1])
	}
}

func TestLilmailListEventsBareArray(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/calendar/events", func(r recordedReq) (int, string) {
		return 200, `[{"id":"e9","title":"Solo","start":"2026-07-02T10:00:00Z"}]`
	})
	evs, err := f.source().ListEvents(bg(), Auth{}, "", "")
	if err != nil {
		t.Fatalf("ListEvents bare array: %v", err)
	}
	if len(evs) != 1 || evs[0].ID != "e9" || evs[0].Title != "Solo" {
		t.Fatalf("bare-array decode wrong: %+v", evs)
	}
}

func TestLilmailListEventsDropsEmpty(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/calendar/events", func(r recordedReq) (int, string) {
		// One good event, one empty (no title AND no start) that must be dropped.
		return 200, `{"events":[{"title":"Keep","start":"2026-07-01T09:00:00Z"},{"location":"nowhere"}]}`
	})
	evs, err := f.source().ListEvents(bg(), Auth{}, "", "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].Title != "Keep" {
		t.Fatalf("empty event not dropped: %+v", evs)
	}
}

func TestLilmailListEventsUnrecognized(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/calendar/events", func(r recordedReq) (int, string) {
		return 200, `"not an events shape"`
	})
	_, err := f.source().ListEvents(bg(), Auth{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "unrecognized events response") {
		t.Fatalf("expected unrecognized-response error, got %v", err)
	}
}

// --- Error / non-200 handling ------------------------------------------------

func TestLilmailReadUnauthorized(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/messages", func(r recordedReq) (int, string) { return 401, `{"error":"nope"}` })
	_, err := f.source().Recent(bg(), Auth{}, "INBOX", 5)
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("401 should surface as not-authenticated, got %v", err)
	}
}

func TestLilmailReadServerError(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("GET /v1/messages", func(r recordedReq) (int, string) { return 500, `boom` })
	_, err := f.source().Recent(bg(), Auth{}, "INBOX", 5)
	if err == nil || !strings.Contains(err.Error(), "mail service error 500") {
		t.Fatalf("500 should surface as service error, got %v", err)
	}
}

func TestLilmailWriteUnauthorized(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/messages", func(r recordedReq) (int, string) { return 401, `{}` })
	err := f.source().SendEmail(bg(), Auth{}, Draft{To: "x@y.com"})
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("write 401 should be not-authenticated, got %v", err)
	}
}

func TestLilmailWriteServerError(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/messages", func(r recordedReq) (int, string) { return 422, `bad payload` })
	err := f.source().SendEmail(bg(), Auth{}, Draft{To: "x@y.com"})
	if err == nil || !strings.Contains(err.Error(), "lilmail send failed: 422") {
		t.Fatalf("write non-2xx should carry status+what, got %v", err)
	}
}

func TestLilmailSaveDraftServerError(t *testing.T) {
	f := newFakeLilmail(t)
	f.on("POST /v1/drafts", func(r recordedReq) (int, string) { return 500, `nope` })
	err := f.source().SaveDraft(bg(), Auth{}, Draft{To: "x@y.com"})
	if err == nil || !strings.Contains(err.Error(), "lilmail draft failed: 500") {
		t.Fatalf("draft non-2xx should surface, got %v", err)
	}
}

// A transport failure (server closed) surfaces as "mail service unreachable".
func TestLilmailUnreachable(t *testing.T) {
	f := newFakeLilmail(t)
	src := f.source()
	f.srv.Close() // now nothing is listening
	if _, err := src.Recent(bg(), Auth{}, "INBOX", 5); err == nil ||
		!strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("closed server should surface as unreachable, got %v", err)
	}
	if err := src.SendEmail(bg(), Auth{}, Draft{To: "x@y.com"}); err == nil ||
		!strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("closed server write should surface as unreachable, got %v", err)
	}
}

func TestLilmailName(t *testing.T) {
	if NewLilmailSource("http://x").Name() != "lilmail" {
		t.Fatal("Name() should be lilmail")
	}
}
