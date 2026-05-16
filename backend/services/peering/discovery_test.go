package peering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// discoveryMockServer builds an httptest.Server that serves canned responses
// for /api/verify/lookup and /api/verify/search. The returned server and a
// *DiscoveryService pre-pointed at it are ready for use; the caller must call
// server.Close() when done.
func discoveryMockServer(t *testing.T,
	lookupFunc func(w http.ResponseWriter, r *http.Request),
	searchFunc func(w http.ResponseWriter, r *http.Request),
) (*httptest.Server, *DiscoveryService) {
	t.Helper()
	mux := http.NewServeMux()
	if lookupFunc != nil {
		mux.HandleFunc("/api/verify/lookup", lookupFunc)
	}
	if searchFunc != nil {
		mux.HandleFunc("/api/verify/search", searchFunc)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	svc := &DiscoveryService{
		baseURL: srv.URL,
		client:  srv.Client(),
	}
	return srv, svc
}

// -------------------------------------------------------------------
// DiscoveryLookupByEmail
// -------------------------------------------------------------------

func TestDiscoveryLookupByEmail_Found(t *testing.T) {
	want := DiscoveryResult{
		VulaID:      "vula:ed25519:5Hb7abcdefghijklmnop",
		Server:      "alice.vulos.org",
		DisplayName: "Alice",
	}
	_, svc := discoveryMockServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("email"); got != "alice@example.com" {
				http.Error(w, "wrong email param", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(want)
		}, nil,
	)

	got, err := svc.DiscoveryLookupByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a result, got nil")
	}
	if got.VulaID != want.VulaID {
		t.Errorf("VulaID: got %q, want %q", got.VulaID, want.VulaID)
	}
	if got.Server != want.Server {
		t.Errorf("Server: got %q, want %q", got.Server, want.Server)
	}
	if got.DisplayName != want.DisplayName {
		t.Errorf("DisplayName: got %q, want %q", got.DisplayName, want.DisplayName)
	}
}

func TestDiscoveryLookupByEmail_NotFound(t *testing.T) {
	_, svc := discoveryMockServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}, nil,
	)

	got, err := svc.DiscoveryLookupByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for not-found, got %+v", got)
	}
}

func TestDiscoveryLookupByEmail_OptedOut(t *testing.T) {
	// Server returns 200 with empty vula_id — treated as not found.
	_, svc := discoveryMockServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(DiscoveryResult{}) // zero value
		}, nil,
	)

	got, err := svc.DiscoveryLookupByEmail(context.Background(), "hidden@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for opted-out user, got %+v", got)
	}
}

func TestDiscoveryLookupByEmail_EmptyEmail(t *testing.T) {
	svc := DiscoveryNewService(nil)
	_, err := svc.DiscoveryLookupByEmail(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty email, got nil")
	}
}

func TestDiscoveryLookupByEmail_ServerError(t *testing.T) {
	_, svc := discoveryMockServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}, nil,
	)

	_, err := svc.DiscoveryLookupByEmail(context.Background(), "alice@example.com")
	if err == nil {
		t.Fatal("expected error for server 500, got nil")
	}
}

// -------------------------------------------------------------------
// DiscoverySearchByName
// -------------------------------------------------------------------

func TestDiscoverySearchByName_MultipleResults(t *testing.T) {
	want := []DiscoveryResult{
		{VulaID: "vula:ed25519:AAA", Server: "alice.vulos.org", DisplayName: "Alice"},
		{VulaID: "vula:ed25519:BBB", Server: "al.vulos.org", DisplayName: "Al"},
	}
	_, svc := discoveryMockServer(t, nil,
		func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("name"); got != "Al" {
				http.Error(w, "wrong name param", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(want)
		},
	)

	got, err := svc.DiscoverySearchByName(context.Background(), "Al")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d results, got %d", len(want), len(got))
	}
	for i, w := range want {
		if got[i].VulaID != w.VulaID {
			t.Errorf("result[%d] VulaID: got %q, want %q", i, got[i].VulaID, w.VulaID)
		}
	}
}

func TestDiscoverySearchByName_NoMatches(t *testing.T) {
	_, svc := discoveryMockServer(t, nil,
		func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		},
	)

	got, err := svc.DiscoverySearchByName(context.Background(), "Zoltan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestDiscoverySearchByName_EmptySliceResponse(t *testing.T) {
	_, svc := discoveryMockServer(t, nil,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("null")) // server sends JSON null
		},
	)

	got, err := svc.DiscoverySearchByName(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected 0 results, got %d", len(got))
	}
}

func TestDiscoverySearchByName_EmptyName(t *testing.T) {
	svc := DiscoveryNewService(nil)
	_, err := svc.DiscoverySearchByName(context.Background(), "  ")
	if err == nil {
		t.Fatal("expected error for blank name, got nil")
	}
}

// -------------------------------------------------------------------
// HTTP handler (handleDiscover)
// -------------------------------------------------------------------

func TestDiscoveryHandler_EmailParam(t *testing.T) {
	want := DiscoveryResult{
		VulaID:      "vula:ed25519:TestKey",
		Server:      "bob.vulos.org",
		DisplayName: "Bob",
	}
	_, upstream := discoveryMockServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(want)
		}, nil,
	)

	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, upstream)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover?email=bob%40example.com", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}

	var body struct {
		Found  bool            `json:"found"`
		Result DiscoveryResult `json:"result"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Found {
		t.Error("expected found=true")
	}
	if body.Result.VulaID != want.VulaID {
		t.Errorf("VulaID: got %q, want %q", body.Result.VulaID, want.VulaID)
	}
}

func TestDiscoveryHandler_EmailNotFound(t *testing.T) {
	_, upstream := discoveryMockServer(t,
		func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}, nil,
	)

	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, upstream)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover?email=ghost%40example.com", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var body map[string]any
	json.NewDecoder(rr.Body).Decode(&body)
	if found, _ := body["found"].(bool); found {
		t.Error("expected found=false for not-found email")
	}
}

func TestDiscoveryHandler_NameParam(t *testing.T) {
	results := []DiscoveryResult{
		{VulaID: "vula:ed25519:ZZZ", Server: "carol.vulos.org", DisplayName: "Carol"},
	}
	_, upstream := discoveryMockServer(t, nil,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(results)
		},
	)

	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, upstream)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover?name=Carol", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var body struct {
		Results []DiscoveryResult `json:"results"`
		Count   int               `json:"count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 1 {
		t.Errorf("count: got %d, want 1", body.Count)
	}
	if len(body.Results) != 1 || body.Results[0].DisplayName != "Carol" {
		t.Errorf("unexpected results: %+v", body.Results)
	}
}

func TestDiscoveryHandler_NameEmpty(t *testing.T) {
	svc := DiscoveryNewService(nil)
	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, svc)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDiscoveryHandler_BothParams(t *testing.T) {
	svc := DiscoveryNewService(nil)
	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, svc)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover?email=a@b.com&name=Alice", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestDiscoveryHandler_NameNoMatches(t *testing.T) {
	_, upstream := discoveryMockServer(t, nil,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
		},
	)

	mux := http.NewServeMux()
	RegisterDiscoveryHandlers(mux, upstream)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/discover?name=Nobody", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var body struct {
		Results []DiscoveryResult `json:"results"`
		Count   int               `json:"count"`
	}
	json.NewDecoder(rr.Body).Decode(&body)
	if body.Count != 0 || len(body.Results) != 0 {
		t.Errorf("expected empty results, got count=%d results=%v", body.Count, body.Results)
	}
}
