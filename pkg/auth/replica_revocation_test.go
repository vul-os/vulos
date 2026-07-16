package auth

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// snapshotSessionsAndUsers copies the current users + sessions rows from src into
// dst, producing a point-in-time (deliberately STALE) replica: rows revoked on
// src AFTER the snapshot still look live in dst. Used to simulate replica lag.
func snapshotSessionsAndUsers(t *testing.T, ctx context.Context, src, dst *cpdb.DB) {
	t.Helper()
	urows, err := src.QueryContext(ctx, `SELECT id, email, password_hash, email_verified, totp_enabled, fleet_admin, failed_2fa_count, created_at, updated_at FROM users`)
	if err != nil {
		t.Fatalf("snapshot users query: %v", err)
	}
	type userRow struct {
		id, email, hash, created, updated                 string
		emailVerified, totpEnabled, fleetAdmin, failed2FA int
	}
	var users []userRow
	for urows.Next() {
		var u userRow
		if err := urows.Scan(&u.id, &u.email, &u.hash, &u.emailVerified, &u.totpEnabled, &u.fleetAdmin, &u.failed2FA, &u.created, &u.updated); err != nil {
			urows.Close()
			t.Fatalf("snapshot users scan: %v", err)
		}
		users = append(users, u)
	}
	urows.Close()
	for _, u := range users {
		if _, err := dst.ExecContext(ctx,
			`INSERT INTO users (id, email, password_hash, email_verified, totp_enabled, fleet_admin, failed_2fa_count, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			u.id, u.email, u.hash, u.emailVerified, u.totpEnabled, u.fleetAdmin, u.failed2FA, u.created, u.updated); err != nil {
			t.Fatalf("snapshot users insert: %v", err)
		}
	}

	srows, err := src.QueryContext(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at, COALESCE(revoked,0) FROM sessions`)
	if err != nil {
		t.Fatalf("snapshot sessions query: %v", err)
	}
	type sessRow struct {
		id, userID, created, lastSeen, expires string
		revoked                                int
	}
	var sessions []sessRow
	for srows.Next() {
		var s sessRow
		if err := srows.Scan(&s.id, &s.userID, &s.created, &s.lastSeen, &s.expires, &s.revoked); err != nil {
			srows.Close()
			t.Fatalf("snapshot sessions scan: %v", err)
		}
		sessions = append(sessions, s)
	}
	srows.Close()
	for _, s := range sessions {
		if _, err := dst.ExecContext(ctx,
			`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, revoked) VALUES (?,?,?,?,?,?)`,
			s.id, s.userID, s.created, s.lastSeen, s.expires, s.revoked); err != nil {
			t.Fatalf("snapshot sessions insert: %v", err)
		}
	}
}

// TestReplicaStaleRevocationNotHonoured is the regression test for the
// replica-staleness REVOCATION BYPASS (IDENTITY-SERVICE §2.4). With
// AUTH_REPLICA_READS=1 and a STALE replica that still shows a session as live,
// LookupSession/IntrospectSession must NOT honour a session that has already been
// revoked on the primary — the confirm-on-valid guard re-reads the primary.
func TestReplicaStaleRevocationNotHonoured(t *testing.T) {
	t.Setenv("AUTH_REPLICA_READS", "1")
	ctx := context.Background()

	// Primary store + a fully-migrated secondary DB that will act as the replica.
	st := openTestStore(t)
	replicaDB, err := cpdb.OpenSQLiteDSN("file:replica_stale?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	// Migrate the replica DB to the same schema by opening an auth store over it.
	if _, err := OpenAuthStore(replicaDB, []byte("test-secret-key-1234567890123456")); err != nil {
		t.Fatalf("migrate replica: %v", err)
	}

	u, token, err := st.Signup(ctx, "revoked@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	// Snapshot NOW, while the session is valid, into the replica — then attach it.
	snapshotSessionsAndUsers(t, ctx, st.db, replicaDB)
	st.db.AttachReplicaForTest(replicaDB.DB)
	if !st.db.HasReplica() {
		t.Fatal("replica should be attached")
	}

	// Sanity: before revocation the (fresh) session validates.
	if _, err := st.LookupSession(ctx, token); err != nil {
		t.Fatalf("pre-revoke LookupSession: %v", err)
	}

	// REVOKE on the PRIMARY only. The stale replica still shows revoked=0.
	if err := st.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	// Confirm the replica is genuinely stale (still returns the row as live).
	var replicaRevoked int
	if err := replicaDB.QueryRowContext(ctx, `SELECT COALESCE(revoked,0) FROM sessions WHERE id = ?`, token).Scan(&replicaRevoked); err != nil {
		t.Fatalf("replica read: %v", err)
	}
	if replicaRevoked != 0 {
		t.Fatal("test precondition broken: replica should still show the session as live")
	}

	// The guard MUST reject: LookupSession re-confirms against the primary.
	if _, err := st.LookupSession(ctx, token); err == nil {
		t.Fatal("SECURITY: revoked session honoured from stale replica (LookupSession)")
	}
	// IntrospectSession must also reject.
	if st.IntrospectSession(ctx, token).Valid {
		t.Fatal("SECURITY: revoked session introspected as valid from stale replica")
	}

	_ = u
}

// TestReplicaStaleSuspensionNotHonoured is the regression test for the
// replica-staleness SUSPENSION BYPASS: a session whose owning account was
// suspended on the primary must be rejected even when a stale replica still
// shows suspended=0. The suspension gate reads the primary directly.
func TestReplicaStaleSuspensionNotHonoured(t *testing.T) {
	t.Setenv("AUTH_REPLICA_READS", "1")
	ctx := context.Background()

	st := openTestStore(t)
	replicaDB, err := cpdb.OpenSQLiteDSN("file:replica_susp?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	if _, err := OpenAuthStore(replicaDB, []byte("test-secret-key-1234567890123456")); err != nil {
		t.Fatalf("migrate replica: %v", err)
	}

	u, token, err := st.Signup(ctx, "suspendme@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	// Snapshot while NOT suspended, then attach the (now-stale) replica.
	snapshotSessionsAndUsers(t, ctx, st.db, replicaDB)
	st.db.AttachReplicaForTest(replicaDB.DB)

	// Suspend on the PRIMARY only.
	st.mu.Lock()
	_, execErr := st.db.ExecContext(ctx, st.db.Rebind(`UPDATE users SET suspended = 1 WHERE id = ?`), u.ID)
	st.mu.Unlock()
	if execErr != nil {
		// The suspended column is added by migration 0008; if absent the test can't run.
		t.Skipf("cannot set suspended (schema?): %v", execErr)
	}

	if _, err := st.LookupSession(ctx, token); err == nil {
		t.Fatal("SECURITY: suspended account's session honoured from stale replica (LookupSession)")
	}
	if st.IntrospectSession(ctx, token).Valid {
		t.Fatal("SECURITY: suspended account's session introspected as valid from stale replica")
	}
}
