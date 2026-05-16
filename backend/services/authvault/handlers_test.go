package authvault

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// newTestHandler builds a Handler backed by a temp-dir store.
// It patches storeFor so all calls for "user1" use the temp dir.
func newTestHandler(t *testing.T) (*Handler, *Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	h := NewHandler()
	h.stores["user1"] = store
	return h, store
}

func TestHandleAdd_URI(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	body := `{"uri":"otpauth://totp/TestService:user@example.com?secret=JBSWY3DPEHPK3PXP&issuer=TestService"}`
	req := httptest.NewRequest("POST", "/api/auth/totp/add", bytes.NewBufferString(body))
	req.Header.Set("X-User-ID", "user1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var acc Account
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if acc.ID == "" {
		t.Error("expected non-empty account ID")
	}
	if acc.Service == "" {
		t.Error("expected non-empty service")
	}
}

func TestHandleAdd_Manual(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	body := `{"service":"GitHub","issuer":"GitHub","account":"user@example.com","secret":"JBSWY3DPEHPK3PXP"}`
	req := httptest.NewRequest("POST", "/api/auth/totp/add", bytes.NewBufferString(body))
	req.Header.Set("X-User-ID", "user1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var acc Account
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if acc.Service != "GitHub" {
		t.Errorf("expected service GitHub, got %q", acc.Service)
	}
}

func TestHandleAdd_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("POST", "/api/auth/totp/add", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleList(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	// Add an entry directly to the store.
	if _, err := store.AddManual("TestSvc", "TestIssuer", "user@example.com", "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/totp/list", nil)
	req.Header.Set("X-User-ID", "user1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var accounts []*Account
	if err := json.Unmarshal(rec.Body.Bytes(), &accounts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
}

func TestHandleList_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/auth/totp/list", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleCode_ValidSixDigit(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	acc, err := store.AddManual("TestSvc", "TestIssuer", "user@example.com", "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/totp/code/"+acc.ID, nil)
	req.Header.Set("X-User-ID", "user1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	code, ok := resp["code"].(string)
	if !ok || code == "" {
		t.Fatalf("expected code string in response, got %v", resp)
	}
	// Must be exactly 6 digits.
	matched, _ := regexp.MatchString(`^\d{6}$`, code)
	if !matched {
		t.Errorf("expected 6-digit code, got %q", code)
	}

	remaining, ok := resp["remaining_seconds"].(float64)
	if !ok || remaining <= 0 || remaining > 30 {
		t.Errorf("expected remaining_seconds in (0,30], got %v", resp["remaining_seconds"])
	}
}

func TestHandleCode_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/auth/totp/code/someID", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleCode_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/auth/totp/code/doesnotexist", nil)
	req.Header.Set("X-User-ID", "user1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDelete(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	acc, err := store.AddManual("TestSvc", "TestIssuer", "user@example.com", "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/auth/totp/"+acc.ID, nil)
	req.Header.Set("X-User-ID", "user1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Confirm it's gone.
	if len(store.List()) != 0 {
		t.Error("account still present after delete")
	}
}

func TestHandleDelete_RequiresAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("DELETE", "/api/auth/totp/someID", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHandleDelete_NotFound(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	req := httptest.NewRequest("DELETE", "/api/auth/totp/doesnotexist", nil)
	req.Header.Set("X-User-ID", "user1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// TestAddThenGetCode: add via HTTP body then GET code returns valid 6-digit code.
func TestAddThenGetCode(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler()
	// Override storeFor by pre-populating with an empty store for "user1"
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	h.stores["user1"] = store

	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	// Step 1: add
	addBody := `{"service":"BankXYZ","issuer":"BankXYZ","account":"alice@example.com","secret":"JBSWY3DPEHPK3PXP"}`
	addReq := httptest.NewRequest("POST", "/api/auth/totp/add", bytes.NewBufferString(addBody))
	addReq.Header.Set("X-User-ID", "user1")
	addReq.Header.Set("Content-Type", "application/json")
	addRec := httptest.NewRecorder()
	mux.ServeHTTP(addRec, addReq)

	if addRec.Code != http.StatusOK {
		t.Fatalf("add: expected 200, got %d: %s", addRec.Code, addRec.Body.String())
	}
	var acc Account
	if err := json.Unmarshal(addRec.Body.Bytes(), &acc); err != nil {
		t.Fatalf("add: unmarshal: %v", err)
	}

	// Step 2: get code
	codeReq := httptest.NewRequest("GET", "/api/auth/totp/code/"+acc.ID, nil)
	codeReq.Header.Set("X-User-ID", "user1")
	codeRec := httptest.NewRecorder()
	mux.ServeHTTP(codeRec, codeReq)

	if codeRec.Code != http.StatusOK {
		t.Fatalf("code: expected 200, got %d: %s", codeRec.Code, codeRec.Body.String())
	}
	var codeResp map[string]any
	if err := json.Unmarshal(codeRec.Body.Bytes(), &codeResp); err != nil {
		t.Fatalf("code: unmarshal: %v", err)
	}
	code, _ := codeResp["code"].(string)
	matched, _ := regexp.MatchString(`^\d{6}$`, code)
	if !matched {
		t.Errorf("expected 6-digit code, got %q", code)
	}
}

// TestUserIsolation verifies that two users cannot see each other's accounts.
func TestUserIsolation(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "user1")
	dir2 := filepath.Join(t.TempDir(), "user2")
	os.MkdirAll(dir1, 0700)
	os.MkdirAll(dir2, 0700)

	h := NewHandler()
	store1, _ := NewStoreAt(dir1)
	store2, _ := NewStoreAt(dir2)
	h.stores["user1"] = store1
	h.stores["user2"] = store2

	// Add an account for user1 only.
	store1.AddManual("BankA", "BankA", "alice@example.com", "JBSWY3DPEHPK3PXP")

	mux := http.NewServeMux()
	h.RegisterHandlers(mux)

	// user2 list should be empty.
	req := httptest.NewRequest("GET", "/api/auth/totp/list", nil)
	req.Header.Set("X-User-ID", "user2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var accounts []*Account
	json.Unmarshal(rec.Body.Bytes(), &accounts)
	if len(accounts) != 0 {
		t.Errorf("user2 should see 0 accounts, got %d", len(accounts))
	}
}
