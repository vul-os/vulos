// webauthn_virtual_authenticator_test.go -- a self-contained software WebAuthn
// authenticator that drives the OS passkeys Finish* / StreamVerifier ceremonies
// end-to-end (WAVE-46).
//
// The OS passkeys.Service returns the challenge as JSON from
// Begin{Registration,Assertion}, so this harness parses the base64url
// challenge straight out of that JSON. It exercises go-webauthn's real
// verifier -- CBOR attestation objects, COSE EC2 public keys, ECDSA-SHA256
// assertion signatures over authData||SHA256(clientDataJSON), and the
// security knobs (tamper / wrong challenge / wrong origin / sign-count
// regression / unknown credential) -- not hand-rolled bytes.
//
// This lets the OS-side tests cover:
//   - Service.FinishRegistration / FinishAssertion (the go-webauthn RP verify)
//   - passkeys.StreamVerifier (the AUTH-13 stream input-gate assertion path)
//   - the LoginService + the cmd/server route handlers, via the exported bodies.
//
// It does NOT cover the browser/client half (navigator.credentials, user
// gestures, real hardware attestation chains) -- see the honest coverage notes
// in the accompanying *_security_test.go files.
package passkeys

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

// newVirtualAuthenticator mints a fresh P-256 keypair, credential ID and AAGUID
// bound to the service's configured RP ID + origin.
func newVirtualAuthenticator(t *testing.T, svc *Service) *virtualAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("virtualAuthenticator: generate key: %v", err)
	}
	va := &virtualAuthenticator{
		rpID:      svc.rpID,
		origin:    svc.origins[0],
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

// challengeFrom extracts the base64url challenge string from the JSON options
// blob returned by Service.BeginRegistration / BeginAssertion.
func challengeFrom(t *testing.T, optionsJSON []byte) string {
	t.Helper()
	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(optionsJSON, &opts); err != nil {
		t.Fatalf("challengeFrom: parse options: %v", err)
	}
	if opts.PublicKey.Challenge == "" {
		t.Fatalf("challengeFrom: no challenge in options: %s", optionsJSON)
	}
	return opts.PublicKey.Challenge
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
func (va *virtualAuthenticator) authData(t *testing.T, flags byte, signCount uint32, includeAttested bool) []byte {
	t.Helper()
	out := make([]byte, 0, 128)
	out = append(out, va.rpIDHash()...)
	out = append(out, flags)

	var sc [4]byte
	binary.BigEndian.PutUint32(sc[:], signCount)
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

// clientDataJSON builds the collected client data for a ceremony type + challenge.
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
// and returns the JSON body accepted by protocol.ParseCredentialCreationResponse
// (and therefore Service.FinishRegistration). challengeB64URL is the challenge
// from BeginRegistration. origin overrides the default (wrong-origin tests);
// pass "" to use the authenticator's origin.
func (va *virtualAuthenticator) makeAttestationResponse(t *testing.T, challengeB64URL, origin string) []byte {
	t.Helper()
	if origin == "" {
		origin = va.origin
	}
	clientData := va.clientDataJSON(t, string(protocol.CreateCeremony), challengeB64URL, origin)

	// Registration: UP+UV+AT flags, attested-credential-data present.
	rawAuthData := va.authData(t, vaFlagUP|vaFlagUV|vaFlagAT, va.signCount, true)

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

// vaAssertionOpts tweaks the assertion the virtual authenticator produces so we
// can build the security-critical negative cases (tampered signature, wrong
// challenge, sign-count regression, wrong origin, unknown credential).
type vaAssertionOpts struct {
	origin         string  // override origin (wrong-origin rejection); "" = default
	challengeB64   string  // challenge to sign (replay/wrong-challenge)
	overrideCredID []byte  // present an unknown credential id; nil = real credID
	signCount      *uint32 // override sign-count (clone/regression); nil = current
	tamperSig      bool    // flip a signature byte after signing
	wrongRPID      string  // sign authData over a different rpIDHash; "" = default
}

// makeAssertionResponse performs a login ("webauthn.get") ceremony and returns
// the JSON body accepted by protocol.ParseCredentialRequestResponse (and
// therefore Service.FinishAssertion).
func (va *virtualAuthenticator) makeAssertionResponse(t *testing.T, opts vaAssertionOpts) []byte {
	t.Helper()
	origin := opts.origin
	if origin == "" {
		origin = va.origin
	}
	if opts.challengeB64 == "" {
		t.Fatal("makeAssertionResponse: challengeB64 required")
	}
	credID := opts.overrideCredID
	if credID == nil {
		credID = va.credID
	}
	sc := va.signCount
	if opts.signCount != nil {
		sc = *opts.signCount
	}

	clientData := va.clientDataJSON(t, string(protocol.AssertCeremony), opts.challengeB64, origin)
	clientDataHash := sha256.Sum256(clientData)

	// Login authData: no attested-credential-data, UP+UV set.
	rawAuthData := va.authData(t, vaFlagUP|vaFlagUV, sc, false)
	if opts.wrongRPID != "" {
		// Overwrite the rpIDHash (first 32 bytes) with a different RP's hash so
		// go-webauthn's rpIDHash check rejects it.
		h := sha256.Sum256([]byte(opts.wrongRPID))
		copy(rawAuthData[:32], h[:])
	}

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

// register runs a full Begin+Finish registration against svc for userID and
// returns the credential ID. The authenticator's credID is now enrolled.
func (va *virtualAuthenticator) register(t *testing.T, svc *Service, userID, displayName string) string {
	t.Helper()
	challenge, sessionData, err := svc.BeginRegistration(userID, displayName)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	body := va.makeAttestationResponse(t, challengeFrom(t, challenge), "")
	credID, err := svc.FinishRegistration(userID, body, sessionData)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	return credID
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
