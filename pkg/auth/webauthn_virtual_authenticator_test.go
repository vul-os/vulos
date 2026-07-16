// webauthn_virtual_authenticator_test.go -- a self-contained software WebAuthn
// authenticator used to drive the Finish* ceremonies end-to-end (WAVE-42).
//
// The go-webauthn Finish{Registration,Login} paths cannot be exercised with
// hand-rolled fake bytes: they parse a real CBOR attestation object / COSE
// public key and cryptographically verify an ECDSA assertion signature over
// authenticatorData||SHA256(clientDataJSON). To reach those paths without a
// browser we implement a minimal but *spec-correct* virtual authenticator:
//
//   - Holds a P-256 (ES256) keypair, a random 16-byte AAGUID and a credential ID.
//   - Emits a "none"-format attestation object (CBOR: {fmt,attStmt,authData})
//     whose authData carries the AT flag + attested-credential-data + a COSE
//     EC2 public key, exactly as go-webauthn's Unmarshal expects.
//   - Emits assertion authenticatorData (rpIDHash|flags|signCount) and an
//     ECDSA-SHA256 DER signature over authData||SHA256(clientDataJSON).
//   - Produces the JSON request bodies (base64url fields) that
//     protocol.ParseCredentialCreationResponse / ...RequestResponse decode.
//
// This lets us test storeWebAuthnCredential + the sign-count / challenge /
// origin / signature verification the library performs, using freshly-minted
// challenges from Begin* each run (no static spec vectors). It does NOT cover
// the browser/client half (navigator.credentials, user gestures, real HW
// attestation chains) -- see the honest coverage notes in the wave-42 test file.
package auth

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
	"github.com/go-webauthn/webauthn/protocol"
)

// Authenticator-data flag bits (WebAuthn §6.1).
const (
	vaFlagUP = 0x01 // User Present
	vaFlagUV = 0x04 // User Verified
	vaFlagAT = 0x40 // Attested credential data included
)

// COSE constants (RFC 8152 / go-webauthn webauthncose).
const (
	coseKtyEC2   = 2  // key type: EC2
	coseAlgES256 = -7 // ECDSA w/ SHA-256
	coseCrvP256  = 1  // curve: P-256
)

// virtualAuthenticator is a software FIDO2 authenticator for tests.
type virtualAuthenticator struct {
	rpID      string
	origin    string
	key       *ecdsa.PrivateKey
	aaguid    [16]byte
	credID    []byte
	signCount uint32
}

// newVirtualAuthenticator mints a fresh P-256 keypair, credential ID and AAGUID.
func newVirtualAuthenticator(t *testing.T, rpID, origin string) *virtualAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("virtualAuthenticator: generate key: %v", err)
	}
	va := &virtualAuthenticator{
		rpID:      rpID,
		origin:    origin,
		key:       key,
		credID:    make([]byte, 32),
		signCount: 1, // start > 0 so the monotonic counter path is exercised
	}
	if _, err := rand.Read(va.aaguid[:]); err != nil {
		t.Fatalf("virtualAuthenticator: aaguid: %v", err)
	}
	if _, err := rand.Read(va.credID); err != nil {
		t.Fatalf("virtualAuthenticator: credID: %v", err)
	}
	return va
}

// coseKey returns the CBOR-encoded COSE_Key (EC2/ES256/P-256) for the public key,
// matching the canonical integer-keyed map go-webauthn's Unmarshal decodes.
func (va *virtualAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	va.key.PublicKey.X.FillBytes(x)
	va.key.PublicKey.Y.FillBytes(y)

	// COSE key as an integer-keyed map: 1=kty, 3=alg, -1=crv, -2=x, -3=y.
	// fxamacker/cbor sorts map keys canonically, which is what go-webauthn expects.
	m := map[int]any{
		1:  coseKtyEC2,
		3:  coseAlgES256,
		-1: coseCrvP256,
		-2: x,
		-3: y,
	}
	enc, err := cbor.Marshal(m)
	if err != nil {
		t.Fatalf("virtualAuthenticator: cose marshal: %v", err)
	}
	return enc
}

// rpIDHash returns SHA-256(rpID).
func (va *virtualAuthenticator) rpIDHash() []byte {
	h := sha256.Sum256([]byte(va.rpID))
	return h[:]
}

// authData builds the raw authenticator data.
//
// Layout (WebAuthn §6.1): rpIDHash(32) | flags(1) | signCount(4) [ | attested-cred-data ].
// When includeAttested is set (registration) the attested credential data is
// appended: aaguid(16) | credIDLen(2, big-endian) | credID | COSE pubkey.
func (va *virtualAuthenticator) authData(t *testing.T, flags byte, includeAttested bool) []byte {
	t.Helper()
	out := make([]byte, 0, 128)
	out = append(out, va.rpIDHash()...)
	out = append(out, flags)

	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], va.signCount)
	out = append(out, sc[:]...)

	if includeAttested {
		out = append(out, va.aaguid[:]...)
		var idLen [2]byte
		binary.BigEndian.PutUint16(idLen[:], uint16(len(va.credID)))
		out = append(out, idLen[:]...)
		out = append(out, va.credID...)
		out = append(out, va.coseKey(t)...)
	}
	return out
}

