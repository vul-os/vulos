package contacts

// handlers_test.go — the owner gate on the unified address book.
//
// This package had NO tests. The gate itself was written correctly and
// fail-closed, but nothing proved it: removing requireOwner, or letting an
// unresolved owner through, would have broken the isolation silently. On a
// multi-user box that is a second account reading the owner's phone book and
// SIM contacts, and writing into it.
//
// These drive RegisterHandlers — the same function cmd/server calls — rather
// than a hand-built mux with a copy of the handlers, so a regression in the
// shipped registration fails here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	owner    = "owner-user-id"
	stranger = "second-profile-id"
)

// newTestMux wires the real routes with a fixed owner and inert sources.
func newTestMux(ownerID func() string) (*http.ServeMux, *Service) {
	svc := New(ownerID)
	svc.CardDAVSource = func(*http.Request) ([]RawContact, bool) {
		return []RawContact{{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}}}, true
	}
	svc.SIMSource = func() ([]RawContact, bool) {
		return []RawContact{{Name: "SIM Entry", Phones: []string{"+27000000000"}}}, true
	}
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)
	return mux, svc
}

func req(mux *http.ServeMux, method, path, userID, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestUnifiedContacts_OwnerOnly(t *testing.T) {
	mux, _ := newTestMux(func() string { return owner })

	if rec := req(mux, "GET", "/api/contacts/unified", owner, ""); rec.Code != http.StatusOK {
		t.Fatalf("owner reading their own address book: got %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	// A second profile on the same box must not see the owner's phone book.
	rec := req(mux, "GET", "/api/contacts/unified", stranger, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("second profile: got %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Ada Lovelace") || strings.Contains(rec.Body.String(), "SIM Entry") {
		t.Errorf("the refusal leaked the owner's contacts: %s", rec.Body)
	}

	// Unauthenticated too — the middleware normally 401s first, but this must
	// not depend on that.
	if rec := req(mux, "GET", "/api/contacts/unified", "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("unauthenticated: got %d, want 403", rec.Code)
	}
}

func TestDeviceIngest_OwnerOnly(t *testing.T) {
	mux, svc := newTestMux(func() string { return owner })
	payload := `{"contacts":[{"name":"Injected","phones":["+1"]}]}`

	rec := req(mux, "POST", "/api/contacts/ingest/device", stranger, payload)
	if rec.Code != http.StatusForbidden {
		t.Errorf("second profile pushing into the owner's address book: got %d, want 403", rec.Code)
	}
	// The write must not have landed — a 403 that still mutated state would be
	// the worst of both.
	svc.mu.Lock()
	pushed := len(svc.device)
	svc.mu.Unlock()
	if pushed != 0 {
		t.Errorf("a rejected push still wrote %d device record(s)", pushed)
	}

	if rec := req(mux, "POST", "/api/contacts/ingest/device", owner, payload); rec.Code != http.StatusOK {
		t.Fatalf("owner pushing their own device contacts: got %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

// An unresolvable owner must deny EVERYONE. On a box whose first user has not
// been created yet, "no owner" must not read as "everyone is the owner".
func TestFailsClosedWithNoResolvableOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ownerID func() string
	}{
		{"nil resolver", nil},
		{"owner unresolved", func() string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newTestMux(tc.ownerID)
			for _, call := range []struct{ method, path, body string }{
				{"GET", "/api/contacts/unified", ""},
				{"POST", "/api/contacts/ingest/device", `{"contacts":[]}`},
			} {
				// Even a caller whose id is the empty string — which is what an
				// unresolved owner compares equal to under a naive check.
				for _, uid := range []string{owner, stranger, ""} {
					rec := req(mux, call.method, call.path, uid, call.body)
					if rec.Code != http.StatusForbidden {
						t.Errorf("%s %s as %q: got %d, want 403", call.method, call.path, uid, rec.Code)
					}
				}
			}
		})
	}
}

// The owner's own read must actually return the merged sources, so the tests
// above are not passing merely because the handler returns nothing to anyone.
func TestOwnerReadReturnsTheMergedSources(t *testing.T) {
	mux, _ := newTestMux(func() string { return owner })
	rec := req(mux, "GET", "/api/contacts/unified", owner, "")

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	if !strings.Contains(rec.Body.String(), "Ada Lovelace") {
		t.Errorf("owner read did not include the CardDAV source: %s", rec.Body)
	}
}
