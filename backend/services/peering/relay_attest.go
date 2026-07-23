// relay_attest.go — TEE remote-attestation verification for relay peers (PEER-39).
//
// # Overview
//
// Before a sender deposits an encrypted blob with an untrusted relay, it must
// verify that the relay is running inside a Trusted Execution Environment (TEE)
// whose code is exactly what was expected.  This file provides:
//
//   - [AttestVerifier] — pluggable interface for provider-specific verification.
//   - [AttestDoc] — attestation document exchanged between relay and sender.
//   - [AttestPolicy] — sender-side policy (expected measurements / provider).
//   - [AttestNitroVerifier] — built-in AWS Nitro Enclave verifier.
//   - [AttestVerifyRelay] — exported helper the relay-send path calls before
//     deposit.  Returns a non-nil error and must be treated as a hard reject.
//
// # Protocol sketch
//
//	Sender → GET /api/peering/relay/attest          ← relay returns AttestDoc JSON
//	Sender calls AttestVerifyRelay(doc, policy)
//	  OK  → proceed to POST /api/peering/relay/deposit
//	  ERR → abort; do NOT deposit
//
// # Relay side
//
// The relay exposes its attestation document at:
//
//	GET /api/peering/relay/attest
//	  Response (200): AttestDoc JSON
//	  No auth required — the document is public evidence.
//
// [RegisterAttestHandlers] wires this endpoint onto an existing *http.ServeMux.
//
// # Pluggable verifier interface
//
// Providers implement [AttestVerifier].  The package ships one built-in
// implementation: [AttestNitroVerifier] (AWS Nitro Enclaves).  The registry is
// DEFAULT-DENY: there is no built-in "accept anything" verifier, so there is
// no built-in way to pass attestation without a deliberately-registered,
// real verifier.
//
// # Reject-on-failure guarantee
//
// [AttestVerifyRelay] returns a typed [AttestError] on every failure path.
// Callers must treat any non-nil return as a hard reject — the blob MUST NOT
// be deposited with the relay.
package peering

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// ─── Errors ───────────────────────────────────────────────────────────────────

// AttestError is a typed wrapper for attestation-verification failures.
// The caller can inspect Code to decide on error handling.
type AttestError struct {
	// Code is a short machine-readable reason tag.
	Code string
	// Msg is a human-readable description.
	Msg string
	// Cause wraps the underlying error (may be nil).
	Cause error
}

func (e *AttestError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("peering/relay/attest [%s]: %s: %v", e.Code, e.Msg, e.Cause)
	}
	return fmt.Sprintf("peering/relay/attest [%s]: %s", e.Code, e.Msg)
}

func (e *AttestError) Unwrap() error { return e.Cause }

// attestErr is a convenience constructor for AttestError.
func attestErr(code, msg string, cause error) *AttestError {
	return &AttestError{Code: code, Msg: msg, Cause: cause}
}

// ─── AttestProvider ───────────────────────────────────────────────────────────

// AttestProvider identifies the TEE hardware/platform that generated an
// attestation document.
type AttestProvider string

const (
	// AttestProviderNitro identifies AWS Nitro Enclaves.
	AttestProviderNitro AttestProvider = "aws-nitro"

	// AttestProviderNoop identifies the no-op verifier used in tests.
	// MUST NOT be used in production deployments.
	AttestProviderNoop AttestProvider = "noop"
)

// ─── AttestDoc ────────────────────────────────────────────────────────────────

