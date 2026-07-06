package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/assistant"
	"vulos/backend/services/auth"
)

// readZip unzips a response body into a name→contents map.
func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func doExport(t *testing.T, mux *http.ServeMux, userID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/export/data", nil)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestExportRequiresSession: no X-User-ID ⇒ 401, no zip.
func TestExportRequiresSession(t *testing.T) {
	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, nil)
	rr := doExport(t, mux, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// TestExportEmptyStillHonest: with no files service and no mail, the export is
// still a valid zip carrying an HONEST manifest that says what was skipped —
// never a silent lie of completeness.
func TestExportEmptyStillHonest(t *testing.T) {
	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, nil)
	rr := doExport(t, mux, "user-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q, want application/zip", ct)
	}
	files := readZip(t, rr.Body.Bytes())
	man, ok := files["MANIFEST.txt"]
	if !ok {
		t.Fatalf("archive is missing MANIFEST.txt; got %v", keys(files))
	}
	for _, want := range []string{"VULOS DATA EXPORT", "mail: SKIPPED", "files: SKIPPED", "NOT INCLUDED"} {
		if !strings.Contains(man, want) {
			t.Errorf("manifest missing %q\n---\n%s", want, man)
		}
	}
}

// TestExportMail streams messages from a fake LilMail /v1 server into .eml files
// + a messages.json index, proving the mail path is real (not hardcoded).
func TestExportMail(t *testing.T) {
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only INBOX has a message; other folders return empty so the walk is exercised.
		if strings.HasPrefix(r.URL.Path, "/v1/messages") && r.URL.Query().Get("folder") == "INBOX" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"messages":[{"id":"m1","from":"a@ex.com","fromName":"Alice","to":"me@ex.com","subject":"Hello there","body":"Body line one\nline two","date":"2026-01-02T03:04:05Z","flags":["\\Seen"],"messageId":"<m1@ex.com>"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"messages":[]}`)
	}))
	defer mail.Close()

	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, mail.URL, nil, nil)
	rr := doExport(t, mux, "user-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	files := readZip(t, rr.Body.Bytes())

	if _, ok := files["mail/INBOX/messages.json"]; !ok {
		t.Errorf("missing mail/INBOX/messages.json; got %v", keys(files))
	}
	var emlName, eml string
	for name, body := range files {
		if strings.HasPrefix(name, "mail/INBOX/") && strings.HasSuffix(name, ".eml") {
			emlName, eml = name, body
		}
	}
	if eml == "" {
		t.Fatalf("no .eml produced; got %v", keys(files))
	}
	for _, want := range []string{"From: Alice <a@ex.com>", "Subject: Hello there", "Body line one"} {
		if !strings.Contains(eml, want) {
			t.Errorf("%s missing %q\n---\n%s", emlName, want, eml)
		}
	}
	if man := files["MANIFEST.txt"]; !strings.Contains(man, "mail/INBOX: 1 messages") {
		t.Errorf("manifest should record the exported mail count; got:\n%s", man)
	}
}

// TestMessageToEML checks the standalone RFC-822 rendering (incl. header
// injection safety) and preview fallback.
func TestMessageToEML(t *testing.T) {
	eml := string(messageToEML(assistant.Message{
		From: "x@y.com", Subject: "Hi\r\nInjected: evil", Preview: "prev", Folder: "INBOX",
	}))
	if strings.Contains(eml, "Injected: evil") && strings.Contains(eml, "\r\nInjected:") {
		t.Errorf("header injection not neutralized:\n%s", eml)
	}
	if !strings.Contains(eml, "prev") {
		t.Errorf("empty body should fall back to preview:\n%s", eml)
	}
}

func TestSafeSegment(t *testing.T) {
	cases := map[string]string{
		"../etc/passwd": "_etc_passwd", // separators + leading dots neutralized; cannot escape the folder
		"a/b":           "a_b",
		"":              "unnamed",
		"..":            "unnamed",
		"normal.txt":    "normal.txt",
	}
	for in, want := range cases {
		if got := safeSegment(in); got != want {
			t.Errorf("safeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestICalTime(t *testing.T) {
	if got := icalTime("2026-01-02T03:04:05Z"); got != "20260102T030405Z" {
		t.Errorf("icalTime rfc3339 = %q", got)
	}
	if got := icalTime("garbage"); got != "" {
		t.Errorf("icalTime(garbage) = %q, want empty", got)
	}
}

// fakeProfileStore is a stand-in *auth.Store for the settings-export tests. It
// lets us hand safeProfileExport a profile that DELIBERATELY carries secrets
// (API key, PIN hash, a token in the free-form Settings map) and then assert the
// export scrubs every one of them.
type fakeProfileStore struct{ profiles map[string]*auth.Profile }

func (f *fakeProfileStore) GetProfile(userID string) (*auth.Profile, bool) {
	p, ok := f.profiles[userID]
	return p, ok
}

// TestExportSettingsScrubsSecrets: the settings.json section carries the user's
// preferences but NEVER their API key, PIN hash, password hash, or any
// credential-looking entry from the free-form settings map. This is the
// no-secret-leak guarantee, tested against a profile that intentionally holds
// every kind of secret.
func TestExportSettingsScrubsSecrets(t *testing.T) {
	store := &fakeProfileStore{profiles: map[string]*auth.Profile{
		"user-1": {
			UserID:      "user-1",
			DisplayName: "Ada Lovelace",
			Theme:       "dark",
			Locale:      "en-ZA",
			Timezone:    "Africa/Johannesburg",
			AIProvider:  "claude",
			AIModel:     "claude-sonnet-4",
			AIAPIKey:    "sk-SUPER-SECRET-KEY",      // must NOT appear
			PinHash:     "argon2id$SECRET-PIN-HASH", // must NOT appear
			Initiative:  "balanced",
			Settings: map[string]string{
				"favorite_color":  "teal",           // safe → kept
				"session_token":   "TOKEN-LEAK-123", // sensitive → dropped
				"backup_password": "hunter2",        // sensitive → dropped
			},
		},
	}}

	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, safeProfileExport(store))
	rr := doExport(t, mux, "user-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	files := readZip(t, rr.Body.Bytes())

	settings, ok := files["settings.json"]
	if !ok {
		t.Fatalf("settings.json missing; got %v", keys(files))
	}
	// Preferences the user WOULD want to take with them are present.
	for _, want := range []string{"Ada Lovelace", "Africa/Johannesburg", "claude", "favorite_color", "teal"} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.json missing expected pref %q\n---\n%s", want, settings)
		}
	}
	// SECRETS: none of these may appear anywhere in the ENTIRE archive.
	whole := rr.Body.String()
	for _, secret := range []string{
		"sk-SUPER-SECRET-KEY", "SECRET-PIN-HASH", "TOKEN-LEAK-123", "hunter2",
		"session_token", "backup_password", "ai_api_key", "pin_hash", "password_hash",
	} {
		if strings.Contains(whole, secret) {
			t.Errorf("SECRET LEAK: %q found in the export archive", secret)
		}
	}
}

// TestExportSettingsOwnerScoped: the provider only ever returns the caller's own
// profile — a different user's preferences can never appear in the export. The
// handler always calls it with the session-derived X-User-ID.
func TestExportSettingsOwnerScoped(t *testing.T) {
	store := &fakeProfileStore{profiles: map[string]*auth.Profile{
		"alice": {UserID: "alice", DisplayName: "Alice", Settings: map[string]string{"note": "alice-only"}},
		"bob":   {UserID: "bob", DisplayName: "Bob"},
	}}
	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, safeProfileExport(store))

	rr := doExport(t, mux, "bob")
	files := readZip(t, rr.Body.Bytes())
	settings := files["settings.json"]
	if !strings.Contains(settings, "Bob") {
		t.Errorf("bob's own settings missing:\n%s", settings)
	}
	if strings.Contains(rr.Body.String(), "Alice") || strings.Contains(rr.Body.String(), "alice-only") {
		t.Fatalf("alice's settings leaked into bob's export:\n%s", settings)
	}
}

// TestExportSettingsSkippedWhenNoProvider: a nil provider omits settings.json
// but the export stays a valid, honest zip that records the skip.
func TestExportSettingsSkippedWhenNoProvider(t *testing.T) {
	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, "", nil, nil)
	rr := doExport(t, mux, "user-1")
	files := readZip(t, rr.Body.Bytes())
	if _, ok := files["settings.json"]; ok {
		t.Errorf("settings.json should be absent with a nil provider")
	}
	if man := files["MANIFEST.txt"]; !strings.Contains(man, "settings: SKIPPED") {
		t.Errorf("manifest should honestly record the skipped settings section:\n%s", man)
	}
}

func TestLooksSensitiveKey(t *testing.T) {
	for _, k := range []string{"api_key", "APIKEY", "sessionToken", "user_password", "device_pin", "pw_hash", "private_key", "aws_secret"} {
		if !looksSensitiveKey(k) {
			t.Errorf("looksSensitiveKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"favorite_color", "theme", "locale", "layout"} {
		if looksSensitiveKey(k) {
			t.Errorf("looksSensitiveKey(%q) = true, want false", k)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
