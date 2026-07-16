package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/publicapi"
)

func openTestStore(t *testing.T) *publicapi.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := publicapi.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// stubAccounts implements publicapi.AccountStore for testing.
type stubAccounts struct{}

func (s *stubAccounts) GetAccountByID(_ context.Context, accountID string) (string, string, time.Time, error) {
	return "test@example.com", "pro", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil
}

// stubWebhooks implements publicapi.WebhookManager for testing.
type stubWebhooks struct {
	subs []publicapi.WebhookSub
}

func (s *stubWebhooks) ListSubscriptions(_ context.Context, accountID string) ([]publicapi.WebhookSub, error) {
	return s.subs, nil
}

func (s *stubWebhooks) CreateSubscription(_ context.Context, accountID, url string, topics []string) (publicapi.WebhookSub, error) {
	sub := publicapi.WebhookSub{
		ID:        "SUB123",
		AccountID: accountID,
		URL:       url,
		Topics:    topics,
		Active:    true,
		CreatedAt: time.Now(),
	}
	s.subs = append(s.subs, sub)
	return sub, nil
}

func (s *stubWebhooks) DeleteSubscription(_ context.Context, accountID, id string) error {
	return nil
}

func bearerOf(t *testing.T, st *publicapi.Store, accountID string) string {
	t.Helper()
	raw, _, err := st.GenerateToken(context.Background(), accountID, "test")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return raw
}

func newRequest(method, path, body, token string) *http.Request {
	var bodyR *strings.Reader
	if body != "" {
		bodyR = strings.NewReader(body)
	} else {
		bodyR = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, bodyR)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func newHandler(st *publicapi.Store, accts publicapi.AccountStore, wh publicapi.WebhookManager) *publicapi.Handler {
	return publicapi.NewHandler(st, accts, nil, nil, nil, nil, wh)
}

// ─── Test 1: GET /api/v1/account/me returns account shape ────────────────────

func TestGetAccountMe(t *testing.T) {
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil)
	token := bearerOf(t, st, "acc1")

	r := newRequest(http.MethodGet, "/api/v1/account/me", "", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var acct publicapi.Account
	if err := json.NewDecoder(w.Body).Decode(&acct); err != nil {
		t.Fatal(err)
	}
	if acct.AccountID != "acc1" {
		t.Errorf("expected account_id=acc1, got %q", acct.AccountID)
	}
	if acct.Plan != "pro" {
		t.Errorf("expected plan=pro, got %q", acct.Plan)
	}
}

// ─── Test 2: GET /api/v1/devices returns list shape ───────────────────────────

func TestListDevices(t *testing.T) {
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil)
	token := bearerOf(t, st, "acc2")

	r := newRequest(http.MethodGet, "/api/v1/devices", "", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var devs []publicapi.Device
	if err := json.NewDecoder(w.Body).Decode(&devs); err != nil {
		t.Fatal(err)
	}
	// Empty list is valid.
	if devs == nil {
		t.Error("expected non-nil device slice")
	}
}

// ─── Test 3: rate limit — 60 req/min ─────────────────────────────────────────

func TestRateLimit(t *testing.T) {
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil)
	token := bearerOf(t, st, "acc3")

	// 60 requests should all pass.
	for i := 0; i < 60; i++ {
		r := newRequest(http.MethodGet, "/api/v1/account/me", "", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d unexpectedly rate-limited", i+1)
		}
	}
	// 61st request should be rate-limited.
	r := newRequest(http.MethodGet, "/api/v1/account/me", "", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after 60 requests, got %d", w.Code)
	}
}

// ─── Test 4: SCIM token does not work for public API ─────────────────────────

func TestScopeIsolation_SCIMTokenRejected(t *testing.T) {
	// The public API uses a different token store; a SCIM token (arbitrary string)
	// must be rejected as unauthorized. This verifies scope isolation.
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil)

	// A SCIM-style raw token that is NOT registered in the public-api token store.
	scimLookingToken := "scim-token-that-should-not-work"
	r := newRequest(http.MethodGet, "/api/v1/account/me", "", scimLookingToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for SCIM token on public API, got %d", w.Code)
	}
}

// ─── Test 5: webhook CRUD via public API ─────────────────────────────────────

func TestWebhookCRUD(t *testing.T) {
	st := openTestStore(t)
	wh := &stubWebhooks{}
	h := newHandler(st, &stubAccounts{}, wh)
	token := bearerOf(t, st, "acc4")

	// Create webhook.
	body := `{"url":"https://example.com/hook","topics":["mail.delivered"]}`
	r := newRequest(http.MethodPost, "/api/v1/webhooks", body, token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}

	// List webhooks.
	r = newRequest(http.MethodGet, "/api/v1/webhooks", "", token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var subs []publicapi.WebhookSub
	if err := json.NewDecoder(w.Body).Decode(&subs); err != nil {
		t.Fatal(err)
	}
	if len(subs) == 0 {
		t.Error("expected at least one webhook subscription")
	}
}

// ─── Test 6: missing Bearer returns 401 ──────────────────────────────────────

func TestMissingBearer(t *testing.T) {
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil)

	r := newRequest(http.MethodGet, "/api/v1/account/me", "", "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", w.Code)
	}
}

// ─── Test 7: billing/wallet/topup returns 503 when billing not configured ────

func TestTopupWithoutBilling(t *testing.T) {
	st := openTestStore(t)
	h := newHandler(st, &stubAccounts{}, nil) // no billing configured
	token := bearerOf(t, st, "acc5")

	r := newRequest(http.MethodPost, "/api/v1/billing/wallet/topup", `{"amount_zar":1000,"email":"a@b.com"}`, token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with no billing, got %d", w.Code)
	}
}