// AttestDoc is the attestation document that a relay exposes to senders.
//
// The relay populates and signs this document (or wraps a platform-native
// document).  Senders call [AttestVerifyRelay] to validate it against their
// [AttestPolicy] before depositing.
type AttestDoc struct {
	// Provider identifies which TEE platform produced this document.
	Provider AttestProvider `json:"provider"`

	// RelayVulaID is the Vula ID of the relay instance that produced this
	// document.  Senders MUST check that it matches the relay they intend to use.
	RelayVulaID string `json:"relay_vula_id"`

	// IssuedAt is when the attestation was produced (RFC 3339 UTC).
	// Senders reject documents older than [AttestPolicy.MaxAge].
	IssuedAt time.Time `json:"issued_at"`

	// PCRs contains Platform Configuration Register measurements keyed by slot
	// index (as a string, e.g. "0", "1", "2").  Present for Nitro; empty for
	// providers that use a different measurement scheme.
	PCRs map[string]string `json:"pcrs,omitempty"`

	// Measurements is a provider-agnostic map of named digest fields
	// (e.g. {"image": "<sha256hex>", "kernel": "<sha256hex>"}).
	// Providers that do not use PCRs populate this instead.
	Measurements map[string]string `json:"measurements,omitempty"`

	// CertChainPEM is the PEM-encoded certificate chain that vouches for the
	// document's authenticity.  For Nitro this is the NSM-issued end-entity
	// certificate plus the Nitro root CA chain.  May be empty for noop.
	CertChainPEM string `json:"cert_chain_pem,omitempty"`

	// RawDocument is the provider-native attestation document (base64-standard
	// encoded).  For Nitro this is the CBOR-encoded NSM attestation document.
	// Callers that only need high-level fields may ignore this field; the
	// built-in verifiers use it for cryptographic validation.
	RawDocument string `json:"raw_document,omitempty"`
}

// ─── AttestPolicy ─────────────────────────────────────────────────────────────

// AttestPolicy is the sender-side policy used to evaluate an [AttestDoc].
//
// All non-empty fields are enforced.  Set only the fields relevant to your
// deployment; unset fields are not checked.
type AttestPolicy struct {
	// Provider is the required TEE provider.  If empty, any provider is
	// accepted — NOT recommended for production.
	Provider AttestProvider

	// ExpectedPCRs is the set of PCR slot → expected hex-digest pairs that MUST
	// all be present and match in the document.  Used with [AttestProviderNitro].
	// Comparison is case-insensitive.
	ExpectedPCRs map[string]string

	// ExpectedMeasurements is the set of named measurement → expected hex-digest
	// pairs for providers that do not use PCRs.
	ExpectedMeasurements map[string]string

	// TrustedRootPEM is the PEM-encoded root certificate to verify the
	// attestation certificate chain against.  If empty, the built-in AWS Nitro
	// root is used for Nitro documents.
	TrustedRootPEM string

	// MaxAge is the maximum accepted age of the attestation. For the generic
	// (provider-agnostic) pre-check this bounds the UNSIGNED [AttestDoc.IssuedAt];
	// zero means no pre-check. For the Nitro verifier this bounds the SIGNED NSM
	// timestamp, and a zero value falls back to [attestDefaultMaxAge] (strict
	// default — a Nitro document is NEVER accepted with an unbounded age, so a
	// backdated/stale signed document cannot be replayed).
	MaxAge time.Duration

	// Nonce, when non-empty, is a caller-supplied challenge that MUST appear in the
	// SIGNED NSM nonce field. It binds an attestation to a specific verification
	// request so a captured-but-valid document cannot be replayed against a
	// different challenge. Empty disables the nonce check (back-compat).
	Nonce []byte
}

// attestDefaultMaxAge is the strict freshness window applied to the SIGNED Nitro
// timestamp when the policy does not pin its own MaxAge. A Nitro attestation older
// (or more future-dated) than this is rejected so a stale/backdated document can
// never be replayed. Nitro attestations are produced on demand and are meant to be
// fresh, so a few minutes is generous.
const attestDefaultMaxAge = 5 * time.Minute

// ─── AttestVerifier interface ─────────────────────────────────────────────────

// AttestVerifier is the pluggable interface for provider-specific attestation
// verification.
//
// Implementations must be safe for concurrent use.
type AttestVerifier interface {
	// Verify checks doc against policy and returns nil if the relay passes.
	// A non-nil error means the relay MUST be rejected.
	Verify(doc AttestDoc, policy AttestPolicy) error
}

// ─── Registry ─────────────────────────────────────────────────────────────────

