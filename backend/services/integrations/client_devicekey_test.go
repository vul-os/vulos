package integrations

// client_devicekey_test.go — INTEG-SEC-01 box-side per-device signing + register.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeSigner is an in-test DeviceSigner backed by a generated ECDSA P-256 key —
// the same shape as devicekey.KeyStore (ASN.1 sig, PKIX pubkey).
type fakeSigner struct{ priv *ecdsa.PrivateKey }

func newFakeSigner(t *testing.T) fakeSigner {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return fakeSigner{priv: priv}
}
func (f fakeSigner) Sign(digest []byte, _ crypto.Hash) ([]byte, error) {
	return ecdsa.SignASN1(rand.Reader, f.priv, digest)
}
func (f fakeSigner) DeviceIdentity() ([]byte, error) {
	return x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
}

func TestMintSendsValidDeviceSig(t *testing.T) {
	signer := newFakeSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the per-device signature exactly as the CP does.
		sigB64 := r.Header.Get("X-Device-Sig")
		raw, err := base64.StdEncoding.DecodeString(sigB64)
		if sigB64 == "" || err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		digest := sha256.Sum256([]byte("integrations:token:google:" + testULID))
		if !ecdsa.VerifyASN1(&signer.priv.PublicKey, digest[:], raw) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-xyz",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"scopes":       "openid email",
			"provider":     "google",
		})
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.SetDeviceSigner(signer)
	tok, err := c.MintToken(context.Background(), ProviderGoogle)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tok.AccessToken != "access-xyz" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestRegisterPinsKey(t *testing.T) {
	signer := newFakeSigner(t)
	var gotPin bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/integrations/device/register" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Bootstrap fleet HMAC over the registration message.
		regMsg := "integrations:register:" + testULID
		mac := hmac.New(sha256.New, []byte(testSecret))
		mac.Write([]byte(regMsg))
		if r.Header.Get("X-Integration-Sig") != hex.EncodeToString(mac.Sum(nil)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			ULID      string `json:"ulid"`
			PubKeyDER string `json:"pubkey_der"`
			Algo      string `json:"algo"`
			SelfSig   string `json:"self_sig"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pubDER, _ := base64.StdEncoding.DecodeString(body.PubKeyDER)
		selfSig, _ := base64.StdEncoding.DecodeString(body.SelfSig)
		pub, err := x509.ParsePKIXPublicKey(pubDER)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Possession: self-sig must verify against the submitted pubkey.
		digest := sha256.Sum256([]byte(regMsg))
		if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], selfSig) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotPin = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ulid": testULID, "pinned": true})
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.SetDeviceSigner(signer)
	if err := c.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !gotPin {
		t.Fatal("broker did not accept the registration")
	}
}

func TestRegisterNoSignerIsNoop(t *testing.T) {
	c := testClient("http://unused")
	if err := c.Register(context.Background()); err != nil {
		t.Fatalf("Register without signer should be a no-op, got %v", err)
	}
}

func TestRegisterConflict(t *testing.T) {
	signer := newFakeSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	c.SetDeviceSigner(signer)
	if err := c.Register(context.Background()); err == nil {
		t.Fatal("expected an error on 409 conflict")
	}
}
