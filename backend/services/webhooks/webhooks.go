// Package webhooks implements outbound webhook subscriptions for the box
// owner (Settings -> Developer -> Webhooks).
//
// The owner registers a URL subscribed to one or more event topics (e.g.
// "backup.completed", "device.enrolled"). Dispatcher.Emit(topic, payload)
// fans out to every active, subscribed URL. Delivery is async with
// at-least-once semantics: events are queued in SQLite, dispatched with
// exponential backoff (1m, 5m, 25m, 2h, 12h -> dead-letter), and signed via
// HMAC-SHA256 (X-Vulos-Signature: t=<ts>,v1=<hex>) so the receiver can
// authenticate the sender — see verify.go.
//
// Every subscription has its own HMAC secret (rotatable via RotateSecret,
// returned raw only once).
//
// Scope: single-owner. The box has exactly one owner-facing set of
// subscriptions; the admin-only gate lives at the HTTP route layer
// (routes_webhooks.go), not in this package. created_by is kept purely as an
// audit trail of which admin configured a given subscription.
//
// SSRF: every subscription URL is screened at create time (ssrf.go,
// validateWebhookURL) AND re-screened at dial time via ssrfSafeDialer, which
// pins the connection to the first DNS answer it validates — this closes the
// DNS-rebind gap where a hostname resolves to a public IP at subscribe time
// and a private/internal IP at delivery time. See ssrf.go for the full
// deny-list. This is the security-critical part of the feature and must
// never be bypassed outside of tests.
//
// Storage: pure-Go SQLite (modernc.org/sqlite, no CGO), package-owned
// migrations under migrations/.
package webhooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"path/filepath"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const whCrockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newWHID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", fmt.Errorf("webhooks: generate id: %w", err)
		}
		b[i] = whCrockford[n.Int64()]
	}
	return string(b), nil
}

// ValidTopics is the set of supported event topics a subscription may pick
// from. "test.ping" is reserved for the Settings panel's "Send test" button
// and is not a real subscribable topic (see Dispatcher.SendTest).
var ValidTopics = map[string]bool{
	"device.enrolled":  true, // a new device/instance joined the box's cluster
	"device.removed":   true, // a device/instance left the box's cluster
	"backup.completed": true, // a scheduled or manual backup finished
	"backup.failed":    true, // a scheduled or manual backup failed
	"snapshot.created": true, // a filesystem/cluster snapshot was taken
	"storage.low":      true, // configured storage is running low
	"auth.new_signin":  true, // a new sign-in was observed on the account
}

// backoffSchedule is the exponential backoff in minutes for each retry attempt.
// Attempt 0 = first delivery (no prior failure).
// After attempt 5 the delivery is moved to dead-letter status.
var backoffSchedule = []time.Duration{
	0,           // attempt 0: deliver immediately
	time.Minute, // attempt 1: retry after 1m
	5 * time.Minute,
	25 * time.Minute,
	2 * time.Hour,
	12 * time.Hour, // attempt 5: final attempt before dead-letter
}

// Sentinel errors.
var (
	ErrSubNotFound = errors.New("webhooks: subscription not found")
)

// Subscription is a webhook subscription record.
type Subscription struct {
	ID        string
	URL       string
	Secret    []byte // raw HMAC secret; never exposed in API responses after creation/rotation
	Topics    []string
	Active    bool
	CreatedBy string // user id of the admin who created it (audit only, not a scope filter)
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Delivery is a webhook delivery attempt record.
type Delivery struct {
	ID             string
	SubscriptionID string
	Topic          string
	Payload        json.RawMessage
	Status         string // pending|delivered|failed|dead
	AttemptCount   int
	NextAttemptAt  time.Time
	LastAttemptAt  *time.Time
	LastStatusCode *int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store is the SQLite-backed webhook store.
type Store struct {
	db          *sql.DB
	validateURL func(rawURL string) error // SSRF guard; defaults to validateWebhookURL
}

// OpenStore opens (or creates) the webhooks SQLite store at
// <dbDir>/webhooks.db and applies package-owned migrations idempotently. If
// dbDir is empty an in-memory store is used (tests only — production code
// must always pass a real dbDir so subscriptions survive a restart).
func OpenStore(dbDir string) (*Store, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "webhooks.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("webhooks: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("webhooks: migrate: %w", err)
	}
	return &Store{db: db, validateURL: validateWebhookURL}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ─── Subscription CRUD ────────────────────────────────────────────────────────

// generateSecret returns a cryptographically random 32-byte secret, base64url-encoded.
func generateSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("webhooks: generate secret: %w", err)
	}
	return b, nil
}

