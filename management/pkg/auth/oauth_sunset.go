package auth

// oauth_sunset.go — the safety net for removing consumer Google/Microsoft login.
//
// Vulos is an identity PROVIDER. Being simultaneously a CLIENT of Google's meant
// Google held a kill switch over our users' access to their own sovereign server,
// and saw a login event every time one of them signed in. So consumer social login
// is gone (see the deleted routes_auth_oauth.go / oauthlogin.go).
//
// THE HAZARD. An account created by signing up with Google was stored with a
// SENTINEL password hash — LockedPasswordHash below. It is not a valid argon2id
// string, so VerifyPassword can never accept it: those accounts have no password
// at all, and Google was their ONLY key. Deleting the login path without giving
// them another one would lock them out permanently. (The old UnlinkOAuthIdentity
// refused to remove a last sign-in method for exactly this reason.)
//
// THE RESCUE. Such an account's identity email is the address Google gave us — an
// EXTERNAL address — so the ordinary password-reset flow can actually reach it.
// OAuthOnlyAccounts finds every account that still depends on a key we no longer
// accept; the composition root puts each one into the reset flow at startup (see
// cmd/server/wire_oauthsunset.go). An account holding a passkey is already safe and
// is deliberately excluded — it has a second key of its own.
//
// This is expected to find NOTHING: social login was never deployed. It exists so
// that if it ever does find someone, they get a way back in rather than a locked
// door.

import (
	"context"
	"fmt"
)

// LockedPasswordHash is the sentinel that OAuth signup wrote into users.password_hash
// for an account that never had a password. It is deliberately not a valid argon2id
// PHC string, so VerifyPassword always rejects it.
const LockedPasswordHash = "!oauth-no-password"

// OAuthOnlyAccount is an account whose only sign-in method was consumer OAuth.
type OAuthOnlyAccount struct {
	ID    string
	Email string
}

// OAuthOnlyAccounts returns every account that (a) carries the locked-password
// sentinel and (b) has no passkey — i.e. every account that had no key but Google's
// and would be orphaned by the removal of social login.
//
// Ordered by id so the startup rescue logs deterministically.
func (s *Store) OAuthOnlyAccounts(ctx context.Context) ([]OAuthOnlyAccount, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT u.id, u.email
		  FROM users u
		 WHERE u.password_hash = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM webauthn_credentials w WHERE w.user_id = u.id
		   )
		 ORDER BY u.id`), LockedPasswordHash)
	if err != nil {
		return nil, fmt.Errorf("auth: enumerate oauth-only accounts: %w", err)
	}
	defer rows.Close()

	var out []OAuthOnlyAccount
	for rows.Next() {
		var a OAuthOnlyAccount
		if err := rows.Scan(&a.ID, &a.Email); err != nil {
			return nil, fmt.Errorf("auth: scan oauth-only account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: enumerate oauth-only accounts: %w", err)
	}
	return out, nil
}
