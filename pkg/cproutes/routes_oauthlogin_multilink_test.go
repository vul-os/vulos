package cproutes

// routes_oauthlogin_multilink_test.go — coverage for the Phase-B social-login
// additions: multi-provider connect/disconnect (identities list + unlink) and the
// mandatory-email completion flow when a provider returns no email.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/oauthclient"
)

func deleteWithSession(mux *http.ServeMux, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	req.RemoteAddr = "127.0.0.1:9000"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestOAuthIdentities_ListAndUnlink proves multiple providers link to one account
// and can be listed + disconnected via the account-settings surface.
func TestOAuthIdentities_ListAndUnlink(t *testing.T) {
	oauthLoginSecureCookie = false
	ctx := context.Background()
	st := openSessionTestStore(t)
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterOAuthLoginRoutes(mux, st, oauthclient.NewRegistryFromEnv())

	uid, cookie := signupAndSession(t, st, "linky@vulos.org", oauthTestPassword)
	if err := st.LinkOAuthIdentity(ctx, "google", "g-sub", uid, "linky@vulos.org", true); err != nil {
		t.Fatalf("link google: %v", err)
	}
	if err := st.LinkOAuthIdentity(ctx, "github", "42", uid, "linky@vulos.org", true); err != nil {
		t.Fatalf("link github: %v", err)
	}

	// GET identities → both providers present.
	rr := getWithCookie(mux, "/api/auth/oauth/identities", cookie.Value)
	if rr.Code != http.StatusOK {
		t.Fatalf("list identities: got %d %s", rr.Code, rr.Body.String())
	}
	var listResp struct {
		Identities []auth.LinkedIdentity `json:"identities"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp.Identities) != 2 {
		t.Fatalf("identities = %d, want 2", len(listResp.Identities))
	}

	// DELETE github → 200, then list shows only google.
	if rr := deleteWithSession(mux, "/api/auth/oauth/identities/github", cookie.Value); rr.Code != http.StatusOK {
		t.Fatalf("unlink github: got %d %s", rr.Code, rr.Body.String())
	}
	rr = getWithCookie(mux, "/api/auth/oauth/identities", cookie.Value)
	listResp.Identities = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	if len(listResp.Identities) != 1 || listResp.Identities[0].Provider != "google" {
		t.Fatalf("after unlink, identities = %+v, want [google]", listResp.Identities)
	}

	// DELETE github again → 404 (idempotent-safe, reports nothing to remove).
	if rr := deleteWithSession(mux, "/api/auth/oauth/identities/github", cookie.Value); rr.Code != http.StatusNotFound {
		t.Errorf("re-unlink github: got %d, want 404", rr.Code)
	}
}

func TestOAuthIdentities_RequiresSession(t *testing.T) {
	st := openSessionTestStore(t)
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterOAuthLoginRoutes(mux, st, oauthclient.NewRegistryFromEnv())
	if rr := getWithCookie(mux, "/api/auth/oauth/identities", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("unauth list identities: got %d, want 401", rr.Code)
	}
}

// noEmailIDToken is a valid OIDC id_token that deliberately omits the email claim.
func noEmailIDToken(t *testing.T, sub string) string {
	return mkIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": sub, "aud": "cid",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
}

// TestOAuthCompleteEmail_ForcesEmailThenCreatesAccount proves the mandatory-email
// rule: a provider that returns no email is blocked at the callback (redirected to
// the email-entry page) and the account is only created once the user types an
// email at /api/auth/oauth/complete-email.
func TestOAuthCompleteEmail_ForcesEmailThenCreatesAccount(t *testing.T) {
	mux, st := setupOAuthMux(t, noEmailIDToken(t, "noemail-sub-1"))

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code-1", state, flow)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: got %d %s", rr.Code, rr.Body.String())
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	if loc.Path != consolePath(oauthEmailPath) {
		t.Fatalf("callback location = %q, want %q", loc.Path, consolePath(oauthEmailPath))
	}
	token := loc.Query().Get("token")
	if token == "" {
		t.Fatal("email-entry redirect carried no token")
	}
	// No account exists yet (nothing was created before the email was supplied).
	if _, err := st.UserIDByEmail(context.Background(), "typed@example.com"); err != auth.ErrNotFound {
		t.Fatalf("account existed before complete-email: err=%v", err)
	}

	// Complete with a typed email → step:set_password + a session cookie.
	body := `{"email_token":"` + token + `","email":"typed@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/oauth/complete-email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	cr := httptest.NewRecorder()
	mux.ServeHTTP(cr, req)
	if cr.Code != http.StatusOK {
		t.Fatalf("complete-email: got %d %s", cr.Code, cr.Body.String())
	}
	var step struct {
		Step string `json:"step"`
	}
	_ = json.Unmarshal(cr.Body.Bytes(), &step)
	if step.Step != "set_password" {
		t.Fatalf("complete-email step = %q, want set_password", step.Step)
	}
	if sessionCookie(cr) == "" {
		t.Fatal("complete-email issued no session for the set-password step")
	}
	// The account now exists with the typed email and is linked to the provider sub.
	uid, err := st.UserIDByEmail(context.Background(), "typed@example.com")
	if err != nil {
		t.Fatalf("account not created after complete-email: %v", err)
	}
	if linked, err := st.FindOAuthIdentity(context.Background(), "test", "noemail-sub-1"); err != nil || linked != uid {
		t.Fatalf("provider identity not linked to new account: linked=%q err=%v", linked, err)
	}
}

func TestOAuthCompleteEmail_RejectsInvalidEmail(t *testing.T) {
	mux, _ := setupOAuthMux(t, noEmailIDToken(t, "noemail-sub-2"))
	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code-2", state, flow)
	loc, _ := url.Parse(rr.Header().Get("Location"))
	token := loc.Query().Get("token")

	body := `{"email_token":"` + token + `","email":"not-an-email"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/oauth/complete-email", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	cr := httptest.NewRecorder()
	mux.ServeHTTP(cr, req)
	if cr.Code != http.StatusBadRequest {
		t.Errorf("complete-email with bad email: got %d, want 400", cr.Code)
	}
}
