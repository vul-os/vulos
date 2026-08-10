package auth

// CLUSTER-02: pure-Go SQLite (modernc.org/sqlite — NEVER mattn/CGO) durable
// write-through backing for auth.Store.
//
// Design:
//   - The in-memory maps in Store remain the authoritative working set so all
//     pointer semantics are preserved exactly as before.
//   - SQLite is a durable write-through mirror: every mutating Store method
//     also persists the affected row(s). Rows store the JSON-serialized struct
//     in a `data` column, so the schema never needs to change when User /
//     Session / Profile gain fields.
//   - On boot NewStore opens+migrates SQLite, loads everything from it, then
//     (only if SQLite has never been seeded) performs a one-time import of the
//     legacy auth.json, guarded by a sentinel row in `meta`.
//   - Degraded mode: if SQLite cannot be opened we log and continue fully
//     in-memory (db == nil). Every persist*/delete* is a no-op when db == nil.

import (
	"database/sql"
	"embed"
	"encoding/json"
	"log"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// legacyImportSentinel marks (in the meta table) that the one-time auth.json
// import has already been performed, so it never runs twice.
const legacyImportSentinel = "legacy_json_imported"

// openDB opens (creating if needed) the SQLite database and runs migrations.
// On any failure it returns (nil, err); callers treat that as degraded mode.
func openDB(path string) (*sql.DB, error) {
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies the embedded schema via the shared forward-only runner.
func migrate(db *sql.DB) error {
	return dbmigrate.Apply(db, migrationsFS, "migrations")
}

// --- write-through helpers (all no-op when db == nil) ---

func (s *Store) persistUser(u *User) {
	if s.db == nil || u == nil {
		return
	}
	data, err := json.Marshal(u)
	if err != nil {
		log.Printf("[auth] sqlite: marshal user %s: %v", u.ID, err)
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO users(id, data) VALUES(?, ?)
		 ON CONFLICT(id) DO UPDATE SET data=excluded.data`,
		u.ID, string(data),
	); err != nil {
		log.Printf("[auth] sqlite: persist user %s: %v", u.ID, err)
	}
}

func (s *Store) persistSession(sess *Session) {
	if s.db == nil || sess == nil {
		return
	}
	data, err := json.Marshal(sess)
	if err != nil {
		log.Printf("[auth] sqlite: marshal session: %v", err)
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO sessions(token, user_id, expires_at, data) VALUES(?, ?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET user_id=excluded.user_id, expires_at=excluded.expires_at, data=excluded.data`,
		sess.Token, sess.UserID, sess.ExpiresAt.Format(time.RFC3339Nano), string(data),
	); err != nil {
		log.Printf("[auth] sqlite: persist session: %v", err)
	}
}

func (s *Store) deleteSession(token string) {
	if s.db == nil {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE token=?`, token); err != nil {
		log.Printf("[auth] sqlite: delete session: %v", err)
	}
}

func (s *Store) deleteUserSessions(userID string) {
	if s.db == nil {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		log.Printf("[auth] sqlite: delete sessions for user %s: %v", userID, err)
	}
}

func (s *Store) persistProfile(p *Profile) {
	if s.db == nil || p == nil {
		return
	}
	// The credential fields and any secret-looking Settings keys are written to
	// profile_secrets instead, so the bytes stored here — the row that
	// replicates to a user's other boxes — never contain them. See
	// profile_secrets.go for why each field is treated the way it is.
	cfg, sec := splitProfile(p)
	replicable, local := splitSettings(p.Settings)
	cfg.Settings = replicable
	s.persistProfileSecrets(p.UserID, sec, local)

	data, err := json.Marshal(&cfg)
	if err != nil {
		log.Printf("[auth] sqlite: marshal profile %s: %v", p.UserID, err)
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO profiles(user_id, data) VALUES(?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET data=excluded.data`,
		p.UserID, string(data),
	); err != nil {
		log.Printf("[auth] sqlite: persist profile %s: %v", p.UserID, err)
	}
}

func (s *Store) deleteProfile(userID string) {
	if s.db == nil {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM profiles WHERE user_id=?`, userID); err != nil {
		log.Printf("[auth] sqlite: delete profile %s: %v", userID, err)
	}
}

// loadFromDB replaces the in-memory maps with the contents of SQLite.
// Expired sessions are skipped (and pruned from the DB) so behaviour matches
// the previous JSON-load semantics. Caller must hold s.mu (or be in NewStore
// before the store is shared).
func (s *Store) loadFromDB() error {
	if s.db == nil {
		return nil
	}

	rows, err := s.db.Query(`SELECT data FROM users`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var u User
		if json.Unmarshal([]byte(raw), &u) == nil {
			cp := u
			s.users[cp.ID] = &cp
		}
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT token, data FROM sessions`)
	if err != nil {
		return err
	}
	var expired []string
	for rows.Next() {
		var token, raw string
		if err := rows.Scan(&token, &raw); err != nil {
			rows.Close()
			return err
		}
		var sess Session
		if json.Unmarshal([]byte(raw), &sess) == nil {
			if sess.ExpiresAt.After(time.Now()) {
				cp := sess
				s.sessions[cp.Token] = &cp
			} else {
				expired = append(expired, token)
			}
		}
	}
	rows.Close()
	for _, t := range expired {
		s.deleteSession(t)
	}

	rows, err = s.db.Query(`SELECT data FROM profiles`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var p Profile
		if json.Unmarshal([]byte(raw), &p) == nil {
			cp := p
			s.profiles[cp.UserID] = &cp
		}
	}
	rows.Close()

	// Stitch the per-device half back on, then move any profile still holding
	// credentials in the old single-blob shape into the new one. Order matters:
	// the migration reads what load has already put in memory, so a value found
	// only in the old location is preserved rather than dropped.
	if err := s.loadProfileSecrets(); err != nil {
		return err
	}
	s.migrateProfileSecrets()

	return nil
}

// legacyImported reports whether the one-time auth.json import sentinel is set.
func (s *Store) legacyImported() bool {
	if s.db == nil {
		return true // nothing to import into
	}
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, legacyImportSentinel).Scan(&v)
	return err == nil && v == "1"
}

// markLegacyImported sets the sentinel so the JSON import never runs again.
func (s *Store) markLegacyImported() {
	if s.db == nil {
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES(?, '1')
		 ON CONFLICT(key) DO UPDATE SET value='1'`,
		legacyImportSentinel,
	); err != nil {
		log.Printf("[auth] sqlite: set legacy-import sentinel: %v", err)
	}
}

// importLegacyJSON performs the one-time migration of an existing auth.json
// into SQLite. It is only called when the sentinel is unset. The in-memory
// maps have already been populated from JSON by the caller; this just mirrors
// them into the durable store and sets the sentinel.
func (s *Store) importLegacyJSON() {
	if s.db == nil {
		return
	}
	for _, u := range s.users {
		s.persistUser(u)
	}
	for _, sess := range s.sessions {
		s.persistSession(sess)
	}
	for _, p := range s.profiles {
		s.persistProfile(p)
	}
	s.markLegacyImported()
	log.Printf("[auth] sqlite: one-time auth.json import complete (%d users, %d sessions, %d profiles)",
		len(s.users), len(s.sessions), len(s.profiles))
}

// Close closes the underlying SQLite database (no-op in degraded mode).
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
