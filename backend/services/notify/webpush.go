package notify

// webpush.go — cell-side DIRECT Web Push send-path (PUSH-CELL-01).
//
// The box (cell) holds a VAPID key pair and POSTs RFC 8291 (aes128gcm) encrypted
// payloads DIRECTLY to the browser-vendor push service (FCM / Apple / Mozilla)
// named by each subscription's endpoint. This is OUTBOUND-ONLY, so it works
// behind NAT with no inbound reachability and no central dependency — the
// sovereign path per vulos-unified-reachability-architecture (push section).
// Vendors ROUTE but CANNOT READ: the payload is E2E-encrypted to the
// subscription's keys by the Web Push protocol.
//
// This whole feature is ADDITIVE and FLAG-GATED: with no VAPID keys configured,
// PushConfig.Enabled() is false, the subscribe routes 503, and SendNotification
// takes exactly its prior path. The private VAPID key is a secret and is never
// logged nor returned in any response.
//
// The Web Push (VAPID) design is a standard, self-contained implementation in
// the OS notify package.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	_ "modernc.org/sqlite" // SQLite driver for the push subscription store

	"vulos/backend/internal/safedial"
)

// ---- configuration -----------------------------------------------------------

// PushConfig holds the VAPID key material and delivery knobs, sourced from the
// environment so the feature is CONFIG-GATED and FAIL-SAFE-OFF. With nothing
// set, Web Push is disabled and only the existing in-app WebSocket path runs.
type PushConfig struct {
	// VAPIDPublic / VAPIDPrivate are base64url keys as produced by webpush-go's
	// GenerateVAPIDKeys. Push is OFF unless BOTH are present (explicitly or via
	// VAPIDKeyFile, resolved by ResolvePushVAPID). The private key is a SECRET.
	VAPIDPublic  string
	VAPIDPrivate string
	// VAPIDSubject is the RFC 8292 contact ("mailto:..." or an https origin).
	VAPIDSubject string
	// VAPIDKeyFile, when set and the explicit keys are empty, is a path a key
	// pair is loaded-or-generated at (0600). Empty = no file fallback.
	VAPIDKeyFile string
	// TTL is the push service message TTL in seconds (how long the vendor holds
	// an undelivered push for an offline device). Defaulted.
	TTL int
}

// LoadPushConfig reads the push configuration from the environment. It does not
// resolve/generate keys — call ResolvePushVAPID after.
func LoadPushConfig() PushConfig {
	c := PushConfig{
		VAPIDPublic:  os.Getenv("VULOS_PUSH_VAPID_PUBLIC"),
		VAPIDPrivate: os.Getenv("VULOS_PUSH_VAPID_PRIVATE"),
		VAPIDSubject: os.Getenv("VULOS_PUSH_VAPID_SUBJECT"),
		VAPIDKeyFile: os.Getenv("VULOS_PUSH_VAPID_KEYFILE"),
		TTL:          300, // 5 minutes; a stale offline alert is not useful
	}
	if c.VAPIDSubject == "" {
		c.VAPIDSubject = "mailto:admin@localhost"
	}
	return c
}

// Enabled reports whether Web Push is configured. Fail-safe: OFF unless BOTH
// halves of the VAPID key pair are present (resolved by ResolvePushVAPID first).
func (c PushConfig) Enabled() bool {
	return c.VAPIDPublic != "" && c.VAPIDPrivate != ""
}

// vapidFileFormat is the on-disk JSON shape for a persisted key pair.
type vapidFileFormat struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