// CreateSubscription creates a new webhook subscription with a fresh HMAC
// secret. Returns the Subscription with Secret populated — the only time the
// raw secret is returned (besides RotateSecret).
//
// The supplied URL is validated against the SSRF deny-list before the
// subscription is persisted: loopback, RFC1918, CGNAT, link-local / metadata
// (169.254.0.0/16), ULA, and non-http(s) URLs are all rejected. See ssrf.go.
func (s *Store) CreateSubscription(ctx context.Context, createdBy, url string, topics []string) (Subscription, error) {
	// SSRF guard: reject internal/reserved addresses before touching the DB.
	if err := s.validateURL(url); err != nil {
		return Subscription{}, fmt.Errorf("webhooks: invalid webhook URL: %w", err)
	}
	if len(topics) == 0 {
		return Subscription{}, errors.New("webhooks: at least one topic is required")
	}
	for _, t := range topics {
		if !ValidTopics[t] {
			return Subscription{}, fmt.Errorf("webhooks: unknown topic %q", t)
		}
	}

	id, err := newWHID()
	if err != nil {
		return Subscription{}, err
	}
	secret, err := generateSecret()
	if err != nil {
		return Subscription{}, err
	}

	now := time.Now().UTC()
	topicsJSON, _ := json.Marshal(topics)
	secretB64 := base64.RawURLEncoding.EncodeToString(secret)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions
		  (id, url, secret, topics, active, created_by, created_at, updated_at)
		VALUES (?,?,?,?,1,?,?,?)`,
		id, url, secretB64, string(topicsJSON), createdBy, now.Unix(), now.Unix(),
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("webhooks: create subscription: %w", err)
	}
	return Subscription{
		ID: id, URL: url, Secret: secret, Topics: topics, Active: true,
		CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func scanSubscription(row interface{ Scan(...any) error }) (Subscription, error) {
	var sub Subscription
	var secretB64, topicsJSON, createdBy string
	var active int
	var createdAt, updatedAt int64
	if err := row.Scan(&sub.ID, &sub.URL, &secretB64, &topicsJSON, &active, &createdBy, &createdAt, &updatedAt); err != nil {
		return Subscription{}, err
	}
	sub.Secret, _ = base64.RawURLEncoding.DecodeString(secretB64)
	_ = json.Unmarshal([]byte(topicsJSON), &sub.Topics)
	sub.Active = active != 0
	sub.CreatedBy = createdBy
	sub.CreatedAt = time.Unix(createdAt, 0).UTC()
	sub.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return sub, nil
}

// GetSubscription returns a subscription by id.
func (s *Store) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, url, secret, topics, active, created_by, created_at, updated_at
		FROM webhook_subscriptions WHERE id=?`, id)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrSubNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("webhooks: get subscription: %w", err)
	}
	return sub, nil
}

