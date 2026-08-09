package main

// routes_assistant_http_test.go — HTTP-level route tests for the assistant/mail
// surface that were previously unexercised: /status, /tier, /home, /api/mail/search,
// and — most importantly — the SAME routes composed behind the REAL auth
// middleware (services/auth), so the authZ contract is asserted at the transport
// layer: X-User-ID is stripped from the client and set from the session, an
// unauthenticated call is 401, and one user cannot execute another's proposal.
//
// These complement routes_assistant_test.go (which drives the handlers directly
// with a scripted model) by wrapping the mux in auth.Handler.Middleware and
// presenting a real session token — the way the running server does.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
	"vulos/backend/services/auth"
)

// --- Handler-level tests for the read/config surface (fixture, no model) ------

// newReadMux wires the assistant routes with a fixture mailbox and a never-called
// model (the read/config routes under test make no model call except /home's
// Attention, which the local ollama-tier scripted model satisfies).
func newReadMux(t *testing.T, replies []string) *http.ServeMux {
	t.Helper()
	deps := assistantDeps{
		model:  &scriptedModel{replies: replies},
		cfg:    ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"},
		source: assistant.NewFixtureSource(),
		ledger: assistant.NewProposalLedger(),
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)
	return mux
}

func TestStatusRouteNoAuthRequired(t *testing.T) {
	mux := newReadMux(t, nil)
	// /status is intentionally pre-auth (the UI badge). No X-User-ID needed.
	req := httptest.NewRequest("GET", "/api/assistant/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["mail_source"] != "fixture" {
		t.Errorf("mail_source = %v, want fixture", out["mail_source"])
	}
	if out["tier"] == nil || out["sovereignty"] == nil {
		t.Errorf("status missing tier/sovereignty: %v", out)
	}
	opts, _ := out["tier_options"].([]any)
	if len(opts) == 0 {
		t.Errorf("tier_options should be populated: %v", out["tier_options"])
	}
}

func TestTierRoutePicksAndRequiresAuth(t *testing.T) {
	mux := newReadMux(t, nil)
	// Unauthenticated ⇒ 401.
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/tier", "", `{"tier":"sovereign"}`)
	if rec.Code != 401 {
		t.Fatalf("unauth tier = %d, want 401", rec.Code)
	}
	// Bad tier ⇒ 400.
	rec, _ = doAssistantJSON(t, mux, "POST", "/api/assistant/tier", "user-1", `{"tier":"nonsense"}`)
	if rec.Code != 400 {
		t.Fatalf("bad tier = %d, want 400", rec.Code)
	}
	// Valid tier ⇒ 200 and echoes the chosen tier back.
	rec, out := doAssistantJSON(t, mux, "POST", "/api/assistant/tier", "user-1", `{"tier":"sovereign"}`)
	if rec.Code != 200 {
		t.Fatalf("tier = %d, want 200", rec.Code)
	}
	if out["tier"] == nil || out["sovereignty"] == nil {
		t.Errorf("tier response missing tier/sovereignty: %v", out)
	}
}

func TestHomeRouteAggregate(t *testing.T) {
	// /home makes one model call (Attention); feed the scripted model a brief.
	mux := newReadMux(t, []string{"Two unread threads need you: a renewal and an invoice."})
	rec, _ := doAssistantJSON(t, mux, "GET", "/api/assistant/home", "", "")
	if rec.Code != 401 {
		t.Fatalf("unauth home = %d, want 401", rec.Code)
	}
	rec, out := doAssistantJSON(t, mux, "GET", "/api/assistant/home", "user-1", "")
	if rec.Code != 200 {
		t.Fatalf("home = %d, want 200", rec.Code)
	}
	if out["mail_source"] != "fixture" {
		t.Errorf("home mail_source = %v", out["mail_source"])
	}
	// Fixture inbox has unread mail ⇒ focus + activity populated.
	focus, _ := out["focus"].([]any)
	activity, _ := out["activity"].([]any)
	if len(focus) == 0 {
		t.Errorf("home focus should be non-empty for the fixture inbox")
	}
	if len(activity) == 0 {
		t.Errorf("home activity should be non-empty for the fixture inbox")
	}
	if out["greeting"] == nil || out["sovereignty"] == nil {
		t.Errorf("home missing greeting/sovereignty: %v", out)
	}
}

func TestMailSearchRoute(t *testing.T) {
	mux := newReadMux(t, nil)
	// Unauthenticated ⇒ 401.
	req := httptest.NewRequest("GET", "/api/mail/search?q=invoice", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauth mail/search = %d, want 401", rec.Code)
	}
	// Authed, matching query ⇒ the fixture "invoice" message.
	req = httptest.NewRequest("GET", "/api/mail/search?q=invoice&limit=5", nil)
	req.Header.Set("X-User-ID", "user-1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mail/search = %d, want 200", rec.Code)
	}
	var out struct {
		Messages []assistant.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Messages) == 0 {
		t.Fatalf("expected at least one match for 'invoice'")
	}
	found := false
	for _, m := range out.Messages {
		if strings.Contains(strings.ToLower(m.Subject), "invoice") {
			found = true
		}
	}
	if !found {
		t.Errorf("no invoice message in results: %+v", out.Messages)
	}

	// Empty q ⇒ 200 with an empty (non-null) list, never a 500.
	req = httptest.NewRequest("GET", "/api/mail/search?q=", nil)
	req.Header.Set("X-User-ID", "user-1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "messages") {
		t.Fatalf("empty-q search should be 200 with messages[]: %d %s", rec.Code, rec.Body.String())
	}
}

// --- REAL auth-middleware integration: the HTTP-layer authZ contract ---------

