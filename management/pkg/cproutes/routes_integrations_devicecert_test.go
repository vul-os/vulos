// routes_integrations_devicecert_test.go — INTEG-SEC-01 owner-attested cert mint.
package cproutes

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

// caSignCert reproduces the management-CA device-cert signature exactly as the
// production envCertSigner / enroll.MemCertSigner does, so this exercises the
// real wire format end-to-end.
func caSignCert(caPriv ed25519.PrivateKey, devicePub []byte, accountID, ulid string) []byte {
	payload := "vulos-device-cert:v1:" +
		base64.RawURLEncoding.EncodeToString(devicePub) + ":" + accountID + ":" + ulid
	return ed25519.Sign(caPriv, []byte(payload))
}

// mintWithCert issues a mint request authenticated by an owner-attested cert.
func (e *integEnv) mintWithCert(t *testing.T, devicePriv ed25519.PrivateKey, devicePub, certSig []byte, ulid string) *httptest.ResponseRecorder {
	t.Helper()
	mintSig := ed25519.Sign(devicePriv, []byte("integrations:token:google:"+ulid))
	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	r.Header.Set("X-Device-ULID", ulid)
	r.Header.Set("X-Device-Pubkey", base64.StdEncoding.EncodeToString(devicePub))
	r.Header.Set("X-Device-Cert", base64.StdEncoding.EncodeToString(certSig))
	r.Header.Set("X-Device-Sig", base64.StdEncoding.EncodeToString(mintSig))
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, r)
	return rr
}

func TestMintWithDeviceCert(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cert := caSignCert(env.caPriv, pub, env.accountID, integTestULID)
	if rr := env.mintWithCert(t, priv, pub, cert, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("cert mint: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMintDeviceCertWrongAccount(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// Cert bound to a DIFFERENT account than OwnerOf(ULID) → must be rejected.
	cert := caSignCert(env.caPriv, pub, "some-other-account", integTestULID)
	if rr := env.mintWithCert(t, priv, pub, cert, integTestULID); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-account cert: want 401, got %d", rr.Code)
	}
}

func TestMintDeviceCertForgedCA(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// Cert signed by an attacker CA, not the real management CA → rejected.
	_, forgedCA, _ := ed25519.GenerateKey(rand.Reader)
	cert := caSignCert(forgedCA, pub, env.accountID, integTestULID)
	if rr := env.mintWithCert(t, priv, pub, cert, integTestULID); rr.Code != http.StatusUnauthorized {
		t.Fatalf("forged-CA cert: want 401, got %d", rr.Code)
	}
}

func TestMintDeviceCertWrongMintSig(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cert := caSignCert(env.caPriv, pub, env.accountID, integTestULID)
	// Valid cert, but the mint signature is from a different key (attacker has the
	// cert but not the private key) → rejected.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	if rr := env.mintWithCert(t, otherPriv, pub, cert, integTestULID); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong mint sig: want 401, got %d", rr.Code)
	}
}

func TestMintCertPreferredOverTOFU(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	// Pin a TOFU ECDSA key first.
	ecPriv, ecDER := genDeviceKey(t)
	if rr := env.registerDevice(t, ecPriv, ecDER, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("register TOFU: %d", rr.Code)
	}
	// A presented owner-attested cert (different ed25519 key) still authenticates —
	// the cert path is preferred and does not require the pinned ECDSA key.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	cert := caSignCert(env.caPriv, pub, env.accountID, integTestULID)
	if rr := env.mintWithCert(t, priv, pub, cert, integTestULID); rr.Code != http.StatusOK {
		t.Fatalf("cert preferred over TOFU: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
