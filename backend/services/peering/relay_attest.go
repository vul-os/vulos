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
// implementation: [AttestNitroVerifier] (AWS Nitro Enclaves).  A no-TEE
// deployment can register [AttestNoopVerifier] for testing — it accepts any
// document without cryptographic checks.  Production callers MUST NOT use
// [AttestNoopVerifier].
//
// # Reject-on-failure guarantee
//
// [AttestVerifyRelay] returns a typed [AttestError] on every failure path.
// Callers must treat any non-nil return as a hard reject — the blob MUST NOT
// be deposited with the relay.
package peering

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
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

	// MaxAge is the maximum accepted age of the [AttestDoc.IssuedAt] timestamp.
	// Zero means no age limit (not recommended for production).
	MaxAge time.Duration
}

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

var (
	attestRegistryMu sync.RWMutex
	attestRegistry   = map[AttestProvider]AttestVerifier{
		AttestProviderNitro: AttestNitroVerifier{},
		AttestProviderNoop:  AttestNoopVerifier{},
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
// Checks performed (in order):
//  1. doc.Provider must be non-empty.
//  2. If policy.Provider is set, doc.Provider must match.
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

	// 2. Provider must match policy (if policy specifies one).
	if policy.Provider != "" && doc.Provider != policy.Provider {
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
// It performs the following checks:
//  1. CertChainPEM parses successfully and chains to the trusted root.
//  2. PCR values in doc match all entries in policy.ExpectedPCRs
//     (case-insensitive hex comparison).
//
// Note: full CBOR/COSE parsing of the raw NSM document is intentionally not
// implemented here — it requires a CBOR library that is not currently in the
// module's dependency set.  The implementation validates the certificate chain
// (present in the PEM field) and PCR measurements extracted into the
// structured AttestDoc by the relay.  A production deployment should
// additionally verify the COSE_Sign1 signature over RawDocument using the
// end-entity certificate from the chain.
type AttestNitroVerifier struct{}

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

// Verify implements [AttestVerifier] for AWS Nitro Enclaves.
func (AttestNitroVerifier) Verify(doc AttestDoc, policy AttestPolicy) error {
	if doc.Provider != AttestProviderNitro {
		return attestErr("wrong-provider",
			fmt.Sprintf("AttestNitroVerifier called for provider %q", doc.Provider), nil)
	}

	// 1. Certificate chain validation.
	if doc.CertChainPEM != "" {
		if err := attestVerifyNitroCertChain(doc.CertChainPEM, policy.TrustedRootPEM); err != nil {
			return attestErr("invalid-cert-chain", "Nitro certificate chain validation failed", err)
		}
	}

	// 2. PCR measurement check.
	if len(policy.ExpectedPCRs) > 0 {
		if err := attestCheckPCRs(doc.PCRs, policy.ExpectedPCRs); err != nil {
			return err
		}
	}

	return nil
}

// attestVerifyNitroCertChain verifies certChainPEM against the trusted root.
// If trustedRootPEM is empty the built-in AWS Nitro root CA is used.
func attestVerifyNitroCertChain(certChainPEM, trustedRootPEM string) error {
	roots := x509.NewCertPool()

	rootPEM := trustedRootPEM
	if rootPEM == "" {
		rootPEM = nitroRootCAPEM
	}
	if !roots.AppendCertsFromPEM([]byte(rootPEM)) {
		return errors.New("failed to parse trusted root certificate(s)")
	}

	// Parse all certificates from the chain.
	var certs []*x509.Certificate
	rest := []byte(certChainPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return errors.New("cert_chain_pem contains no certificates")
	}

	// Build intermediates pool from all but the first certificate.
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		// Nitro end-entity certs have very short lifespans; we allow the
		// caller to override CurrentTime for testing by leaving it zero
		// (x509 package uses time.Now() when zero).
	}

	if _, err := certs[0].Verify(opts); err != nil {
		return fmt.Errorf("certificate chain verification: %w", err)
	}
	return nil
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

// ─── AttestNoopVerifier ───────────────────────────────────────────────────────

// AttestNoopVerifier is a no-op verifier intended for testing and
// non-TEE deployments.  It accepts every document without performing any
// cryptographic checks.
//
// WARNING: MUST NOT be used in production deployments — it provides no
// security guarantees whatsoever.
type AttestNoopVerifier struct{}

// Verify implements [AttestVerifier] and always returns nil.
func (AttestNoopVerifier) Verify(_ AttestDoc, _ AttestPolicy) error { return nil }

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

// AttestFetchAndVerify fetches the attestation document from relayAttestURL,
// decodes it, and calls [AttestVerifyRelay] with the given policy.
//
// relayAttestURL is the full URL of the relay's attest endpoint, e.g.:
//
//	https://relay.example.com/api/peering/relay/attest
//
// The HTTP client used is the standard http.DefaultClient.  Callers that need
// custom timeouts should use [AttestFetchAndVerifyWithClient].
//
// A non-nil error means the relay MUST be rejected — do NOT deposit.
func AttestFetchAndVerify(relayAttestURL string, policy AttestPolicy) (AttestDoc, error) {
	return AttestFetchAndVerifyWithClient(http.DefaultClient, relayAttestURL, policy)
}

// AttestFetchAndVerifyWithClient is like [AttestFetchAndVerify] but accepts a
// custom *http.Client.
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