// The default registry is DEFAULT-DENY. No "accept anything" verifier is
// registered here — doing so by default would silently disable attestation.
// A deployment that genuinely wants no TEE checks must make that an explicit,
// auditable choice by registering its own verifier.
var (
	attestRegistryMu sync.RWMutex
	attestRegistry   = map[AttestProvider]AttestVerifier{
		AttestProviderNitro: AttestNitroVerifier{},
	}
)

// AttestRegisterVerifier registers (or replaces) the verifier for provider.
// This must be called before any verification requests are made.
// It is safe for concurrent use.
func AttestRegisterVerifier(provider AttestProvider, v AttestVerifier) {
	attestRegistryMu.Lock()
	defer attestRegistryMu.Unlock()
	attestRegistry[provider] = v
}

// attestLookupVerifier returns the registered verifier for provider.
func attestLookupVerifier(provider AttestProvider) (AttestVerifier, bool) {
	attestRegistryMu.RLock()
	defer attestRegistryMu.RUnlock()
	v, ok := attestRegistry[provider]
	return v, ok
}

// ─── AttestVerifyRelay ────────────────────────────────────────────────────────

// AttestVerifyRelay is the exported helper that the relay-send path calls
// before depositing a blob.
//
// It verifies doc against policy using the registered [AttestVerifier] for
// doc.Provider.  Any non-nil return is a hard reject — the caller MUST NOT
// proceed with the deposit.
//
// DEFAULT-DENY: an empty/zero policy that pins no provider is REJECTED. A
// policy that "accepts any provider" provides no security, so callers must
// explicitly name the provider they trust.
//
// Checks performed (in order):
//  1. doc.Provider must be non-empty.
//  2. policy.Provider must be set (default-deny) AND doc.Provider must match it.
//  3. doc.RelayVulaID must be non-empty.
//  4. doc.IssuedAt must not be zero.
//  5. If policy.MaxAge > 0, doc.IssuedAt must be within MaxAge of now.
//  6. The registered AttestVerifier for doc.Provider must return nil.
func AttestVerifyRelay(doc AttestDoc, policy AttestPolicy) error {
	// 1. Provider must be set.
	if doc.Provider == "" {
		return attestErr("missing-provider",
			"attestation document does not specify a provider", nil)
	}

	// 2. Default-deny: the policy MUST pin an expected provider, and the
	//    document's provider MUST match it. An empty policy is refused outright
	//    rather than accepting whatever provider the relay self-declares.
	if policy.Provider == "" {
		return attestErr("empty-policy",
			"attestation policy pins no provider (default-deny): refusing to accept a self-declared provider", nil)
	}
	if doc.Provider != policy.Provider {
		return attestErr("provider-mismatch",
			fmt.Sprintf("expected provider %q, got %q", policy.Provider, doc.Provider), nil)
	}

	// 3. RelayVulaID must be present.
	if doc.RelayVulaID == "" {
		return attestErr("missing-relay-id",
			"attestation document does not contain relay_vula_id", nil)
	}

	// 4. IssuedAt must not be zero.
	if doc.IssuedAt.IsZero() {
		return attestErr("missing-issued-at",
			"attestation document is missing issued_at timestamp", nil)
	}

	// 5. Age check.
	if policy.MaxAge > 0 {
		age := time.Since(doc.IssuedAt)
		if age < 0 {
			// Future-dated documents are also suspicious.
			age = -age
		}
		if age > policy.MaxAge {
			return attestErr("doc-expired",
				fmt.Sprintf("attestation document is %.0fs old, max allowed %s",
					age.Seconds(), policy.MaxAge), nil)
		}
	}

	// 6. Provider-specific cryptographic verification.
	v, ok := attestLookupVerifier(doc.Provider)
	if !ok {
		return attestErr("unknown-provider",
			fmt.Sprintf("no verifier registered for provider %q", doc.Provider), nil)
	}
	if err := v.Verify(doc, policy); err != nil {
		var ae *AttestError
		if errors.As(err, &ae) {
			return ae
		}
		return attestErr("verify-failed", "provider verification failed", err)
	}
	return nil
}

