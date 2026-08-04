package lan

// Package documentation now lives in doc.go (FIX-LAN-PATH-CONST-01) — it
// covers both the LAN reachability rationale and the cert delivery contract
// with the externally operated LAN-cert issuer.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/internal/datadir"
)

// CertSource supplies the TLS certificate (and matching private key) that the
// LAN HTTPS listener serves for the box's LAN hostname.
//
// It is deliberately small so that the production implementation — issued
// externally by the cloud via ACME DNS-01 (task LANCERT-01) — can plug in
// without touching the rest of this package. A DNS-01 source will typically
// watch a cert file on disk and hot-swap it on renewal; that is why Certificate
// returns a fresh *tls.Certificate on every call (and why callers should wire it
// through tls.Config.GetCertificate rather than caching the result).
type CertSource interface {
	// Certificate returns the current certificate+key for hello.ServerName.
	// The ClientHelloInfo may be nil when a caller just wants the default
	// certificate (e.g. for inspection or tests). Implementations must be safe
	// for concurrent use.
	Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// TLSConfig builds a *tls.Config whose certificate is resolved on every
// handshake from src. This keeps hot-reload semantics: when src starts returning
// a renewed cert (e.g. after an ACME DNS-01 renewal), new connections pick it up
// with no listener restart.
func TLSConfig(src CertSource) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return src.Certificate(hello)
		},
	}
}

// SelfSignedCertSource is a dev/offline CertSource that mints a single
// self-signed certificate covering the supplied hostnames + LAN IPs. It lets the
// LAN HTTPS path build and be tested without the external (cloud-issued) cert.
//
// In production the trusted, no-warning cert comes from LANCERT-01; this source
// is the fallback so a box is never left without TLS material.
//
// PERSISTED IDENTITY (FIX-LAN-KEY-PERSIST-01): only the PRIVATE KEY is
// persisted to disk, at keyPath. The certificate itself is always re-minted in
// memory from that key using the CURRENT hosts/ips at process start. This is
// deliberate: certificate pinning (and a browser's "I accepted this
// self-signed cert" exception) is anchored to the key's SPKI, not to the
// certificate bytes, so reusing the same key across reboots/IP changes keeps
// every pin and every browser exception valid while still letting the cert's
// SANs track the box's current LAN IP. See defaultSelfSignedKeyPath for the
// on-disk location and loadOrCreateKey for the reuse/rotate rules.
type SelfSignedCertSource struct {
	once    sync.Once
	mu      sync.RWMutex
	cert    *tls.Certificate
	err     error
	hosts   []string
	ips     []net.IP
	ttl     time.Duration
	keyPath string
}

// NewSelfSignedCertSource returns a self-signed source covering the given DNS
// names and IP addresses. The certificate is generated lazily on first use and
// cached for the lifetime of the source. The private key backing it is
// persisted under the box data dir (defaultSelfSignedKeyPath) and reused
// across restarts; see loadOrCreateKey.
func NewSelfSignedCertSource(hosts []string, ips []net.IP) *SelfSignedCertSource {
	return NewSelfSignedCertSourceWithKeyPath(hosts, ips, defaultSelfSignedKeyPath())
}

// NewSelfSignedCertSourceWithKeyPath is like NewSelfSignedCertSource but lets
// the caller override where the private key is persisted. Production callers
// should use NewSelfSignedCertSource (default: <data dir>/tls/lan-selfsigned.key,
// following the datadir.Join convention used elsewhere in backend/); this
// exists mainly so tests can point at a temp dir instead of the real box data
// dir.
func NewSelfSignedCertSourceWithKeyPath(hosts []string, ips []net.IP, keyPath string) *SelfSignedCertSource {
	return &SelfSignedCertSource{
		hosts:   append([]string(nil), hosts...),
		ips:     append([]net.IP(nil), ips...),
		ttl:     10 * 365 * 24 * time.Hour, // long-lived; this is dev/offline material
		keyPath: keyPath,
	}
}

// defaultSelfSignedKeyPath is where the self-signed LAN identity's private
// key is persisted so a reboot reuses the same SPKI (see the type doc on
// SelfSignedCertSource for why that matters). Follows the datadir.Join
// convention every other on-disk key/state store in backend/ uses (e.g.
// datadir.Join("auth", "tpm") for the device key store).
func defaultSelfSignedKeyPath() string {
	return datadir.Join("tls", "lan-selfsigned.key")
}

// Certificate implements CertSource. The ClientHelloInfo is ignored — the
// self-signed cert already carries every configured SAN.
func (s *SelfSignedCertSource) Certificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.once.Do(func() {
		cert, err := s.generate()
		s.mu.Lock()
		s.cert, s.err = cert, err
		s.mu.Unlock()
	})
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cert, s.err
}

