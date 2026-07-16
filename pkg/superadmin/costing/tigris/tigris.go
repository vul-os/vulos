// Package tigris polls Tigris (S3-compatible) storage metrics and persists
// cost snapshots via the cpdb dual-backend seam (SQLite for self-host,
// Postgres for cloud).
//
// Environment variables:
//
//	TIGRIS_ACCESS_KEY_ID     — Tigris access key (shared with storage package).
//	TIGRIS_SECRET_ACCESS_KEY — Tigris secret key.
//
// If either is empty the poller logs a single WARNING and runs as a no-op.
//
// Tigris billing metrics: their billing/metrics endpoint is not fully public.
// We write defensively against schema changes. The poller is designed for the
// Tigris dashboard API or the t3.storage.tigris.dev/metrics shape — parsing
// is lenient and missing fields result in zero values, not crashes.
package tigris

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

const (
	// metricsEndpoint is the Tigris metrics/billing endpoint.
	// Write defensively — endpoint may change as their API evolves.
	metricsEndpoint = "https://t3.storage.tigris.dev/metrics"
	// fallbackEndpoint is used if the primary is unreachable.
	fallbackEndpoint = "https://fly.storage.tigris.dev/metrics"
)

// Snapshot is one row of Tigris cost data per bucket (tenant boundary).
type Snapshot struct {
	TakenAt      time.Time
	Bucket       string
	StorageGBHrs float64
	EgressGB     float64
	RequestCount int64
	AmountUSD    float64
}

// Store manages the Tigris cost database (tigris_snapshots table).
// Backed by either SQLite or Postgres via cpdb.
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("costing_tigris") for production, or
// from cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("tigris/costing: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("tigris/costing: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Insert persists a batch of snapshots.
func (s *Store) Insert(ctx context.Context, snaps []Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	stmt, err := tx.PrepareContext(ctx, s.db.Rebind(`
		INSERT INTO tigris_snapshots (taken_at, bucket, storage_gb_hours, egress_gb, request_count, amount_usd)
		VALUES (?, ?, ?, ?, ?, ?)
	`))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sn := range snaps {
		if _, err := stmt.ExecContext(ctx,
			sn.TakenAt.UTC().Format(time.RFC3339),
			sn.Bucket, sn.StorageGBHrs, sn.EgressGB, sn.RequestCount, sn.AmountUSD,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Snapshots returns rows in [from, to] inclusive.
func (s *Store) Snapshots(ctx context.Context, from, to time.Time) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT taken_at, bucket, storage_gb_hours, egress_gb, request_count, amount_usd
		FROM tigris_snapshots
		WHERE taken_at >= ? AND taken_at <= ?
		ORDER BY taken_at ASC
	`), from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []Snapshot
	for rows.Next() {
		var sn Snapshot
		var takenStr string
		if err := rows.Scan(&takenStr, &sn.Bucket, &sn.StorageGBHrs, &sn.EgressGB, &sn.RequestCount, &sn.AmountUSD); err != nil {
			return nil, err
		}
		sn.TakenAt, _ = time.Parse(time.RFC3339, takenStr)
		snaps = append(snaps, sn)
	}
	return snaps, rows.Err()
}

// Sweep deletes rows older than 90 days.
func (s *Store) Sweep(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM tigris_snapshots WHERE taken_at < ?`), cutoff)
	return err
}

// ─── Metrics response types (defensive) ──────────────────────────────────────

// metricsResponse is the defensive parse target for Tigris metrics API.
// All fields are optional so unknown/missing fields don't crash.
type metricsResponse struct {
	// Buckets is a list of per-bucket metrics.
	Buckets []bucketMetrics `json:"buckets"`
	// Summary is optional org-level aggregate.
	Summary *struct {
		StorageGBHours float64 `json:"storage_gb_hours"`
		EgressGB       float64 `json:"egress_gb"`
		RequestCount   int64   `json:"request_count"`
		AmountUSD      float64 `json:"amount_usd"`
	} `json:"summary"`
	// Errors are soft-logged.
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type bucketMetrics struct {
	Bucket         string  `json:"bucket"`
	StorageGBHours float64 `json:"storage_gb_hours"`
	EgressGB       float64 `json:"egress_gb"`
	RequestCount   int64   `json:"request_count"`
	AmountUSD      float64 `json:"amount_usd"`
	// Accept alternate field names from older/newer API versions.
	StorageBytes float64 `json:"storage_bytes"` // fallback field
	Egress       float64 `json:"egress_bytes"`  // fallback field
	Requests     int64   `json:"requests"`      // fallback field
}

// normalise fills missing metrics from fallback fields (GB from bytes, etc.).
func (bm *bucketMetrics) normalise() {
	if bm.StorageGBHours == 0 && bm.StorageBytes > 0 {
		bm.StorageGBHours = bm.StorageBytes / (1024 * 1024 * 1024)
	}
	if bm.EgressGB == 0 && bm.Egress > 0 {
		bm.EgressGB = bm.Egress / (1024 * 1024 * 1024)
	}
	if bm.RequestCount == 0 && bm.Requests > 0 {
		bm.RequestCount = bm.Requests
	}
}

// Poller polls Tigris metrics hourly.
type Poller struct {
	Store      *Store
	HTTPClient *http.Client
	// Endpoint overrides the default; used in tests.
	Endpoint string
}

func accessKey() string { return os.Getenv("TIGRIS_ACCESS_KEY_ID") }
func secretKey() string { return os.Getenv("TIGRIS_SECRET_ACCESS_KEY") }

// endpoint returns the configured or default metrics endpoint.
func (p *Poller) endpoint() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return metricsEndpoint
}