// ─── AttestNitroVerifier ──────────────────────────────────────────────────────

// AttestNitroVerifier verifies AWS Nitro Enclave attestation documents.
//
// SECURITY (PEER-39): this verifier performs REAL cryptographic verification of
// the NSM COSE_Sign1 attestation document. It:
//
//  1. base64-decodes doc.RawDocument → the CBOR COSE_Sign1 structure.
//  2. parses the COSE_Sign1 (protected header, payload, signature) and the
//     embedded NSM attestation payload (PCRs, certificate, cabundle, user_data).
//  3. verifies the end-entity certificate chains to the AWS Nitro root CA (or a
//     caller-pinned root) through the cabundle intermediates.
//  4. verifies the COSE_Sign1 ES384 signature over the Sig_structure using the
//     end-entity certificate's P-384 public key — this is what binds the PCRs to
//     AWS's hardware root of trust.
//  5. reads the PCRs FROM THE SIGNED PAYLOAD (never from the unsigned doc.PCRs)
//     and checks them against policy.ExpectedPCRs.
//  6. binds doc.RelayVulaID to the signed NSM user_data so the document cannot be
//     replayed for a different relay identity.
//
// It still FAILS CLOSED on every error path and when doc.RawDocument is absent
// (a doc with only self-asserted doc.PCRs and no signed document is rejected).
type AttestNitroVerifier struct{}

// coseAlgES384 is the COSE algorithm identifier for ECDSA w/ SHA-384 (RFC 9053).
const coseAlgES384 = -35

// nsmAttestationDoc mirrors the CBOR map of an AWS Nitro NSM attestation document.
// Field names follow the NSM specification. Optional fields may be nil.
type nsmAttestationDoc struct {
	ModuleID    string            `cbor:"module_id"`
	Digest      string            `cbor:"digest"`
	Timestamp   uint64            `cbor:"timestamp"`
	PCRs        map[uint64][]byte `cbor:"pcrs"`
	Certificate []byte            `cbor:"certificate"` // DER end-entity cert
	CABundle    [][]byte          `cbor:"cabundle"`    // DER intermediates (root-first)
	PublicKey   []byte            `cbor:"public_key"`
	UserData    []byte            `cbor:"user_data"`
	Nonce       []byte            `cbor:"nonce"`
}

// attestParseCOSESign1 decodes a (possibly tag-18-wrapped) COSE_Sign1 structure
// into its raw protected-header bytes, payload bytes, and signature bytes. The
// bstr CONTENTS are returned verbatim so the Sig_structure can be reconstructed
// byte-for-byte for signature verification.
func attestParseCOSESign1(raw []byte) (protected, payload, signature []byte, err error) {
	// COSE_Sign1 may be wrapped in CBOR tag 18. Try the untagged 4-array first
	// (NSM emits untagged), then fall back to the tagged form.
	var arr []cbor.RawMessage
	if e := cbor.Unmarshal(raw, &arr); e != nil || len(arr) != 4 {
		var tag cbor.Tag
		if e2 := cbor.Unmarshal(raw, &tag); e2 != nil {
			return nil, nil, nil, fmt.Errorf("not a COSE_Sign1 structure: %w", e)
		}
		if tag.Number != 18 {
			return nil, nil, nil, fmt.Errorf("unexpected COSE tag %d (want 18)", tag.Number)
		}
		inner, e3 := cbor.Marshal(tag.Content)
		if e3 != nil {
			return nil, nil, nil, fmt.Errorf("re-encode tagged COSE content: %w", e3)
		}
		if e4 := cbor.Unmarshal(inner, &arr); e4 != nil || len(arr) != 4 {
			return nil, nil, nil, errors.New("tagged COSE_Sign1 is not a 4-element array")
		}
	}

	if e := cbor.Unmarshal(arr[0], &protected); e != nil {
		return nil, nil, nil, fmt.Errorf("decode protected header bstr: %w", e)
	}
	if e := cbor.Unmarshal(arr[2], &payload); e != nil {
		return nil, nil, nil, fmt.Errorf("decode payload bstr: %w", e)
	}
	if e := cbor.Unmarshal(arr[3], &signature); e != nil {
		return nil, nil, nil, fmt.Errorf("decode signature bstr: %w", e)
	}
	return protected, payload, signature, nil
}