func (s *SelfSignedCertSource) generate() (*tls.Certificate, error) {
	priv, err := s.loadOrCreateKey()
	if err != nil {
		return nil, fmt.Errorf("lan: generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("lan: serial: %w", err)
	}

	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Vulos LAN (self-signed)", Organization: []string{"Vulos"}},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(s.ttl),
		// SECURITY (audit P0-1): this is a self-signed TLS *leaf* served directly
		// by the LAN HTTPS listener — it is NOT a certificate authority. It must
		// therefore carry only the key usages a server leaf needs (digital
		// signature for the TLS handshake signature, key encipherment for RSA
		// kex; harmless for ECDSA) plus the serverAuth ext-key-usage. It must
		// NEVER carry KeyUsageCertSign / IsCA: a CA-capable cert added to a trust
		// store could sign arbitrary certificates for any name.
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Mark BasicConstraints as present-and-not-a-CA so the IsCA:false is
		// explicit and authoritative on the wire (a missing extension is
		// ambiguous; an explicit CA:FALSE is not).
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              s.hosts,
		IPAddresses:           s.ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("lan: create cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("lan: marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("lan: x509 keypair: %w", err)
	}
	// Keep the parsed leaf around so callers/tests can inspect SANs cheaply.
	if leaf, err := x509.ParseCertificate(der); err == nil {
		tlsCert.Leaf = leaf
	}
	return &tlsCert, nil
}

// worldAccessibleMask is the "other" permission bits (r/w/x for users outside
// the owner and group). A key file with any of these set is readable by any
// local user/process on the box, so we treat it as leaked.
const worldAccessibleMask = 0o007

// keyFileMode and keyDirMode are the on-disk permissions the persisted
// private key (and its parent dir) must have. Chosen so the key is readable
// only by the box process's own user.
const (
	keyFileMode os.FileMode = 0o600
	keyDirMode  os.FileMode = 0o700
)

// loadOrCreateKey resolves s.keyPath to a P-256 private key:
//
//   - If s.keyPath is empty (e.g. a zero-value SelfSignedCertSource used
//     directly in a test), a key is generated in memory only — nothing is
//     persisted and every call mints a new identity, matching the pre-persistence
//     behaviour.
//   - If no file exists yet, a new key is generated and persisted.
//   - If a file exists but is WORLD-ACCESSIBLE (mode & worldAccessibleMask !=
//     0), it is treated as compromised: this logs loudly and rotates to a
//     freshly generated + persisted key rather than silently reusing
//     potentially-leaked key material. This costs one browser warning /
//     re-pin; reusing a leaked key would cost the security property entirely.
//   - If a file exists, has safe permissions, and parses as a P-256 EC private
//     key, it is reused as-is — this is the normal reboot path and is what
//     keeps the SPKI (and therefore every certificate pin and browser
//     "accept this cert" exception) stable across restarts.
//   - If a file exists with safe permissions but fails to read/decode/parse,
//     it is treated the same as "missing": log and generate+persist a
//     replacement rather than failing the whole LAN HTTPS path over one bad
//     file.
func (s *SelfSignedCertSource) loadOrCreateKey() (*ecdsa.PrivateKey, error) {
	if s.keyPath == "" {
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}

	fi, statErr := os.Stat(s.keyPath)
	switch {
	case statErr == nil:
		if fi.Mode().Perm()&worldAccessibleMask != 0 {
			log.Printf("[lan] SECURITY: self-signed TLS key at %s is world-accessible (mode %s) — treating it as compromised and ROTATING to a freshly generated key. This invalidates existing certificate pins/browser exceptions once, which is the price of not reusing a potentially leaked key.", s.keyPath, fi.Mode().Perm())
			return s.generateAndPersist()
		}
		priv, err := loadKeyFile(s.keyPath)
		if err != nil {
			log.Printf("[lan] WARNING: could not reuse persisted self-signed TLS key at %s (%v) — generating a replacement", s.keyPath, err)
			return s.generateAndPersist()
		}
		return priv, nil
	case os.IsNotExist(statErr):
		return s.generateAndPersist()
	default:
		log.Printf("[lan] WARNING: could not stat self-signed TLS key at %s (%v) — generating a key (best-effort persist)", s.keyPath, statErr)
		return s.generateAndPersist()
	}
}

// loadKeyFile reads and parses a persisted EC private key, verifying it is on
// the P-256 curve this package always issues (defense in depth against a
// corrupted or foreign file being silently trusted).
func loadKeyFile(path string) (*ecdsa.PrivateKey, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("not a PEM EC PRIVATE KEY block")
	}
	priv, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("unexpected curve %s (want P-256)", priv.Curve.Params().Name)
	}
	return priv, nil
}

// generateAndPersist creates a fresh P-256 key and best-effort persists it to
// s.keyPath (0600, parent dir 0700). A persistence failure is logged but does
// not fail cert generation — the box must never be left without LAN TLS
// material just because e.g. the data dir is temporarily unwritable; the
// only consequence of a failed persist is that the identity does not survive
// this process's next restart, which is exactly today's (pre-fix) behaviour.
func (s *SelfSignedCertSource) generateAndPersist() (*ecdsa.PrivateKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}
	if err := persistKeyFile(s.keyPath, priv); err != nil {
		log.Printf("[lan] WARNING: could not persist self-signed TLS key to %s (%v) — continuing with an in-memory-only key; it will NOT survive a restart", s.keyPath, err)
	}
	return priv, nil
}

// persistKeyFile writes priv to path as a PEM-encoded SEC1 EC private key,
// creating the parent directory (0700) if needed and forcing both the
// directory and file to their required modes even if they already existed
// with looser permissions (a plain os.WriteFile only applies its mode bits
// through umask on CREATE, so an existing over-permissive file would
// otherwise stay over-permissive).
func persistKeyFile(path string, priv *ecdsa.PrivateKey) error {
	if path == "" {
		return fmt.Errorf("no key path configured")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, keyDirMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, keyDirMode); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}

	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	// Write to a temp file in the same dir then rename, so a crash mid-write
	// never leaves a truncated/corrupt key file for the next boot to trip
	// over. os.WriteFile only guarantees the mode on create, so we still
	// Chmod explicitly before the rename.
	tmp, err := os.CreateTemp(dir, ".lan-selfsigned.key.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp key file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(keyPEM); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp key file: %w", err)
	}
	if err := os.Chmod(tmpPath, keyFileMode); err != nil {
		return fmt.Errorf("chmod temp key file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