// authTestServer builds an httptest.Server whose mux is wrapped in the REAL
// auth.Handler.Middleware, with the assistant routes registered behind it. It
// returns the server plus a valid session token for a freshly-created user.
// This exercises the actual identity path: the middleware strips any
// client-supplied X-User-ID and sets it from the validated session.
func authTestServer(t *testing.T) (*httptest.Server, *auth.Store) {
	t.Helper()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := auth.NewHandler(store)

	mux := http.NewServeMux()
	deps := assistantDeps{
		model:  &scriptedModel{replies: []string{`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"S","body":"b"}}`}},
		cfg:    ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"},
		source: assistant.NewFixtureSource(),
		ledger: assistant.NewProposalLedger(),
	}
	registerAssistantRoutesWithDeps(mux, deps)

	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, store
}

func sessionToken(t *testing.T, store *auth.Store, provider, id, email string) string {
	t.Helper()
	u := store.FindOrCreateUser(provider, id, email, email, "", true)
	sess := store.CreateSession(u, "dev-"+id)
	return sess.Token
}

// bearerReq issues a request with a Bearer session token (and any extra headers).
func bearerReq(t *testing.T, srv *httptest.Server, method, path, token, body string, hdr map[string]string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, err = http.NewRequest(method, srv.URL+path, nil)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// TestMiddlewareStripsClientUserID: a forged X-User-ID from the client must be
// deleted by the middleware; with no valid session the request is 401 (the
// forged header cannot authenticate the caller).
func TestMiddlewareStripsClientUserID(t *testing.T) {
	srv, _ := authTestServer(t)
	// No session token, but the client tries to inject an identity header.
	resp := bearerReq(t, srv, "GET", "/api/assistant/home", "", "", map[string]string{"X-User-ID": "attacker"})
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("forged X-User-ID should not authenticate; got %d", resp.StatusCode)
	}
}

// TestMiddlewareSetsUserIDFromSession: a valid session token authenticates and
// the handler sees the session's user id (the fixture Home renders 200).
func TestMiddlewareSetsUserIDFromSession(t *testing.T) {
	srv, store := authTestServer(t)
	tok := sessionToken(t, store, "google", "u1", "u1@ex.com")
	// Even if the client ALSO sends a bogus X-User-ID, the session wins.
	resp := bearerReq(t, srv, "GET", "/api/assistant/status", tok, "", map[string]string{"X-User-ID": "spoof"})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("valid session status = %d, want 200", resp.StatusCode)
	}
}

// TestExecuteCrossUserRejectedThroughMiddleware: user A creates a proposal;
// user B (a DIFFERENT real session) cannot execute it by id — 403, nothing sent.
// This is the cross-user authZ check at the full HTTP layer (two real sessions,
// two real tokens), not just the handler seam.
func TestExecuteCrossUserRejectedThroughMiddleware(t *testing.T) {
	srv, store := authTestServer(t)
	tokA := sessionToken(t, store, "google", "alice", "alice@ex.com")
	tokB := sessionToken(t, store, "google", "bob", "bob@ex.com")

	// Alice creates a mutating proposal.
	resp := bearerReq(t, srv, "POST", "/api/assistant/agent", tokA, `{"message":"email dana"}`, nil)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	prop, _ := out["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("no proposal for alice: %v", out)
	}
	id, _ := prop["id"].(string)
	if id == "" {
		t.Fatal("proposal has no id")
	}

	// Bob tries to execute Alice's proposal ⇒ 403.
	resp = bearerReq(t, srv, "POST", "/api/assistant/execute", tokB, `{"id":"`+id+`"}`, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-user execute = %d, want 403 (%s)", resp.StatusCode, body)
	}

	// Alice can still execute it (Bob's probe did not consume it).
	resp = bearerReq(t, srv, "POST", "/api/assistant/execute", tokA, `{"id":"`+id+`"}`, nil)
	var ok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ok)
	resp.Body.Close()
	if resp.StatusCode != 200 || ok["executed"] != true {
		t.Fatalf("owner execute = %d %v, want 200 executed", resp.StatusCode, ok)
	}
}

// TestUnauthedThroughMiddleware: no token ⇒ 401 on a protected route.
func TestUnauthedThroughMiddleware(t *testing.T) {
	srv, _ := authTestServer(t)
	resp := bearerReq(t, srv, "POST", "/api/assistant/execute", "", `{"id":"prop_x"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated execute = %d, want 401", resp.StatusCode)
	}
}

// TestMailURLRoute checks the Mail-app URL endpoint returns the configured (or
// default) LilMail base.
func TestMailURLRoute(t *testing.T) {
	t.Setenv("VULOS_MAIL_URL", "https://mail.example.test")
	mux := http.NewServeMux()
	registerMailRoutes(mux)
	req := httptest.NewRequest("GET", "/api/mail/url", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mail/url = %d, want 200", rec.Code)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["url"] != "https://mail.example.test" {
		t.Errorf("url = %q, want configured value", out["url"])
	}
}

// TestExportThroughMiddleware proves the export endpoint's identity comes from
// the session, not the client: a valid session streams a zip (200), a forged
// X-User-ID with no session is 401.
func TestExportThroughMiddleware(t *testing.T) {
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := auth.NewHandler(store)
	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, nil, nil) // no files svc, no mail — still a valid honest zip
	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)

	// Forged header, no session ⇒ 401.
	resp := bearerReq(t, srv, "GET", "/api/export/data", "", "", map[string]string{"X-User-ID": "attacker"})
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("forged export = %d, want 401", resp.StatusCode)
	}

	// Valid session ⇒ 200 application/zip.
	tok := sessionToken(t, store, "google", "carol", "carol@ex.com")
	resp = bearerReq(t, srv, "GET", "/api/export/data", tok, "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session export = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("export content-type = %q, want application/zip", ct)
	}
}
