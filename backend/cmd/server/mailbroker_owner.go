package main

// mailbroker_owner.go — WHO is allowed to use the box's BROKERED mail credential.
//
// The X-Vulos-Mail-* headers assistantBrokerHeaders() builds come from the box's
// ENVIRONMENT (VULOS_MAIL_BROKER_EMAIL / _USERNAME / _AUTH / _IMAP_* / _SMTP_*).
// They describe ONE mailbox for the WHOLE BOX — the owner's. When they are
// injected, lilmail authenticates the request with THEM and answers about THAT
// mailbox no matter which OS profile made the call: the caller's OS identity is
// never sent downstream (routes_pim.go deletes X-User-ID before proxying, and
// assistant.Auth documents that UserID is "never sent to the mail service").
//
// Vulos is multi-profile — several accounts share one box — so on a box running
// in broker mode every route that injects these headers is reading (and, through
// the PIM proxy, WRITING) the OWNER's mail, calendar and contacts on behalf of
// whoever called it. services/contacts already draws exactly this line over the
// same data: its whole surface is owner-gated because "the box SIM and the pushed
// phone book are the OWNER's personal address book; another account on a
// multi-user box must never see them". The brokered routes must draw it too.
//
// brokeredMailAllowed is that gate. It fails CLOSED: an absent or unresolvable
// owner denies everyone rather than allowing everyone.
//
// In session-cookie mode (no broker headers configured) the downstream credential
// is the CALLER'S OWN forwarded cookie, so there is no box-wide credential to
// protect — lilmail scopes the answer to whoever the cookie belongs to, and every
// authenticated profile is allowed through.

import (
	"strings"

	"vulos/backend/services/auth"
)

// brokeredMailAllowed reports whether userID may reach the mail service through
// the box-wide brokered credential in broker.
//
//	broker empty  ⇒ session-cookie mode: allowed (the caller's own cookie is the
//	                credential, so there is nothing box-wide to leak).
//	broker set    ⇒ allowed ONLY for the resolved box owner. A nil resolver, an
//	                unresolved owner, or an empty caller id denies.
func brokeredMailAllowed(broker map[string]string, ownerID func() string, userID string) bool {
	if len(broker) == 0 {
		return true
	}
	if ownerID == nil {
		return false
	}
	caller := strings.TrimSpace(userID)
	owner := strings.TrimSpace(ownerID())
	return owner != "" && caller != "" && owner == caller
}

// boxOwnerID adapts the auth store into the owner resolver the brokered-mail
// routes take. Resolved LIVE on every call (not captured at wiring time) so a
// box whose first user is created after startup is tracked, matching
// registerContactsRoutes and routes_telephony.go. A nil store resolves to "",
// which brokeredMailAllowed treats as deny-all.
func boxOwnerID(store *auth.Store) func() string {
	return func() string {
		if store == nil {
			return ""
		}
		return store.AdminUserID()
	}
}