// ListSubscriptions returns every subscription on the box, oldest first.
func (s *Store) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret, topics, active, created_by, created_at, updated_at
		FROM webhook_subscriptions ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("webhooks: list subscriptions scan: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// UpdateSubscription edits an existing subscription's URL, topics, and/or
// active flag. Pass nil/empty to leave a field unchanged. A non-empty newURL
// is re-validated against the SSRF guard exactly like CreateSubscription.
func (s *Store) UpdateSubscription(ctx context.Context, id string, newURL *string, newTopics []string, newActive *bool) (Subscription, error) {
	sub, err := s.GetSubscription(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	if newURL != nil && *newURL != "" {
		if err := s.validateURL(*newURL); err != nil {
			return Subscription{}, fmt.Errorf("webhooks: invalid webhook URL: %w", err)
		}
		sub.URL = *newURL
	}
	if newTopics != nil {
		for _, t := range newTopics {
			if !ValidTopics[t] {
				return Subscription{}, fmt.Errorf("webhooks: unknown topic %q", t)
			}
		}
		if len(newTopics) == 0 {
			return Subscription{}, errors.New("webhooks: at least one topic is required")
		}
		sub.Topics = newTopics
	}
	if newActive != nil {
		sub.Active = *newActive
	}

	now := time.Now().UTC()
	topicsJSON, _ := json.Marshal(sub.Topics)
	activeInt := 0
	if sub.Active {
		activeInt = 1
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhook_subscriptions SET url=?, topics=?, active=?, updated_at=?
		WHERE id=?`,
		sub.URL, string(topicsJSON), activeInt, now.Unix(), id,
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("webhooks: update subscription: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Subscription{}, ErrSubNotFound
	}
	sub.UpdatedAt = now
	sub.Secret = nil // never re-expose the secret on an edit
	return sub, nil
}

// DeleteSubscription removes a subscription (and, via ON DELETE CASCADE, its
// delivery log).
func (s *Store) DeleteSubscription(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("webhooks: delete subscription: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSubNotFound
	}
	return nil
}

// RotateSecret generates a new HMAC secret for a subscription.
// Returns the new raw secret (only time it is returned).
func (s *Store) RotateSecret(ctx context.Context, id string) ([]byte, error) {
	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}
	secretB64 := base64.RawURLEncoding.EncodeToString(secret)
	now := time.Now().UTC().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET secret=?, updated_at=? WHERE id=?`,
		secretB64, now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("webhooks: rotate secret: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrSubNotFound
	}
	return secret, nil
}

// ─── Dispatch ─────────────────────────────────────────────────────────────────

// enqueueDeliveries creates pending delivery rows for every active
// subscription that includes topic.
func (s *Store) enqueueDeliveries(ctx context.Context, topic string, payload json.RawMessage) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, topics FROM webhook_subscriptions WHERE active=1`)
	if err != nil {
		return fmt.Errorf("webhooks: enqueue query subs: %w", err)
	}
	type match struct{ id string }
	var subs []match
	for rows.Next() {
		var id, topicsJSON string
		if err := rows.Scan(&id, &topicsJSON); err != nil {
			rows.Close()
			return err
		}
		var topics []string
		_ = json.Unmarshal([]byte(topicsJSON), &topics)
		for _, t := range topics {
			if t == topic {
				subs = append(subs, match{id: id})
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, m := range subs {
		did, err := newWHID()
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO webhook_deliveries
			  (id, subscription_id, topic, payload, status, attempt_count,
			   next_attempt_at, created_at, updated_at)
			VALUES (?,?,?,?,?,0,?,?,?)`,
			did, m.id, topic, string(payload), "pending",
			now.Unix(), now.Unix(), now.Unix(),
		)
		if err != nil {
			return fmt.Errorf("webhooks: enqueue delivery: %w", err)
		}
	}
	return nil
}

// nextBackoff returns the time to wait before the next attempt (0 for first).
func nextBackoff(attempt int) time.Duration {
	if attempt < len(backoffSchedule) {
		return backoffSchedule[attempt]
	}
	return 12 * time.Hour
}

// isDeadLetter returns true if the delivery has exceeded all retry attempts.
func isDeadLetter(attempt int) bool {
	return attempt >= len(backoffSchedule)
}

// Dispatcher fans out events to webhook subscriptions.
type Dispatcher struct {
	store      *Store
	httpClient *http.Client
}

// NewDispatcher creates a Dispatcher backed by the given store.
//
// The returned Dispatcher uses an SSRF-safe HTTP client (ssrfSafeDialer) that
// resolves the webhook hostname once at dial time, validates every resolved IP
// against the deny-list, and pins the connection to the first approved IP.
// This prevents DNS-rebinding attacks where an initial subscription-time check
// passes but the DNS answer is subsequently swapped for an internal address.
func NewDispatcher(store *Store) *Dispatcher {
	return &Dispatcher{
		store:      store,
		httpClient: ssrfSafeDialer(),
	}
}

