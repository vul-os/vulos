package accountsecurity

// SQLite backing for account-takeover (ATO) monitoring. Pure-Go
// modernc.org/sqlite (never CGO), matching the auth.Store / files.Store
// convention: single-writer connection + the shared forward-only migration
// runner (internal/migrate).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const rfc = time.RFC3339Nano

// store is the SQLite-backed persistence layer for sensitive-action records
// and the alerts derived from them. Unexported: callers use *Service.
type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// modernc/sqlite is not safe for unbounded concurrent writers on one file.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

// ─── Sensitive action log ─────────────────────────────────────────────────

// recordSensitiveAction appends one entry to the raw log used as the
// anomaly-detection window.
func (s *store) recordSensitiveAction(ctx context.Context, userID string, action Action, clientIP, userAgent string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO acctsec_sensitive_actions(event_id,ts,user_id,action,client_ip,user_agent) VALUES(?,?,?,?,?,?)`,
		newEventID(), time.Now().UTC().Format(rfc), userID, string(action), clientIP, userAgent,
	)
	return err
}

// countInWindow returns how many sensitive actions userID performed in the
// last windowMins minutes (inclusive of the one just recorded by the caller).
func (s *store) countInWindow(ctx context.Context, userID string, windowMins int) (int, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(windowMins) * time.Minute).Format(rfc)
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM acctsec_sensitive_actions WHERE user_id=? AND ts >= ?`,
		userID, cutoff,
	).Scan(&n)
	return n, err
}

// countFromIPSince returns how many sensitive actions userID performed from
// clientIP since cutoff (RFC3339Nano). Used to decide whether clientIP is
// "new" to this user's recent activity.
func (s *store) countFromIPSince(ctx context.Context, userID, clientIP, cutoff string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM acctsec_sensitive_actions WHERE user_id=? AND client_ip=? AND ts >= ?`,
		userID, clientIP, cutoff,
	).Scan(&n)
	return n, err
}

// recentSensitiveActions returns the most recent sensitive-action rows for
// userID, newest first.
func (s *store) recentSensitiveActions(ctx context.Context, userID string, limit int) ([]SensitiveActionRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,ts,action,client_ip,user_agent FROM acctsec_sensitive_actions
		 WHERE user_id=? ORDER BY id DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SensitiveActionRecord
	for rows.Next() {
		var rec SensitiveActionRecord
		var ts string
		if err := rows.Scan(&rec.ID, &ts, &rec.Action, &rec.ClientIP, &rec.UserAgent); err != nil {
			return nil, err
		}
		rec.Ts, _ = time.Parse(rfc, ts)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ─── Alerts ────────────────────────────────────────────────────────────────

// insertAlert persists a triggered anomaly and returns its new ID.
func (s *store) insertAlert(ctx context.Context, userID string, action Action, clientIP, reason, notifID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO acctsec_alerts(ts,user_id,action,client_ip,reason,notif_id,status)
		 VALUES(?,?,?,?,?,?, 'pending')`,
		time.Now().UTC().Format(rfc), userID, string(action), clientIP, reason, notifID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// alertOwner returns the user_id an alert belongs to, or "" if it does not
// exist. Callers use this to enforce that a user can only resolve their own
// alerts before mutating anything.
func (s *store) alertOwner(ctx context.Context, alertID int64) (string, error) {
	var userID string
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM acctsec_alerts WHERE id=?`, alertID,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return userID, err
}

// setAlertStatus resolves an alert to status (dismissed|locked), stamping
// resolved_at. Only rows still pending are updated (idempotent — resolving an
// already-resolved alert twice is a no-op, not an error).
func (s *store) setAlertStatus(ctx context.Context, alertID int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE acctsec_alerts SET status=?, resolved_at=? WHERE id=? AND status='pending'`,
		status, time.Now().UTC().Format(rfc), alertID,
	)
	return err
}

// recentAlerts returns userID's alerts newest first (pending and resolved),
// capped at limit.
func (s *store) recentAlerts(ctx context.Context, userID string, limit int) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,ts,action,client_ip,reason,status,resolved_at FROM acctsec_alerts
		 WHERE user_id=? ORDER BY id DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var ts, resolvedAt string
		if err := rows.Scan(&a.ID, &ts, &a.Action, &a.ClientIP, &a.Reason, &a.Status, &resolvedAt); err != nil {
			return nil, err
		}
		a.Ts, _ = time.Parse(rfc, ts)
		if resolvedAt != "" {
			if t, err := time.Parse(rfc, resolvedAt); err == nil {
				a.ResolvedAt = &t
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// newEventID returns a globally-unique identity for one audit entry.
//
// The table's INTEGER PRIMARY KEY AUTOINCREMENT is allocated per box, so two
// machines assign the same id to two different events. That is fine for a local
// log and wrong for a replicated one: a CRDT keyed on it would see one key with
// two conflicting values and drop one box's real event. See migration 0002.
//
// Random rather than derived from the content, because two genuinely identical
// actions (same user, same action, same second, same IP) are two separate
// events and must both survive — a content hash would silently merge them into
// one. crypto/rand rather than math/rand: this key is what a hostile peer would
// have to PREDICT to pre-empt an entry under grow-only merge, so guessability
// is a security property here, not just a collision concern.
func newEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not survivable for a value whose
		// unpredictability is load-bearing, and this runs on the audit path —
		// the one place that must not quietly degrade. A timestamp fallback
		// would be predictable by construction, so refuse instead.
		panic("accountsecurity: crypto/rand unavailable while minting an audit event id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
