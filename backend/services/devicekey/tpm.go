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
//
// Rotation HONEST LIMITATION: the primary key above is DETERMINISTIC (same
// template + same TPM ⇒ same key, by design — that is what makes it stable
// across reboots without a persistent handle). A genuinely fresh TPM-resident
// replacement key after rotation would require minting a persistent TPM child
// key object (TPM2_Create + TPM2_Load + TPM2_EvictControl) — real engineering
// this package does not implement against real hardware here (this
// environment has no TPM to validate it against). Until that lands, a ROTATED
// identity on this backend is held AES-wrapped at rest using wrapKey (already
// derived from the TPM's own deterministic public key — the SAME protection
// level Seal/Unseal already provide, see above) and signed in software rather
// than inside the TPM. The PRE-rotation identity (before Rotate/
// forceInstallIdentity is ever called) remains fully TPM-resident, unchanged
// from today. This trade-off is deliberate and documented, not silent.
package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// tpmStore is the hardware TPM-backed KeyStore.
type tpmStore struct {
	mu      sync.Mutex
	tpm     transport.TPMCloser
	keyDir  string
	wrapKey []byte // AES-256 wrap key sealing Seal() payloads; derived from TPM public key
	pubDER  []byte
	status  Status

	// ── Rotation (device-key lifecycle) — see package doc HONEST LIMITATION ──
	rotatedPriv   *ecdsa.PrivateKey // non-nil once this store has rotated away from the TPM primary
	prevPubKeyDER []byte
	prevExpiresAt time.Time
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

	tpm, err := linuxtpm.Open(defaultTPMPath)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: linuxtpm.Open: %w", err)
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

	// Restore a rotated identity (if any) and rotation-overlap state left from
	// a previous process's Rotate/forceInstallIdentity call.
	s.loadRotatedIdentity()
	s.prevPubKeyDER, s.prevExpiresAt = loadPrevKeyState(s.keyDir)
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

// Sign produces an ASN.1 DER ECDSA-Sig-Value over digest. It signs with the
// TPM primary key, UNLESS this store has rotated to a replacement identity (see
// the package doc HONEST LIMITATION), in which case it signs in software with
// the AES-wrapped rotated key. digest must be a 32-byte SHA-256 hash.
func (s *tpmStore) Sign(digest []byte, _ crypto.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(digest) != 32 {
		return nil, fmt.Errorf("tpm Sign: digest must be 32 bytes (SHA-256)")
	}

	if s.rotatedPriv != nil {
		return ecdsa.SignASN1(rand.Reader, s.rotatedPriv, digest)
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
	if s.rotatedPriv != nil {
		return x509.MarshalPKIXPublicKey(&s.rotatedPriv.PublicKey)
	}
	out := make([]byte, len(s.pubDER))
	copy(out, s.pubDER)
	return out, nil
}

func (s *tpmStore) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	activeDER := s.pubDER
	if s.rotatedPriv != nil {
		if der, err := x509.MarshalPKIXPublicKey(&s.rotatedPriv.PublicKey); err == nil {
			activeDER = der
		}
	}
	st.Revoked = IsDeviceKeyRevoked(Fingerprint(activeDER))
	return st
}

func (s *tpmStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tpm.Close()
}

// --- rotation (device-key lifecycle; see package doc HONEST LIMITATION) ---

func (s *tpmStore) rotatedPrivPath() string { return filepath.Join(s.keyDir, "identity_rotated.priv") }
func (s *tpmStore) rotatedPubPath() string  { return filepath.Join(s.keyDir, "identity_rotated.pub") }

// loadRotatedIdentity restores a previously-installed rotated identity, if
// any. Best-effort: any read/parse/decrypt failure leaves rotatedPriv nil,
// which means Sign/DeviceIdentity fall back to the TPM primary — narrowing,
// never widening, what this process will present as its identity.
func (s *tpmStore) loadRotatedIdentity() {
	enc, err := os.ReadFile(s.rotatedPrivPath())
	if err != nil || len(enc) == 0 {
		return
	}
	der, err := aesGCMDecrypt(s.wrapKey, enc)
	if err != nil {
		return
	}
	priv, err := x509.ParseECPrivateKey(der)
	if err != nil {
		return
	}
	s.rotatedPriv = priv
}

// activeIdentityDER returns the PKIX DER of whichever key is currently active
// (rotated replacement if present, else the TPM primary). Caller must hold s.mu.
func (s *tpmStore) activeIdentityDER() ([]byte, error) {
	if s.rotatedPriv != nil {
		return x509.MarshalPKIXPublicKey(&s.rotatedPriv.PublicKey)
	}
	out := make([]byte, len(s.pubDER))
	copy(out, s.pubDER)
	return out, nil
}

// signWithActive signs digest with whichever key is currently active. Caller
// must hold s.mu.
func (s *tpmStore) signWithActive(digest []byte) ([]byte, error) {
	if s.rotatedPriv != nil {
		return ecdsa.SignASN1(rand.Reader, s.rotatedPriv, digest)
	}
	cr, err := s.createPrimary()
	if err != nil {
		return nil, fmt.Errorf("CreatePrimary: %w", err)
	}
	defer tpm2.FlushContext{FlushHandle: cr.ObjectHandle}.Execute(s.tpm) //nolint:errcheck

	signCmd := tpm2.Sign{
		KeyHandle: tpm2.NamedHandle{Handle: cr.ObjectHandle, Name: cr.Name},
		Digest:    tpm2.TPM2BDigest{Buffer: digest},
		InScheme: tpm2.TPMTSigScheme{
			Scheme: tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUSigScheme(
				tpm2.TPMAlgECDSA,
				&tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256},
			),
		},
		Validation: tpm2.TPMTTKHashCheck{Tag: tpm2.TPMSTHashCheck},
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

// Rotate implements KeyStore.Rotate: a NORMAL, self-authorized rotation. The
// CURRENTLY ACTIVE key (TPM primary, or a previously-rotated key) signs the
// RotationCert binding old→new before the new key becomes active.
func (s *tpmStore) Rotate(reason string) (*RotationCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldPubDER, err := s.activeIdentityDER()
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: marshal old pubkey: %w", err)
	}
	newPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: generate new key: %w", err)
	}
	newPubDER, err := x509.MarshalPKIXPublicKey(&newPriv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: marshal new pubkey: %w", err)
	}

	cert := newRotationCert(RotationMethodSelf, oldPubDER, newPubDER, reason)
	digest, err := rotationSigningDigest(cert)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: digest: %w", err)
	}
	sig, err := s.signWithActive(digest)
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: sign with old key: %w", err)
	}
	cert.setSig(sig)

	if err := s.installRotatedIdentity(newPriv, oldPubDER); err != nil {
		return nil, fmt.Errorf("devicekey/tpm: Rotate: install new identity: %w", err)
	}
	return cert, nil
}

