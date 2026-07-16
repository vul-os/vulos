package lancert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// DefaultLANDomain is the public DNS suffix under which per-box LAN
// hostnames are issued. The full hostname is `box.<box-id>.<DefaultLANDomain>`.
const DefaultLANDomain = "lan.vulos.org"

// DefaultRenewWindow is how far before NotAfter the renewer kicks in.
// LE recommends 30 days for 90-day certs.
const DefaultRenewWindow = 30 * 24 * time.Hour

// DefaultRenewInterval is how often the renewal loop wakes up to check.
const DefaultRenewInterval = 12 * time.Hour

// DefaultARecordTTL is the TTL on published A records (low, since LAN IPs
// may change as DHCP leases rotate).
const DefaultARecordTTL = 300

// DefaultTXTRecordTTL is the TTL on the DNS-01 TXT challenge record.
const DefaultTXTRecordTTL = 60

// Service is the lancert orchestrator: it owns the store, the ACME client,
// and the DNS provider, and exposes the high-level operations the HTTP
// handlers and renewal loop call.
type Service struct {
	Store            Store
	ACME             ACMEClient
	DNS              DNSProvider
	LANDomain        string        // default "lan.vulos.org"
	RenewWindow      time.Duration // default 30 days
	RenewTick        time.Duration // default 12h
	PropagationDelay time.Duration // default 30s; tests use 0
	Logf             func(format string, args ...any)
}

// NewService constructs a Service with sensible defaults filled in.
func NewService(st Store, acmeC ACMEClient, dns DNSProvider) *Service {
	return &Service{
		Store:            st,
		ACME:             acmeC,
		DNS:              dns,
		LANDomain:        DefaultLANDomain,
		RenewWindow:      DefaultRenewWindow,
		RenewTick:        DefaultRenewInterval,
		PropagationDelay: PropagationDelay,
		Logf:             log.Printf,
	}
}

// FQDNForBox returns the public LAN hostname for boxID.
func (s *Service) FQDNForBox(boxID string) string {
	domain := s.LANDomain
	if domain == "" {
		domain = DefaultLANDomain
	}
	return "box." + boxID + "." + domain
}

// ReportLANIP records the LAN IP a box just reported and publishes the
// public A-record (box.<id>.lan.vulos.org → ip). Idempotent.
func (s *Service) ReportLANIP(ctx context.Context, boxID, lanIP string) error {
	if boxID == "" {
		return ErrInvalidInput
	}
	if !validBoxID(boxID) {
		return fmt.Errorf("%w: box_id contains disallowed chars", ErrInvalidInput)
	}
	if net.ParseIP(lanIP) == nil {
		return fmt.Errorf("%w: lan_ip is not a valid IP", ErrInvalidInput)
	}
	fqdn := s.FQDNForBox(boxID)
	now := time.Now().UTC()
	if err := s.Store.UpsertLANIP(ctx, boxID, fqdn, lanIP, now); err != nil {
		return err
	}
	if err := s.DNS.SetA(ctx, fqdn, lanIP, DefaultARecordTTL); err != nil {
		// Surface but don't roll back the stored IP — the next renewal pass
		// can re-publish from the stored row.
		return fmt.Errorf("lancert: publish A: %w", err)
	}
	return nil
}

// Issue performs the full DNS-01 issuance flow for the box, persists the
// resulting cert+key, and logs the outcome. Safe to call repeatedly (it
// triggers a fresh issuance every time — callers gate this via the renewer).
//
// Flow:
//  1. Read the box's stored row to learn the FQDN.
//  2. Ask ACME for a new order → DNS-01 token.
//  3. Publish `_acme-challenge.<fqdn>` TXT via the DNSProvider.
//  4. Sleep PropagationDelay so the record is visible to the CA.
//  5. Generate a fresh per-cert ECDSA key, build CSR, Finalize.
//  6. Persist cert+key PEM + parse not_before / not_after.
//  7. Best-effort: delete the TXT record.
func (s *Service) Issue(ctx context.Context, boxID string) (BoxCert, error) {
	if boxID == "" {
		return BoxCert{}, ErrInvalidInput
	}
	fqdn := s.FQDNForBox(boxID)

	chalValue, orderState, err := s.ACME.NewOrder(ctx, fqdn)
	if err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "new_order: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: new order: %w", err)
	}
	chalFQDN := "_acme-challenge." + fqdn
	if err := s.DNS.SetTXT(ctx, chalFQDN, chalValue, DefaultTXTRecordTTL); err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "set_txt: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: publish TXT: %w", err)
	}
	defer func() {
		if delErr := s.DNS.DeleteTXT(context.Background(), chalFQDN); delErr != nil {
			s.Logf("[lancert] WARN: failed to delete TXT %s: %v", chalFQDN, delErr)
		}
	}()

	if s.PropagationDelay > 0 {
		select {
		case <-time.After(s.PropagationDelay):
		case <-ctx.Done():
			return BoxCert{}, ctx.Err()
		}
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "gen_key: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: generate cert key: %w", err)
	}
	csr, err := MakeCSR(fqdn, certKey)
	if err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "make_csr: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: make csr: %w", err)
	}

	chain, err := s.ACME.Finalize(ctx, orderState, csr)
	if err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "finalize: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: finalize: %w", err)
	}
	if len(chain) == 0 {
		s.logEvent(ctx, boxID, fqdn, "failed", "empty cert chain")
		return BoxCert{}, errors.New("lancert: ACME returned empty cert chain")
	}

	// Parse leaf to learn validity window.
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "parse leaf: "+err.Error())
		return BoxCert{}, fmt.Errorf("lancert: parse leaf: %w", err)
	}

	// PEM-encode the chain (leaf + intermediates) into one cert_pem blob.
	var certPEM strings.Builder
	for _, der := range chain {
		if err := pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return BoxCert{}, fmt.Errorf("lancert: pem encode cert: %w", err)
		}
	}

	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return BoxCert{}, fmt.Errorf("lancert: marshal ec key: %w", err)
	}
	var keyPEM strings.Builder
	if err := pem.Encode(&keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return BoxCert{}, fmt.Errorf("lancert: pem encode key: %w", err)
	}

	cert := BoxCert{
		BoxID:     boxID,
		FQDN:      fqdn,
		CertPEM:   certPEM.String(),
		KeyPEM:    keyPEM.String(),
		NotBefore: leaf.NotBefore.UTC(),
		NotAfter:  leaf.NotAfter.UTC(),
	}
	now := time.Now().UTC()
	if err := s.Store.SaveCert(ctx, cert, now); err != nil {
		s.logEvent(ctx, boxID, fqdn, "failed", "save_cert: "+err.Error())
		return BoxCert{}, err
	}
	s.logEvent(ctx, boxID, fqdn, "issued",
		fmt.Sprintf("not_after=%s", cert.NotAfter.Format(time.RFC3339)))
	return cert, nil
}

