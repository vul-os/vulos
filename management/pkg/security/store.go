// Package security provides layered security hardening for vulos.cloud:
// WAF (coraza), bot detection (JA3/JA4), credential-stuffing defense,
// account takeover monitoring, anti-enumeration, CT monitoring,
// honeypot accounts, and egress anomaly detection.
//
// All state is stored via the cpdb dual-backend seam — SQLite
// (modernc.org/sqlite, pure-Go, no CGO) by default; Postgres via
// pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL is set. Obtain a Store
// with Open. The store is shared by all sub-packages in this file.
package security

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Store is the shared cpdb-backed store for the security package.
// Obtain one via Open.
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("security") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("security: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("security: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// DB exposes the raw database handle for test helpers and dashboard queries.
func (s *Store) DB() *sql.DB { return s.db.DB }

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ─── WAF events ────────────────────────────────────────────────────────────────

// WAFEvent records a single WAF rule match.
type WAFEvent struct {
	ID       int64
	Ts       time.Time
	RuleID   string
	Pattern  string
	ClientIP string
	Path     string
}

// RecordWAFEvent persists a WAF violation to the DB.
func (s *Store) RecordWAFEvent(ctx context.Context, ruleID, pattern, clientIP, path string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_waf_events(ts,rule_id,pattern,client_ip,path)
		 VALUES(?,?,?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), ruleID, pattern, clientIP, path,
	)
	return err
}

// RecentWAFEvents returns up to limit WAF events in the last 24 h.
func (s *Store) RecentWAFEvents(ctx context.Context, limit int) ([]WAFEvent, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,rule_id,pattern,client_ip,path
		 FROM security_waf_events WHERE ts >= ? ORDER BY id DESC LIMIT ?`),
		cutoff, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WAFEvent
	for rows.Next() {
		var e WAFEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.RuleID, &e.Pattern, &e.ClientIP, &e.Path); err == nil {
			e.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ─── Bot events ────────────────────────────────────────────────────────────────

// BotEvent records a bot-detection flag.
type BotEvent struct {
	ID       int64
	Ts       time.Time
	ClientIP string
	Score    float64
	Reason   string
	UA       string
}

// RecordBotEvent persists a bot flag.
func (s *Store) RecordBotEvent(ctx context.Context, clientIP string, score float64, reason, ua string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_bot_events(ts,client_ip,score,reason,ua)
		 VALUES(?,?,?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), clientIP, score, reason, ua,
	)
	return err
}

// RecentBotEvents returns up to limit bot events in the last 24 h.
func (s *Store) RecentBotEvents(ctx context.Context, limit int) ([]BotEvent, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,client_ip,score,reason,ua
		 FROM security_bot_events WHERE ts >= ? ORDER BY id DESC LIMIT ?`),
		cutoff, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotEvent
	for rows.Next() {
		var e BotEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.ClientIP, &e.Score, &e.Reason, &e.UA); err == nil {
			e.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ─── Step-up events ────────────────────────────────────────────────────────────

// StepUpEvent records a risk-based step-up challenge.
type StepUpEvent struct {
	ID        int64
	Ts        time.Time
	UserID    string
	ClientIP  string
	RiskScore float64
}

// RecordStepUpEvent persists a step-up challenge.
func (s *Store) RecordStepUpEvent(ctx context.Context, userID, clientIP string, risk float64) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_stepup_events(ts,user_id,client_ip,risk_score)
		 VALUES(?,?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), userID, clientIP, risk,
	)
	return err
}

// RecentStepUpEvents returns up to limit step-up events in the last 24 h.
func (s *Store) RecentStepUpEvents(ctx context.Context, limit int) ([]StepUpEvent, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,user_id,client_ip,risk_score
		 FROM security_stepup_events WHERE ts >= ? ORDER BY id DESC LIMIT ?`),
		cutoff, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepUpEvent
	for rows.Next() {
		var e StepUpEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.ClientIP, &e.RiskScore); err == nil {
			e.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ─── ATO events ────────────────────────────────────────────────────────────────

// ATOEvent records an account takeover anomaly.
type ATOEvent struct {
	ID        int64
	Ts        time.Time
	UserID    string
	Action    string
	ClientIP  string
	Dismissed bool
}

// RecordATOEvent persists an ATO anomaly.
func (s *Store) RecordATOEvent(ctx context.Context, userID, action, clientIP string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_ato_events(ts,user_id,action,client_ip,dismissed)
		 VALUES(?,?,?,?,0)`),
		time.Now().UTC().Format(time.RFC3339), userID, action, clientIP,
	)
	return err
}

