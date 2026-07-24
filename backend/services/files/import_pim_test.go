package files

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── GoogleContactsSource tests ────────────────────────────────────────────────

// TestGoogleContactsSourceList drives GoogleContactsSource.List against a mocked
// People API and asserts that each person is returned as a flat Node (no dirs)
// with the correct ID and display name.
func TestGoogleContactsSourceList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Verify it hits the right path.
		if !strings.Contains(r.URL.Path, "connections") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connections": []map[string]any{
				{
					"resourceName":   "people/c1",
					"names":          []map[string]any{{"displayName": "Alice Smith", "givenName": "Alice", "familyName": "Smith"}},
					"emailAddresses": []map[string]any{{"value": "alice@example.com"}},
				},
				{
					"resourceName":   "people/c2",
					"emailAddresses": []map[string]any{{"value": "bob@example.com"}},
				},
			},
			// no nextPageToken → single page
		})
	}))
	defer srv.Close()

	src := NewGoogleContactsSource()
	src.peopleBase = srv.URL
	call := ProviderCall{Token: "tok"}

	nodes, err := src.List(context.Background(), call, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[0].ID != "people/c1" || nodes[0].IsDir {
		t.Fatalf("node[0] wrong: %+v", nodes[0])
	}
	if nodes[0].Name != "Alice Smith" {
		t.Errorf("node[0].Name = %q, want Alice Smith", nodes[0].Name)
	}
	// c2 has no names; display name falls back to email.
	if nodes[1].ID != "people/c2" || nodes[1].Name != "bob@example.com" {
		t.Fatalf("node[1] wrong: %+v", nodes[1])
	}
}

// TestGoogleContactsSourceOpenForImport verifies that OpenForImport fetches a
// single person and builds a valid vCard 4.0 with UID = resourceName.
func TestGoogleContactsSourceOpenForImport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resourceName":   "people/c99",
			"names":          []map[string]any{{"displayName": "Carol Jones", "givenName": "Carol", "familyName": "Jones"}},
			"emailAddresses": []map[string]any{{"value": "carol@example.com"}},
			"phoneNumbers":   []map[string]any{{"value": "+1-555-0100"}},
		})
	}))
	defer srv.Close()

	src := NewGoogleContactsSource()
	src.peopleBase = srv.URL
	call := ProviderCall{Token: "tok"}
	node := &Node{ID: "people/c99", Name: "Carol Jones"}

	rc, name, ct, size, err := src.OpenForImport(context.Background(), call, node)
	if err != nil {
		t.Fatalf("OpenForImport: %v", err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)

	if ct != "text/vcard" {
		t.Errorf("content-type = %q, want text/vcard", ct)
	}
	if size <= 0 {
		t.Errorf("size = %d, want >0", size)
	}
	if !strings.HasSuffix(name, ".vcf") {
		t.Errorf("name = %q, want *.vcf", name)
	}
	vc := string(body)
	if !strings.Contains(vc, "BEGIN:VCARD") {
		t.Errorf("body missing BEGIN:VCARD: %s", vc)
	}
	if !strings.Contains(vc, "UID:people/c99") {
		t.Errorf("body missing UID:people/c99: %s", vc)
	}
	if !strings.Contains(vc, "FN:Carol Jones") {
		t.Errorf("body missing FN: %s", vc)
	}
	if !strings.Contains(vc, "carol@example.com") {
		t.Errorf("body missing email: %s", vc)
	}
}

// ── GoogleCalendarSource tests ─────────────────────────────────────────────────

