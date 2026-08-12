package devicekey

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- helper: open a software store in a temp dir ---

func newTestSoftwareStore(t *testing.T) *softwareStore {
	t.Helper()
	dir := t.TempDir()
	ks, err := openSoftware(dir)
	if err != nil {
		t.Fatalf("openSoftware: %v", err)
	}
	return ks
}

// --- Open() falls back to software when no hardware TPM is present ---

func TestOpen_FallsBackToSoftware(t *testing.T) {
	dir := t.TempDir()
	ks, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ks.Close()

	st := ks.Status()
	// On CI / dev machines without a TPM, expect software backend.
	// On hardware with a TPM, hardware backend is also acceptable.
	if st.Backend != BackendSoftware && st.Backend != BackendHardware {
		t.Errorf("unexpected backend %q", st.Backend)
	}
	if !st.Available {
		t.Error("Status.Available should be true")
	}
	if st.DeviceID == "" {
		t.Error("Status.DeviceID should not be empty")
	}
}

// --- Software backend: seal / unseal round-trip ---

func TestSoftware_SealUnsealRoundTrip(t *testing.T) {
	ks := newTestSoftwareStore(t)

	plaintext := []byte("secret data for Vulos OS AUTH-09")
	ct, err := ks.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	got, err := ks.Unseal(ct)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip failed: got %q, want %q", got, plaintext)
	}
}

// --- Software backend: empty plaintext ---

func TestSoftware_SealUnseal_EmptyPlaintext(t *testing.T) {
	ks := newTestSoftwareStore(t)
	ct, err := ks.Seal([]byte{})
	if err != nil {
		t.Fatalf("Seal empty: %v", err)
	}
	got, err := ks.Unseal(ct)
	if err != nil {
		t.Fatalf("Unseal empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(got))
	}
}

// --- Software backend: tampered ciphertext returns error ---

func TestSoftware_Unseal_TamperedCiphertext(t *testing.T) {
	ks := newTestSoftwareStore(t)
	ct, _ := ks.Seal([]byte("hello"))
	// Flip a byte in the ciphertext portion.
	ct[len(ct)-2] ^= 0xFF
	_, err := ks.Unseal(ct)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext, got nil")
	}
}

// --- Software backend: Sign produces a valid ECDSA signature ---

func TestSoftware_Sign(t *testing.T) {
	ks := newTestSoftwareStore(t)

	msg := []byte("vulos device sign test")
	digest := sha256.Sum256(msg)

	sig, err := ks.Sign(digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("signature is empty")
	}

	// Verify using the public key returned by DeviceIdentity.
	pubDER, err := ks.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity: %v", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("ParsePKIXPublicKey: %v", err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("DeviceIdentity did not return an ECDSA key")
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Error("signature verification failed")
	}
}

// --- Software backend: stable device identity across reopen ---

func TestSoftware_StableDeviceIdentity(t *testing.T) {
	dir := t.TempDir()

	ks1, err := openSoftware(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	pub1, err := ks1.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity (1): %v", err)
	}
	_ = ks1.Close()

	ks2, err := openSoftware(dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	pub2, err := ks2.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity (2): %v", err)
	}
	_ = ks2.Close()

	if !bytes.Equal(pub1, pub2) {
		t.Error("device identity changed across reopen — must be stable")
	}
}

// --- Software backend: sealed data survives reopen ---

func TestSoftware_SealedDataSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	secret := []byte("persist me across restarts")

	ks1, _ := openSoftware(dir)
	ct, err := ks1.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_ = ks1.Close()

	ks2, _ := openSoftware(dir)
	got, err := ks2.Unseal(ct)
	if err != nil {
		t.Fatalf("Unseal after reopen: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("want %q, got %q", secret, got)
	}
	_ = ks2.Close()
}

// --- Software backend: Status fields ---

func TestSoftware_Status(t *testing.T) {
	ks := newTestSoftwareStore(t)
	st := ks.Status()

	if st.Backend != BackendSoftware {
		t.Errorf("expected BackendSoftware, got %q", st.Backend)
	}
	if !st.Available {
		t.Error("Status.Available should be true")
	}
	if st.DeviceID == "" {
		t.Error("Status.DeviceID should not be empty")
	}
	if st.DevicePath != "" {
		t.Errorf("software backend should have empty DevicePath, got %q", st.DevicePath)
	}
	if st.KeyDir == "" {
		t.Error("Status.KeyDir should not be empty")
	}
}

// --- Software backend: files are created under keyDir ---

func TestSoftware_KeyFilesPresent(t *testing.T) {
	dir := t.TempDir()
	ks, _ := openSoftware(dir)
	_ = ks.Close()

	for _, name := range []string{"device.secret", "wrap.key", "device_key.priv", "device_key.pub"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected key file %q to exist: %v", path, err)
		}
	}
}

// --- Software backend: concurrent Seal/Unseal is safe ---

func TestSoftware_ConcurrentSealUnseal(t *testing.T) {
	ks := newTestSoftwareStore(t)
	const goroutines = 20
	errc := make(chan error, goroutines*2)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			pt := []byte("payload")
			ct, err := ks.Seal(pt)
			if err != nil {
				errc <- err
				return
			}
			got, err := ks.Unseal(ct)
			if err != nil {
				errc <- err
				return
			}
			if !bytes.Equal(got, pt) {
				errc <- fmt.Errorf("goroutine %d: round-trip mismatch", i)
				return
			}
			errc <- nil
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errc; err != nil {
			t.Error(err)
		}
	}
}

// --- Open() respects custom keyDir ---

func TestOpen_CustomKeyDir(t *testing.T) {
	dir := t.TempDir()
	ks, err := Open(dir)
	if err != nil {
		t.Fatalf("Open with custom dir: %v", err)
	}
	defer ks.Close()

	st := ks.Status()
	if st.KeyDir != dir {
		t.Errorf("KeyDir = %q, want %q", st.KeyDir, dir)
	}
}