// ResolvePushVAPID fills in the VAPID key pair on c when it is not set
// explicitly:
//   - if both keys are already present, it is a no-op;
//   - else if VAPIDKeyFile is set, it loads-or-generates a pair at that path
//     (0600 — the private key is a secret);
//   - else it leaves the keys empty (Web Push stays disabled — fail-safe-off).
//
// It returns an error only when a configured key file cannot be read/written; an
// unconfigured push feature is not an error.
func ResolvePushVAPID(c *PushConfig) error {
	if c.VAPIDPublic != "" && c.VAPIDPrivate != "" {
		return nil // explicit keys win
	}
	if c.VAPIDKeyFile == "" {
		return nil // no file fallback → push stays off
	}
	if data, err := os.ReadFile(c.VAPIDKeyFile); err == nil {
		var f vapidFileFormat
		if json.Unmarshal(data, &f) == nil && f.Private != "" && f.Public != "" {
			c.VAPIDPrivate, c.VAPIDPublic = f.Private, f.Public
			return nil
		}
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("notify: generate VAPID keys: %w", err)
	}
	raw, err := json.Marshal(vapidFileFormat{Private: priv, Public: pub})
	if err != nil {
		return fmt.Errorf("notify: marshal VAPID keys: %w", err)
	}
	if err := os.WriteFile(c.VAPIDKeyFile, raw, 0600); err != nil {
		return fmt.Errorf("notify: write VAPID key file %s: %w", c.VAPIDKeyFile, err)
	}
	c.VAPIDPrivate, c.VAPIDPublic = priv, pub
	return nil
}

// ---- subscription model ------------------------------------------------------

// PushSubscription mirrors the browser's PushSubscription.toJSON() shape (the
// object PushManager.subscribe() yields). OwnerID is the server-assigned owner;
// it is NEVER read from the client body — the handler stamps it from the
// verified session, which is what makes subscriptions per-owner isolated. It is
// the SAME identity space as Notification.UserID, so the dispatcher can target a
// notification's owner directly.
type PushSubscription struct {
	OwnerID   string    `json:"owner_id"`
	Endpoint  string    `json:"endpoint"`
	P256DH    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxSubsPerUser bounds how many push endpoints one owner may register, so a
// client cannot pin unbounded rows (a device/browser rotates endpoints).
const MaxSubsPerUser = 20

// maxEndpointLen bounds the stored endpoint URL length (also the primary-key
// length) so a client cannot store an oversized blob.
const maxEndpointLen = 2048

// ValidateSubscription checks a client-supplied subscription is well-formed and
// within bounds before it is stored. It does NOT validate the crypto material
// beyond presence/length — an invalid key simply fails to receive pushes and is
// pruned on the 410 Gone response.
func ValidateSubscription(sub PushSubscription) error {
	if sub.Endpoint == "" || sub.P256DH == "" || sub.Auth == "" {
		return fmt.Errorf("subscription missing required fields")
	}
	if len(sub.Endpoint) > maxEndpointLen {
		return fmt.Errorf("subscription endpoint too long")
	}
	// Endpoints are vendor https URLs; reject anything not https to avoid being
	// pointed at an arbitrary scheme/host as an SSRF-ish sink.
	if !strings.HasPrefix(sub.Endpoint, "https://") {
		return fmt.Errorf("subscription endpoint must be https")
	}
	// SSRF guard (parse-time half): the box POSTs the encrypted push payload to
	// this endpoint OUTBOUND, so a malicious owner could otherwise register an
	// endpoint pointing at a loopback/private/link-local/metadata host and make
	// the box probe internal services. Screen the endpoint host FORM here; the
	// send path re-screens the RESOLVED IP at connect time (defeats DNS-rebind).
	if err := screenPushEndpoint(sub.Endpoint); err != nil {
		return err
	}
	// p256dh is a 65-byte uncompressed EC point (base64url ~88 chars); auth is a
	// 16-byte salt (~24 chars). Bound generously.
	if len(sub.P256DH) > 256 || len(sub.Auth) > 128 {
		return fmt.Errorf("subscription key material too long")
	}
	return nil
}

// screenPushEndpoint rejects an endpoint whose host is (or literally is) a
// non-public address, so a subscription can never point the box's outbound push
// POST at an internal/metadata target. A bare hostname is allowed here (it is
// re-screened against its RESOLVED IP at send time by the guarded dialer);
// an IP LITERAL — including obfuscated decimal/hex/octal forms — must be public.
func screenPushEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("subscription endpoint invalid")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("subscription endpoint has no host")
	}
	// Block obvious internal hostnames outright (the resolved-IP re-screen at send
	// time is the real defense for names that resolve to private space).
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return fmt.Errorf("subscription endpoint host not permitted")
	}
	// If the host is an IP literal (incl. obfuscated forms), it must be public.
	if ip := safedial.NormalizeIPLiteral(host); ip != nil {
		if safedial.IsDeniedIP(ip, false /*no LAN — push vendors are public*/) {
			return fmt.Errorf("subscription endpoint host not permitted")
		}
	}
	return nil
}

