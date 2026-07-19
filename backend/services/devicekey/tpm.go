// TPM backend for the devicekey package.
// Uses github.com/google/go-tpm/tpm2 to drive a hardware TPM at /dev/tpmrm0.
//
// Key design notes:
//   - A primary key (ECC P-256) is created under the Owner hierarchy on every
//     Open() call; primary keys are deterministic in the TPM so the same key is
//     produced each time the same template is used (no persistent handle needed).
//   - Seal/Unseal wraps caller data with AES-256-GCM using a wrap key that is
//     derived from the deterministic TPM public key — this keeps the API surface
//     identical to the software backend while still binding the wrapped data to
//     this specific TPM instance.
//   - Sign() re-creates the primary key handle per call (stateless across restarts).
//   - DeviceIdentity() returns the PKIX-DER of the primary ECC public key; this is
//     stable across reboots because TPM primary keys are derived deterministically.
package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// tpmStore is the hardware TPM-backed KeyStore.
type tpmStore struct {
	mu      sync.Mutex
	tpm     transport.TPMCloser
	keyDir  string
	wrapKey []byte // AES-256 wrap key sealing Seal() payloads; derived from TPM public key
	pubDER  []byte
	status  Status
}

// openTPM attempts to open the TPM at defaultTPMPath.
// Returns an error if the device is absent or inaccessible — callers should
// fall through to openSoftware().
func openTPM(keyDir string) (*tpmStore, error) {
	// Quick probe: can we open the device node at all?
	f, err := os.OpenFile(defaultTPMPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: open %s: %w", defaultTPMPath, err)
	}
	f.Close()

	tpm, err := transport.OpenTPM(defaultTPMPath)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: transport.OpenTPM: %w", err)
	}

	s := &tpmStore{tpm: tpm, keyDir: keyDir}
	if err := s.init(); err != nil {
		_ = tpm.Close()
		return nil, fmt.Errorf("devicekey/tpm: init: %w", err)
	}
	return s, nil
}

// eccP256SignTemplate is the ECC P-256 template for the Owner-hierarchy primary key.
// TPM primary keys are deterministic: same template + same hierarchy produces the
// same key every time on the same TPM.
var eccP256SignTemplate = tpm2.TPMTPublic{
	Type:    tpm2.TPMAlgECC,
	NameAlg: tpm2.TPMAlgSHA256,
	ObjectAttributes: tpm2.TPMAObject{
		FixedTPM:            true,
		FixedParent:         true,
		SensitiveDataOrigin: true,
		UserWithAuth:        true,
		SignEncrypt:         true,
	},
	Parameters: tpm2.NewTPMUPublicParms(
		tpm2.TPMAlgECC,
		&tpm2.TPMSECCParms{
			CurveID: tpm2.TPMECCNistP256,
			Scheme: tpm2.TPMTECCScheme{
				Scheme: tpm2.TPMAlgECDSA,
				Details: tpm2.NewTPMUAsymScheme(
					tpm2.TPMAlgECDSA,
					&tpm2.TPMSSigSchemeECDSA{
						HashAlg: tpm2.TPMAlgSHA256,
					},
				),
			},
		},
	),
}

// createPrimary runs TPM2_CreatePrimary and returns the response.
// The caller is responsible for flushing the handle when done.
func (s *tpmStore) createPrimary() (*tpm2.CreatePrimaryResponse, error) {
	cmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(eccP256SignTemplate),
	}
	return cmd.Execute(s.tpm)
}