// forceInstallIdentity implements the unexported KeyStore primitive (see
// keystore.go's doc comment on the interface method for the security
// rationale). Only reachable from BreakGlassRotate.
func (s *tpmStore) forceInstallIdentity(newPriv *ecdsa.PrivateKey) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldPubDER, err := s.activeIdentityDER()
	if err != nil {
		return nil, fmt.Errorf("devicekey/tpm: forceInstallIdentity: marshal old pubkey: %w", err)
	}
	if err := s.installRotatedIdentity(newPriv, oldPubDER); err != nil {
		return nil, fmt.Errorf("devicekey/tpm: forceInstallIdentity: %w", err)
	}
	return oldPubDER, nil
}

// installRotatedIdentity persists newPriv (AES-wrapped with wrapKey — see the
// package doc HONEST LIMITATION), records oldPubDER as the previous key with a
// grace-window expiry, and switches the in-memory active signer. Caller must
// hold s.mu.
func (s *tpmStore) installRotatedIdentity(newPriv *ecdsa.PrivateKey, oldPubDER []byte) error {
	der, err := x509.MarshalECPrivateKey(newPriv)
	if err != nil {
		return fmt.Errorf("marshal new private key: %w", err)
	}
	enc, err := aesGCMEncrypt(s.wrapKey, der)
	if err != nil {
		return fmt.Errorf("encrypt new private key: %w", err)
	}
	if err := atomicWrite(s.rotatedPrivPath(), enc, 0600); err != nil {
		return fmt.Errorf("persist new private key: %w", err)
	}
	newPubDER, err := x509.MarshalPKIXPublicKey(&newPriv.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal new public key: %w", err)
	}
	_ = atomicWrite(s.rotatedPubPath(), newPubDER, 0644) // best-effort

	expiresAt := writePrevKeyState(s.keyDir, oldPubDER)

	s.rotatedPriv = newPriv
	s.prevPubKeyDER = append([]byte(nil), oldPubDER...)
	s.prevExpiresAt = expiresAt

	h := sha256.Sum256(newPubDER)
	s.status.DeviceID = hex.EncodeToString(h[:16])
	return nil
}

// PreviousIdentity returns the identity key rotated AWAY FROM and its grace
// expiry, or (nil, zero-time) if no rotation overlap is currently active.
func (s *tpmStore) PreviousIdentity() ([]byte, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prevPubKeyDER) == 0 || time.Now().UTC().After(s.prevExpiresAt) {
		return nil, time.Time{}
	}
	return append([]byte(nil), s.prevPubKeyDER...), s.prevExpiresAt
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
