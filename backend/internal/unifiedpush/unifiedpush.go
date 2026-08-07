// Package unifiedpush is a self-contained, sovereign UnifiedPush endpoint
// sender — UP-CELL-01, the box-side half of D96-F in docs/decisions.md.
//
// UnifiedPush (https://unifiedpush.org) lets a user nominate their OWN push
// endpoint — handed to them by a "distributor" app they installed and trust
// (e.g. ntfy, self-hosted or not) — instead of the box POSTing to a fixed
// browser-vendor push service (FCM/Mozilla/APNs, see backend/internal/webpush).
// On Android this removes the vendor from the path entirely; there is no
// UnifiedPush distributor for iOS (Apple mandates APNs — see webpush's
// README and docs/USER-GUIDE.md for why that gap is real and stays
// documented, not glossed over).
//
// This package is deliberately generic, mirroring backend/internal/webpush's
// shape: it has no dependency on any particular notification/service model.
// See backend/services/notify/unifiedpush_service.go for how the notify
// service wires it in (Service.SetUnifiedPush / maybeUnifiedPush / upBinding)
// ALONGSIDE (not instead of) the existing Web Push path — a box may run
// either, both, or neither per user.
//
// The whole feature is ADDITIVE and FLAG-GATED: with VULOS_PUSH_UNIFIEDPUSH_ENABLE
// unset, Config.Enabled is false and callers treat the feature as off — no
// endpoint registration is accepted, and a user who has registered nothing
// sees no behavior change at all.
//
// A UnifiedPush endpoint is a plain HTTP(S) POST target — unlike Web Push
// there is no RFC 8291 encryption envelope or VAPID key material; the spec
// treats the endpoint as an opaque "poke this URL with a body" contract that
// the distributor forwards verbatim to the receiving app. Because a
// user-supplied endpoint is exactly the shape of an SSRF vector (the box
// would otherwise happily probe an internal service on the caller's say-so),
// every endpoint is screened the same way webpush's are: once at
// registration (parse-time host screen) and again at send time (resolved-IP
// re-screen via the shared backend/internal/safedial dialer, which defeats
// DNS-rebinding). Both layers reuse safedial rather than re-implementing a
// second private-IP deny-list.
package unifiedpush

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // SQLite driver for the endpoint store

	"vulos/backend/internal/safedial"
)

// ---- configuration -----------------------------------------------------------

// Config holds the delivery knobs for UnifiedPush, sourced from the
// environment so the feature is CONFIG-GATED and FAIL-SAFE-OFF.
type Config struct {
	// Enabled gates the whole feature. Fail-safe-off: false unless the
	// operator explicitly opts in via VULOS_PUSH_UNIFIEDPUSH_ENABLE=1. With
	// this false, registration 503s and nothing is ever sent — the box's
	// behavior is unchanged from before this package existed.
	Enabled bool
}

// LoadConfig reads the UnifiedPush configuration from the environment.
func LoadConfig() Config {
	return Config{Enabled: truthy(os.Getenv("VULOS_PUSH_UNIFIEDPUSH_ENABLE"))}
}

// truthy accepts the same {"1","true","yes"} spellings other VULOS_*_ENABLE
// flags in this codebase use (see internal/lan/lancert_puller.go).
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// ---- endpoint model -----------------------------------------------------------