// clientDataJSON builds the collected client data for a given ceremony type and
// the base64url challenge string carried in the go-webauthn SessionData.
func (va *virtualAuthenticator) clientDataJSON(t *testing.T, ceremony, challengeB64URL, origin string) []byte {
	t.Helper()
	cd := map[string]any{
		"type":        ceremony,
		"challenge":   challengeB64URL,
		"origin":      origin,
		"crossOrigin": false,
	}
	b, err := json.Marshal(cd)
	if err != nil {
		t.Fatalf("virtualAuthenticator: clientData marshal: %v", err)
	}
	return b
}

// makeAttestationResponse performs a registration ("webauthn.create") ceremony
// and returns the JSON body accepted by protocol.ParseCredentialCreationResponse.
//
// origin overrides the authenticator's default origin (to test wrong-origin
// rejection); pass "" to use the default.
func (va *virtualAuthenticator) makeAttestationResponse(t *testing.T, session interface{ challenge() string }, origin string) []byte {
	t.Helper()
	if origin == "" {
		origin = va.origin
	}
	clientData := va.clientDataJSON(t, string(protocol.CreateCeremony), session.challenge(), origin)

	// Registration: UP+UV+AT flags, attested-credential-data present.
	rawAuthData := va.authData(t, vaFlagUP|vaFlagUV|vaFlagAT, true)

	attObj := map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": rawAuthData,
	}
	attCBOR, err := cbor.Marshal(attObj)
	if err != nil {
		t.Fatalf("virtualAuthenticator: attestation cbor: %v", err)
	}

	credIDB64 := base64.RawURLEncoding.EncodeToString(va.credID)
	body := map[string]any{
		"id":    credIDB64,
		"rawId": credIDB64,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attCBOR),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("virtualAuthenticator: attestation body: %v", err)
	}
	return b
}

// assertionOpts tweaks the assertion the virtual authenticator produces, so we
// can build the security-critical negative cases (tampered signature, wrong
// challenge, sign-count regression, wrong origin, unknown credential).
type assertionOpts struct {
	origin         string  // override origin (wrong-origin rejection); "" = default
	challengeB64   string  // override challenge (replay/wrong-challenge); "" = session
	overrideCredID []byte  // present an unknown credential id; nil = real credID
	signCount      *uint32 // override sign-count (clone/regression); nil = current
	tamperSig      bool    // flip a signature byte after signing
}

// makeAssertionResponse performs a login ("webauthn.get") ceremony and returns
// the JSON body accepted by protocol.ParseCredentialRequestResponse.
func (va *virtualAuthenticator) makeAssertionResponse(t *testing.T, session interface{ challenge() string }, opts assertionOpts) []byte {
	t.Helper()
	origin := opts.origin
	if origin == "" {
		origin = va.origin
	}
	challenge := opts.challengeB64
	if challenge == "" {
		challenge = session.challenge()
	}
	credID := opts.overrideCredID
	if credID == nil {
		credID = va.credID
	}
	sc := va.signCount
	if opts.signCount != nil {
		sc = *opts.signCount
	}

	clientData := va.clientDataJSON(t, string(protocol.AssertCeremony), challenge, origin)
	clientDataHash := sha256.Sum256(clientData)

	// Login authData: no attested-credential-data, UP+UV set.
	savedSC := va.signCount
	va.signCount = sc
	rawAuthData := va.authData(t, vaFlagUP|vaFlagUV, false)
	va.signCount = savedSC

	// Signature = ECDSA-SHA256(authData || SHA256(clientDataJSON)), DER-encoded.
	sigData := append(append([]byte{}, rawAuthData...), clientDataHash[:]...)
	digest := sha256.Sum256(sigData)
	r, s, err := ecdsa.Sign(rand.Reader, va.key, digest[:])
	if err != nil {
		t.Fatalf("virtualAuthenticator: sign: %v", err)
	}
	sigDER, err := marshalECDSASig(r, s)
	if err != nil {
		t.Fatalf("virtualAuthenticator: marshal sig: %v", err)
	}
	if opts.tamperSig {
		sigDER = tamperLastByte(sigDER)
	}

	credIDB64 := base64.RawURLEncoding.EncodeToString(credID)
	body := map[string]any{
		"id":    credIDB64,
		"rawId": credIDB64,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(rawAuthData),
			"signature":         base64.RawURLEncoding.EncodeToString(sigDER),
		},
		"clientExtensionResults": map[string]any{},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("virtualAuthenticator: assertion body: %v", err)
	}
	return b
}

// marshalECDSASig DER-encodes an ECDSA signature the way FIDO2 authenticators
// and go-webauthn's asn1.Unmarshal expect: SEQUENCE { INTEGER r, INTEGER s }.
func marshalECDSASig(r, s *big.Int) ([]byte, error) {
	return asn1.Marshal(struct{ R, S *big.Int }{r, s})
}

func tamperLastByte(b []byte) []byte {
	out := append([]byte{}, b...)
	if len(out) > 0 {
		out[len(out)-1] ^= 0xFF
	}
	return out
}
