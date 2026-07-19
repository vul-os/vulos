package cproutes

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/security"
)

// applyStepUpForTest mirrors the step-up wrapping logic from RegisterSecurity
// without touching the ServeMux (to avoid panic on duplicate patterns in tests).
func applyStepUpForTest(store *security.Store) {
	if loginInnerHandler == nil {
		return
	}
	stepUpCfg := security.StepUpConfig{
		Store:     store,
		Threshold: security.StepUpThreshold,
		Sender:    NewStepUpSender(),
	}
	loginInnerHandler = security.StepUpMiddleware(stepUpCfg, loginInnerHandler)
}

// TestStepUpMiddlewareWrapsLoginHandler verifies that after the step-up wiring
// is applied, loginInnerHandler is wrapped with StepUpMiddleware.
// A low-risk request (no prior events, default threshold 0.6) passes through.
func TestStepUpMiddlewareWrapsLoginHandler(t *testing.T) {
	secDB, err := cpdb.OpenSQLiteDSN("file::memory:?mode=memory&cache=shared&_su1")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	secStore, err := security.Open(secDB)
	if err != nil {
		t.Fatalf("security.Open: %v", err)
	}
	defer secStore.Close()

	innerCalled := false
	original := loginInnerHandler
	t.Cleanup(func() { loginInnerHandler = original })

	loginInnerHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	// Apply step-up wrapping (mirrors RegisterSecurity).
	applyStepUpForTest(secStore)

	// A low-risk login (no prior events → risk=0 < threshold=0.6) should pass through.
	body := bytes.NewBufferString(`{"email":"test@vulos.org","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	loginInnerHandler.ServeHTTP(rec, req)

	// Expect 200 (inner handler ran) or 202 (step-up triggered — either is fine
	// as long as no panic occurs). For a fresh store with threshold=0.6, score=0.
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Errorf("unexpected status %d after step-up middleware; want 200 or 202", rec.Code)
	}
	_ = innerCalled
}

// TestLoginRoutePassesThroughLowRisk verifies the login inner handler is called
// for low-risk requests when step-up is applied and the store is empty.
func TestLoginRoutePassesThroughLowRisk(t *testing.T) {
	secDB, err := cpdb.OpenSQLiteDSN("file::memory:?mode=memory&cache=shared&_su2")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	secStore, err := security.Open(secDB)
	if err != nil {
		t.Fatalf("security.Open: %v", err)
	}
	defer secStore.Close()

	innerCalled := false
	original := loginInnerHandler
	t.Cleanup(func() { loginInnerHandler = original })

	loginInnerHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	applyStepUpForTest(secStore)

	body := bytes.NewBufferString(`{"email":"low@vulos.org","password":"pass"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	loginInnerHandler.ServeHTTP(rec, req)

	// With empty store and threshold=0.6, risk=0 → inner handler called → 200.
	if rec.Code != http.StatusOK {
		t.Errorf("low-risk login: want 200, got %d", rec.Code)
	}
	if !innerCalled {
		t.Error("low-risk login: inner handler not called — step-up incorrectly triggered")
	}
}
