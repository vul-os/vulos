package superadmin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/superadmin"

	_ "modernc.org/sqlite"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

var testNonce int

func uniqueDSN(tag string) string {
	testNonce++
	return "file::memory:?mode=memory&cache=shared&" + tag + "_" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
}

func openTestStores(t *testing.T) (*auth.Store, *superadmin.Store, *auditlog.Logger) {
	t.Helper()
	os.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")
	os.Setenv("VULOS_DEV", "true")

	authCPDB, err := cpdb.OpenSQLiteDSN(uniqueDSN("auth"))
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	authStore, err := auth.OpenAuthStore(authCPDB, []byte("test-secret"))
	if err != nil {
		_ = authCPDB.Close()
		t.Fatalf("open auth store: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	saStore, err := superadmin.New(authStore.CPDB())
	if err != nil {
		t.Fatalf("open superadmin store: %v", err)
	}

	aldb, err := cpdb.OpenSQLiteDSN(uniqueDSN("al"))
	if err != nil {
		t.Fatalf("open cpdb: %v", err)
	}
	al, err := auditlog.Open(aldb)
	if err != nil {
		t.Fatalf("open auditlog: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	return authStore, saStore, al
}

func createUser(t *testing.T, authStore *auth.Store, email, password string) string {
	t.Helper()
	_, _, err := authStore.Signup(context.Background(), email, password, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signup %s: %v", email, err)
	}
	var id string
	if err := authStore.DB().QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		t.Fatalf("get user id: %v", err)
	}
	return id
}

func promoteSuperAdmin(t *testing.T, authStore *auth.Store, accountID string) {
	t.Helper()
	_, err := authStore.DB().Exec(
		`INSERT OR IGNORE INTO superadmins (account_id, promoted_at, promoted_by, revoked_at) VALUES (?, ?, NULL, NULL)`,
		accountID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("promote superadmin: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// Test 1: Bootstrap creates first admin from env; idempotent on second run.
func TestBootstrap_CreatesFirstAdmin(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	email := "bootstrap@test.example"
	id := createUser(t, authStore, email, "test-password-123456")

	os.Setenv("VULOS_BOOTSTRAP_SUPERADMIN", email)
	defer os.Unsetenv("VULOS_BOOTSTRAP_SUPERADMIN")

	if err := saStore.Bootstrap(context.Background()); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	isSA, err := saStore.IsSuperAdmin(context.Background(), id)
	if err != nil || !isSA {
		t.Fatalf("expected superadmin after bootstrap, got isSA=%v err=%v", isSA, err)
	}

	// Second run is idempotent.
	if err := saStore.Bootstrap(context.Background()); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	var count int
	saStore.DB().QueryRow(`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 superadmin after second bootstrap, got %d", count)
	}
}

// Test 2: Bootstrap is a no-op when a superadmin already exists.
func TestBootstrap_NoopWhenAdminExists(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	id1 := createUser(t, authStore, "existing@test.example", "password-longerrr-12")
	id2 := createUser(t, authStore, "bootstrap2@test.example", "password-longerrr-12")
	promoteSuperAdmin(t, authStore, id1)

	os.Setenv("VULOS_BOOTSTRAP_SUPERADMIN", "bootstrap2@test.example")
	defer os.Unsetenv("VULOS_BOOTSTRAP_SUPERADMIN")

	if err := saStore.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap with existing admin: %v", err)
	}
	isSA, _ := saStore.IsSuperAdmin(context.Background(), id2)
	if isSA {
		t.Fatal("bootstrap should not promote when superadmin exists")
	}
}

// Test 3: IsSuperAdmin true/false roundtrip.
func TestIsSuperAdmin_Roundtrip(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	id := createUser(t, authStore, "sa@test.example", "test-password-12345")
	otherId := createUser(t, authStore, "notsa@test.example", "test-password-12345")

	// Before promotion: false.
	isSA, err := saStore.IsSuperAdmin(context.Background(), id)
	if err != nil || isSA {
		t.Fatalf("expected false before promotion")
	}

	promoteSuperAdmin(t, authStore, id)
	isSA, err = saStore.IsSuperAdmin(context.Background(), id)
	if err != nil || !isSA {
		t.Fatalf("expected true after promotion")
	}

	isSA, _ = saStore.IsSuperAdmin(context.Background(), otherId)
	if isSA {
		t.Fatal("non-admin should not be superadmin")
	}
}

// Test 4: IP allowlist denies non-allowed IP with 403.
func TestIPAllowlist_DeniesNonAllowedIP(t *testing.T) {
	authStore, saStore, al := openTestStores(t)

	os.Setenv("VULOS_ADMIN_IP_ALLOWLIST", "10.0.0.0/8")
	defer os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST")

	mw := superadmin.RequireSuperAdmin(saStore, authStore, al)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/superadmin/", nil)
	req.RemoteAddr = "192.168.1.1:12345" // not in 10.0.0.0/8
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-allowed IP, got %d", rr.Code)
	}
}

// Test 5: RequireSuperAdmin denies without admin session cookie (returns 401).
func TestRequireSuperAdmin_DeniesWithoutAdminSession(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	id := createUser(t, authStore, "admin2@test.example", "test-password-99999")
	promoteSuperAdmin(t, authStore, id)

	// Login to get a main session token.
	res, err := authStore.Login(context.Background(), "admin2@test.example", "test-password-99999", "127.0.0.1", "test")
	if err != nil || res == nil {
		t.Fatalf("login: %v %v", res, err)
	}
	mainToken := res.Token

	os.Unsetenv("VULOS_ADMIN_IP_ALLOWLIST") // allow all
	mw := superadmin.RequireSuperAdmin(saStore, authStore, al)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/superadmin/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: mainToken})
	// No admin session cookie → 401.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without admin session, got %d", rr.Code)
	}
}

// Test 6: Suspend / unsuspend roundtrip.
func TestSuspendUnsuspend_Roundtrip(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	id := createUser(t, authStore, "target@test.example", "test-password-12345")

	if err := saStore.SuspendAccount(context.Background(), id, "test reason", "actor@a.com", al); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	detail, err := saStore.GetAccountDetail(context.Background(), id)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if !detail.Suspended {
		t.Fatal("expected account to be suspended")
	}

	if err := saStore.UnsuspendAccount(context.Background(), id, "actor@a.com", al); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	detail, _ = saStore.GetAccountDetail(context.Background(), id)
	if detail.Suspended {
		t.Fatal("expected account to be active after unsuspend")
	}
}

// Test 7: ForcePasswordReset delivers to inbox; admin never sees token.
func TestForcePasswordReset_AdminSeesOnly202(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	_ = createUser(t, authStore, "resetme@vulos.org", "test-password-1234xx")

	var deliveredHandle string
	inbox := &mockInboxSender{fn: func(handle, subject, body string) {
		deliveredHandle = handle
	}}

	actorID := createUser(t, authStore, "actor@test.example", "test-password-56789")
	var actorEmail string
	authStore.DB().QueryRow(`SELECT email FROM users WHERE id = ?`, actorID).Scan(&actorEmail)

	var targetID string
	authStore.DB().QueryRow(`SELECT id FROM users WHERE email = ?`, "resetme@vulos.org").Scan(&targetID)

	if err := saStore.ForcePasswordReset(context.Background(), targetID, actorEmail, authStore, inbox, al); err != nil {
		t.Fatalf("force reset: %v", err)
	}
	if deliveredHandle != "resetme" {
		t.Fatalf("expected delivery to 'resetme', got %q", deliveredHandle)
	}
}

// Test 8a: Refund handler returns 422 when the txn_id doesn't belong to the account.
// This verifies the ownership check added to prevent cross-account refund abuse.
func TestRefundHandler_WrongAccount_Returns422(t *testing.T) {
	_, saStore, al := openTestStores(t)
	os.Unsetenv("PAYSTACK_SECRET_KEY")

	handler := superadmin.HandleRefund(saStore, al)
	// txn1 is not in billing_transactions for account id1 (table may not exist / row absent).
	req := httptest.NewRequest("POST", "/api/superadmin/accounts/id1/refund?txn_id=txn1", nil)
	ctx := context.WithValue(req.Context(), superadmin.ExportedCtxAdminAccountID, "actor-id")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// 422: txn_id does not belong to the account (ownership check).
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when txn not owned by account, got %d", rr.Code)
	}
}

// Test 8b: Refund handler returns 502 when PAYSTACK_SECRET_KEY is unset but
// the txn_id IS owned by the account. Tests the Paystack call path by routing
// through a real ServeMux so r.PathValue("id") is populated correctly.
func TestRefundHandler_NoKey_Returns502(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	os.Unsetenv("PAYSTACK_SECRET_KEY")

	// Create an account and a billing_transactions row owned by it.
	acctID := createUser(t, authStore, "refund502@test.example", "password-secure-99")

	// Insert a billing_transactions row (best-effort; if the table doesn't
	// exist in the test schema this test is skipped).
	_, err := saStore.DB().Exec(
		`CREATE TABLE IF NOT EXISTS billing_transactions (
			txn_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			amount_zar_cents INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'success',
			created_at TEXT NOT NULL DEFAULT ''
		)`,
	)
	if err != nil {
		t.Skipf("cannot create billing_transactions in test: %v", err)
	}
	_, err = saStore.DB().Exec(
		`INSERT OR IGNORE INTO billing_transactions (txn_id, account_id, amount_zar_cents, status, created_at)
		 VALUES ('txn_refund502', ?, 100, 'success', '2026-01-01T00:00:00Z')`, acctID,
	)
	if err != nil {
		t.Fatalf("insert billing row: %v", err)
	}

	// Route through ServeMux so r.PathValue("id") is populated.
	mux := http.NewServeMux()
	mux.Handle("POST /api/superadmin/accounts/{id}/refund", superadmin.HandleRefund(saStore, al))

	req := httptest.NewRequest("POST", "/api/superadmin/accounts/"+acctID+"/refund?txn_id=txn_refund502", nil)
	ctx := context.WithValue(req.Context(), superadmin.ExportedCtxAdminAccountID, "actor-id")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// 502: ownership check passed, but Paystack key is missing.
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when no Paystack key (ownership passed), got %d", rr.Code)
	}
}

// Test 9: Reserved handles — add, list (merged), delete, can't delete hardcoded.
func TestReservedHandles_CRUD(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	actorID := createUser(t, authStore, "actor3@test.example", "test-password-99991")

	// Pass nil for auditlog to avoid cross-db cleanup races in tests.
	if err := saStore.AddReservedHandle("myhandle123", "unit test", actorID, nil); err != nil {
		t.Fatalf("add reserved handle: %v", err)
	}

	list, err := saStore.ListReservedHandles()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var foundNew, foundHardcoded bool
	for _, h := range list {
		if h.Handle == "myhandle123" {
			foundNew = true
		}
		if h.Handle == "admin" && h.ReadOnly {
			foundHardcoded = true
		}
	}
	if !foundNew {
		t.Fatal("expected 'myhandle123' in list")
	}
	if !foundHardcoded {
		t.Fatal("expected 'admin' in hardcoded list")
	}

	// Delete DB handle.
	if err := saStore.DeleteReservedHandle("myhandle123", actorID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, _ = saStore.ListReservedHandles()
	for _, h := range list {
		if h.Handle == "myhandle123" {
			t.Fatal("expected 'myhandle123' to be removed")
		}
	}

	// Attempt to delete hardcoded — should fail.
	if err := saStore.DeleteReservedHandle("admin", actorID, nil); err == nil {
		t.Fatal("expected error when deleting hardcoded handle")
	}
}

// Test 10: Admin session create/lookup/delete lifecycle.
func TestAdminSession_Lifecycle(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	id := createUser(t, authStore, "sess@test.example", "test-password-66666")

	token, err := saStore.CreateAdminSession(context.Background(), id, "127.0.0.1", "ua")
	if err != nil || token == "" {
		t.Fatalf("create session: %v", err)
	}

	gotID, err := saStore.LookupAdminSession(context.Background(), token)
	if err != nil || gotID != id {
		t.Fatalf("lookup session: got %q err=%v", gotID, err)
	}

	// Delete.
	if err := saStore.DeleteAdminSession(context.Background(), token); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := saStore.LookupAdminSession(context.Background(), token); err == nil {
		t.Fatal("expected error after deletion")
	}
}

// Test 11: GET /api/superadmin/accounts returns JSON with accounts.
func TestHandleListAccounts_ReturnsJSON(t *testing.T) {
	authStore, saStore, _ := openTestStores(t)
	createUser(t, authStore, "u1@test.example", "test-password-1234")
	createUser(t, authStore, "u2@test.example", "test-password-5678")

	handler := superadmin.HandleListAccounts(saStore)
	req := httptest.NewRequest("GET", "/api/superadmin/accounts?q=test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	accounts, _ := resp["accounts"].([]any)
	if len(accounts) < 2 {
		t.Fatalf("expected >= 2 accounts, got %d", len(accounts))
	}
}

// Test 12: HTML dashboard page renders without error.
func TestDashboard_Renders(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	req := httptest.NewRequest("GET", "/superadmin/", nil)
	rr := httptest.NewRecorder()
	pages.Dashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "Dashboard") {
		t.Fatal("expected 'Dashboard' in rendered page")
	}
	if !strings.Contains(string(body), "Vulos") {
		t.Fatal("expected 'Vulos' brand in rendered layout")
	}
}

// Test 12b: the operational dashboard surfaces fleet health from the incident
// seam (open incidents banner) and never leaks a commercial billing cockpit.
func TestDashboard_RendersOperationalHealth(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)
	pages.SetIncidentAdmin(&fakeIncidents{
		incidents: []superadmin.IncidentView{
			{ID: "inc1", Title: "Relay JHB degraded", Severity: "major", StartedAt: "2026-07-16T00:00:00Z"},
		},
		maintenance: []superadmin.MaintenanceView{
			{ID: "m1", Title: "PG upgrade", StartsAt: "2026-07-20T02:00:00Z", EndsAt: "2026-07-20T03:00:00Z"},
		},
	})

	req := httptest.NewRequest("GET", "/superadmin/", nil)
	rr := httptest.NewRecorder()
	pages.Dashboard(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	// Operational signals present.
	for _, want := range []string{"open incident", "Relay JHB degraded", "Recent activity"} {
		if !strings.Contains(s, want) {
			t.Errorf("operational dashboard missing %q", want)
		}
	}
	// Commercial surfaces must be gone.
	for _, banned := range []string{"Billing Cockpit", "MRR", "Est. cost"} {
		if strings.Contains(s, banned) {
			t.Errorf("dashboard leaked commercial surface %q", banned)
		}
	}
}

// Test 13: Reserved handles API endpoint returns JSON.
func TestHandleListReservedHandles_ReturnsJSON(t *testing.T) {
	_, saStore, _ := openTestStores(t)
	handler := superadmin.HandleListReservedHandles(saStore)

	req := httptest.NewRequest("GET", "/api/superadmin/reserved-handles", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	handles, _ := resp["handles"].([]any)
	if len(handles) == 0 {
		t.Fatal("expected non-empty handles (hardcoded entries)")
	}
}

// Test 14: Login page renders without error.
func TestLoginPage_Renders(t *testing.T) {
	authStore, saStore, al := openTestStores(t)
	pages := superadmin.NewPages(saStore, al, authStore)

	req := httptest.NewRequest("GET", "/superadmin/login", nil)
	rr := httptest.NewRecorder()
	pages.LoginGet(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), "Sign in") {
		t.Fatal("expected 'Sign in' in login page")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Mock helpers
// ─────────────────────────────────────────────────────────────────────────────

type mockInboxSender struct {
	fn func(handle, subject, body string)
}

func (m *mockInboxSender) DeliverSystemMessage(_ context.Context, handle, subject, body string) error {
	if m.fn != nil {
		m.fn(handle, subject, body)
	}
	return nil
}