// attestVerifyCOSESignature reconstructs the COSE Sig_structure and verifies the
// ES384 signature (raw r||s, 96 bytes) using the P-384 public key in leaf.
func attestVerifyCOSESignature(leaf *x509.Certificate, protected, payload, signature []byte) error {
	// The protected header must select ES384.
	var hdr map[int]any
	if err := cbor.Unmarshal(protected, &hdr); err != nil {
		return fmt.Errorf("decode protected header map: %w", err)
	}
	algRaw, ok := hdr[1] // label 1 = alg
	if !ok {
		return errors.New("protected header has no alg")
	}
	alg, ok := attestAsInt(algRaw)
	if !ok || alg != coseAlgES384 {
		return fmt.Errorf("unsupported COSE alg %v (want ES384 %d)", algRaw, coseAlgES384)
	}

	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P384() {
		return errors.New("end-entity certificate key is not ECDSA P-384")
	}
	if len(signature) != 96 {
		return fmt.Errorf("ES384 signature length %d, want 96", len(signature))
	}

	// Sig_structure = [ "Signature1", body_protected, external_aad (empty), payload ].
	sigStructure := []any{"Signature1", protected, []byte{}, payload}
	toBeSigned, err := cbor.Marshal(sigStructure)
	if err != nil {
		return fmt.Errorf("encode Sig_structure: %w", err)
	}
	digest := sha512.Sum384(toBeSigned)

	r := new(big.Int).SetBytes(signature[:48])
	s := new(big.Int).SetBytes(signature[48:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return errors.New("COSE_Sign1 ES384 signature does not verify")
	}
	return nil
}

// attestAsInt coerces a CBOR-decoded integer (which may be int, int64, uint64)
// to an int, reporting whether the conversion was lossless-enough for a label.
func attestAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	default:
		return 0, false
	}
}

// attestVerifyNitroDERChain verifies a DER end-entity certificate against the
// trusted root (AWS Nitro root by default, or trustedRootPEM when set) using the
// DER cabundle as intermediates. It returns the parsed leaf certificate.
func attestVerifyNitroDERChain(leafDER []byte, cabundle [][]byte, trustedRootPEM string, at time.Time) (*x509.Certificate, error) {
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, fmt.Errorf("parse end-entity certificate: %w", err)
	}

	roots := x509.NewCertPool()
	rootPEM := trustedRootPEM
	if rootPEM == "" {
		rootPEM = nitroRootCAPEM
	}
	if !roots.AppendCertsFromPEM([]byte(rootPEM)) {
		return nil, errors.New("failed to parse trusted root certificate(s)")
	}

	intermediates := x509.NewCertPool()
	for _, der := range cabundle {
		c, e := x509.ParseCertificate(der)
		if e != nil {
			return nil, fmt.Errorf("parse cabundle certificate: %w", e)
		}
		intermediates.AddCert(c)
	}

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return nil, fmt.Errorf("end-entity chain verification: %w", err)
	}
	return leaf, nil
}

