package integrations

// client_cert_test.go — INTEG-SEC-01 method 1: box presents an owner-attested
// device cert on the mint request.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeCertProvider struct {
	cert, pub []byte
	priv      ed25519.PrivateKey
}

func (f fakeCertProvider) DeviceCert() (cert, pub []byte, ok bool) { return f.cert, f.pub, true }
func (f fakeCertProvider) SignMint(msg string) ([]byte, error) {
	return ed25519.Sign(f.priv, []byte(msg)), nil
}

func TestMintPresentsOwnerAttestedCert(t *testing.T) {
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	const account = "acct-xyz"
	payload := "vulos-device-cert:v1:" + base64.RawURLEncoding.EncodeToString(pub) + ":" + account + ":" + testULID
	cert := ed25519.Sign(caPriv, []byte(payload))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		certB, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Cert"))
		pubB, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Pubkey"))
		sigB, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Sig"))
		// Verify the cert against the CA exactly as the CP does.
		p := "vulos-device-cert:v1:" + base64.RawURLEncoding.EncodeToString(pubB) + ":" + account + ":" + testULID
		if len(certB) == 0 || !ed25519.Verify(caPub, []byte(p), certB) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Verify the per-request signature against the cert's pubkey.
		if !ed25519.Verify(pubB, []byte("integrations:token:google:"+testULID), sigB) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-cert",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"scopes":       "openid email", "provider": "google",
		})
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.SetCertProvider(fakeCertProvider{cert: cert, pub: pub, priv: priv})
	tok, err := c.MintToken(context.Background(), ProviderGoogle)
	if err != nil {
		t.Fatalf("MintToken with cert: %v", err)
	}
	if tok.AccessToken != "access-cert" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}