func (s *tpmStore) init() error {
	cr, err := s.createPrimary()
	if err != nil {
		return fmt.Errorf("CreatePrimary: %w", err)
	}
	// Flush immediately — we only need the public portion to derive the wrap key.
	tpm2.FlushContext{FlushHandle: cr.ObjectHandle}.Execute(s.tpm) //nolint:errcheck

	// Extract PKIX DER of the ECC public key.
	pub, err := cr.OutPublic.Contents()
	if err != nil {
		return fmt.Errorf("OutPublic.Contents: %w", err)
	}
	eccPub, err := pub.Unique.ECC()
	if err != nil {
		return fmt.Errorf("pub.Unique.ECC: %w", err)
	}

	ecKey, err := buildECDSAPublicKey(eccPub.X.Buffer, eccPub.Y.Buffer)
	if err != nil {
		return fmt.Errorf("buildECDSAPublicKey: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(ecKey)
	if err != nil {
		return fmt.Errorf("MarshalPKIXPublicKey: %w", err)
	}
	s.pubDER = der

	// Persist public key for callers that want a quick read.
	_ = atomicWrite(filepath.Join(s.keyDir, "device_key.pub"), der, 0644)

	// Derive wrap key from the stable public key DER (first 32 bytes).
	secret := der[:32]
	wrapKey, err := s.loadOrCreateWrapKey(secret)
	if err != nil {
		return fmt.Errorf("wrap key: %w", err)
	}
	s.wrapKey = wrapKey

	h := sha256.Sum256(der)
	s.status = Status{
		Backend:    BackendHardware,
		Available:  true,
		DevicePath: defaultTPMPath,
		KeyDir:     s.keyDir,
		DeviceID:   hex.EncodeToString(h[:16]),
	}
	return nil
}

func (s *tpmStore) wrapKeyPath() string { return filepath.Join(s.keyDir, "wrap_tpm.key") }

func (s *tpmStore) loadOrCreateWrapKey(secret []byte) ([]byte, error) {
	path := s.wrapKeyPath()
	if enc, err := os.ReadFile(path); err == nil {
		return aesGCMDecrypt(secret, enc)
	}
	wrapKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, wrapKey); err != nil {
		return nil, err
	}
	enc, err := aesGCMEncrypt(secret, wrapKey)
	if err != nil {
		return nil, err
	}
	return wrapKey, atomicWrite(path, enc, 0600)
}

// --- KeyStore implementation ---

func (s *tpmStore) Seal(plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return aesGCMEncrypt(s.wrapKey, plaintext)
}

func (s *tpmStore) Unseal(ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return aesGCMDecrypt(s.wrapKey, ciphertext)
}

// Sign produces an ASN.1 DER ECDSA-Sig-Value over digest using the TPM primary key.
// digest must be a 32-byte SHA-256 hash.
func (s *tpmStore) Sign(digest []byte, _ crypto.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(digest) != 32 {
		return nil, fmt.Errorf("tpm Sign: digest must be 32 bytes (SHA-256)")
	}

	cr, err := s.createPrimary()
	if err != nil {
		return nil, fmt.Errorf("tpm Sign CreatePrimary: %w", err)
	}
	defer tpm2.FlushContext{FlushHandle: cr.ObjectHandle}.Execute(s.tpm) //nolint:errcheck

	signCmd := tpm2.Sign{
		KeyHandle: tpm2.NamedHandle{
			Handle: cr.ObjectHandle,
			Name:   cr.Name,
		},
		Digest: tpm2.TPM2BDigest{Buffer: digest},
		InScheme: tpm2.TPMTSigScheme{
			Scheme: tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUSigScheme(
				tpm2.TPMAlgECDSA,
				&tpm2.TPMSSchemeHash{
					HashAlg: tpm2.TPMAlgSHA256,
				},
			),
		},
		Validation: tpm2.TPMTTKHashCheck{
			Tag: tpm2.TPMSTHashCheck,
		},
	}
	rsp, err := signCmd.Execute(s.tpm)
	if err != nil {
		return nil, fmt.Errorf("tpm Sign: %w", err)
	}

	ecSig, err := rsp.Signature.Signature.ECDSA()
	if err != nil {
		return nil, fmt.Errorf("tpm Sign extract ECDSA: %w", err)
	}
	return marshalECDSASig(ecSig.SignatureR.Buffer, ecSig.SignatureS.Buffer)
}

func (s *tpmStore) DeviceIdentity() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.pubDER))
	copy(out, s.pubDER)
	return out, nil
}

func (s *tpmStore) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *tpmStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tpm.Close()
}

// --- encoding helpers ---

// buildECDSAPublicKey reconstructs a Go *ecdsa.PublicKey from raw X/Y bytes
// by wrapping them in a minimal SubjectPublicKeyInfo DER structure.
func buildECDSAPublicKey(x, y []byte) (*ecdsa.PublicKey, error) {
	// Pad X and Y to 32 bytes (P-256 coordinate size).
	xPad := padTo32(x)
	yPad := padTo32(y)

	// Uncompressed point: 0x04 || X || Y
	point := make([]byte, 65)
	point[0] = 0x04
	copy(point[1:33], xPad)
	copy(point[33:65], yPad)

	pub, err := x509.ParsePKIXPublicKey(wrapInSPKI(point))
	if err != nil {
		return nil, err
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an ECDSA public key")
	}
	return ecPub, nil
}

func padTo32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// wrapInSPKI builds a minimal SPKI (SubjectPublicKeyInfo) DER wrapping a raw
// P-256 uncompressed point so x509.ParsePKIXPublicKey can parse it.
func wrapInSPKI(point []byte) []byte {
	// P-256 AlgorithmIdentifier: OID ecPublicKey + OID P-256
	algoID := []byte{
		0x30, 0x13,
		0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, // ecPublicKey
		0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, // P-256
	}
	// BIT STRING: 1 (length) unused bits byte + point
	bsLen := 1 + len(point)
	bs := make([]byte, 0, 2+bsLen)
	bs = append(bs, 0x03, byte(bsLen), 0x00)
	bs = append(bs, point...)

	inner := append(algoID, bs...)
	spki := make([]byte, 0, 2+len(inner))
	spki = append(spki, 0x30, byte(len(inner)))
	spki = append(spki, inner...)
	return spki
}

// marshalECDSASig encodes r, s as an ASN.1 DER ECDSA-Sig-Value SEQUENCE.
func marshalECDSASig(r, s []byte) ([]byte, error) {
	encInt := func(b []byte) []byte {
		// Strip leading zeros, but keep value positive (prepend 0x00 if high bit set).
		for len(b) > 1 && b[0] == 0 {
			b = b[1:]
		}
		if len(b) > 0 && b[0]&0x80 != 0 {
			b = append([]byte{0x00}, b...)
		}
		return append([]byte{0x02, byte(len(b))}, b...)
	}
	ri := encInt(append([]byte(nil), r...))
	si := encInt(append([]byte(nil), s...))
	seq := append(ri, si...)
	return append([]byte{0x30, byte(len(seq))}, seq...), nil
}