// DismissATOEvent marks an ATO event as reviewed.
func (s *Store) DismissATOEvent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE security_ato_events SET dismissed=1 WHERE id=?`), id,
	)
	return err
}

// PendingATOEvents returns un-dismissed ATO events.
func (s *Store) PendingATOEvents(ctx context.Context, limit int) ([]ATOEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,user_id,action,client_ip,dismissed
		 FROM security_ato_events WHERE dismissed=0 ORDER BY id DESC LIMIT ?`),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ATOEvent
	for rows.Next() {
		var e ATOEvent
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.Action, &e.ClientIP, &e.Dismissed); err == nil {
			e.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ─── CT events ────────────────────────────────────────────────────────────────

// CTCert records a certificate observed via CT monitoring.
type CTCert struct {
	ID         int64
	Ts         time.Time
	CommonName string
	Issuer     string
	NotBefore  string
	NotAfter   string
	Recognized bool
}

// UpsertCTCert inserts a newly discovered cert or ignores if already known.
func (s *Store) UpsertCTCert(ctx context.Context, cn, issuer, notBefore, notAfter string, recognized bool) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_ct_certs(ts,common_name,issuer,not_before,not_after,recognized)
		 VALUES(?,?,?,?,?,?) ON CONFLICT(common_name,not_before) DO NOTHING`),
		time.Now().UTC().Format(time.RFC3339), cn, issuer, notBefore, notAfter, recognized,
	)
	return err
}

// RecentCTCerts returns recent CT certificates.
func (s *Store) RecentCTCerts(ctx context.Context, limit int) ([]CTCert, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,common_name,issuer,not_before,not_after,recognized
		 FROM security_ct_certs ORDER BY id DESC LIMIT ?`),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CTCert
	for rows.Next() {
		var c CTCert
		var ts string
		if err := rows.Scan(&c.ID, &ts, &c.CommonName, &c.Issuer, &c.NotBefore, &c.NotAfter, &c.Recognized); err == nil {
			c.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// ─── Honeypot hits ────────────────────────────────────────────────────────────

// HoneypotHit records an attempt against a honeypot account.
type HoneypotHit struct {
	ID       int64
	Ts       time.Time
	Email    string
	ClientIP string
}

// RecordHoneypotHit persists a honeypot login attempt.
func (s *Store) RecordHoneypotHit(ctx context.Context, email, clientIP string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_honeypot_hits(ts,email,client_ip)
		 VALUES(?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), email, clientIP,
	)
	return err
}

// RecentHoneypotHits returns recent honeypot hits.
func (s *Store) RecentHoneypotHits(ctx context.Context, limit int) ([]HoneypotHit, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,email,client_ip
		 FROM security_honeypot_hits ORDER BY id DESC LIMIT ?`),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HoneypotHit
	for rows.Next() {
		var h HoneypotHit
		var ts string
		if err := rows.Scan(&h.ID, &ts, &h.Email, &h.ClientIP); err == nil {
			h.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// ─── Egress anomalies ────────────────────────────────────────────────────────

// EgressAnomaly records a flagged per-account egress spike.
type EgressAnomaly struct {
	ID       int64
	Ts       time.Time
	UserID   string
	BytesHr  int64
	Baseline float64
	Sigma    float64
}

// RecordEgressAnomaly persists a flagged egress spike.
func (s *Store) RecordEgressAnomaly(ctx context.Context, userID string, bytesHr int64, baseline, sigma float64) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_egress_anomalies(ts,user_id,bytes_hr,baseline,sigma)
		 VALUES(?,?,?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), userID, bytesHr, baseline, sigma,
	)
	return err
}

// RecordEgressSample records hourly egress bytes for a user (for baseline calculation).
func (s *Store) RecordEgressSample(ctx context.Context, userID string, bytes int64) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_egress_samples(ts,user_id,bytes)
		 VALUES(?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), userID, bytes,
	)
	return err
}

// EgressBaseline returns the mean and stddev of hourly egress for a user over 7d.
func (s *Store) EgressBaseline(ctx context.Context, userID string) (mean, stddev float64, n int, err error) {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	row := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*),
		        COALESCE(AVG(bytes),0),
		        COALESCE(MAX(bytes)-MIN(bytes),0)
		 FROM security_egress_samples
		 WHERE user_id=? AND ts >= ?`),
		userID, cutoff,
	)
	var rangeVal float64
	if scanErr := row.Scan(&n, &mean, &rangeVal); scanErr != nil {
		return 0, 0, 0, scanErr
	}
	// Simple stddev approximation from range when <3 samples.
	if n < 3 {
		return mean, rangeVal / 2, n, nil
	}
	// Full stddev via second pass.
	rows, qErr := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT bytes FROM security_egress_samples WHERE user_id=? AND ts >= ?`),
		userID, cutoff,
	)
	if qErr != nil {
		return mean, rangeVal / 2, n, nil
	}
	defer rows.Close()
	var sumSq float64
	var cnt int
	for rows.Next() {
		var b float64
		if rows.Scan(&b) == nil {
			d := b - mean
			sumSq += d * d
			cnt++
		}
	}
	if cnt > 1 {
		stddev = math_sqrt(sumSq / float64(cnt-1))
	}
	return mean, stddev, n, nil
}

// math_sqrt is a simple float64 square root (avoids importing math in store.go).
func math_sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x / 2
	for i := 0; i < 50; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// RecentEgressAnomalies returns flagged egress anomalies in the last 7d.
func (s *Store) RecentEgressAnomalies(ctx context.Context, limit int) ([]EgressAnomaly, error) {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id,ts,user_id,bytes_hr,baseline,sigma
		 FROM security_egress_anomalies WHERE ts >= ? ORDER BY id DESC LIMIT ?`),
		cutoff, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressAnomaly
	for rows.Next() {
		var e EgressAnomaly
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.BytesHr, &e.Baseline, &e.Sigma); err == nil {
			e.Ts, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// ─── IP blocklist (honeypot / ATO auto-block) ─────────────────────────────────

// BlockIP inserts a 24-hour IP block. Idempotent.
func (s *Store) BlockIP(ctx context.Context, ip, reason string) error {
	expires := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_ip_blocks(ip,reason,expires_at)
		 VALUES(?,?,?) ON CONFLICT(ip) DO UPDATE SET reason=EXCLUDED.reason, expires_at=EXCLUDED.expires_at`),
		ip, reason, expires,
	)
	return err
}

// IsBlocked returns true if the IP has an active block.
func (s *Store) IsBlocked(ctx context.Context, ip string) bool {
	now := time.Now().UTC().Format(time.RFC3339)
	var count int
	_ = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM security_ip_blocks WHERE ip=? AND expires_at > ?`),
		ip, now,
	).Scan(&count)
	return count > 0
}