// nitroRootCAPEM is the AWS Nitro Enclaves root CA certificate (PEM).
// Source: https://aws-nitro-enclaves.amazonaws.com/AWS_NitroEnclaves_Root-G1.zip
// This is a public document; embedding it avoids a network call at verify time.
const nitroRootCAPEM = `-----BEGIN CERTIFICATE-----
MIICETCCAZagAwIBAgIRAPkxdWgbkK/hHUbMtOTn+FYwCgYIKoZIzj0EAwMwSTEL
MAkGA1UEBhMCVVMxDzANBgNVBAoMBkFtYXpvbjEMMAoGA1UECwwDQVdTMRswGQYD
VQQDDBJhd3Mubml0cm8tZW5jbGF2ZXMwHhcNMTkxMDI4MTMyODA1WhcNNDkxMDI4
MTQyODA1WjBJMQswCQYDVAQGEwJVUzEPMA0GA1UECgwGQW1hem9uMQwwCgYDVQQL
DANBV1MxGzAZBgNVBAMMEmF3cy5uaXRyby1lbmNsYXZlczB2MBAGByqGSM49AgEG
BSuBBAAiA2IABPwCVOumCMHzaHDimtqQvkY4MpJzbolL//Zy2YlES1OUIyE50LMP
XnB0lPkOv4vgEIHLYFSA5+1JLZEzNYG3dQF/hxT2oGJUxVhMrPFKiqCIlP7SVFF
Mx1W4M3l7+Xv/qNjMGEwHQYDVR0OBBYEFJAltQ3ZBUfnlsOW+nKdz5mp30uWMB8G
A1UdIwQYMBaAFJAltQ3ZBUfnlsOW+nKdz5mp30uWMA8GA1UdEwEB/wQFMAMBAf8w
DgYDVR0PAQH/BAQDAgGGMAoGCCqGSM49BAAAA2kAMGYCMQD3KPe/gRqM4fT3HJBM
YbGEJBnRLSvRnHjVrp2bKWpYEv/pJIHBbIGDvxYT5scHjGkCMQCOJzOCM8Fwzuhl
1MHb7VQ5HM/7e0KKAqCG8v2T0fjvhYLNXIx6ioSzIaT1F09aJpk=
-----END CERTIFICATE-----`

