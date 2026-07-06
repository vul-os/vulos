package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/storage"
	"vulos/backend/services/files"
)

// newExportFilesSvc builds a REAL files.Service backed by a local-FS grant broker
// so the export's Drive walk moves real bytes (no fakes on the data plane).
func newExportFilesSvc(t *testing.T) *files.Service {
	t.Helper()
	root := t.TempDir()
	resolver := storage.NewResolver(storage.ResolverConfig{BucketPrefix: "vulos-", LocalRoot: root})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, time.Minute)
	svc, err := files.New(filepath.Join(t.TempDir(), "files.db"), broker, resolver.BucketFor)
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

func putFile(t *testing.T, svc *files.Service, userID, parentID, name, body string) *files.Node {
	t.Helper()
	n, _, err := svc.UploadGrant(context.Background(), userID, parentID, name, "text/plain", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant(%s): %v", name, err)
	}
	if _, err := svc.PutContent(context.Background(), userID, n.ID, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("PutContent(%s): %v", name, err)
	}
	return n
}

// The Drive export walks the real tree, copies real bytes, and — crucially —
// sanitizes every node name so a hostile filename cannot escape its folder inside
// the zip (path traversal) nor inject zip entries at the archive root.
func TestExportDriveTreeRealBytesAndPathSafe(t *testing.T) {
	svc := newExportFilesSvc(t)
	const user = "user-export"

	putFile(t, svc, user, "", "notes.txt", "top level note")
	dir, err := svc.CreateFolder(user, "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	putFile(t, svc, user, dir.ID, "memo.txt", "inside docs")
	// A hostile-but-separator-free name (the files service rejects '/'): leading
	// dots + control chars that safeSegment must neutralize before it hits the zip.
	putFile(t, svc, user, dir.ID, "..passwd\x01evil", "should be neutralized")

	mux := http.NewServeMux()
	registerExportRoutes(mux, svc, "", nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/export/data", nil)
	req.Header.Set("X-User-ID", user)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	zfiles := readZip(t, rr.Body.Bytes())

	// Real bytes for the ordinary files.
	if got := zfiles["files/notes.txt"]; got != "top level note" {
		t.Errorf("files/notes.txt = %q", got)
	}
	if got := zfiles["files/docs/memo.txt"]; got != "inside docs" {
		t.Errorf("files/docs/memo.txt = %q", got)
	}

	// No entry may escape the files/ prefix or contain traversal.
	for name := range zfiles {
		if name == "MANIFEST.txt" {
			continue
		}
		if strings.Contains(name, "..") {
			t.Errorf("zip entry %q contains traversal", name)
		}
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "etc/") {
			t.Errorf("zip entry %q escaped the archive layout", name)
		}
	}
	// The hostile file is present but under the docs/ folder with a neutralized
	// name: no leading dot, no control char, exactly two path separators.
	found := false
	for name := range zfiles {
		if strings.HasPrefix(name, "files/docs/") && strings.Contains(name, "passwd") {
			found = true
			seg := strings.TrimPrefix(name, "files/docs/")
			if strings.HasPrefix(seg, ".") {
				t.Errorf("hostile name kept a leading dot: %q", name)
			}
			if strings.ContainsRune(seg, '\x01') {
				t.Errorf("hostile name kept a control char: %q", name)
			}
			if strings.Count(name, "/") != 2 {
				t.Errorf("hostile name gained extra path segments: %q", name)
			}
		}
	}
	if !found {
		t.Errorf("neutralized hostile file missing; entries=%v", keys(zfiles))
	}

	// Manifest reports the real counts.
	man := zfiles["MANIFEST.txt"]
	if !strings.Contains(man, "file(s) exported") || !strings.Contains(man, "folder(s)") {
		t.Errorf("manifest missing file/folder tally:\n%s", man)
	}
}

// Only the caller's OWN Drive nodes are exported — the walk is ACL-scoped, so
// another user's files never leak into someone's export.
func TestExportDriveTreeIsOwnerScoped(t *testing.T) {
	svc := newExportFilesSvc(t)
	putFile(t, svc, "alice", "", "alice-secret.txt", "alice only")
	putFile(t, svc, "bob", "", "bob-file.txt", "bob only")

	mux := http.NewServeMux()
	registerExportRoutes(mux, svc, "", nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/export/data", nil)
	req.Header.Set("X-User-ID", "bob")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	zfiles := readZip(t, rr.Body.Bytes())
	if _, ok := zfiles["files/bob-file.txt"]; !ok {
		t.Errorf("bob's own file missing from his export: %v", keys(zfiles))
	}
	for name := range zfiles {
		if strings.Contains(name, "alice") {
			t.Fatalf("alice's data leaked into bob's export: %q", name)
		}
	}
}

// The calendar/contacts export escapes untrusted field values per RFC 5545/6350,
// so a malicious title/name cannot inject extra iCal/vCard lines (property
// injection). Drives fetchCalendarICS + fetchContactsVCF + icalText/vcardText.
func TestExportCalendarContactsInjectionSafe(t *testing.T) {
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/calendar/events"):
			io.WriteString(w, `{"events":[{"id":"e1","title":"Sync\nEND:VEVENT\nBEGIN:VEVENT\nSUMMARY:injected","start":"2026-07-08T14:00:00Z","end":"2026-07-08T15:00:00Z","location":"HQ; room 1, floor 2"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/contacts"):
			io.WriteString(w, `{"contacts":[{"name":"Eve\nEND:VCARD\nBEGIN:VCARD\nFN:injected","email":"eve@x.io","phone":"+1;555,1234"}]}`)
		default:
			io.WriteString(w, `{"messages":[]}`)
		}
	}))
	defer mail.Close()

	mux := http.NewServeMux()
	registerExportRoutes(mux, nil, mail.URL, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/export/data", nil)
	req.Header.Set("X-User-ID", "user-1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	zfiles := readZip(t, rr.Body.Bytes())

	ics, ok := zfiles["calendar.ics"]
	if !ok {
		t.Fatalf("calendar.ics missing; got %v", keys(zfiles))
	}
	// The escaped newline must NOT produce a second REAL VEVENT — i.e. a line that
	// begins with BEGIN:VEVENT. The injected "BEGIN:VEVENT" survives only as
	// escaped text inside the SUMMARY line, never at a line start.
	if n := countLinesWithPrefix(ics, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("iCal injection: expected exactly 1 real VEVENT line, got %d\n%s", n, ics)
	}
	if !strings.Contains(ics, "\\n") {
		t.Errorf("newline in title should be escaped to \\n:\n%s", ics)
	}
	// Location's ; and , must be escaped so they don't split the property.
	if !strings.Contains(ics, "HQ\\; room 1\\, floor 2") {
		t.Errorf("iCal special chars not escaped:\n%s", ics)
	}

	vcf, ok := zfiles["contacts.vcf"]
	if !ok {
		t.Fatalf("contacts.vcf missing; got %v", keys(zfiles))
	}
	if n := countLinesWithPrefix(vcf, "BEGIN:VCARD"); n != 1 {
		t.Errorf("vCard injection: expected exactly 1 real VCARD line, got %d\n%s", n, vcf)
	}
}

// countLinesWithPrefix counts CRLF/LF lines that START with prefix — used to tell
// a real iCal/vCard component boundary from the same text escaped inside a value.
func countLinesWithPrefix(s, prefix string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}