// PushStore persists Web Push subscriptions, isolated per owner. Every method is
// scoped by ownerID: there is no cross-owner read, and Save stamps the owner
// from the caller, so one user can never read, overwrite, or delete another
// user's subscriptions.
type PushStore interface {
	// Save upserts a subscription for ownerID (keyed by endpoint). Enforces the
	// per-user cap: past MaxSubsPerUser the OLDEST endpoint for that owner is
	// evicted so a live device always registers.
	Save(ownerID string, sub PushSubscription) error
	// Delete removes one endpoint for ownerID (idempotent). It NEVER deletes
	// another owner's row even if the endpoint string collides.
	Delete(ownerID, endpoint string) error
	// List returns ownerID's subscriptions (empty when none).
	List(ownerID string) ([]PushSubscription, error)
	Close() error
}

// ---- in-memory store (tests / ephemeral) ------------------------------------

type memPushStore struct {
	mu  sync.Mutex
	byU map[string]map[string]PushSubscription // ownerID → endpoint → sub
}

// NewMemPushStore returns an in-memory PushStore.
func NewMemPushStore() PushStore {
	return &memPushStore{byU: map[string]map[string]PushSubscription{}}
}

func (m *memPushStore) Save(ownerID string, sub PushSubscription) error {
	if ownerID == "" || sub.Endpoint == "" {
		return fmt.Errorf("push: owner and endpoint required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.byU[ownerID]
	if set == nil {
		set = map[string]PushSubscription{}
		m.byU[ownerID] = set
	}
	sub.OwnerID = ownerID
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now().UTC()
	}
	if _, exists := set[sub.Endpoint]; !exists && len(set) >= MaxSubsPerUser {
		var oldestEP string
		var oldestT time.Time
		for ep, s := range set {
			if oldestEP == "" || s.CreatedAt.Before(oldestT) {
				oldestEP, oldestT = ep, s.CreatedAt
			}
		}
		delete(set, oldestEP)
	}
	set[sub.Endpoint] = sub
	return nil
}

func (m *memPushStore) Delete(ownerID, endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if set := m.byU[ownerID]; set != nil {
		delete(set, endpoint)
		if len(set) == 0 {
			delete(m.byU, ownerID)
		}
	}
	return nil
}

func (m *memPushStore) List(ownerID string) ([]PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.byU[ownerID]
	out := make([]PushSubscription, 0, len(set))
	for _, s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *memPushStore) Close() error { return nil }

// ---- SQLite store ------------------------------------------------------------

type sqlitePushStore struct {
	db *sql.DB
}

// NewSQLitePushStore opens (or creates) a SQLite database at dsn dedicated to
// push subscriptions and ensures the schema. It keeps its own handle so it never
// contends on any other write path.
func NewSQLitePushStore(dsn string) (PushStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("push: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS push_subscriptions (
			owner_id   TEXT NOT NULL,
			endpoint   TEXT NOT NULL,
			p256dh     TEXT NOT NULL,
			auth       TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (owner_id, endpoint)
		);
		CREATE INDEX IF NOT EXISTS idx_push_owner ON push_subscriptions(owner_id);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("push: init schema: %w", err)
	}
	return &sqlitePushStore{db: db}, nil
}

