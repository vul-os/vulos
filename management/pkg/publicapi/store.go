// Package publicapi implements the versioned public REST API for vulos.cloud.
//
// Routes (all under /api/v1/):
//
//	GET  /api/v1/account/me
//	GET  /api/v1/account/usage
//	GET  /api/v1/billing/wallet
//	POST /api/v1/billing/wallet/topup      (Paystack init only)
//	GET  /api/v1/mailboxes
//	GET  /api/v1/mailboxes/{id}/usage
//	GET  /api/v1/devices
//	GET  /api/v1/webhooks
//	POST /api/v1/webhooks
//	DELETE /api/v1/webhooks/{id}
//
// Auth: per-account API token (Bearer). Scope is separate from session cookies
// and from SCIM bearer tokens. 60 req/min per token.
//
// Storage: pure-Go modernc.org/sqlite (never CGO).
package publicapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"math/big"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

const apiCrockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newAPIID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", fmt.Errorf("publicapi: generate id: %w", err)
		}
		b[i] = apiCrockford[n.Int64()]
	}
	return string(b), nil
}

// Store is the public-API backend.
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
// db should be obtained from cpdb.Open("publicapi") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("publicapi: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("publicapi: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ─── Rate limiter (per API token, 60 req/min) ─────────────────────────────────

// tokenBucket is a per-token rate-limit bucket.
type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen time.Time
}

// allow returns true if the request should be allowed and consumes one token.
func (b *tokenBucket) allow(rps float64, burst int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.lastSeen = now
	b.tokens += elapsed * rps
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimiter enforces 60 req/min per API token with lazy eviction.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	lastGC  time.Time
}

// NewRateLimiter creates a RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*tokenBucket),
	}
}

// Allow returns true if the token is within its rate limit (60 req/min).
func (rl *RateLimiter) Allow(tokenID string) bool {
	const rps = 1.0  // 60/min = 1/s sustained
	const burst = 60 // burst of 60 (one minute's worth)

	rl.mu.Lock()
	now := time.Now()
	// Lazy GC: evict buckets idle for > 5 minutes.
	if now.Sub(rl.lastGC) > 5*time.Minute {
		for k, bkt := range rl.buckets {
			bkt.mu.Lock()
			idle := now.Sub(bkt.lastSeen)
			bkt.mu.Unlock()
			if idle > 5*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastGC = now
	}
	bkt, ok := rl.buckets[tokenID]
	if !ok {
		bkt = &tokenBucket{tokens: float64(burst), lastSeen: now}
		rl.buckets[tokenID] = bkt
	}
	rl.mu.Unlock()
	return bkt.allow(rps, burst)
}

// ─── Account types ────────────────────────────────────────────────────────────

// Account is the /api/v1/account/me response shape.
type Account struct {
	AccountID string    `json:"account_id"`
	Email     string    `json:"email"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

// AccountUsage is the /api/v1/account/usage response shape.
type AccountUsage struct {
	AccountID       string `json:"account_id"`
	MailboxCount    int    `json:"mailbox_count"`
	DeviceCount     int    `json:"device_count"`
	StorageUsedMB   int64  `json:"storage_used_mb"`
	BillingCycleEnd string `json:"billing_cycle_end,omitempty"`
}

// Wallet is the /api/v1/billing/wallet response shape.
type Wallet struct {
	AccountID    string     `json:"account_id"`
	BalanceZAR   float64    `json:"balance_zar"`
	BalanceUSD   float64    `json:"balance_usd"`
	ExchangeRate float64    `json:"exchange_rate"`
	LastTopup    *time.Time `json:"last_topup,omitempty"`
}

// TopupRequest is the POST /api/v1/billing/wallet/topup body.
type TopupRequest struct {
	AmountZAR int64  `json:"amount_zar"` // ZAR subunits (ZAR * 100)
	Email     string `json:"email"`
}

// TopupResponse is returned after Paystack initialisation.
type TopupResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	Reference        string `json:"reference"`
}

// Mailbox is a row in the /api/v1/mailboxes list.
type Mailbox struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

// MailboxUsage is the /api/v1/mailboxes/{id}/usage response.
type MailboxUsage struct {
	MailboxID string `json:"mailbox_id"`
	SentToday int64  `json:"sent_today"`
	SentMonth int64  `json:"sent_month"`
	RecvToday int64  `json:"recv_today"`
	DailyCap  int    `json:"daily_cap"`
}

// Device is a row in the /api/v1/devices list.
type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	EnrolledAt time.Time  `json:"enrolled_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// AccountStore provides account lookups for the public API handlers.
// Implemented by auth.Store in production; stubbed in tests.
type AccountStore interface {
	// GetAccountByID returns email, plan, and created_at for the account.
	GetAccountByID(ctx context.Context, accountID string) (email, plan string, createdAt time.Time, err error)
}

// BillingStore provides wallet info. Implemented by billing.Store.
type BillingStore interface {
	WalletBalance(ctx context.Context, accountID string) (balanceZAR float64, err error)
	ExchangeRate(ctx context.Context) (float64, error)
}

// TopUpInitializer initialises a wallet top-up transaction with the configured
// payment processor. Implemented by the billing layer's payment client.
type TopUpInitializer interface {
	InitTopup(ctx context.Context, accountID, email string, amountZAR int64) (authURL, reference string, err error)
}

// MailboxLister lists mailboxes for an account.
type MailboxLister interface {
	ListMailboxes(ctx context.Context, accountID string) ([]Mailbox, error)
	MailboxUsage(ctx context.Context, accountID, mailboxID string) (MailboxUsage, error)
}

// DeviceLister lists devices for an account.
type DeviceLister interface {
	ListDevices(ctx context.Context, accountID string) ([]Device, error)
}

// WebhookManager manages webhook subscriptions.
type WebhookManager interface {
	ListSubscriptions(ctx context.Context, accountID string) ([]WebhookSub, error)
	CreateSubscription(ctx context.Context, accountID, url string, topics []string) (WebhookSub, error)
	DeleteSubscription(ctx context.Context, accountID, id string) error
}

// WebhookSub is the public webhook subscription shape (no raw secret).
type WebhookSub struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	URL       string    `json:"url"`
	Topics    []string  `json:"topics"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}
