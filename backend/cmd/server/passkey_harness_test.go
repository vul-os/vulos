package main

// passkey_harness_test.go -- WAVE-46 virtual WebAuthn authenticator for the
// cmd/server route-handler tests (routes_passkeys / routes_passkey_login /
// routes_stream_webauthn).
//
// The full harness lives in services/passkeys as an in-package _test.go file and
// cannot be imported from package main, so this is a compact sibling: same
// spec-correct technique (CBOR "none" attestation, COSE EC2 key, ECDSA-SHA256
// assertion signatures over authData||SHA256(clientDataJSON)) reduced to what the
// HTTP round-trip tests need. It lets these tests drive the REAL passkeys.Service
// through the REAL route handlers, exercising the go-webauthn verifier end-to-end
// rather than asserting on hand-rolled bytes.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

const (
	pkFlagUP = 0x01
	pkFlagUV = 0x04
	pkFlagAT = 0x40
)

type pkAuthenticator struct {
	rpID      string
	origin    string
	key       *ecdsa.PrivateKey
	aaguid    [16]byte
	credID    []byte
	signCount uint32
}

func newPKAuthenticator(t *testing.T, rpID, origin string) *pkAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("pkAuthenticator: key: %v", err)
	}
	a := &pkAuthenticator{rpID: rpID, origin: origin, key: key, credID: make([]byte, 32), signCount: 1}
	rand.Read(a.aaguid[:])
	rand.Read(a.credID)
	return a
}

func (a *pkAuthenticator) cose(t *testing.T) []byte {
	t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	a.key.X.FillBytes(x)
	a.key.Y.FillBytes(y)
	enc, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatalf("cose: %v", err)
	}
	return enc
}

func (a *pkAuthenticator) rpIDHash() []byte { h := sha256.Sum256([]byte(a.rpID)); return h[:] }

func (a *pkAuthenticator) authData(t *testing.T, flags byte, sc uint32, attested bool) []byte {
	t.Helper()
	out := append([]byte{}, a.rpIDHash()...)
	out = append(out, flags)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], sc)
	out = append(out, b[:]...)
	if attested {
		out = append(out, a.aaguid[:]...)
		var idLen [2]byte
		binary.BigEndian.PutUint16(idLen[:], uint16(len(a.credID)))
		out = append(out, idLen[:]...)
		out = append(out, a.credID...)
		out = append(out, a.cose(t)...)
	}
	return out
}

func (a *pkAuthenticator) clientData(t *testing.T, typ, challenge, origin string) []byte {
	t.Helper()
	if origin == "" {
		origin = a.origin
	}
	b, err := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	if err != nil {
		t.Fatalf("clientData: %v", err)
	}
	return b
}

// attestation builds the attestation_response body for a challenge (base64url).
func (a *pkAuthenticator) attestation(t *testing.T, challenge string) json.RawMessage {
	t.Helper()
	cd := a.clientData(t, "webauthn.create", challenge, "")
	ad := a.authData(t, pkFlagUP|pkFlagUV|pkFlagAT, a.signCount, true)
	attCBOR, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		t.Fatalf("attestation cbor: %v", err)
	}
	id := base64.RawURLEncoding.EncodeToString(a.credID)
	b, _ := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attCBOR),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	return b
}

type pkAssertOpts struct {
	origin    string
	challenge string
	signCount *uint32
	credID    []byte
	tamper    bool
}

// assertion builds the assertion_response body for a challenge (base64url).
func (a *pkAuthenticator) assertion(t *testing.T, opts pkAssertOpts) json.RawMessage {
	t.Helper()
	sc := a.signCount
	if opts.signCount != nil {
		sc = *opts.signCount
	}
	credID := opts.credID
	if credID == nil {
		credID = a.credID
	}
	cd := a.clientData(t, "webauthn.get", opts.challenge, opts.origin)
	cdHash := sha256.Sum256(cd)
	ad := a.authData(t, pkFlagUP|pkFlagUV, sc, false)
	sigData := append(append([]byte{}, ad...), cdHash[:]...)
	digest := sha256.Sum256(sigData)
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig, _ := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	if opts.tamper && len(sig) > 0 {
		sig[len(sig)-1] ^= 0xFF
	}
	id := base64.RawURLEncoding.EncodeToString(credID)
	b, _ := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(ad),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
		"clientExtensionResults": map[string]any{},
	})
	return b
}

// decodeB64URL decodes a raw-url-encoded base64 string, failing the test on error.
func decodeB64URL(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decodeB64URL: %v", err)
	}
	return b
}

// pkChallengeFrom pulls the base64url challenge out of a Begin* options JSON.
func pkChallengeFrom(t *testing.T, optionsJSON []byte) string {
	t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON, &o); err != nil {
		t.Fatalf("pkChallengeFrom: %v", err)
	}
	return o.PublicKey.Challenge
}
