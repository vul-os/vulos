package webhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// domain.go models custom domains attached to a site and defines the
// DomainManager seam. Attaching a domain returns the DNS the owner must
// publish; whether that domain ever goes "active" depends on the installed
// CertProvider (cert.go). With the default no-op provider a domain stays
// "pending" forever — honestly, because no cert can be issued in this build.

// Domain status values.
const (
	DomainStatusPending = "pending" // attached; awaiting DNS + certificate
	DomainStatusActive  = "active"  // DNS verified and a certificate is present
)

// acmeZone is the Vulos-controlled zone that owners CNAME-delegate the
// _acme-challenge record into (the acme-dns pattern; see cert.go).
const acmeZone = "acme.vulos.org"

// AttachResult is returned when a domain is attached to a site: the DNS records
// the owner must create for verification + routing.
type AttachResult struct {
	Status   string   `json:"status"`           // always DomainStatusPending on attach
	CNAME    string   `json:"cname"`            // the _acme-challenge delegation record to publish
	ARecords []string `json:"a_records"`        // instance IPs to point the domain at
	Domain   string   `json:"domain,omitempty"` // normalized domain
}

// DomainManager is the swappable custom-domain backend. Splitting it from the
// Service keeps the cert/DNS mechanics behind an interface the store does not
// need to know about.
type DomainManager interface {
	// AttachDomain records domain against (userID, site) and returns the DNS
	// the owner must publish. It does not wait for verification.
	AttachDomain(ctx context.Context, userID, site, domain string) (AttachResult, error)
	// DomainStatus reports the current status of an attached domain
	// (pending/active).
	DomainStatus(ctx context.Context, userID, site, domain string) (string, error)
	// DetachDomain removes a domain from a site.
	DetachDomain(ctx context.Context, userID, site, domain string) error
}

// Service implements DomainManager over its own meta store + CertProvider.
var _ DomainManager = (*Service)(nil)

// normalizeDomain lower-cases and trims a domain and does light structural
// validation. It intentionally rejects anything that is not a plausible
// hostname (spaces, path/scheme junk, leading/trailing dots, empty labels) so
// a bad value never reaches DNS instructions or the cert backend.
func normalizeDomain(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", fmt.Errorf("webhost: empty domain")
	}
	if len(d) > 253 {
		return "", fmt.Errorf("webhost: domain too long")
	}
	if strings.ContainsAny(d, " \t/\\:@?#") {
		return "", fmt.Errorf("webhost: invalid domain")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("webhost: domain must have at least two labels")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", fmt.Errorf("webhost: invalid domain label")
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("webhost: invalid character in domain")
			}
		}
		if strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return "", fmt.Errorf("webhost: label may not start or end with '-'")
		}
	}
	return d, nil
}

// randToken returns a random hex acme-dns delegation label.
func randToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failing is fatal-adjacent; return an obviously-unusable
		// token rather than a predictable one.
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// AttachDomain records domain against a site and returns the DNS the owner must
// publish. The site must already exist. A domain that is already attached is
// returned idempotently with its existing delegation token.
func (s *Service) AttachDomain(ctx context.Context, userID, site, domain string) (AttachResult, error) {
	d, err := normalizeDomain(domain)
	if err != nil {
		return AttachResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	base, err := s.siteBase(userID, site)
	if err != nil {
		return AttachResult{}, err
	}
	if _, statErr := os.Stat(base); statErr != nil {
		return AttachResult{}, ErrNotFound
	}

	meta, err := s.loadMeta(userID, site)
	if err != nil {
		return AttachResult{}, err
	}
	if meta.AcmeTokens == nil {
		meta.AcmeTokens = map[string]string{}
	}

	token, existed := meta.AcmeTokens[d]
	if !existed {
		token = randToken()
		meta.AcmeTokens[d] = token
		meta.Domains = append(meta.Domains, Domain{Name: d, Status: DomainStatusPending})
		if err := s.saveMeta(userID, site, meta); err != nil {
			return AttachResult{}, err
		}
	}

	return AttachResult{
		Status:   DomainStatusPending,
		Domain:   d,
		CNAME:    fmt.Sprintf("_acme-challenge.%s CNAME %s.%s", d, token, acmeZone),
		ARecords: append([]string(nil), s.instanceIPs...),
	}, nil
}

// DomainStatus reports pending/active for an attached domain. A domain reaches
// "active" only when the installed CertProvider confirms it holds a
// certificate; with the default no-op provider this never happens (and the
// persisted status is updated to match on read).
func (s *Service) DomainStatus(ctx context.Context, userID, site, domain string) (string, error) {
	d, err := normalizeDomain(domain)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.loadMeta(userID, site)
	if err != nil {
		return "", err
	}
	idx := -1
	for i := range meta.Domains {
		if meta.Domains[i].Name == d {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", ErrNotFound
	}

	status := DomainStatusPending
	if s.certs != nil && s.certs.HasCert(d) {
		status = DomainStatusActive
	}
	// Persist a status transition if it changed (best-effort).
	if meta.Domains[idx].Status != status {
		meta.Domains[idx].Status = status
		_ = s.saveMeta(userID, site, meta)
	}
	return status, nil
}

// DetachDomain removes a domain from a site. Detaching a domain that is not
// attached is a no-op success (idempotent).
func (s *Service) DetachDomain(ctx context.Context, userID, site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.loadMeta(userID, site)
	if err != nil {
		return err
	}
	kept := meta.Domains[:0]
	for _, dm := range meta.Domains {
		if dm.Name != d {
			kept = append(kept, dm)
		}
	}
	meta.Domains = kept
	if meta.AcmeTokens != nil {
		delete(meta.AcmeTokens, d)
	}
	return s.saveMeta(userID, site, meta)
}