// Verify implements [AttestVerifier] for AWS Nitro Enclaves with real COSE_Sign1
// verification (PEER-39). It FAILS CLOSED on every error path.
func (AttestNitroVerifier) Verify(doc AttestDoc, policy AttestPolicy) error {
	if doc.Provider != AttestProviderNitro {
		return attestErr("wrong-provider",
			fmt.Sprintf("AttestNitroVerifier called for provider %q", doc.Provider), nil)
	}

	// The signed NSM document is mandatory. Without it the only PCRs available
	// are the self-asserted doc.PCRs, which carry no cryptographic weight; reject.
	if doc.RawDocument == "" {
		return attestErr("cose-unverified",
			"Nitro attestation has no raw_document: PCRs would be self-asserted and cannot be trusted (fail-closed)", nil)
	}

	// Default-deny on measurements: a Nitro document that pins NO expected PCRs
	// would accept any enclave image. Require at least one pinned PCR.
	if len(policy.ExpectedPCRs) == 0 {
		return attestErr("no-expected-pcrs",
			"Nitro policy pins no ExpectedPCRs (default-deny): refusing to accept any enclave image", nil)
	}

	rawCOSE, err := base64.StdEncoding.DecodeString(doc.RawDocument)
	if err != nil {
		return attestErr("bad-raw-document", "raw_document is not valid base64", err)
	}

	// 1. Parse the COSE_Sign1 envelope.
	protected, payload, signature, err := attestParseCOSESign1(rawCOSE)
	if err != nil {
		return attestErr("bad-cose", "failed to parse COSE_Sign1 structure", err)
	}

	// 2. Parse the signed NSM attestation payload.
	var nsm nsmAttestationDoc
	if err := cbor.Unmarshal(payload, &nsm); err != nil {
		return attestErr("bad-nsm-payload", "failed to parse NSM attestation payload", err)
	}
	if len(nsm.Certificate) == 0 {
		return attestErr("missing-leaf-cert", "NSM payload has no end-entity certificate", nil)
	}

	// 2b. Freshness on SIGNED data. The outer doc.IssuedAt is attacker-mutable, so
	//     it carries no weight; the NSM timestamp is inside the COSE-signed payload
	//     we are about to verify the signature over. We require it to be present and
	//     within a bounded window so a captured-but-valid (backdated) document cannot
	//     be replayed. A zero/missing timestamp is fail-closed.
	if nsm.Timestamp == 0 {
		return attestErr("missing-timestamp",
			"signed NSM document has no timestamp: freshness cannot be established (fail-closed)", nil)
	}
	signedAt := time.UnixMilli(int64(nsm.Timestamp))
	maxAge := policy.MaxAge
	if maxAge <= 0 {
		maxAge = attestDefaultMaxAge // strict default — never unbounded for Nitro.
	}
	if age := time.Since(signedAt); age > maxAge || -age > maxAge {
		return attestErr("stale-attestation",
			fmt.Sprintf("signed NSM timestamp is %s away from now, max allowed %s (replay/backdate guard)",
				absDuration(age), maxAge), nil)
	}

	// 3. Verify the end-entity certificate chains to the trusted Nitro root via
	//    the cabundle intermediates. The verification time is the SIGNED NSM
	//    timestamp (Nitro leaf certs are short-lived); using the unsigned
	//    doc.IssuedAt here would let an attacker pick a validity instant.
	at := signedAt
	leaf, err := attestVerifyNitroDERChain(nsm.Certificate, nsm.CABundle, policy.TrustedRootPEM, at)
	if err != nil {
		return attestErr("invalid-cert-chain", "Nitro certificate chain validation failed", err)
	}

	// 4. Verify the COSE_Sign1 ES384 signature using the (now trusted) leaf key.
	//    THIS is what binds the PCRs to AWS's hardware root of trust.
	if err := attestVerifyCOSESignature(leaf, protected, payload, signature); err != nil {
		return attestErr("bad-signature", "COSE_Sign1 signature verification failed", err)
	}

	// 5. Read PCRs FROM THE SIGNED PAYLOAD and check them against the policy.
	signedPCRs := make(map[string]string, len(nsm.PCRs))
	for idx, val := range nsm.PCRs {
		signedPCRs[fmt.Sprintf("%d", idx)] = fmt.Sprintf("%x", val)
	}
	if err := attestCheckPCRs(signedPCRs, policy.ExpectedPCRs); err != nil {
		return err // already an *AttestError with pcr-missing / pcr-mismatch.
	}

	// 6. Bind the document to the relay identity. The relay MUST place its Vula
	//    ID in the signed NSM user_data; otherwise the document could be replayed
	//    to vouch for a different relay.
	if len(nsm.UserData) == 0 {
		return attestErr("missing-identity-binding",
			"NSM user_data is empty: cannot bind attestation to relay_vula_id (fail-closed)", nil)
	}
	if string(nsm.UserData) != doc.RelayVulaID {
		return attestErr("identity-mismatch",
			fmt.Sprintf("signed user_data does not match relay_vula_id %q", doc.RelayVulaID), nil)
	}

	// 7. Bind a caller-supplied challenge nonce (anti-replay). When the policy pins
	//    a Nonce, the SIGNED NSM nonce MUST equal it; otherwise a captured valid
	//    attestation could be replayed against a different verification request.
	if len(policy.Nonce) > 0 {
		if !bytes.Equal(nsm.Nonce, policy.Nonce) {
			return attestErr("nonce-mismatch",
				"signed NSM nonce does not match the caller-supplied challenge (replay guard)", nil)
		}
	}

	return nil
}

// absDuration returns the absolute value of d.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// attestCheckPCRs verifies that every entry in expected is present in actual
// and that the hex digests match (case-insensitive).
func attestCheckPCRs(actual, expected map[string]string) error {
	for slot, wantHex := range expected {
		gotHex, ok := actual[slot]
		if !ok {
			return attestErr("pcr-missing",
				fmt.Sprintf("PCR slot %q is absent from attestation document", slot), nil)
		}
		if !strings.EqualFold(strings.TrimSpace(gotHex), strings.TrimSpace(wantHex)) {
			return attestErr("pcr-mismatch",
				fmt.Sprintf("PCR slot %q: expected %q got %q", slot, wantHex, gotHex), nil)
		}
	}
	return nil
}

// ─── Relay-side: AttestStore ──────────────────────────────────────────────────