// TestGoogleCalendarSourceList drives GoogleCalendarSource.List against a mocked
// Calendar API and checks that events are returned as flat Nodes.
func TestGoogleCalendarSourceList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, "events") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "evt1", "summary": "Team sync",
					"start": map[string]any{"dateTime": "2026-06-28T10:00:00Z"},
					"end":   map[string]any{"dateTime": "2026-06-28T11:00:00Z"}},
				{"id": "evt2", "summary": "All-day offsite",
					"start": map[string]any{"date": "2026-06-29"},
					"end":   map[string]any{"date": "2026-06-30"}},
			},
		})
	}))
	defer srv.Close()

	src := NewGoogleCalendarSource()
	src.calBase = srv.URL
	call := ProviderCall{Token: "tok"}

	nodes, err := src.List(context.Background(), call, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[0].ID != "evt1" || nodes[0].IsDir || nodes[0].Name != "Team sync" {
		t.Fatalf("node[0] wrong: %+v", nodes[0])
	}
	if nodes[1].ID != "evt2" {
		t.Fatalf("node[1].ID = %q, want evt2", nodes[1].ID)
	}
}

// TestGoogleCalendarSourceOpenForImport verifies that OpenForImport fetches an
// event and returns a valid VCALENDAR iCal string with UID = event ID.
func TestGoogleCalendarSourceOpenForImport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       "evt42",
			"summary":  "Kickoff",
			"start":    map[string]any{"dateTime": "2026-06-28T09:00:00Z"},
			"end":      map[string]any{"dateTime": "2026-06-28T10:00:00Z"},
			"location": "Room 5",
		})
	}))
	defer srv.Close()

	src := NewGoogleCalendarSource()
	src.calBase = srv.URL
	call := ProviderCall{Token: "tok"}
	node := &Node{ID: "evt42", Name: "Kickoff"}

	rc, name, ct, size, err := src.OpenForImport(context.Background(), call, node)
	if err != nil {
		t.Fatalf("OpenForImport: %v", err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)

	if ct != "text/calendar" {
		t.Errorf("content-type = %q, want text/calendar", ct)
	}
	if size <= 0 {
		t.Errorf("size = %d, want >0", size)
	}
	if !strings.HasSuffix(name, ".ics") {
		t.Errorf("name = %q, want *.ics", name)
	}
	ics := string(body)
	if !strings.Contains(ics, "BEGIN:VCALENDAR") || !strings.Contains(ics, "BEGIN:VEVENT") {
		t.Errorf("body missing VCALENDAR/VEVENT: %s", ics)
	}
	if !strings.Contains(ics, "UID:evt42") {
		t.Errorf("body missing UID:evt42: %s", ics)
	}
	if !strings.Contains(ics, "SUMMARY:Kickoff") {
		t.Errorf("body missing SUMMARY: %s", ics)
	}
	if !strings.Contains(ics, "DTSTART:20260628T090000Z") {
		t.Errorf("body missing DTSTART: %s", ics)
	}
}

// ── PIM import runner tests ───────────────────────────────────────────────────

