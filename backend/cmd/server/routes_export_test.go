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
	registerExportRoutes(mux, nil, "", nil)
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
	registerExportRoutes(mux, nil, "", nil)
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
	registerExportRoutes(mux, nil, mail.URL, nil)
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

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
