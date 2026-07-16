// Package webhooks implements the Vulos webhook delivery system.
//
// Users register webhook URLs subscribed to event topics:
//   - mail.delivered, mail.bounced, account.suspended, billing.topup, device.enrolled
//
// Dispatcher.Emit(topic, payload) fans out to all subscribed URLs.
// Delivery is async with at-least-once semantics: events are queued in the
// database, dispatched with exponential backoff (1m, 5m, 25m, 2h, 12h →
// dead-letter), and signed via HMAC-SHA256
// (X-Vulos-Signature: t=<ts>,v1=<hex>).
//
// Each subscriber has its own HMAC secret (rotatable via RotateSecret).
//
// Storage: cpdb dual-backend seam (SQLite for self-host, Postgres for cloud).
package webhooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

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

// ValidTopics is the set of supported event topics.
var ValidTopics = map[string]bool{
	"mail.delivered":    true,
	"mail.bounced":      true,
	"account.suspended": true,
	"billing.topup":     true,
	"device.enrolled":   true,
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
	AccountID string
	URL       string
	Secret    []byte // raw HMAC secret; never exposed in API responses
	Topics    []string
	Active    bool
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

// Store is the cpdb-backed webhook store.
type Store struct {
	db          *cpdb.DB
	mu          sync.Mutex
	validateURL func(rawURL string) error // SSRF guard; defaults to validateWebhookURL
}

// Open opens the webhook store using the provided cpdb.DB and runs
// the embedded schema migration idempotently.
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("webhooks: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
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

// CreateSubscription creates a new webhook subscription with a fresh HMAC secret.
// Returns the Subscription (with Secret populated — only time it is returned raw).
//
// The supplied URL is validated against the SSRF deny-list before the
// subscription is persisted: loopback, RFC1918, CGNAT, link-local / metadata
// (169.254.0.0/16), ULA, and non-http(s) URLs are all rejected.
func (s *Store) CreateSubscription(ctx context.Context, accountID, url string, topics []string) (Subscription, error) {
	// SSRF guard: reject internal/reserved addresses before acquiring the lock
	// so a slow DNS lookup does not hold up other writers.
	if err := s.validateURL(url); err != nil {
		return Subscription{}, fmt.Errorf("webhooks: invalid webhook URL: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate topics.
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

	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO webhook_subscriptions
		  (id, account_id, url, secret, topics, created_at, updated_at, active)
		VALUES (?,?,?,?,?,?,?,1)`),
		id, accountID, url, secretB64, string(topicsJSON),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("webhooks: create subscription: %w", err)
	}
	return Subscription{
		ID: id, AccountID: accountID, URL: url,
		Secret: secret, Topics: topics, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// GetSubscription returns a subscription by id (scoped to accountID).
func (s *Store) GetSubscription(ctx context.Context, accountID, id string) (Subscription, error) {
	var sub Subscription
	var secretB64, topicsJSON, cat, uat string
	var active int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, url, secret, topics, created_at, updated_at, active
		FROM webhook_subscriptions WHERE id=? AND account_id=?`), id, accountID,
	).Scan(&sub.ID, &sub.AccountID, &sub.URL, &secretB64, &topicsJSON, &cat, &uat, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrSubNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("webhooks: get subscription: %w", err)
	}
	sub.Secret, _ = base64.RawURLEncoding.DecodeString(secretB64)
	_ = json.Unmarshal([]byte(topicsJSON), &sub.Topics)
	sub.Active = active != 0
	sub.CreatedAt, _ = time.Parse(time.RFC3339, cat)
	sub.UpdatedAt, _ = time.Parse(time.RFC3339, uat)
	return sub, nil
}

// ListSubscriptions returns all subscriptions for an account.
func (s *Store) ListSubscriptions(ctx context.Context, accountID string) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, account_id, url, secret, topics, created_at, updated_at, active
		FROM webhook_subscriptions WHERE account_id=? ORDER BY created_at ASC`), accountID)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var sub Subscription
		var secretB64, topicsJSON, cat, uat string
		var active int
		if err := rows.Scan(&sub.ID, &sub.AccountID, &sub.URL, &secretB64, &topicsJSON, &cat, &uat, &active); err != nil {
			return nil, err
		}
		sub.Secret, _ = base64.RawURLEncoding.DecodeString(secretB64)
		_ = json.Unmarshal([]byte(topicsJSON), &sub.Topics)
		sub.Active = active != 0
		sub.CreatedAt, _ = time.Parse(time.RFC3339, cat)
		sub.UpdatedAt, _ = time.Parse(time.RFC3339, uat)
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteSubscription removes a subscription.
func (s *Store) DeleteSubscription(ctx context.Context, accountID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webhook_subscriptions WHERE id=? AND account_id=?`), id, accountID)
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
func (s *Store) RotateSecret(ctx context.Context, accountID, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}
	secretB64 := base64.RawURLEncoding.EncodeToString(secret)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE webhook_subscriptions SET secret=?, updated_at=? WHERE id=? AND account_id=?`),
		secretB64, now, id, accountID,
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

// enqueueDeliveries creates pending delivery rows for all subscriptions that
// match the given topic. Called by Dispatcher.Emit inside a mutex.
func (s *Store) enqueueDeliveries(ctx context.Context, topic string, payload json.RawMessage) error {
	// Find subscriptions that include this topic.
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id FROM webhook_subscriptions WHERE active=1`))
	if err != nil {
		return fmt.Errorf("webhooks: enqueue query subs: %w", err)
	}
	var subIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		subIDs = append(subIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// For each subscription, check if it subscribes to this topic.
	now := time.Now().UTC()
	for _, subID := range subIDs {
		var topicsJSON string
		if err := s.db.QueryRowContext(ctx,
			s.db.Rebind(`SELECT topics FROM webhook_subscriptions WHERE id=?`), subID,
		).Scan(&topicsJSON); err != nil {
			continue
		}
		var topics []string
		_ = json.Unmarshal([]byte(topicsJSON), &topics)
		subscribed := false
		for _, t := range topics {
			if t == topic {
				subscribed = true
				break
			}
		}
		if !subscribed {
			continue
		}

		did, err := newWHID()
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, s.db.Rebind(`
			INSERT INTO webhook_deliveries
			  (id, subscription_id, topic, payload, status, attempt_count,
			   next_attempt_at, created_at, updated_at)
			VALUES (?,?,?,?,?,0,?,?,?)`),
			did, subID, topic, string(payload), "pending",
			now.Format(time.RFC3339),
			now.Format(time.RFC3339), now.Format(time.RFC3339),
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
	mu         sync.Mutex
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

// Emit enqueues payload for all subscriptions on topic and kicks off delivery.
// Non-blocking: enqueue is synchronous but HTTP delivery is async.
func (d *Dispatcher) Emit(ctx context.Context, topic string, payload json.RawMessage) error {
	d.store.mu.Lock()
	err := d.store.enqueueDeliveries(ctx, topic, payload)
	d.store.mu.Unlock()
	if err != nil {
		return fmt.Errorf("webhooks: emit %s: %w", topic, err)
	}
	// Kick off async delivery.
	go d.drainPending(context.Background())
	return nil
}

// drainPending attempts delivery for all pending deliveries whose next_attempt_at is due.
func (d *Dispatcher) drainPending(ctx context.Context) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := d.store.db.QueryContext(ctx, d.store.db.Rebind(`
		SELECT id FROM webhook_deliveries
		WHERE status='pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at ASC
		LIMIT 100`), now)
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
	d.store.mu.Lock()
	// Load the delivery + subscription.
	var del Delivery
	var subID, topic, payloadStr, status string
	var attemptCount int
	err := d.store.db.QueryRowContext(ctx, d.store.db.Rebind(`
		SELECT id, subscription_id, topic, payload, status, attempt_count
		FROM webhook_deliveries WHERE id=?`), deliveryID,
	).Scan(&del.ID, &subID, &topic, &payloadStr, &status, &attemptCount)
	if err != nil {
		d.store.mu.Unlock()
		return
	}
	if status != "pending" {
		d.store.mu.Unlock()
		return
	}

	// Load subscription for URL + secret.
	var url, secretB64 string
	var active int
	err = d.store.db.QueryRowContext(ctx, d.store.db.Rebind(`
		SELECT url, secret, active FROM webhook_subscriptions WHERE id=?`), subID,
	).Scan(&url, &secretB64, &active)
	d.store.mu.Unlock()

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

	// Non-2xx: schedule retry.
	nextAttempt := attemptCount + 1
	if isDeadLetter(nextAttempt) {
		d.recordAttempt(ctx, deliveryID, nextAttempt, resp.StatusCode, "dead")
	} else {
		d.recordAttempt(ctx, deliveryID, nextAttempt, resp.StatusCode, "failed")
	}
}

// recordAttempt updates the delivery row after an attempt.
func (d *Dispatcher) recordAttempt(ctx context.Context, deliveryID string, count, statusCode int, newStatus string) {
	d.store.mu.Lock()
	defer d.store.mu.Unlock()

	now := time.Now().UTC()
	var nextAt time.Time
	if newStatus == "failed" {
		nextAt = now.Add(nextBackoff(count))
	} else {
		nextAt = now // delivered or dead: no more retries
	}

	_, _ = d.store.db.ExecContext(ctx, d.store.db.Rebind(`
		UPDATE webhook_deliveries SET
		  status=?, attempt_count=?, next_attempt_at=?, last_attempt_at=?,
		  last_status_code=?, updated_at=?
		WHERE id=?`),
		newStatus, count, nextAt.Format(time.RFC3339),
		now.Format(time.RFC3339), statusCode, now.Format(time.RFC3339),
		deliveryID,
	)
}

// topicsContain checks if a JSON topics string contains the given topic.
func topicsContain(topicsJSON, topic string) bool {
	var topics []string
	_ = json.Unmarshal([]byte(topicsJSON), &topics)
	for _, t := range topics {
		if t == topic {
			return true
		}
	}
	return false
}

// DeliveryStatus returns the current status of a delivery.
func (s *Store) DeliveryStatus(ctx context.Context, deliveryID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT status FROM webhook_deliveries WHERE id=?`), deliveryID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("webhooks: delivery not found")
	}
	return status, err
}

// PendingDeliveries returns deliveries that are pending for a subscription
// (used in tests to inspect queue state).
func (s *Store) PendingDeliveries(ctx context.Context, subID string) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, subscription_id, topic, payload, status, attempt_count
		FROM webhook_deliveries WHERE subscription_id=? ORDER BY created_at ASC`), subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		var payloadStr string
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.Topic, &payloadStr, &d.Status, &d.AttemptCount); err != nil {
			return nil, err
		}
		d.Payload = json.RawMessage(payloadStr)
		out = append(out, d)
	}
	return out, rows.Err()
}

// setDeliveryStatus is a test helper.
func (s *Store) setDeliveryStatus(ctx context.Context, id, status string, attempts int, nextAt time.Time) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE webhook_deliveries SET status=?, attempt_count=?, next_attempt_at=?, updated_at=?
		WHERE id=?`),
		status, attempts, nextAt.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// topicsContainStr is needed by the filter; keep package-level for clarity.
var _ = strings.Contains