// NewDispatcherWithClient creates a Dispatcher with a custom HTTP client (for tests).
func NewDispatcherWithClient(store *Store, client *http.Client) *Dispatcher {
	return &Dispatcher{store: store, httpClient: client}
}

// Emit enqueues payload for every subscription on topic and kicks off delivery.
// Non-blocking: enqueue is synchronous but HTTP delivery is async.
func (d *Dispatcher) Emit(ctx context.Context, topic string, payload json.RawMessage) error {
	if err := d.store.enqueueDeliveries(ctx, topic, payload); err != nil {
		return fmt.Errorf("webhooks: emit %s: %w", topic, err)
	}
	// Kick off async delivery.
	go d.drainPending(context.Background())
	return nil
}

// drainPending attempts delivery for all pending deliveries whose next_attempt_at is due.
func (d *Dispatcher) drainPending(ctx context.Context) {
	now := time.Now().UTC().Unix()
	rows, err := d.store.db.QueryContext(ctx, `
		SELECT id FROM webhook_deliveries
		WHERE status='pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at ASC
		LIMIT 100`, now)
	if err != nil {
		log.Printf("[webhooks] drainPending query: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		d.deliverOne(ctx, id)
	}
}

// deliverOne attempts a single delivery.
func (d *Dispatcher) deliverOne(ctx context.Context, deliveryID string) {
	var subID, topic, payloadStr, status string
	var attemptCount int
	err := d.store.db.QueryRowContext(ctx, `
		SELECT subscription_id, topic, payload, status, attempt_count
		FROM webhook_deliveries WHERE id=?`, deliveryID,
	).Scan(&subID, &topic, &payloadStr, &status, &attemptCount)
	if err != nil || status != "pending" {
		return
	}

	var url, secretB64 string
	var active int
	err = d.store.db.QueryRowContext(ctx, `
		SELECT url, secret, active FROM webhook_subscriptions WHERE id=?`, subID,
	).Scan(&url, &secretB64, &active)
	if err != nil || active == 0 {
		return
	}

	secret, _ := base64.RawURLEncoding.DecodeString(secretB64)
	body := []byte(payloadStr)
	ts := time.Now().Unix()
	sig := BuildSignature(secret, ts, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		d.recordAttempt(ctx, deliveryID, attemptCount+1, 0, "failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vulos-Signature", sig)
	req.Header.Set("X-Vulos-Topic", topic)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.recordAttempt(ctx, deliveryID, attemptCount+1, 0, "failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.recordAttempt(ctx, deliveryID, attemptCount+1, resp.StatusCode, "delivered")
		return
	}

	nextAttempt := attemptCount + 1
	if isDeadLetter(nextAttempt) {
		d.recordAttempt(ctx, deliveryID, nextAttempt, resp.StatusCode, "dead")
	} else {
		d.recordAttempt(ctx, deliveryID, nextAttempt, resp.StatusCode, "failed")
	}
}

// recordAttempt updates the delivery row after an attempt.
func (d *Dispatcher) recordAttempt(ctx context.Context, deliveryID string, count, statusCode int, newStatus string) {
	now := time.Now().UTC()
	var nextAt time.Time
	if newStatus == "failed" {
		nextAt = now.Add(nextBackoff(count))
	} else {
		nextAt = now // delivered or dead: no more retries
	}

	_, _ = d.store.db.ExecContext(ctx, `
		UPDATE webhook_deliveries SET
		  status=?, attempt_count=?, next_attempt_at=?, last_attempt_at=?,
		  last_status_code=?, updated_at=?
		WHERE id=?`,
		newStatus, count, nextAt.Unix(), now.Unix(), statusCode, now.Unix(), deliveryID,
	)
}

// SendTest performs an immediate, synchronous delivery attempt to sub using a
// synthetic "test.ping" payload — used by the Settings panel's "Send test"
// button so the owner gets instant feedback instead of waiting on the async
// queue/backoff. Goes through the same SSRF-safe client as real deliveries
// and is recorded in the delivery log like any other attempt.
func (d *Dispatcher) SendTest(ctx context.Context, sub Subscription) (statusCode int, err error) {
	did, err := newWHID()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	payload, _ := json.Marshal(map[string]any{
		"test":    true,
		"sent_at": now.Format(time.RFC3339),
	})

	_, dbErr := d.store.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries
		  (id, subscription_id, topic, payload, status, attempt_count,
		   next_attempt_at, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?,?)`,
		did, sub.ID, "test.ping", string(payload), "pending", now.Unix(), now.Unix(), now.Unix(),
	)
	if dbErr != nil {
		return 0, fmt.Errorf("webhooks: record test delivery: %w", dbErr)
	}

	ts := time.Now().Unix()
	sig := BuildSignature(sub.Secret, ts, payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		d.recordAttempt(ctx, did, 1, 0, "failed")
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vulos-Signature", sig)
	req.Header.Set("X-Vulos-Topic", "test.ping")

	resp, doErr := d.httpClient.Do(req)
	if doErr != nil {
		d.recordAttempt(ctx, did, 1, 0, "failed")
		return 0, doErr
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.recordAttempt(ctx, did, 1, resp.StatusCode, "delivered")
	} else {
		d.recordAttempt(ctx, did, 1, resp.StatusCode, "dead") // one-shot test: no retry
	}
	return resp.StatusCode, nil
}

// DeliveryStatus returns the current status of a delivery.
func (s *Store) DeliveryStatus(ctx context.Context, deliveryID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM webhook_deliveries WHERE id=?`, deliveryID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("webhooks: delivery not found")
	}
	return status, err
}

// PendingDeliveries returns all deliveries for a subscription, oldest first
// (used by tests to inspect queue state).
func (s *Store) PendingDeliveries(ctx context.Context, subID string) ([]Delivery, error) {
	return s.deliveriesFor(ctx, subID, 0, false)
}

// ListDeliveries returns the delivery log for a subscription, most recent
// first, capped at limit rows (0 = default of 50). Used by the Settings
// panel's delivery-status view.
func (s *Store) ListDeliveries(ctx context.Context, subID string, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.deliveriesFor(ctx, subID, limit, true)
}

func (s *Store) deliveriesFor(ctx context.Context, subID string, limit int, newestFirst bool) ([]Delivery, error) {
	order := "ASC"
	if newestFirst {
		order = "DESC"
	}
	q := fmt.Sprintf(`
		SELECT id, subscription_id, topic, payload, status, attempt_count,
		       next_attempt_at, last_attempt_at, last_status_code, created_at, updated_at
		FROM webhook_deliveries WHERE subscription_id=? ORDER BY created_at %s`, order)
	args := []any{subID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var payloadStr string
		var nextAt, createdAt, updatedAt int64
		var lastAttempt, lastStatus sql.NullInt64
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.Topic, &payloadStr, &d.Status, &d.AttemptCount,
			&nextAt, &lastAttempt, &lastStatus, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("webhooks: list deliveries scan: %w", err)
		}
		d.Payload = json.RawMessage(payloadStr)
		d.NextAttemptAt = time.Unix(nextAt, 0).UTC()
		d.CreatedAt = time.Unix(createdAt, 0).UTC()
		d.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		if lastAttempt.Valid {
			t := time.Unix(lastAttempt.Int64, 0).UTC()
			d.LastAttemptAt = &t
		}
		if lastStatus.Valid {
			c := int(lastStatus.Int64)
			d.LastStatusCode = &c
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// setDeliveryStatus is a test helper.
func (s *Store) setDeliveryStatus(ctx context.Context, id, status string, attempts int, nextAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries SET status=?, attempt_count=?, next_attempt_at=?, updated_at=?
		WHERE id=?`,
		status, attempts, nextAt.Unix(), time.Now().UTC().Unix(), id)
	return err
}
