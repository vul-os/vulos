// Package lancert issues publicly-trusted TLS certificates for the per-box
// LAN hostname `box.<box-id>.lan.vulos.org` via ACME DNS-01, publishes the
// associated public A-record pointing at the box's reported LAN IP, and
// delivers the cert+key back to the box over an authenticated channel.
//
// This package completes the offline-LAN chain started in the OS (OFFLINE-01,
// `LoadCertSource` with on-disk hot-reload) and the resolver
// (RESOLVE-LAN-01, returning `https://box.<id>.lan.vulos.org` as a LAN
// candidate). It is the cloud-side counterpart: even when the internet is
// down, a browser on the same LAN as the box sees a publicly-trusted cert
// served straight from the box.
//
// Why DNS-01:
//   - The box has no inbound reachability from the public internet, so HTTP-01
//     and TLS-ALPN-01 do not work.
//   - The cloud already controls the `vulos.org` zone (see internal/dnsplane),
//     so it can answer the DNS-01 challenge on the box's behalf.
//
// Cert delivery contract (mirror image of OFFLINE-01 on the OS side):
//
//	Box authenticates with the existing `X-Device-Auth: <CP_SHARED_SECRET>`
//	header (the same header used by all other OS→CP control endpoints).
//	The box pulls fresh cert+key material from:
//
//	    POST /api/lancert/report-ip           { "box_id": "...", "lan_ip": "..." }
//	    GET  /api/lancert/cert?box_id=<id>    → 200 { "cert_pem": "...", "key_pem": "...", "fqdn": "...", "not_after": ... }
//
//	The OS writes the bytes to its CertSource paths
//	    /var/lib/vulos/tls/lan.crt
//	    /var/lib/vulos/tls/lan.key
//	and `LoadCertSource` hot-reloads them on the next fsnotify tick.
//
// Storage: pure-Go modernc.org/sqlite (CGO_ENABLED=0 always).
//
// NEVER imports vulos-mail / vulos-relay / vulos-office.
package lancert

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// BoxCert is the cert+key pair issued for a single box's LAN hostname.
// Both PEMs are intended to be written verbatim to the box's CertSource files.
type BoxCert struct {
	BoxID     string    `json:"box_id"`
	FQDN      string    `json:"fqdn"`
	CertPEM   string    `json:"cert_pem"`
	KeyPEM    string    `json:"key_pem"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
}

// BoxState is the persisted row for a registered box (LAN IP + last issuance).
type BoxState struct {
	BoxID          string
	FQDN           string
	LANIP          string
	LANIPUpdatedAt time.Time
	CertPEM        string
	KeyPEM         string
	NotBefore      time.Time
	NotAfter       time.Time
	LastIssuedAt   time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// LogEntry is one row of the issuance audit log.
type LogEntry struct {
	BoxID      string
	FQDN       string
	Event      string // "issued" | "renewed" | "failed"
	Detail     string
	OccurredAt time.Time
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrUnknownBox is returned by Store.Get when no row matches the box_id.
	ErrUnknownBox = errors.New("lancert: unknown box_id")

	// ErrNoCert is returned by GetCert when the box has been registered but no
	// cert has been issued yet (e.g. issuance is still queued).
	ErrNoCert = errors.New("lancert: no cert issued yet")

	// ErrInvalidInput is returned for empty / malformed required fields.
	ErrInvalidInput = errors.New("lancert: invalid input")
)

// ---------------------------------------------------------------------------
// Store — persistence contract (pure-Go SQLite implementation in store.go)
// ---------------------------------------------------------------------------

// Store is the persistence contract. The SQLite implementation and any test
// fakes must both satisfy it; the Service depends ONLY on this interface.
type Store interface {
	// UpsertLANIP records the latest LAN IP a box reported (used for A-record
	// publish). Creates the row if needed.
	UpsertLANIP(ctx context.Context, boxID, fqdn, lanIP string, now time.Time) error

	// SaveCert persists a freshly-issued cert+key pair for the box.
	SaveCert(ctx context.Context, c BoxCert, now time.Time) error

	// Get returns the full state row for a box.
	Get(ctx context.Context, boxID string) (BoxState, error)

	// DueForRenewal returns boxes whose cert NotAfter is at or before
	// (now + window). Used by the renewal scheduler.
	DueForRenewal(ctx context.Context, now time.Time, window time.Duration) ([]BoxState, error)

	// AppendLog records an issuance event (success or failure).
	AppendLog(ctx context.Context, e LogEntry) error

	// RecentLog returns up to limit log entries for the box, newest first.
	RecentLog(ctx context.Context, boxID string, limit int) ([]LogEntry, error)

	// Close releases the underlying database connection.
	Close() error
}

// ---------------------------------------------------------------------------
// DNSProvider — narrow interface to publish A + TXT records
// ---------------------------------------------------------------------------

// DNSProvider is the narrow interface lancert uses to publish public DNS
// records. It is satisfied by the existing dnsplane.Provider (adapter lives
// in service.go) and by NoopDNSProvider for tests.
//
// Two record kinds are needed:
//   - A      — `box.<id>.lan.vulos.org` → the box's reported LAN IP
//   - TXT    — `_acme-challenge.box.<id>.lan.vulos.org` → DNS-01 challenge token
//
// The provider MUST replace any prior record with the same (fqdn, recordType)
// pair (CreateOrReplace semantics) so re-publishing during renewal is safe.
type DNSProvider interface {
	// SetA publishes / replaces the A record fqdn → ip with TTL ttlSeconds.
	SetA(ctx context.Context, fqdn, ip string, ttlSeconds int) error

	// SetTXT publishes / replaces the TXT record fqdn → value with TTL
	// ttlSeconds. Used to answer ACME DNS-01 challenges.
	SetTXT(ctx context.Context, fqdn, value string, ttlSeconds int) error

	// DeleteTXT removes the TXT record at fqdn (post-challenge cleanup).
	// Best-effort: a not-found error must NOT be returned as an error.
	DeleteTXT(ctx context.Context, fqdn string) error
}

// ---------------------------------------------------------------------------
// ACMEClient — narrow ACME interface (DNS-01 only)
// ---------------------------------------------------------------------------

// ACMEClient is the narrow ACME interface lancert depends on. The production
// implementation in acme.go wraps golang.org/x/crypto/acme. Tests inject a
// mock that returns a pre-fabricated cert without making any network calls.
//
// The flow is:
//  1. NewOrder(fqdn) returns the DNS-01 token the caller must publish at
//     `_acme-challenge.<fqdn>` as a TXT record.
//  2. The caller publishes the TXT via DNSProvider.
//  3. Finalize(fqdn, csr, key) tells the CA the challenge is ready, polls
//     until the order is valid, finalises with the CSR, and returns the
//     DER-encoded cert chain.
type ACMEClient interface {
	// NewOrder starts an order for the FQDN and returns the DNS-01 challenge
	// value that must be published at `_acme-challenge.<fqdn>`. The opaque
	// orderState is fed back into Finalize.
	NewOrder(ctx context.Context, fqdn string) (challengeValue string, orderState any, err error)

	// Finalize tells the CA to verify the published challenge, finalises the
	// order with csr, and returns the certificate chain (leaf first) in DER
	// form. accountKey is the ACME account key; certKey is the per-cert key
	// used to sign the CSR.
	Finalize(ctx context.Context, orderState any, csr []byte) (certDER [][]byte, err error)
}

// ---------------------------------------------------------------------------
// CSRMaker — small helper, exported for testability
// ---------------------------------------------------------------------------

// MakeCSR builds an ASN.1 DER-encoded PKCS#10 CSR for the given FQDN, signed
// with certKey. It is a thin wrapper over crypto/x509 for clarity; lives here
// so tests can drive the ACMEClient mock without depending on x/crypto/acme.
func MakeCSR(fqdn string, certKey crypto.Signer) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		DNSNames: []string{fqdn},
	}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, certKey)
}
