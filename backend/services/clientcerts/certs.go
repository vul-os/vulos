// Package clientcerts provides per-domain client certificate (mTLS) storage and
// management for Vula OS. Private keys are sealed via the devicekey.KeyStore so
// they are never stored in plaintext on disk.
//
// On-disk layout under ~/.vulos/auth/certificates/<domain>/:
//
//	client.crt      – PEM-encoded X.509 client certificate
//	client.key.enc  – private key (PEM bytes) sealed by devicekey.KeyStore
//	ca-chain.pem    – optional PEM bundle of the issuer's certificate chain
//
// All operations are safe for concurrent use.
package clientcerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/devicekey"
)

// CertInfo is the metadata returned by list and status endpoints.
type CertInfo struct {
	Domain    string    `json:"domain"`
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Expired   bool      `json:"expired"`
	HasKey    bool      `json:"has_key"`
	HasChain  bool      `json:"has_chain"`
}

// CSRInfo is returned after generating a CSR.
type CSRInfo struct {
	Domain string `json:"domain"`
	PEM    string `json:"pem"`
}

// Store manages client certificates for multiple domains.
type Store struct {
	mu      sync.RWMutex
	baseDir string // ~/.vulos/auth/certificates
	ks      devicekey.KeyStore
}

// NewStore creates a Store rooted at baseDir, using ks to seal/unseal private
// keys.  If baseDir is "" it defaults to ~/.vulos/auth/certificates.
func NewStore(baseDir string, ks devicekey.KeyStore) (*Store, error) {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("clientcerts: cannot determine home dir: %w", err)
		}
		baseDir = filepath.Join(home, ".vulos", "auth", "certificates")
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("clientcerts: cannot create base dir %q: %w", baseDir, err)
	}
	return &Store{baseDir: baseDir, ks: ks}, nil
}

// domainDir returns the per-domain directory, sanitising the name to avoid
// path traversal.
func (s *Store) domainDir(domain string) (string, error) {
	clean := filepath.Clean(domain)
	if clean == "." || strings.ContainsAny(clean, "/\\") {
		return "", fmt.Errorf("clientcerts: invalid domain %q", domain)
	}
	return filepath.Join(s.baseDir, clean), nil
}

// Install stores a client certificate and its private key for domain.
// certPEM must be a valid PEM-encoded X.509 certificate.
// keyPEM must be a valid PEM-encoded private key (PKCS#8 or EC).
// chainPEM is optional; supply "" or nil to skip.
func (s *Store) Install(domain, certPEM, keyPEM string, chainPEM string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate cert.
	cert, err := parseCert(certPEM)
	if err != nil {
		return fmt.Errorf("clientcerts: invalid certificate: %w", err)
	}
	_ = cert // used for validation only; we store the raw PEM

	// Validate key parses as something usable.
	if _, err := parsePrivKey(keyPEM); err != nil {
		return fmt.Errorf("clientcerts: invalid private key: %w", err)
	}

	dir, err := s.domainDir(domain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("clientcerts: create domain dir: %w", err)
	}

	// Store cert in plaintext (it's public information).
	if err := atomicWrite(filepath.Join(dir, "client.crt"), []byte(certPEM), 0644); err != nil {
		return fmt.Errorf("clientcerts: write cert: %w", err)
	}

	// Seal and store private key.
	sealed, err := s.ks.Seal([]byte(keyPEM))
	if err != nil {
		return fmt.Errorf("clientcerts: seal private key: %w", err)
	}
	if err := atomicWrite(filepath.Join(dir, "client.key.enc"), sealed, 0600); err != nil {
		return fmt.Errorf("clientcerts: write sealed key: %w", err)
	}

	// Store chain if provided.
	if chainPEM != "" {
		if err := atomicWrite(filepath.Join(dir, "ca-chain.pem"), []byte(chainPEM), 0644); err != nil {
			return fmt.Errorf("clientcerts: write ca chain: %w", err)
		}
	}

	return nil
}

// List returns metadata for every domain that has an installed certificate.
func (s *Store) List() ([]CertInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("clientcerts: read base dir: %w", err)
	}

	var infos []CertInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		domain := e.Name()
		info, err := s.statusLocked(domain)
		if err != nil {
			// Skip domains where we cannot read the cert.
			continue
		}
		infos = append(infos, *info)
	}
	return infos, nil
}

// Status returns metadata for a single domain.
func (s *Store) Status(domain string) (*CertInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusLocked(domain)
}

