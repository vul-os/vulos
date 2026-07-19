// cloudstatus_test.go — security + fail-safe tests for the two cloud
// status surfaces (CLOUD-STATUS-01):
//
//   - public-no-leak    : GET /api/cloud/status carries no infra addresses / tenant
//     data and only the fixed component field set.
//   - user-scoped-no-IDOR: GET /api/account/status returns ONLY the caller's own
//     boxes; user A can never see user B's resources.
//   - fail-safe         : a failing probe / unavailable source degrades to
//     down/empty and returns 200 — it never 500s or panics.
//   - fail-closed auth  : no session ⇒ 401 with no body data.
package cproutes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cloudstatus"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/routing"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

func csOKProbe(context.Context, string) bool   { return true }
func csFailProbe(context.Context, string) bool { return false }

// newCloudStatusTestMux wires the routes over the given stores with a deterministic
// probe (no network), returning the mux + auth store to mint sessions against.
func newCloudStatusTestMux(t *testing.T, db cloudstatus.DBPinger, routingStore routing.Store, fleetStore fleet.Store, probe cloudstatus.Probe) (*http.ServeMux, *auth.Store) {
	t.Helper()
	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("test-secret"))
	if err != nil {
		t.Fatalf("open auth store: %v", err)
	}
	t.Cleanup(func() { _ = authStore.Close() })

	agg := cloudstatus.New(cloudstatus.Config{
		DB:      db,
		PoPs:    csPoPAdapter{store: routingStore},
		Devices: csDeviceAdapter{store: fleetStore},
		Relay:   csRelayAdapter{store: nil},
		Probe:   probe,
		Now:     func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) },
	})
	mux := http.NewServeMux()
	RegisterCloudStatus(mux, agg, authStore)
	return mux, authStore
}

func TestCloudStatus_Public_NoLeak(t *testing.T) {
	rs := routing.NewMemStore()
	// A PoP carrying a real IP — the public page must NEVER surface it.
	if err := rs.UpsertPoP(context.Background(), routing.PoP{ID: "pop-eu-fra-01", Region: "eu-central", IP: "203.0.113.7", Healthy: true}); err != nil {
		t.Fatalf("upsert pop: %v", err)
	}
	mux, _ := newCloudStatusTestMux(t, stubPinger{}, rs, fleet.NewMemStore(), csOKProbe)

	req := httptest.NewRequest(http.MethodGet, "/api/cloud/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing permissive CORS header")
	}
	if cc := rr.Header().Get("Cache-Control"); cc == "" || cc == "no-store" {
		t.Errorf("public page should be cache-friendly, got Cache-Control=%q", cc)
	}
	body := rr.Body.String()
	for _, forbidden := range []string{"203.0.113.7", "://", "9443", "internal.example.test", "healthz"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("public status leaked %q: %s", forbidden, body)
		}
	}
	// Field-set pin: only the fixed public keys.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range top {
		if k != "generated_at" && k != "overall" && k != "components" {
			t.Errorf("unexpected top-level field %q in public status", k)
		}
	}
}

func TestCloudStatus_Public_FailSafeOnProbeError(t *testing.T) {
	// DB unreachable + probe failing: the handler must still answer 200 (down),
	// never 500.
	mux, _ := newCloudStatusTestMux(t, stubPinger{err: context.DeadlineExceeded}, routing.NewMemStore(), fleet.NewMemStore(), csFailProbe)
	req := httptest.NewRequest(http.MethodGet, "/api/cloud/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when checks fail; body=%s", rr.Code, rr.Body.String())
	}
	var got cloudstatus.PublicStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Overall != cloudstatus.StatusDown {
		t.Errorf("overall = %q, want down", got.Overall)
	}
}

func TestAccountStatus_Unauthenticated_401(t *testing.T) {
	mux, _ := newCloudStatusTestMux(t, stubPinger{}, routing.NewMemStore(), fleet.NewMemStore(), csOKProbe)
	req := httptest.NewRequest(http.MethodGet, "/api/account/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rr.Code)
	}
	// Fail-closed: no account payload leaks in the 401 body.
	if strings.Contains(rr.Body.String(), "\"boxes\"") {
		t.Errorf("401 body leaked account data: %s", rr.Body.String())
	}
}

func TestAccountStatus_NoIDOR(t *testing.T) {
	fs := fleet.NewMemStore()
	ctx := context.Background()
	// Two accounts, each owning a distinct box.
	mux, authStore := newCloudStatusTestMux(t, stubPinger{}, routing.NewMemStore(), fs, csOKProbe)

	userA, tokenA, err := authStore.Signup(ctx, "a@example.test", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("signup A: %v", err)
	}
	userB, _, err := authStore.Signup(ctx, "b@example.test", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("signup B: %v", err)
	}
	hb := time.Now().UTC()
	if err := fs.UpsertDevice(ctx, fleet.Device{ULID: "01BOXAAA", AccountID: userA.ID, Name: "alice-box", Health: "ok", LastHeartbeat: &hb}); err != nil {
		t.Fatalf("upsert A device: %v", err)
	}
	if err := fs.UpsertDevice(ctx, fleet.Device{ULID: "01BOXBBB", AccountID: userB.ID, Name: "bob-box", Health: "ok", LastHeartbeat: &hb}); err != nil {
		t.Fatalf("upsert B device: %v", err)
	}

	// Call as A.
	req := httptest.NewRequest(http.MethodGet, "/api/account/status", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tokenA})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "01BOXAAA") || !strings.Contains(body, "alice-box") {
		t.Errorf("A's own box missing from A's view: %s", body)
	}
	// The critical assertion: B's resources MUST NOT appear in A's view.
	if strings.Contains(body, "01BOXBBB") || strings.Contains(body, "bob-box") {
		t.Errorf("IDOR: A's status view leaked B's box: %s", body)
	}

	var got cloudstatus.AccountStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Boxes) != 1 || got.Boxes[0].ULID != "01BOXAAA" {
		t.Fatalf("A boxes = %+v, want exactly 01BOXAAA", got.Boxes)
	}
}

func TestAccountStatus_FailSafeOnNilSource(t *testing.T) {
	// No fleet store wired: the section is empty but the request still succeeds.
	mux, authStore := newCloudStatusTestMux(t, stubPinger{}, routing.NewMemStore(), nil, csOKProbe)
	user, token, err := authStore.Signup(context.Background(), "c@example.test", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	_ = user
	req := httptest.NewRequest(http.MethodGet, "/api/account/status", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got cloudstatus.AccountStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Boxes == nil {
		t.Errorf("boxes must serialize as [], not null")
	}
}
