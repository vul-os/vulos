package lan

// Package documentation now lives in doc.go (FIX-LAN-PATH-CONST-01) — it
// covers both the LAN reachability rationale and the cross-repo cert delivery
// contract with vulos-cloud's lancert package.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
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
type SelfSignedCertSource struct {
	once  sync.Once
	mu    sync.RWMutex
	cert  *tls.Certificate
	err   error
	hosts []string
	ips   []net.IP
	ttl   time.Duration
}

// NewSelfSignedCertSource returns a self-signed source covering the given DNS
// names and IP addresses. The certificate is generated lazily on first use and
// cached for the lifetime of the source.
func NewSelfSignedCertSource(hosts []string, ips []net.IP) *SelfSignedCertSource {
	return &SelfSignedCertSource{
		hosts: append([]string(nil), hosts...),
		ips:   append([]net.IP(nil), ips...),
		ttl:   10 * 365 * 24 * time.Hour, // long-lived; this is dev/offline material
	}
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
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Self-signed leaf used directly; mark it as a CA so it can be added to a
		// trust store in dev if desired.
		BasicConstraintsValid: true,
		IsCA:                  true,
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