// Endpoint is a single UnifiedPush registration: the URL a distributor gave
// the user, scoped to an OwnerID the caller must stamp from a verified
// session (never trust the client body for it — same rule as
// webpush.Subscription.OwnerID).
type Endpoint struct {
	OwnerID   string    `json:"owner_id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxEndpointsPerUser bounds how many UnifiedPush endpoints one owner may
// register (mirrors webpush.MaxSubsPerUser's purpose: a client cannot pin
// unbounded rows). A user typically has one distributor per device.
const MaxEndpointsPerUser = 10

// maxEndpointLen bounds the stored endpoint URL length.
const maxEndpointLen = 2048

// maxPayloadBytes is the UnifiedPush spec's recommended maximum body size a
// distributor is guaranteed to accept without its own truncation/rejection.
const maxPayloadBytes = 4096

// Validate checks a client-supplied endpoint is well-formed, bounded, and
// SSRF-safe before it is ever stored. This is the registration-time half of
// the SSRF guard; LiveSender.Send re-screens the RESOLVED IP at send time.
func Validate(ep Endpoint) error {
	if ep.URL == "" {
		return fmt.Errorf("endpoint url required")
	}
	if len(ep.URL) > maxEndpointLen {
		return fmt.Errorf("endpoint url too long")
	}
	// UnifiedPush endpoints are always https in practice (distributors are
	// installed apps talking to a remote push server); reject anything else so
	// a client cannot register an arbitrary scheme/host as an SSRF-ish sink.
	if !strings.HasPrefix(ep.URL, "https://") {
		return fmt.Errorf("endpoint url must be https")
	}
	if err := screenEndpoint(ep.URL); err != nil {
		return err
	}
	return nil
}

// screenEndpoint rejects an endpoint whose host is (or literally is) a
// non-public address — loopback, link-local, private LAN, CGNAT, or a cloud
// metadata address — so a registered endpoint can never point the box's
// outbound POST at an internal/metadata target. A bare hostname is allowed
// here (it is re-screened against its RESOLVED IP at send time by the
// SSRF-guarded dialer); an IP LITERAL — including obfuscated
// decimal/hex/octal forms — must be public. allowLAN is always false: a
// UnifiedPush distributor is expected to be reachable on the public internet,
// and the box's own LAN is explicitly in scope of what this guard must deny.
func screenEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("endpoint url invalid")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint url has no host")
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("endpoint url host not permitted")
	}
	if ip := safedial.NormalizeIPLiteral(host); ip != nil {
		if safedial.IsDeniedIP(ip, false /*no LAN — must not reach the box's own network*/) {
			return fmt.Errorf("endpoint url host not permitted")
		}
	}
	return nil
}

// Store persists UnifiedPush endpoints, isolated per owner — the same
// isolation contract as webpush.Store: every method is scoped by ownerID, so
// one user can never read, overwrite, or delete another user's endpoints.
type Store interface {
	// Save upserts an endpoint for ownerID (keyed by URL). Enforces the
	// per-user cap: past MaxEndpointsPerUser the OLDEST endpoint for that
	// owner is evicted so a live device always registers.
	Save(ownerID string, ep Endpoint) error
	// Delete removes one endpoint for ownerID (idempotent). It NEVER deletes
	// another owner's row even if the URL string collides.
	Delete(ownerID, endpointURL string) error
	// List returns ownerID's endpoints (empty when none).
	List(ownerID string) ([]Endpoint, error)
	Close() error
}

// ---- in-memory store (tests / ephemeral) ------------------------------------

type memStore struct {
	mu  sync.Mutex
	byU map[string]map[string]Endpoint // ownerID → url → endpoint
}

// NewMemStore returns an in-memory Store.
func NewMemStore() Store {
	return &memStore{byU: map[string]map[string]Endpoint{}}
}

func (m *memStore) Save(ownerID string, ep Endpoint) error {
	if ownerID == "" || ep.URL == "" {
		return fmt.Errorf("unifiedpush: owner and url required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.byU[ownerID]
	if set == nil {
		set = map[string]Endpoint{}
		m.byU[ownerID] = set
	}
	ep.OwnerID = ownerID
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}
	if _, exists := set[ep.URL]; !exists && len(set) >= MaxEndpointsPerUser {
		var oldestURL string
		var oldestT time.Time
		for u, e := range set {
			if oldestURL == "" || e.CreatedAt.Before(oldestT) {
				oldestURL, oldestT = u, e.CreatedAt
			}
		}
		delete(set, oldestURL)
	}
	set[ep.URL] = ep
	return nil
}

func (m *memStore) Delete(ownerID, endpointURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if set := m.byU[ownerID]; set != nil {
		delete(set, endpointURL)
		if len(set) == 0 {
			delete(m.byU, ownerID)
		}
	}
	return nil
}

func (m *memStore) List(ownerID string) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.byU[ownerID]
	out := make([]Endpoint, 0, len(set))
	for _, e := range set {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *memStore) Close() error { return nil }

// ---- SQLite store ------------------------------------------------------------

type sqliteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dsn dedicated to
// UnifiedPush endpoints and ensures the schema. It keeps its own handle so it
// never contends on any other write path.
func NewSQLiteStore(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("unifiedpush: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS unifiedpush_endpoints (
			owner_id   TEXT NOT NULL,
			url        TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (owner_id, url)
		);
		CREATE INDEX IF NOT EXISTS idx_unifiedpush_owner ON unifiedpush_endpoints(owner_id);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("unifiedpush: init schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Save(ownerID string, ep Endpoint) error {
	if ownerID == "" || ep.URL == "" {
		return fmt.Errorf("unifiedpush: owner and url required")
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM unifiedpush_endpoints WHERE owner_id = ? AND url <> ?`,
		ownerID, ep.URL).Scan(&n); err != nil {
		return err
	}
	if n >= MaxEndpointsPerUser {
		if _, err := tx.Exec(
			`DELETE FROM unifiedpush_endpoints
			 WHERE owner_id = ? AND url = (
			   SELECT url FROM unifiedpush_endpoints
			   WHERE owner_id = ? AND url <> ?
			   ORDER BY created_at ASC LIMIT 1)`,
			ownerID, ownerID, ep.URL); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO unifiedpush_endpoints (owner_id, url, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(owner_id, url) DO UPDATE SET created_at=excluded.created_at`,
		ownerID, ep.URL, ep.CreatedAt.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Delete(ownerID, endpointURL string) error {
	_, err := s.db.Exec(
		`DELETE FROM unifiedpush_endpoints WHERE owner_id = ? AND url = ?`,
		ownerID, endpointURL)
	return err
}

func (s *sqliteStore) List(ownerID string) ([]Endpoint, error) {
	rows, err := s.db.Query(
		`SELECT url, created_at FROM unifiedpush_endpoints
		 WHERE owner_id = ? ORDER BY created_at ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		var createdNanos int64
		if err := rows.Scan(&ep.URL, &createdNanos); err != nil {
			return nil, err
		}
		ep.OwnerID = ownerID
		ep.CreatedAt = time.Unix(0, createdNanos).UTC()
		out = append(out, ep)
	}
	return out, rows.Err()
}