// TestPIMImportContactsFlow exercises the full runPIMImport path for contacts:
//   - Mocked People API returns two contacts.
//   - Mocked lilmail bulk endpoint records received vCards.
//   - After one run, both are in files_import_items (dedup).
//   - A second run (additive-only) skips both.
func TestPIMImportContactsFlow(t *testing.T) {
	ctx := context.Background()

	// Mock People API: list + per-person fetch.
	var contactFetches int
	peopleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "connections"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connections": []map[string]any{
					{"resourceName": "people/cA", "names": []map[string]any{{"displayName": "Person A"}}},
					{"resourceName": "people/cB", "names": []map[string]any{{"displayName": "Person B"}}},
				},
			})
		default:
			contactFetches++
			id := strings.TrimPrefix(r.URL.Path, "/")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resourceName": id,
				"names":        []map[string]any{{"displayName": "Person " + id}},
			})
		}
	}))
	defer peopleSrv.Close()

	// Mock lilmail bulk contacts endpoint.
	var mailReceived []string
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vulos-Broker-Auth") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Account string   `json:"account"`
			VCards  []string `json:"vcards"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mailReceived = append(mailReceived, req.VCards...)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]int{"imported": len(req.VCards), "errors": 0})
	}))
	defer mailSrv.Close()

	svc, _ := newTestService(t)
	src := NewGoogleContactsSource()
	src.peopleBase = peopleSrv.URL
	svc.WithExternal(&fakeTokenSource{token: "tok"})
	svc.WithImport(src)
	svc.WithPIMConfig(mailSrv.URL, "secret", func(string) string { return "alice@example.com" })

	// First run: import two contacts.
	job, err := svc.StartImport(ctx, "u1", "google-contacts", "", "sync")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	if job.Kind != ImportKindContacts {
		t.Fatalf("job.Kind = %q, want contacts", job.Kind)
	}
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("RunImportJob: %v", err)
	}
	jobs, _ := svc.ListImportJobs("u1")
	if jobs[0].Imported != 2 || jobs[0].Errors != 0 {
		t.Fatalf("after run: imported=%d errors=%d, want 2/0", jobs[0].Imported, jobs[0].Errors)
	}
	if jobs[0].Status != ImportStatusDone {
		t.Errorf("status = %q, want done", jobs[0].Status)
	}
	if len(mailReceived) != 2 {
		t.Errorf("mail endpoint received %d vCards, want 2", len(mailReceived))
	}
	for _, vc := range mailReceived {
		if !strings.Contains(vc, "BEGIN:VCARD") {
			t.Errorf("received non-vcard: %q", vc)
		}
	}

	// Second run (additive-only re-pull): both already in dedup map → all skipped.
	mailReceived = mailReceived[:0]
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("second RunImportJob: %v", err)
	}
	jobs, _ = svc.ListImportJobs("u1")
	if jobs[0].Imported != 0 || jobs[0].Skipped != 2 {
		t.Fatalf("re-pull: imported=%d skipped=%d, want 0/2", jobs[0].Imported, jobs[0].Skipped)
	}
	if len(mailReceived) != 0 {
		t.Errorf("re-pull posted %d vCards to mail, want 0 (additive-only)", len(mailReceived))
	}
}

// TestPIMImportCalendarFlow exercises runPIMImport for calendar events:
//   - Mocked Calendar API returns two events.
//   - Mocked lilmail events endpoint records received iCal strings.
//   - Verifies DataKind() dispatches to the calendar path.
func TestPIMImportCalendarFlow(t *testing.T) {
	ctx := context.Background()

	calSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "events") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "ev1", "summary": "Standup",
						"start": map[string]any{"dateTime": "2026-06-28T09:00:00Z"},
						"end":   map[string]any{"dateTime": "2026-06-28T09:30:00Z"}},
					{"id": "ev2", "summary": "Planning",
						"start": map[string]any{"dateTime": "2026-06-28T14:00:00Z"},
						"end":   map[string]any{"dateTime": "2026-06-28T15:00:00Z"}},
				},
			})
		} else {
			// Per-event fetch: extract id from path.
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-1]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      id,
				"summary": "Event " + id,
				"start":   map[string]any{"dateTime": "2026-06-28T09:00:00Z"},
				"end":     map[string]any{"dateTime": "2026-06-28T10:00:00Z"},
			})
		}
	}))
	defer calSrv.Close()

	var mailICS []string
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vulos-Broker-Auth") != "sec" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Events []string `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mailICS = append(mailICS, req.Events...)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]int{"imported": len(req.Events), "errors": 0})
	}))
	defer mailSrv.Close()

	svc, _ := newTestService(t)
	src := NewGoogleCalendarSource()
	src.calBase = calSrv.URL
	svc.WithExternal(&fakeTokenSource{token: "tok"})
	svc.WithImport(src)
	svc.WithPIMConfig(mailSrv.URL, "sec", func(string) string { return "alice@example.com" })

	job, err := svc.StartImport(ctx, "u1", "google-calendar", "", "once")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	if job.Kind != ImportKindCalendar {
		t.Fatalf("job.Kind = %q, want calendar", job.Kind)
	}
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("RunImportJob: %v", err)
	}
	jobs, _ := svc.ListImportJobs("u1")
	if jobs[0].Imported != 2 || jobs[0].Errors != 0 {
		t.Fatalf("after run: imported=%d errors=%d, want 2/0", jobs[0].Imported, jobs[0].Errors)
	}
	if len(mailICS) != 2 {
		t.Errorf("mail endpoint received %d iCal, want 2", len(mailICS))
	}
	for _, ics := range mailICS {
		if !strings.Contains(ics, "BEGIN:VCALENDAR") || !strings.Contains(ics, "BEGIN:VEVENT") {
			t.Errorf("received non-ical: %q", ics)
		}
	}
}

