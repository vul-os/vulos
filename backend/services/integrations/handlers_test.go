package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestMux(base string) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterHandlers(mux, testClient(base))
	return mux
}

func brokerOK(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mac := hmac.New(sha256.New, []byte(testSecret))
		mac.Write([]byte("integrations:token:google:" + testULID))
		if r.Header.Get("X-Integration-Sig") != hex.EncodeToString(mac.Sum(nil)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-1",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"scopes":       "openid email",
		})
	}))
}

func TestHandlerTokenRequiresUser(t *testing.T) {
	srv := brokerOK(t)
	defer srv.Close()
	mux := newTestMux(srv.URL)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/integrations/google/token", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no user: want 401, got %d", rr.Code)
	}
}

func TestHandlerTokenOK(t *testing.T) {
	srv := brokerOK(t)
	defer srv.Close()
	mux := newTestMux(srv.URL)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/google/token", nil)
	r.Header.Set("X-User-ID", "user-1")
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "tok-1" {
		t.Fatalf("unexpected body: %+v", out)
	}
}

func TestHandlerTokenNotConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	mux := newTestMux(srv.URL)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/google/token", nil)
	r.Header.Set("X-User-ID", "user-1")
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("not connected: want 404, got %d", rr.Code)
	}
}

func TestHandlerStatusUnprovisioned(t *testing.T) {
	// A box with no device credentials reports configured=false, connected=false.
	mux := http.NewServeMux()
	unprov := &Client{cloudBaseURL: "http://unused", cache: map[string]Token{}, now: time.Now, httpClient: http.DefaultClient}
	RegisterHandlers(mux, unprov)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/status", nil)
	r.Header.Set("X-User-ID", "user-1")
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	var st map[string]any
	json.NewDecoder(rr.Body).Decode(&st)
	if st["configured"] != false || st["connected"] != false {
		t.Fatalf("unprovisioned status: %+v", st)
	}
}