func (s *sqliteStore) Close() error { return s.db.Close() }

// ---- send transport ----------------------------------------------------------

// Payload is the JSON body POSTed to a UnifiedPush endpoint. It deliberately
// mirrors webpush.Payload's shape (Title/Body/Tag/Source/URL) so a client
// that receives notification content over either path decodes the same
// structure. Unlike Web Push there is no RFC 8291 encryption envelope here —
// per the UnifiedPush spec the endpoint is a plain HTTP POST target, and the
// distributor forwards the body to the app verbatim.
type Payload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Tag    string `json:"tag"`
	Source string `json:"source,omitempty"`
	URL    string `json:"url,omitempty"`
}

// Sender abstracts the transport so callers/tests can assert dispatch
// without a live distributor endpoint. It returns the HTTP status so a
// permanently-gone endpoint (404/410 — same convention RFC 8030 §8.3 uses
// for Web Push, and what UnifiedPush distributors follow too) can be pruned.
type Sender interface {
	Send(ep Endpoint, payload []byte, cfg Config) (statusCode int, err error)
}

// LiveSender is the production Sender: it POSTs the payload straight to the
// endpoint URL the user registered.
type LiveSender struct{}

// upPumpTimeout bounds a full round of sends for one notification so a slow
// distributor cannot pin the caller's send goroutine.
const upPumpTimeout = 20 * time.Second

// upHTTPClient is the SSRF-guarded HTTP client used to POST to UnifiedPush
// endpoints. Its dialer re-screens the RESOLVED IP right before connect(2)
// (safedial Control hook), so an endpoint whose host resolves — now or via a
// DNS rebind — to a loopback/private/link-local/metadata address is refused
// at the socket, not just at registration. Redirects are NOT followed: a 30x
// response could otherwise bounce the POST to an internal target after the
// registration-time screen. allowLAN=false: this guard must deny the box's
// own LAN, exactly as it denies loopback/link-local/metadata.
var upHTTPClient = &http.Client{
	Timeout: upPumpTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("unifiedpush: redirect not allowed")
	},
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Control: safedial.ControlFunc(false), Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
		MaxIdleConns:        1,
	},
}

// Send implements Sender.
func (LiveSender) Send(ep Endpoint, payload []byte, _ Config) (int, error) {
	// Defense-in-depth: re-screen the endpoint host FORM here too, so a
	// sender is never handed an endpoint that skipped Validate (e.g. a legacy
	// row persisted before this guard existed).
	if err := screenEndpoint(ep.URL); err != nil {
		return 0, err
	}
	body := payload
	if len(body) > maxPayloadBytes {
		// A distributor is only guaranteed to accept up to the spec's
		// recommended cap; sending more risks a silent drop. Fail loudly
		// instead (the caller logs it) rather than pretend delivery.
		return 0, fmt.Errorf("unifiedpush: payload exceeds %d bytes", maxPayloadBytes)
	}
	req, err := http.NewRequest(http.MethodPost, ep.URL, strings.NewReader(string(body)))
	if err != nil {
		return 0, fmt.Errorf("unifiedpush: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := upHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