// statusLocked must be called with at least a read lock held.
func (s *Store) statusLocked(domain string) (*CertInfo, error) {
	dir, err := s.domainDir(domain)
	if err != nil {
		return nil, err
	}

	certData, err := os.ReadFile(filepath.Join(dir, "client.crt"))
	if err != nil {
		return nil, fmt.Errorf("clientcerts: read cert for %q: %w", domain, err)
	}

	cert, err := parseCert(string(certData))
	if err != nil {
		return nil, fmt.Errorf("clientcerts: parse cert for %q: %w", domain, err)
	}

	_, hasKeyErr := os.Stat(filepath.Join(dir, "client.key.enc"))
	_, hasChainErr := os.Stat(filepath.Join(dir, "ca-chain.pem"))

	return &CertInfo{
		Domain:    domain,
		Issuer:    cert.Issuer.String(),
		Subject:   cert.Subject.String(),
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
		Expired:   time.Now().After(cert.NotAfter),
		HasKey:    hasKeyErr == nil,
		HasChain:  hasChainErr == nil,
	}, nil
}

// Delete removes all files for domain.
func (s *Store) Delete(domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.domainDir(domain)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clientcerts: domain %q not found", domain)
	}
	return os.RemoveAll(dir)
}

// UnsealKey retrieves and unseals the private key for domain, returning its
// PEM representation.  This is intentionally separate from Status so that key
// material is only decrypted when explicitly requested.
func (s *Store) UnsealKey(domain string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir, err := s.domainDir(domain)
	if err != nil {
		return "", err
	}

	sealed, err := os.ReadFile(filepath.Join(dir, "client.key.enc"))
	if err != nil {
		return "", fmt.Errorf("clientcerts: read sealed key for %q: %w", domain, err)
	}

	plain, err := s.ks.Unseal(sealed)
	if err != nil {
		return "", fmt.Errorf("clientcerts: unseal key for %q: %w", domain, err)
	}
	return string(plain), nil
}

// CSRRequest specifies the parameters for generating a new CSR.
type CSRRequest struct {
	Domain string   // domain label used to store the private key (required)
	CN     string   // common name for the CSR subject (required)
	SANs   []string // additional subject alternative names (DNS or IP)
	O      string   // organisation (optional)
	C      string   // country code (optional)
}

// GenerateCSR creates a fresh ECDSA P-256 key pair for domain, seals and
// stores the private key, and returns a PEM-encoded CSR.
// The generated key is stored under the domain directory so it is ready when
// the signed certificate comes back (call Install to add the cert).
func (s *Store) GenerateCSR(req CSRRequest) (*CSRInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Domain == "" || req.CN == "" {
		return nil, errors.New("clientcerts: domain and CN are required for CSR generation")
	}

	// Generate a new P-256 key pair.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("clientcerts: generate key: %w", err)
	}

	// Build subject.
	subj := pkix.Name{CommonName: req.CN}
	if req.O != "" {
		subj.Organization = []string{req.O}
	}
	if req.C != "" {
		subj.Country = []string{req.C}
	}

	// Collect SANs.
	tmpl := &x509.CertificateRequest{Subject: subj}
	for _, san := range req.SANs {
		if ip := net.ParseIP(san); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, san)
		}
	}

	// Create the CSR.
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, priv)
	if err != nil {
		return nil, fmt.Errorf("clientcerts: create CSR: %w", err)
	}

	// Validate the CSR round-trips correctly.
	if _, err := x509.ParseCertificateRequest(csrDER); err != nil {
		return nil, fmt.Errorf("clientcerts: CSR validation failed: %w", err)
	}

	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	// Marshal and seal the private key.
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("clientcerts: marshal key: %w", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))

	sealed, err := s.ks.Seal([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("clientcerts: seal generated key: %w", err)
	}

	// Persist the sealed key under the domain directory.
	dir, err := s.domainDir(req.Domain)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("clientcerts: create domain dir: %w", err)
	}
	if err := atomicWrite(filepath.Join(dir, "client.key.enc"), sealed, 0600); err != nil {
		return nil, fmt.Errorf("clientcerts: write sealed key: %w", err)
	}

	return &CSRInfo{Domain: req.Domain, PEM: csrPEM}, nil
}

// --- helpers ---

// parseCert decodes the first PEM certificate block in s and parses it.
func parseCert(s string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no valid CERTIFICATE PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// parsePrivKey tries to parse PEM-encoded private key material using PKCS#8
// then EC-specific encoding.
func parsePrivKey(s string) (any, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("no valid PEM block found")
	}
	switch block.Type {
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM type %q", block.Type)
	}
}

// atomicWrite writes data to path via a temp file + rename.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