// GetCert returns the latest stored cert+key for the box. Returns ErrNoCert
// when the box has registered a LAN IP but issuance has not completed.
func (s *Service) GetCert(ctx context.Context, boxID string) (BoxCert, error) {
	st, err := s.Store.Get(ctx, boxID)
	if err != nil {
		return BoxCert{}, err
	}
	if st.CertPEM == "" || st.KeyPEM == "" {
		return BoxCert{}, ErrNoCert
	}
	return BoxCert{
		BoxID:     st.BoxID,
		FQDN:      st.FQDN,
		CertPEM:   st.CertPEM,
		KeyPEM:    st.KeyPEM,
		NotBefore: st.NotBefore,
		NotAfter:  st.NotAfter,
	}, nil
}

// RunRenewals runs one renewal pass: every box whose cert is within
// RenewWindow of expiry has Issue called again. Errors are logged but do not
// stop the pass — one bad box must not block the others.
func (s *Service) RunRenewals(ctx context.Context) {
	now := time.Now().UTC()
	due, err := s.Store.DueForRenewal(ctx, now, s.RenewWindow)
	if err != nil {
		s.Logf("[lancert] DueForRenewal: %v", err)
		return
	}
	for _, bs := range due {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.Issue(ctx, bs.BoxID); err != nil {
			s.Logf("[lancert] renew %s: %v", bs.BoxID, err)
			continue
		}
		_ = s.Store.AppendLog(ctx, LogEntry{
			BoxID: bs.BoxID, FQDN: bs.FQDN, Event: "renewed",
			Detail: "renewal pass", OccurredAt: time.Now().UTC(),
		})
	}
}

// StartRenewalLoop launches a goroutine that runs RunRenewals every
// RenewTick until ctx is cancelled. Returns immediately.
func (s *Service) StartRenewalLoop(ctx context.Context) {
	tick := s.RenewTick
	if tick <= 0 {
		tick = DefaultRenewInterval
	}
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			s.RunRenewals(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Service) logEvent(ctx context.Context, boxID, fqdn, event, detail string) {
	if err := s.Store.AppendLog(ctx, LogEntry{
		BoxID: boxID, FQDN: fqdn, Event: event, Detail: detail,
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		s.Logf("[lancert] append log (%s/%s): %v", boxID, event, err)
	}
}

// validBoxID accepts the same character set as DNS labels: letters,
// digits, and hyphens. Must not start or end with a hyphen.
func validBoxID(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// NoopDNSProvider — for tests / dev when no real DNS provider is configured
// ---------------------------------------------------------------------------

// NoopDNSProvider records calls but never hits any real DNS host. Returned by
// default-wired Service when no provider is configured; safe for tests.
type NoopDNSProvider struct {
	A   map[string]string
	TXT map[string]string
}

// NewNoopDNSProvider returns a fresh NoopDNSProvider.
func NewNoopDNSProvider() *NoopDNSProvider {
	return &NoopDNSProvider{A: map[string]string{}, TXT: map[string]string{}}
}

// SetA implements DNSProvider.
func (n *NoopDNSProvider) SetA(_ context.Context, fqdn, ip string, _ int) error {
	n.A[fqdn] = ip
	return nil
}

// SetTXT implements DNSProvider.
func (n *NoopDNSProvider) SetTXT(_ context.Context, fqdn, value string, _ int) error {
	n.TXT[fqdn] = value
	return nil
}

// DeleteTXT implements DNSProvider.
func (n *NoopDNSProvider) DeleteTXT(_ context.Context, fqdn string) error {
	delete(n.TXT, fqdn)
	return nil
}

// compile-time assertion
var _ DNSProvider = (*NoopDNSProvider)(nil)
