// routes_integrations_devicekey_test.go — INTEG-SEC-01 per-device key binding.
package cproutes

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/integrations"
)

func genDeviceKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	return priv, der
}

func deviceSignB64(t *testing.T, priv *ecdsa.PrivateKey, msg string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func fleetHMAC(secret, msg string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// registerDevice POSTs a device-key registration with a custom pubkey + self-sig.
func (e *integEnv) registerDevice(t *testing.T, selfPriv *ecdsa.PrivateKey, pubDER []byte, ulid string) *httptest.ResponseRecorder {
	t.Helper()
	regMsg := integrations.RegisterSigMessage(ulid)
	body, _ := json.Marshal(map[string]string{
		"ulid":       ulid,
		"pubkey_der": base64.StdEncoding.EncodeToString(pubDER),
		"algo":       integrations.AlgoECDSAP256,
		"self_sig":   deviceSignB64(t, selfPriv, regMsg),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/device/register", bytes.NewReader(body))
	r.Header.Set("X-Integration-Sig", fleetHMAC("device-shared-secret", regMsg))
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, r)
	return rr
}

// ensureDeviceKey lazily enrolls (once per env) a per-device key for the env's
// canonical test ULID and caches it, so mint helpers can authenticate with a
// real X-Device-Sig. Tests that exercise registration directly do NOT call this,
// so their own register/conflict flows are unaffected.
func (e *integEnv) ensureDeviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	if e.devicePriv != nil {
		return e.devicePriv
	}
	priv, der := genDeviceKey(t)
	if rr := e.registerDevice(t, priv, der, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("ensureDeviceKey register: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	e.devicePriv = priv
	return priv
}

func (e *integEnv) mintWithDeviceSig(t *testing.T, priv *ecdsa.PrivateKey, ulid string) *httptest.ResponseRecorder {
	t.Helper()
	msg := "integrations:token:google:" + ulid
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", ulid)
	r.Header.Set("X-Device-Sig", deviceSignB64(t, priv, msg))
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, r)
	return rr
}

func TestDeviceKeyRegisterAndMint(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	priv, der := genDeviceKey(t)
	if rr := env.registerDevice(t, priv, der, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Pinned device with a valid per-device signature → 200.
	if rr := env.mintWithDeviceSig(t, priv, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("mint with device sig: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMintPinnedRejectsFleetHMACOnly(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	priv, der := genDeviceKey(t)
	if rr := env.registerDevice(t, priv, der, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register: want 200, got %d", rr.Code)
	}

	// Once pinned, the OLD fleet-HMAC-only path must NOT work (this is the fix):
	// a caller with only DEVICE_SHARED_SECRET + the ULID cannot mint.
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", integTestULID)
	r.Header.Set("X-Integration-Sig", integSig("device-shared-secret", integTestULID))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("fleet-HMAC-only mint on pinned device: want 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// And a WRONG device key (attacker holding the fleet secret) is rejected.
	attacker, _ := genDeviceKey(t)
	if rr := env.mintWithDeviceSig(t, attacker, integTestULID); rr.Code != http.StatusUnauthorized {
		t.Fatalf("attacker device sig: want 401, got %d", rr.Code)
	}
}

func TestDeviceKeyRegisterConflict(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})

	priv1, der1 := genDeviceKey(t)
	if rr := env.registerDevice(t, priv1, der1, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("first register: want 200, got %d", rr.Code)
	}
	// Idempotent re-register with the SAME key → 200.
	if rr := env.registerDevice(t, priv1, der1, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("idempotent re-register: want 200, got %d", rr.Code)
	}
	// A DIFFERENT key for the same ULID → 409 (tamper signal).
	priv2, der2 := genDeviceKey(t)
	if rr := env.registerDevice(t, priv2, der2, integTestULID); rr.Code != http.StatusConflict {
		t.Fatalf("conflicting re-register: want 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeviceKeyRegisterBadSelfSig(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})

	// Register pubkey of key A but self-sign with key B → possession not proven.
	_, derA := genDeviceKey(t)
	privB, _ := genDeviceKey(t)
	if rr := env.registerDevice(t, privB, derA, integTestULID); rr.Code != http.StatusBadRequest {
		t.Fatalf("mismatched self-sig: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeDeviceKeyAllowsRePin(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	a, derA := genDeviceKey(t)
	if rr := env.registerDevice(t, a, derA, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register A: %d", rr.Code)
	}
	// Owner revokes the pinned key (session-authed).
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodDelete, "/api/integrations/device/"+integTestULID+"/key", true))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d: %s", rr.Code, rr.Body.String())
	}
	// A re-keyed box can now register a DIFFERENT key (no 409) and mint — this is
	// the fix for the re-key lockout.
	b, derB := genDeviceKey(t)
	if rr := env.registerDevice(t, b, derB, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("re-register B after revoke: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := env.mintWithDeviceSig(t, b, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("mint with rotated key: want 200, got %d", rr.Code)
	}
}

func TestRevokeDeviceKeyRequiresOwnership(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	const notOwned = "01BX5ZZKBKACTAV9WEVGEMMVRZ" // valid ULID, not owned by the session account
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodDelete, "/api/integrations/device/"+notOwned+"/key", true))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("revoke not-owned ULID: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRevokeDeviceKeyRequiresSession(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodDelete, "/api/integrations/device/"+integTestULID+"/key", false))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoke without session: want 401, got %d", rr.Code)
	}
}

func TestDeviceKeyStatus(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	var s struct {
		Pinned bool `json:"pinned"`
	}

	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/device/"+integTestULID+"/key", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("status (unpinned): want 200, got %d", rr.Code)
	}
	json.Unmarshal(rr.Body.Bytes(), &s)
	if s.Pinned {
		t.Fatal("want pinned=false before registration")
	}

	priv, der := genDeviceKey(t)
	if rr := env.registerDevice(t, priv, der, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/device/"+integTestULID+"/key", true))
	json.Unmarshal(rr.Body.Bytes(), &s)
	if !s.Pinned {
		t.Fatal("want pinned=true after registration")
	}
}

func TestRegisterRejectsUnknownAlgo(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	priv, der := genDeviceKey(t)
	regMsg := integrations.RegisterSigMessage(integTestULID)
	body, _ := json.Marshal(map[string]string{
		"ulid":       integTestULID,
		"pubkey_der": base64.StdEncoding.EncodeToString(der),
		"algo":       "ed25519", // unsupported
		"self_sig":   deviceSignB64(t, priv, regMsg),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/device/register", bytes.NewReader(body))
	r.Header.Set("X-Integration-Sig", fleetHMAC("device-shared-secret", regMsg))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown algo: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestDeviceKeyRegisterBadFleetHMAC(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	priv, der := genDeviceKey(t)
	regMsg := integrations.RegisterSigMessage(integTestULID)
	body, _ := json.Marshal(map[string]string{
		"ulid":       integTestULID,
		"pubkey_der": base64.StdEncoding.EncodeToString(der),
		"algo":       integrations.AlgoECDSAP256,
		"self_sig":   deviceSignB64(t, priv, regMsg),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/device/register", bytes.NewReader(body))
	r.Header.Set("X-Integration-Sig", fleetHMAC("wrong-secret", regMsg)) // not a fleet box
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad fleet HMAC: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMintFleetRequireDeviceKey(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	t.Setenv("FLEET_REQUIRE_DEVICE_KEY", "1") // explicit "require" (also the default)

	// No key pinned + enforcement on → fleet-HMAC fallback is refused.
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", integTestULID)
	r.Header.Set("X-Integration-Sig", integSig("device-shared-secret", integTestULID))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unpinned mint under FLEET_REQUIRE_DEVICE_KEY: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestFleetRequireDeviceKeyDefault verifies that when FLEET_REQUIRE_DEVICE_KEY
// is unset (the production default), the fleet-HMAC bootstrap fallback is
// refused for unpinned devices — protecting against cross-tenant token mint.
//
// This test overrides the "0" set by newIntegEnv (which enables bootstrap mode
// so the other fleet-HMAC tests can run), then checks that removing the opt-out
// restores secure behaviour.
func TestFleetRequireDeviceKeyDefault(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	// Remove the bootstrap opt-out set by newIntegEnv; simulate unset (default).
	t.Setenv("FLEET_REQUIRE_DEVICE_KEY", "")

	// Fleet-HMAC only (no device key registered) → must be refused.
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", integTestULID)
	r.Header.Set("X-Integration-Sig", integSig("device-shared-secret", integTestULID))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("default (unset) FLEET_REQUIRE_DEVICE_KEY should refuse fleet-HMAC-only mint: want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestFleetRequireDeviceKeyBootstrapOptIn (reworked): the fleet-HMAC mint
// fallback is GONE. The enrollment bootstrap is now register-device-key-THEN-mint
// over a PER-DEVICE signature. This verifies the proper flow works, and that the
// (now-dead) FLEET_REQUIRE_DEVICE_KEY=0 opt-out no longer re-enables a
// shared-secret mint.
func TestFleetRequireDeviceKeyBootstrapOptIn(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	// The opt-out is dead: setting it must NOT resurrect the shared-secret mint.
	t.Setenv("FLEET_REQUIRE_DEVICE_KEY", "0")

	// Fleet-HMAC only (no per-device key) is refused regardless of the env var.
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", integTestULID)
	r.Header.Set("X-Integration-Sig", integSig("device-shared-secret", integTestULID))
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("fleet-HMAC-only mint must be refused even with FLEET_REQUIRE_DEVICE_KEY=0: want 401, got %d: %s", rr.Code, rr.Body.String())
	}

	// The correct bootstrap: register a per-device key (fleet-HMAC enrollment +
	// possession self-sig), then mint with that device's signature → 200.
	priv, der := genDeviceKey(t)
	if rr := env.registerDevice(t, priv, der, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("enroll device key: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := env.mintWithDeviceSig(t, priv, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register-then-mint: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