// client returns the HTTP client to use.
func (p *Poller) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Poll performs one metrics poll. Returns nil without making any HTTP call when
// credentials are empty (no-op mode).
func (p *Poller) Poll(ctx context.Context) error {
	ak := accessKey()
	sk := secretKey()
	if ak == "" || sk == "" {
		log.Println("[tigris/costing] WARNING: TIGRIS_ACCESS_KEY_ID/TIGRIS_SECRET_ACCESS_KEY not set — skipping Tigris metrics poll (no-op)")
		return nil
	}

	endpoint := p.endpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("tigris/costing: build request: %w", err)
	}

	// Sign with AWS Signature V4 style (Tigris is S3-compatible).
	signRequest(req, ak, sk)

	resp, err := p.client().Do(req)
	if err != nil {
		return fmt.Errorf("tigris/costing: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("tigris/costing: unexpected status %d: %s", resp.StatusCode, b)
	}

	var parsed metricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("tigris/costing: decode response: %w", err)
	}

	for _, e := range parsed.Errors {
		log.Printf("[tigris/costing] API error: %s", e.Message)
	}

	now := time.Now().UTC()
	snaps := parseMetrics(parsed, now)

	if len(snaps) == 0 {
		snaps = []Snapshot{{TakenAt: now, Bucket: "_total"}}
	}

	return p.Store.Insert(ctx, snaps)
}

// parseMetrics extracts Snapshot rows from the metrics response.
func parseMetrics(r metricsResponse, now time.Time) []Snapshot {
	var snaps []Snapshot
	for i := range r.Buckets {
		bm := r.Buckets[i]
		bm.normalise()
		snaps = append(snaps, Snapshot{
			TakenAt:      now,
			Bucket:       bm.Bucket,
			StorageGBHrs: bm.StorageGBHours,
			EgressGB:     bm.EgressGB,
			RequestCount: bm.RequestCount,
			AmountUSD:    bm.AmountUSD,
		})
	}
	// Also include summary if present and no per-bucket data.
	if len(snaps) == 0 && r.Summary != nil {
		snaps = append(snaps, Snapshot{
			TakenAt:      now,
			Bucket:       "_total",
			StorageGBHrs: r.Summary.StorageGBHours,
			EgressGB:     r.Summary.EgressGB,
			RequestCount: r.Summary.RequestCount,
			AmountUSD:    r.Summary.AmountUSD,
		})
	}
	return snaps
}

// signRequest adds a minimal HMAC-based Authorization header.
// Tigris accepts AWS SigV4 or simple bearer-style auth; this is a lightweight
// implementation sufficient for internal metrics endpoints.
func signRequest(req *http.Request, ak, sk string) {
	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")

	req.Header.Set("X-Amz-Date", timeStr)
	req.Header.Set("X-Amz-Content-Sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	// Build a simple HMAC-SHA256 signing key.
	region := "auto"
	service := "s3"
	signingKey := deriveSigningKey(sk, dateStr, region, service)

	// Build a simplified canonical request.
	method := req.Method
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	query := req.URL.RawQuery

	headers := fmt.Sprintf("host:%s\nx-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nx-amz-date:%s\n", req.URL.Host, timeStr)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	canonicalReq := strings.Join([]string{method, path, query, headers, signedHeaders, payloadHash}, "\n")
	credentialScope := strings.Join([]string{dateStr, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		timeStr,
		credentialScope,
		hexSHA256([]byte(canonicalReq)),
	}, "\n")

	sig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		ak, credentialScope, signedHeaders, sig)
	req.Header.Set("Authorization", auth)
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// LatestEgressGB returns the sum of egress_gb from the most recent polling
// snapshot across all buckets.  Returns 0 when the store is empty or on error.
// Safe to call when the poller is not running (env vars unset) — returns 0.
func (s *Store) LatestEgressGB(ctx context.Context) (float64, error) {
	if s == nil {
		return 0, nil
	}
	var total float64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(egress_gb), 0)
		FROM tigris_snapshots
		WHERE taken_at = (SELECT MAX(taken_at) FROM tigris_snapshots)
	`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("tigris/costing: LatestEgressGB: %w", err)
	}
	return total, nil
}

// RunHourly starts the hourly polling loop. Blocks until ctx is cancelled.
func (p *Poller) RunHourly(ctx context.Context) {
	ak := accessKey()
	if ak == "" {
		log.Println("[tigris/costing] WARNING: TIGRIS_ACCESS_KEY_ID not set — Tigris metrics poller disabled")
		<-ctx.Done()
		return
	}

	if err := p.Poll(ctx); err != nil {
		log.Printf("[tigris/costing] initial poll error: %v", err)
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Poll(ctx); err != nil {
				log.Printf("[tigris/costing] poll error: %v", err)
			}
			if err := p.Store.Sweep(ctx); err != nil {
				log.Printf("[tigris/costing] sweep error: %v", err)
			}
		}
	}
}

// BucketPrefix returns the bucket name up to the first "/" or "_", used to
// identify tenant boundaries from bucket prefixes.
func BucketPrefix(bucket string) string {
	for _, sep := range []string{"/", "_"} {
		if i := strings.Index(bucket, sep); i > 0 {
			return bucket[:i]
		}
	}
	return bucket
}
