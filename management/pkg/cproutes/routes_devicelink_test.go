// routes_devicelink_test.go — end-to-end device-link flow (start→approve→poll
// issues an account credential).
//
// NOTE: the relay-entitlement tests that previously shared this file's fixtures
// were billing-coupled (registerRelayEntitlementRoute over a commercial
// billing.Store) and stay cloud-side; the device-link routes themselves are
// operational and live here.
package cproutes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/devicelink"
)

func deviceLinkTestMux(t *testing.T) (*http.ServeMux, *auth.Store, devicelink.Store) {
	t.Helper()
	as := openTestAuthStore(t)
	links := devicelink.NewMemStore()
	mux := http.NewServeMux()
	RegisterDeviceLinkRoutes(mux, links, as, "/app/link")
	return mux, as, links
}

func postJSONReq(t *testing.T, mux *http.ServeMux, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:5555"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDeviceLink_StartApprovePoll_IssuesCredential(t *testing.T) {
	mux, as, _ := deviceLinkTestMux(t)
	userID, cookie := signupAndSession(t, as, "linker@example.com", "password-1234")

	// 1) start (unauthenticated install)
	rec := postJSONReq(t, mux, "/api/link/device/start", map[string]any{}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var start struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		Interval   int    `json:"interval"`
		ExpiresIn  int    `json:"expires_in"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	if start.DeviceCode == "" || start.UserCode == "" {
		t.Fatalf("empty codes: %+v", start)
	}

	// 2) poll before approval → 428 pending
	rec = postJSONReq(t, mux, "/api/link/device/poll", map[string]any{"device_code": start.DeviceCode}, nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("poll pending: want 428, got %d: %s", rec.Code, rec.Body)
	}

	// 3) approve (session-authed human)
	rec = postJSONReq(t, mux, "/api/link/device/approve", map[string]any{"user_code": start.UserCode}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: want 200, got %d: %s", rec.Code, rec.Body)
	}

	// 4) poll after approval → install credential bound to the account
	rec = postJSONReq(t, mux, "/api/link/device/poll", map[string]any{"device_code": start.DeviceCode}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll approved: want 200, got %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Credential string `json:"install_credential"`
		AccountID  string `json:"account_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.AccountID != userID {
		t.Fatalf("account mismatch: want %q got %q", userID, got.AccountID)
	}
	if got.Credential == "" {
		t.Fatal("empty install credential")
	}

	// 5) second poll → 410 Gone (credential already issued, no re-mint)
	rec = postJSONReq(t, mux, "/api/link/device/poll", map[string]any{"device_code": start.DeviceCode}, nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("second poll: want 410, got %d: %s", rec.Code, rec.Body)
	}
}

func TestDeviceLink_ApproveRequiresSession(t *testing.T) {
	mux, _, _ := deviceLinkTestMux(t)
	rec := postJSONReq(t, mux, "/api/link/device/start", map[string]any{}, nil)
	var start struct {
		UserCode string `json:"user_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &start)

	// Approve with no session → not 200 (auth gate rejects).
	rec = postJSONReq(t, mux, "/api/link/device/approve", map[string]any{"user_code": start.UserCode}, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("approve without session must not succeed, got %d", rec.Code)
	}
}