// ─── Sensitive action log ─────────────────────────────────────────────────────

// RecordSensitiveAction records a sensitive account action for ATO analysis.
func (s *Store) RecordSensitiveAction(ctx context.Context, userID, action, clientIP string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO security_sensitive_actions(ts,user_id,action,client_ip)
		 VALUES(?,?,?,?)`),
		time.Now().UTC().Format(time.RFC3339), userID, action, clientIP,
	)
	return err
}

// SensitiveActionsInWindow returns sensitive actions for a user in the last windowMins.
func (s *Store) SensitiveActionsInWindow(ctx context.Context, userID string, windowMins int) (int, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(windowMins) * time.Minute).Format(time.RFC3339)
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM security_sensitive_actions WHERE user_id=? AND ts >= ?`),
		userID, cutoff,
	).Scan(&count)
	return count, err
}

// ─── Honeypot accounts table ──────────────────────────────────────────────────

// IsHoneypotAccount returns true if the email belongs to a seeded honeypot account.
func (s *Store) IsHoneypotAccount(ctx context.Context, email string) bool {
	var count int
	_ = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM security_honeypot_accounts WHERE email=?`), email,
	).Scan(&count)
	return count > 0
}

// SeedHoneypotAccounts creates N honeypot accounts if not already present.
// Called once at bootstrap.
func (s *Store) SeedHoneypotAccounts(ctx context.Context, n int) error {
	var existing int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM security_honeypot_accounts`,
	).Scan(&existing)
	if existing >= n {
		log.Printf("[security/honeypot] %d honeypot accounts already seeded", existing)
		return nil
	}
	for i := existing; i < n; i++ {
		email := honeypotEmail(i)
		_, err := s.db.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO security_honeypot_accounts(email,created_at)
			 VALUES(?,?) ON CONFLICT(email) DO NOTHING`),
			email, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return err
		}
		log.Printf("[security/honeypot] seeded %s", email)
	}
	return nil
}

// honeypotEmail generates a deterministic but realistic-looking honeypot email.
// Pattern: dev_test_<2-digit-base36><suffix>@vulos.org
func honeypotEmail(idx int) string {
	suffixes := []string{"42", "99", "07", "13", "88", "21", "55", "77", "33", "61"}
	s := suffixes[idx%len(suffixes)]
	return fmt.Sprintf("dev_test_%s@vulos.org", s)
}
