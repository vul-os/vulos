package lan

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spkiFingerprint returns the SHA-256 of a certificate's SubjectPublicKeyInfo
// — this is what certificate pinning anchors to, and what must survive a
// key-reuse restart even though the certificate bytes themselves (serial,
// NotBefore, ...) change every time generate() runs.
func spkiFingerprint(t *testing.T, cert *tls.Certificate) [32]byte {
	t.Helper()
	if cert.Leaf == nil {
		t.Fatal("cert has no parsed Leaf")
	}
	return sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)
}

// TestSelfSignedCertSource_ReusesPersistedKeyAcrossInstances is the
// FIX-LAN-KEY-PERSIST-01 core regression guard: a fresh SelfSignedCertSource
// pointed at the same key path as a prior one (simulating a process
// restart) must produce a certificate with the IDENTICAL SPKI, so
// certificate pins and "I accepted this cert" browser exceptions survive a
// reboot.
func TestSelfSignedCertSource_ReusesPersistedKeyAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "tls", "lan-selfsigned.key")
	hosts := []string{"vulos.local"}
	ips := []net.IP{net.IPv4(192, 168, 1, 50)}

	src1 := NewSelfSignedCertSourceWithKeyPath(hosts, ips, keyPath)
	cert1, err := src1.Certificate(nil)
	if err != nil {
		t.Fatalf("first Certificate: %v", err)
	}

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected key file to be persisted at %s: %v", keyPath, err)
	}
	if perm := fi.Mode().Perm(); perm != keyFileMode {
		t.Errorf("key file mode = %v, want %v", perm, keyFileMode)
	}
	if dirFi, err := os.Stat(filepath.Dir(keyPath)); err != nil {
		t.Fatalf("stat key dir: %v", err)
	} else if perm := dirFi.Mode().Perm(); perm != keyDirMode {
		t.Errorf("key dir mode = %v, want %v", perm, keyDirMode)
	}

	// Simulate a restart: a brand new source object, same key path, same
	// process otherwise gone.
	src2 := NewSelfSignedCertSourceWithKeyPath(hosts, ips, keyPath)
	cert2, err := src2.Certificate(nil)
	if err != nil {
		t.Fatalf("second Certificate: %v", err)
	}

	fp1 := spkiFingerprint(t, cert1)
	fp2 := spkiFingerprint(t, cert2)
	if fp1 != fp2 {
		t.Fatalf("SPKI fingerprint changed across restart with the same key path:\n  before: %x\n  after:  %x", fp1, fp2)
	}

	// Sanity: the certs are still distinct objects/DER (fresh serial/validity
	// each time) — only the key (and therefore SPKI) is expected to be stable.
	if bytes.Equal(cert1.Certificate[0], cert2.Certificate[0]) {
		t.Log("note: certificate DER happened to be byte-identical (serial collision is astronomically unlikely but harmless if it happened)")
	}
}

// TestSelfSignedCertSource_RegeneratesWhenKeyFileMissing proves the other
// half of the contract: no persisted key (fresh box, or the file was
// deleted) means a NEW key — and therefore a different SPKI — every time,
// and that a fresh key file is written with the correct mode.
func TestSelfSignedCertSource_RegeneratesWhenKeyFileMissing(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "tls", "lan-selfsigned.key")
	hosts := []string{"vulos.local"}

	src1 := NewSelfSignedCertSourceWithKeyPath(hosts, nil, keyPath)
	cert1, err := src1.Certificate(nil)
	if err != nil {
		t.Fatalf("first Certificate: %v", err)
	}
	fp1 := spkiFingerprint(t, cert1)

	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key file: %v", err)
	}

	src2 := NewSelfSignedCertSourceWithKeyPath(hosts, nil, keyPath)
	cert2, err := src2.Certificate(nil)
	if err != nil {
		t.Fatalf("second Certificate: %v", err)
	}
	fp2 := spkiFingerprint(t, cert2)

	if fp1 == fp2 {
		t.Fatal("expected a DIFFERENT SPKI after deleting the key file and restarting, got the same one")
	}

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected key file to be recreated at %s: %v", keyPath, err)
	}
	if perm := fi.Mode().Perm(); perm != keyFileMode {
		t.Errorf("recreated key file mode = %v, want %v", perm, keyFileMode)
	}
}

// TestSelfSignedCertSource_RotatesWorldAccessibleKey is the leak-response
// regression guard: if the persisted key file's permissions have been
// widened to be world-accessible (e.g. by a misconfigured backup tool or a
// bug elsewhere), the box must NOT silently keep using it — it must rotate
// to a fresh key (different SPKI) and log loudly, trading one browser
// warning for not reusing potentially-compromised key material.
func TestSelfSignedCertSource_RotatesWorldAccessibleKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "tls", "lan-selfsigned.key")
	hosts := []string{"vulos.local"}

	src1 := NewSelfSignedCertSourceWithKeyPath(hosts, nil, keyPath)
	cert1, err := src1.Certificate(nil)
	if err != nil {
		t.Fatalf("first Certificate: %v", err)
	}
	fp1 := spkiFingerprint(t, cert1)

	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod key file world-readable: %v", err)
	}

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	src2 := NewSelfSignedCertSourceWithKeyPath(hosts, nil, keyPath)
	cert2, err := src2.Certificate(nil)
	if err != nil {
		t.Fatalf("second Certificate: %v", err)
	}
	fp2 := spkiFingerprint(t, cert2)

	if fp1 == fp2 {
		t.Fatal("expected rotation (different SPKI) when the key file was world-accessible, got the same key reused")
	}

	logged := logBuf.String()
	if !strings.Contains(strings.ToLower(logged), "world-accessible") || !strings.Contains(strings.ToLower(logged), "rotat") {
		t.Errorf("expected a loud log about world-accessible + rotation, got: %q", logged)
	}

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat rotated key file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != keyFileMode {
		t.Errorf("rotated key file mode = %v, want %v (rotation must fix permissions too)", perm, keyFileMode)
	}
}

// TestSelfSignedCertSource_EmptyKeyPathNeverPersists documents the
// zero-value/no-path fallback: a source with no keyPath configured behaves
// like pre-fix code (fresh key every generate, nothing written to disk).
// This matters for any caller that constructs SelfSignedCertSource directly
// rather than via the New* constructors.
func TestSelfSignedCertSource_EmptyKeyPathNeverPersists(t *testing.T) {
	s := &SelfSignedCertSource{hosts: []string{"vulos.local"}}
	priv1, err := s.loadOrCreateKey()
	if err != nil {
		t.Fatalf("loadOrCreateKey: %v", err)
	}
	priv2, err := s.loadOrCreateKey()
	if err != nil {
		t.Fatalf("loadOrCreateKey (2nd): %v", err)
	}
	if priv1.D.Cmp(priv2.D) == 0 {
		t.Fatal("expected two independent in-memory keys when keyPath is empty, got the same key twice")
	}
}
