package auth

// oauth_sunset_test.go — the safety net must actually catch the people it is for.
// Removing consumer social login orphans any account whose only key was Google;
// OAuthOnlyAccounts is what finds them so the server can put them into recovery.

import (
	"context"
	"testing"
	"time"
)

// insertRawUser writes a users row directly, so a test can construct an account in
// a state Signup would never produce (e.g. carrying the OAuth sentinel hash).
func insertRawUser(t *testing.T, s *Store, id, email, hash string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(context.Background(), s.db.Rebind(
		`INSERT INTO users (id, email, password_hash, email_verified, totp_enabled, fleet_admin, failed_2fa_count, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		id, email, hash, 1, 0, 0, 0, now, now); err != nil {
		t.Fatalf("insert raw user %s: %v", id, err)
	}
}

func insertPasskey(t *testing.T, s *Store, userID, credID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(context.Background(), s.db.Rebind(
		`INSERT INTO webauthn_credentials (id, user_id, credential_id, public_key, sign_count, transports, friendly_name, aaguid, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		"cred-"+userID, userID, credID, []byte("pk"), 0, "[]", "", "", now); err != nil {
		t.Fatalf("insert passkey for %s: %v", userID, err)
	}
}

// The headline guarantee: an account whose only key was Google (sentinel hash, no
// passkey) is reported so the startup rescue can send it a reset — it is NOT left
// silently locked out.
func TestOAuthOnlyAccounts_FindsTheOrphan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	insertRawUser(t, s, "u-orphan", "orphan@gmail.com", LockedPasswordHash)

	got, err := s.OAuthOnlyAccounts(ctx)
	if err != nil {
		t.Fatalf("OAuthOnlyAccounts: %v", err)
	}
	if len(got) != 1 || got[0].ID != "u-orphan" || got[0].Email != "orphan@gmail.com" {
		t.Fatalf("expected exactly the orphan, got %+v", got)
	}

	// And that email must be routable by the ordinary reset flow — otherwise the
	// rescue is theatre. (Its identity email is the external Google address.)
	if err := s.RequestPasswordReset(ctx, "orphan@gmail.com"); err != nil {
		t.Fatalf("orphan must be reachable by password reset: %v", err)
	}
}

// A sentinel account that ALSO holds a passkey has a second key of its own and must
// NOT be flagged — flagging it would email a pointless reset.
func TestOAuthOnlyAccounts_SkipsPasskeyHolders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	insertRawUser(t, s, "u-passkey", "haspasskey@gmail.com", LockedPasswordHash)
	insertPasskey(t, s, "u-passkey", "credential-1")

	got, err := s.OAuthOnlyAccounts(ctx)
	if err != nil {
		t.Fatalf("OAuthOnlyAccounts: %v", err)
	}
	for _, a := range got {
		if a.ID == "u-passkey" {
			t.Fatalf("account with a passkey has a second key and must not be flagged as orphaned")
		}
	}
}

// A normal password account is never flagged — the query keys strictly on the
// sentinel, so real users are untouched by the sunset.
func TestOAuthOnlyAccounts_IgnoresPasswordAccounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, _, err := s.Signup(ctx, "real@vulos.test", "correct horse battery staple", "127.0.0.1", "test"); err != nil {
		t.Fatalf("signup: %v", err)
	}

	got, err := s.OAuthOnlyAccounts(ctx)
	if err != nil {
		t.Fatalf("OAuthOnlyAccounts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a normal password account must never be flagged, got %+v", got)
	}
}
