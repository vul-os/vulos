// routes_opaque_test.go -- HTTP-level tests for the flag-gated OPAQUE endpoints.
package cproutes

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/bytemare/opaque"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// opaqueMux builds a mux with auth + OPAQUE routes registered.
func opaqueMux(t *testing.T, st *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	registerOpaqueRoutes(mux, st)
	return mux
}

// TestOpaqueRoutesDisabledReturn404 verifies the endpoints are invisible (404)
// when the feature flag is off — the live path stays argon2 and no OPAQUE surface
// is exposed by default.
func TestOpaqueRoutesDisabledReturn404(t *testing.T) {
	t.Setenv("AUTH_OPAQUE_ENABLED", "")
	st := openE2EStore(t)
	mux := opaqueMux(t, st)

	rr := e2ePost(mux, "/api/auth/opaque/login/start", `{"email":"x@example.com","ke1":"AA=="}`, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled OPAQUE login/start: want 404, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestOpaqueRoutesEndToEnd drives the full OPAQUE register + login through the
// HTTP surface: signup (argon2, gets a session) → register/response →
// register/store → login/start → login/finish → full session cookie.
func TestOpaqueRoutesEndToEnd(t *testing.T) {
	t.Setenv("AUTH_OPAQUE_ENABLED", "1")
	t.Setenv("VULOS_DOMAIN", "vulos.to")
	keyHex, err := auth.GenerateOpaqueServerKeyHex()
	if err != nil {
		t.Fatalf("GenerateOpaqueServerKeyHex: %v", err)
	}
	t.Setenv("OPAQUE_SERVER_KEY", keyHex)

	st := openE2EStore(t)
	mux := opaqueMux(t, st)

	// Signup with the normal argon2 path to create the account + a session.
	rr := e2ePost(mux, "/api/auth/signup", `{"handle":"opaque_http","password":"strongpassword1234!"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	body := jsonBody(t, rr)
	userID, _ := body["user_id"].(string)
	sessionTok := cookieValue(rr, auth.SessionCookieName)
	if userID == "" || sessionTok == "" {
		t.Fatalf("signup missing user_id/session: %v", body)
	}

	const opaquePW = "opaque-only-password-9876"
	conf := opaque.DefaultConfiguration()
	client, _ := conf.Client()
	deser, _ := conf.Deserializer()

	// register/response
	req, _ := client.RegistrationInit([]byte(opaquePW))
	rr = e2ePost(mux, "/api/auth/opaque/register/response",
		`{"request":"`+base64.StdEncoding.EncodeToString(req.Serialize())+`"}`, sessionTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("register/response: %d %s", rr.Code, rr.Body.String())
	}
	respB64, _ := jsonBody(t, rr)["response"].(string)
	respBytes, _ := base64.StdEncoding.DecodeString(respB64)
	resp, err := deser.RegistrationResponse(respBytes)
	if err != nil {
		t.Fatalf("deser response: %v", err)
	}
	record, _, err := client.RegistrationFinalize(resp, []byte(userID), []byte("idp.vulos.to"))
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// register/store
	rr = e2ePost(mux, "/api/auth/opaque/register/store",
		`{"record":"`+base64.StdEncoding.EncodeToString(record.Serialize())+`"}`, sessionTok)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("register/store: %d %s", rr.Code, rr.Body.String())
	}

	// login/start — fresh client instance (a client carries per-ceremony OPRF
	// state, so registration and login must not share one).
	loginClient, _ := conf.Client()
	ke1, err := loginClient.GenerateKE1([]byte(opaquePW))
	if err != nil {
		t.Fatalf("GenerateKE1: %v", err)
	}
	rr = e2ePost(mux, "/api/auth/opaque/login/start",
		`{"email":"opaque_http@vulos.to","ke1":"`+base64.StdEncoding.EncodeToString(ke1.Serialize())+`"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("login/start: %d %s", rr.Code, rr.Body.String())
	}
	startBody := jsonBody(t, rr)
	handshakeID, _ := startBody["handshake_id"].(string)
	ke2B64, _ := startBody["ke2"].(string)
	ke2Bytes, _ := base64.StdEncoding.DecodeString(ke2B64)
	ke2, err := deser.KE2(ke2Bytes)
	if err != nil {
		t.Fatalf("deser ke2: %v", err)
	}
	ke3, _, _, err := loginClient.GenerateKE3(ke2, []byte(userID), []byte("idp.vulos.to"))
	if err != nil {
		t.Fatalf("GenerateKE3: %v", err)
	}

	// login/finish → full session (unverified logins allowed in test env).
	rr = e2ePost(mux, "/api/auth/opaque/login/finish",
		`{"handshake_id":"`+handshakeID+`","ke3":"`+base64.StdEncoding.EncodeToString(ke3.Serialize())+`"}`, "")
	if rr.Code != http.StatusOK && rr.Code != http.StatusNoContent {
		t.Fatalf("login/finish: %d %s", rr.Code, rr.Body.String())
	}
	if cookieValue(rr, auth.SessionCookieName) == "" {
		t.Fatalf("login/finish: expected session cookie; got none (body=%s)", rr.Body.String())
	}
}
