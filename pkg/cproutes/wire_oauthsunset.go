// wire_oauthsunset.go — startup rescue for accounts orphaned by the removal of
// consumer Google/Microsoft login. See internal/auth/oauth_sunset.go for why this
// exists; in short: an account created by signing up with Google has no password at
// all (a sentinel hash VerifyPassword can never accept), so Google was its only key.
//
// Runs once at boot, before serving. Every such account is put into the ordinary
// password-reset flow — its identity email is the address Google gave us, an
// external address, so the reset can actually reach it. Accounts holding a passkey
// already have a second key and are excluded.
//
// Best-effort by design: a failure to rescue is logged loudly but must NEVER stop
// the control plane from booting. It is expected to find nothing — social login was
// never deployed — and to say so quietly when it does.
package cproutes

import (
	"context"
	"log"

	"github.com/vul-os/vulos-management/pkg/auth"
)

// RescueOAuthOnlyAccounts issues a password reset for every account whose only
// sign-in method was consumer OAuth. Returns the number of accounts it rescued.
func RescueOAuthOnlyAccounts(ctx context.Context, st *auth.Store) int {
	orphans, err := st.OAuthOnlyAccounts(ctx)
	if err != nil {
		log.Printf("[oauth-sunset] WARNING: could not enumerate oauth-only accounts: %v", err)
		return 0
	}
	if len(orphans) == 0 {
		return 0
	}

	log.Printf("[oauth-sunset] WARNING: %d account(s) had NO sign-in method but consumer OAuth, "+
		"which no longer exists. Issuing a password reset to each so they are not locked out.", len(orphans))

	rescued := 0
	for _, a := range orphans {
		if err := st.RequestPasswordReset(ctx, a.Email); err != nil {
			// Loud, and naming the account: a human needs to fix this one by hand,
			// because the alternative is a customer who simply cannot get back in.
			log.Printf("[oauth-sunset] ERROR: account %s (%s) could not be sent a password reset (%v) — "+
				"it currently has NO way to sign in and needs manual recovery", a.ID, a.Email, err)
			continue
		}
		log.Printf("[oauth-sunset] account %s (%s): password reset issued", a.ID, a.Email)
		rescued++
	}
	return rescued
}
