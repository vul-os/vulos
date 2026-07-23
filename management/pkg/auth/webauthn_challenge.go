// webauthn_challenge.go -- shared, multi-machine-safe storage for pending
// WebAuthn ceremonies (IDENTITY-SERVICE §4).
//
// The go-webauthn Begin* calls return a *webauthn.SessionData (the challenge and
// ceremony parameters) that must be handed back to the matching Finish* call.
// Historically the route layer kept this in a per-process in-memory map
// (waCeremonyStore), which is correct on a single machine but BREAKS across a
// multi-machine fleet: a `begin` on machine A and a `finish` on machine B would
// not find the challenge.
//
// This store persists the SessionData in the shared `auth` schema
// (webauthn_challenges table, migration 0012) so any machine can complete a
// ceremony started on any other. Rows are short-lived (waChallengeTTL) and are
// deleted on finish. Because they are written-then-read within seconds, they
// MUST be served from the PRIMARY — never a read replica — so these reads
// deliberately use the primary handle (s.db), not the *Replica accessors.
//
// Keyed by (user_id, kind): at most one pending ceremony of each flavour per
// user at a time, matching the previous in-memory semantics. A new `begin`
// upserts (replaces) any earlier pending ceremony of the same kind.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// waChallengeTTL is how long a pending WebAuthn ceremony remains valid. Matches
// the previous in-memory waPendingTTL.
const waChallengeTTL = 5 * time.Minute

// WebAuthn ceremony kinds. Stored in webauthn_challenges.kind. Exported so the
// route layer names them at the call sites.
const (
	WAKindRegister = "register"
	WAKindLogin    = "login"
	WAKindLogin2FA = "login2fa"
)

// PutWebAuthnChallenge stores (or replaces) the pending ceremony SessionData for
// (userID, kind). It always writes the PRIMARY. Expired rows for the same
// (user, kind) are overwritten; a lazy sweep of globally-expired rows keeps the
// table small without a background job.
func (s *Store) PutWebAuthnChallenge(ctx context.Context, userID, kind string, sd *webauthn.SessionData) error {
	blob, err := json.Marshal(sd)
	if err != nil {
		return fmt.Errorf("auth: marshal webauthn challenge: %w", err)
	}
	n := now()
	expires := n.Add(waChallengeTTL).Format(time.RFC3339)
	created := n.Format(time.RFC3339)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Lazy GC of globally-expired ceremonies (cheap; keyed index on expires_at).
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webauthn_challenges WHERE expires_at < ?`),
		created,
	)

	// Delete any existing pending ceremony of the same kind for this user, then
	// insert the fresh one. A DELETE+INSERT keeps the statement dialect-neutral
	// (no ON CONFLICT / UPSERT syntax differences between SQLite and Postgres)
	// and (user_id, kind) is logically unique here.
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webauthn_challenges WHERE user_id = ? AND kind = ?`),
		userID, kind,
	); err != nil {
		return fmt.Errorf("auth: clear prior webauthn challenge: %w", err)
	}

	id, err := newSessionToken() // reuse the crypto/rand token generator as an opaque handle
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO webauthn_challenges (id, user_id, kind, session_data, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		id, userID, kind, string(blob), expires, created,
	); err != nil {
		return fmt.Errorf("auth: insert webauthn challenge: %w", err)
	}
	return nil
}

// TakeWebAuthnChallenge fetches AND deletes (single-use) the pending ceremony
// SessionData for (userID, kind). Returns ok=false when there is no live
// ceremony (absent or expired). Reads/writes the PRIMARY.
func (s *Store) TakeWebAuthnChallenge(ctx context.Context, userID, kind string) (*webauthn.SessionData, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var blob, expiresAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT session_data, expires_at FROM webauthn_challenges WHERE user_id = ? AND kind = ?`),
		userID, kind,
	).Scan(&blob, &expiresAt)
	if err != nil {
		// sql.ErrNoRows or any error → treat as "no pending ceremony".
		return nil, false, nil
	}

	// Single-use: delete regardless of expiry so a stale row can't be reused.
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webauthn_challenges WHERE user_id = ? AND kind = ?`),
		userID, kind,
	)

	exp, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil || now().After(exp) {
		return nil, false, nil
	}

	var sd webauthn.SessionData
	if err := json.Unmarshal([]byte(blob), &sd); err != nil {
		return nil, false, fmt.Errorf("auth: unmarshal webauthn challenge: %w", err)
	}
	return &sd, true, nil
}