// AttestStore holds the attestation document served by the relay to senders.
//
// In a real TEE deployment the relay would call the platform SDK to generate
// the document on startup (and periodically refresh it).  In this
// implementation the document is set programmatically via [AttestStore.Set].
type AttestStore struct {
	mu  sync.RWMutex
	doc *AttestDoc
}

// NewAttestStore returns an initialised but empty [AttestStore].
func NewAttestStore() *AttestStore {
	return &AttestStore{}
}

// Set replaces the stored attestation document.  Set validates basic fields.
func (as *AttestStore) Set(doc AttestDoc) error {
	if doc.Provider == "" {
		return errors.New("peering/relay/attest: provider must not be empty")
	}
	if doc.RelayVulaID == "" {
		return errors.New("peering/relay/attest: relay_vula_id must not be empty")
	}
	if doc.IssuedAt.IsZero() {
		return errors.New("peering/relay/attest: issued_at must not be zero")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	cp := doc
	as.doc = &cp
	return nil
}

// Get returns the current attestation document and true, or (zero, false) if
// none has been set yet.
func (as *AttestStore) Get() (AttestDoc, bool) {
	as.mu.RLock()
	defer as.mu.RUnlock()
	if as.doc == nil {
		return AttestDoc{}, false
	}
	return *as.doc, true
}

// ─── HTTP: relay exposes attestation ─────────────────────────────────────────

// RegisterAttestHandlers mounts the attestation endpoints on mux.
//
//	GET /api/peering/relay/attest  → returns the current AttestDoc as JSON.
//
// The endpoint is unauthenticated — the document is public evidence that the
// relay presents to any potential sender.
func RegisterAttestHandlers(mux *http.ServeMux, store *AttestStore) {
	if mux == nil {
		panic("peering/relay/attest: RegisterAttestHandlers: mux must not be nil")
	}
	if store == nil {
		panic("peering/relay/attest: RegisterAttestHandlers: store must not be nil")
	}
	mux.HandleFunc("GET /api/peering/relay/attest", store.handleGetAttest)
}

// handleGetAttest serves GET /api/peering/relay/attest.
func (as *AttestStore) handleGetAttest(w http.ResponseWriter, r *http.Request) {
	doc, ok := as.Get()
	if !ok {
		http.Error(w, `{"error":"no attestation document available"}`,
			http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		// Encoding error after headers are sent — nothing useful we can do.
		_ = err
	}
}

// ─── Sender-side helper: fetch + verify ───────────────────────────────────────

// AttestFetchAndVerifyWithClient fetches the attestation document from
// relayAttestURL, decodes it, and calls [AttestVerifyRelay] with the given
// policy, using the given *http.Client.
func AttestFetchAndVerifyWithClient(client *http.Client, relayAttestURL string, policy AttestPolicy) (AttestDoc, error) {
	if client == nil {
		return AttestDoc{}, attestErr("nil-client", "http client must not be nil", nil)
	}
	resp, err := client.Get(relayAttestURL) //nolint:noctx
	if err != nil {
		return AttestDoc{}, attestErr("fetch-failed",
			"failed to fetch attestation document from relay", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return AttestDoc{}, attestErr("fetch-bad-status",
			fmt.Sprintf("relay attest endpoint returned HTTP %d", resp.StatusCode), nil)
	}

	var doc AttestDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return AttestDoc{}, attestErr("decode-failed",
			"failed to decode attestation document JSON", err)
	}

	if err := AttestVerifyRelay(doc, policy); err != nil {
		return AttestDoc{}, err
	}
	return doc, nil
}

// ─── Utility ──────────────────────────────────────────────────────────────────

// AttestDocumentDigest returns the SHA-256 hex digest of the raw attestation
// document bytes (RawDocument field, base64-decoded).  Returns an empty string
// if RawDocument is absent.  This is a convenience for logging/auditing.
func AttestDocumentDigest(doc AttestDoc) string {
	if doc.RawDocument == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(doc.RawDocument)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}