func (s *sqlitePushStore) Save(ownerID string, sub PushSubscription) error {
	if ownerID == "" || sub.Endpoint == "" {
		return fmt.Errorf("push: owner and endpoint required")
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM push_subscriptions WHERE owner_id = ? AND endpoint <> ?`,
		ownerID, sub.Endpoint).Scan(&n); err != nil {
		return err
	}
	if n >= MaxSubsPerUser {
		if _, err := tx.Exec(
			`DELETE FROM push_subscriptions
			 WHERE owner_id = ? AND endpoint = (
			   SELECT endpoint FROM push_subscriptions
			   WHERE owner_id = ? AND endpoint <> ?
			   ORDER BY created_at ASC LIMIT 1)`,
			ownerID, ownerID, sub.Endpoint); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO push_subscriptions (owner_id, endpoint, p256dh, auth, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(owner_id, endpoint)
		 DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth`,
		ownerID, sub.Endpoint, sub.P256DH, sub.Auth, sub.CreatedAt.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlitePushStore) Delete(ownerID, endpoint string) error {
	_, err := s.db.Exec(
		`DELETE FROM push_subscriptions WHERE owner_id = ? AND endpoint = ?`,
		ownerID, endpoint)
	return err
}

func (s *sqlitePushStore) List(ownerID string) ([]PushSubscription, error) {
	rows, err := s.db.Query(
		`SELECT endpoint, p256dh, auth, created_at FROM push_subscriptions
		 WHERE owner_id = ? ORDER BY created_at ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var sub PushSubscription
		var createdNanos int64
		if err := rows.Scan(&sub.Endpoint, &sub.P256DH, &sub.Auth, &createdNanos); err != nil {
			return nil, err
		}
		sub.OwnerID = ownerID
		sub.CreatedAt = time.Unix(0, createdNanos).UTC()
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *sqlitePushStore) Close() error { return s.db.Close() }

// ---- send transport ----------------------------------------------------------

// PushPayload is the JSON the service worker's `push` handler receives. It is
// deliberately minimal and carries only what the SW needs to render the OS
// notification: a title, a short body snippet, a tag (for coalescing), a source,
// and an optional deep-link URL. It NEVER carries a recipient identifier — the
// payload is encrypted end-to-end to the subscription's keys (RFC 8291), and the
// dispatcher only ever hands a payload to that owner's own subscriptions.
type PushPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Tag    string `json:"tag"`
	Source string `json:"source,omitempty"`
	URL    string `json:"url,omitempty"`
}

// pushSender abstracts the webpush transport so tests can assert dispatch
// without a live push service. It returns the vendor HTTP status so gone
// subscriptions (404/410) can be pruned.
type pushSender interface {
	send(sub PushSubscription, payload []byte, cfg PushConfig) (statusCode int, err error)
}

type livePushSender struct{}

// pushHTTPClient is the SSRF-guarded HTTP client the box uses to POST encrypted
// push payloads to vendor endpoints. Its dialer re-screens the RESOLVED IP right
// before connect(2) (safedial Control hook), so a subscription whose host
// resolves — now or via a rebind — to a loopback/private/link-local/metadata
// address is refused at the socket, not just at validation. Redirects are NOT
// followed: a 30x from a vendor could otherwise bounce the POST to an internal
// target after the parse-time screen. allowLAN=false: push vendors are public.
var pushHTTPClient = &http.Client{
	Timeout: pushPumpTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("push: redirect not allowed")
	},
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Control: safedial.ControlFunc(false), Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
		MaxIdleConns:        1,
	},
}

func (livePushSender) send(sub PushSubscription, payload []byte, cfg PushConfig) (int, error) {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 300
	}
	// Defense-in-depth: re-screen the endpoint host FORM here too, so a sender is
	// never handed a subscription that skipped ValidateSubscription (e.g. a legacy
	// row persisted before this guard existed).
	if err := screenPushEndpoint(sub.Endpoint); err != nil {
		return 0, err
	}
	resp, err := webpush.SendNotification(payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      pushHTTPClient,
		Subscriber:      cfg.VAPIDSubject,
		VAPIDPublicKey:  cfg.VAPIDPublic,
		VAPIDPrivateKey: cfg.VAPIDPrivate,
		TTL:             ttl,
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// pushPumpTimeout bounds a full round of push sends for one notification so a
// slow vendor cannot pin the SendNotification goroutine (the pump runs async, so
// this is belt-and-suspenders).
const pushPumpTimeout = 20 * time.Second

var _ = context.Background // reserved for a future context-aware sender