// TestPIMImportPersistAfterDisconnect proves that once contacts/events are
// written to lilmail, disconnecting the integration (token goes dead) does
// not remove them — the import job metadata can be deleted without affecting
// the lilmail copies.
func TestPIMImportPersistAfterDisconnect(t *testing.T) {
	ctx := context.Background()

	peopleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "connections") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connections": []map[string]any{
					{"resourceName": "people/cX", "names": []map[string]any{{"displayName": "X"}}},
				},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceName": "people/cX",
				"names": []map[string]any{{"displayName": "X"}}})
		}
	}))
	defer peopleSrv.Close()

	var mailStored []string
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VCards []string `json:"vcards"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mailStored = append(mailStored, req.VCards...)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]int{"imported": len(req.VCards)})
	}))
	defer mailSrv.Close()

	svc, _ := newTestService(t)
	src := NewGoogleContactsSource()
	src.peopleBase = peopleSrv.URL
	svc.WithExternal(&fakeTokenSource{token: "live"})
	svc.WithImport(src)
	svc.WithPIMConfig(mailSrv.URL, "secret", func(string) string { return "u@example.com" })

	job, _ := svc.StartImport(ctx, "u1", "google-contacts", "", "once")
	_ = svc.RunImportJob(ctx, job.ID)

	if len(mailStored) != 1 {
		t.Fatalf("mail stored %d contacts, want 1", len(mailStored))
	}
	storedContact := mailStored[0]

	// Disconnect: token goes dead. The lilmail data is untouched — the OS
	// has no mechanism to delete from lilmail, by design.
	svc.WithExternal(&fakeTokenSource{token: ""})
	// The stored contact is whatever was POSTed to mailSrv; it remains there
	// regardless of the OS integration state.
	if !strings.Contains(storedContact, "BEGIN:VCARD") {
		t.Errorf("stored contact is not a vCard: %s", storedContact)
	}

	// Delete the import job (keeps data per contract).
	if err := svc.DeleteImportJob("u1", job.ID); err != nil {
		t.Fatalf("DeleteImportJob: %v", err)
	}
	jobs, _ := svc.ListImportJobs("u1")
	if len(jobs) != 0 {
		t.Errorf("job not deleted")
	}
	// The contact remains in mailStored (lilmail data is the source of truth).
	if len(mailStored) != 1 {
		t.Errorf("lilmail contact was removed (should never happen): %v", mailStored)
	}
}

// TestDataKindDispatch confirms that importDataKind() returns the right kind
// for each source type, and the Files-source default is preserved.
func TestDataKindDispatch(t *testing.T) {
	cases := []struct {
		src  ImportSource
		want string
	}{
		{NewGDriveProvider(), ImportKindFiles},   // no DataKind() → files
		{NewOneDriveProvider(), ImportKindFiles}, // no DataKind() → files
		{NewGoogleContactsSource(), ImportKindContacts},
		{NewGoogleCalendarSource(), ImportKindCalendar},
	}
	for _, c := range cases {
		got := importDataKind(c.src)
		if got != c.want {
			t.Errorf("importDataKind(%T) = %q, want %q", c.src, got, c.want)
		}
	}
}
