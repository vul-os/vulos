// webauthn_enroll.go — operator (super-admin) WebAuthn admin-key ENROLMENT.
//
// A fresh self-host operator, bootstrapped via VULOS_BOOTSTRAP_SUPERADMIN, has a
// main account + super-admin status but NO admin passkey. The admin console login
// (pages.go) requires a WebAuthn assertion, so without a first key the operator
// can never reach the console — and therefore can never enrol a key through the
// authenticated surface. These endpoints close that bootstrap gap.
//
// They mirror the WebAuthn REGISTER begin/finish handlers used for ordinary
// accounts (pkg/cproutes/routes_auth.go) but bind to the ADMIN authenticator
// (admin_webauthn_credentials) and are gated by RequireSuperAdminEnroll — IP
// allowlist + main session + super-admin status, WITHOUT the post-WebAuthn admin
// session that a first-time operator cannot yet hold.
//
// BOOTSTRAP-ONLY. Because the gate accepts a bare main session (no hardware-key
// factor), both handlers refuse once the account already has an admin credential.
// The main-session-only path therefore exists solely for the very first key;
// additional keys must be enrolled through the fully-authenticated console.
package superadmin

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auditlog"
)

// isAdminWebAuthnNotConfigured reports whether err is the "WebAuthn not
// configured" condition from newAdminWebAuthn (ADMIN_WEBAUTHN_RPID / _ORIGIN
// unset). No sentinel is exported by the store, so match on the stable message.
func isAdminWebAuthnNotConfigured(err error) bool {
	return err != nil && strings.Contains(err.Error(), "WebAuthn not configured")
}

// HandleAdminWebAuthnRegisterBegin handles
// POST /api/superadmin/webauthn/register/begin.
// It returns the CredentialCreation options for the bootstrap operator's first
// admin passkey. Gated by RequireSuperAdminEnroll; refuses if a key already exists.
func HandleAdminWebAuthnRegisterBegin(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := AdminAccountIDFromCtx(r.Context())
		if accountID == "" {
			jsonError(w, "forbidden", http.StatusForbidden)
			return
		}

		// Bootstrap-only: once a key exists, this main-session-only path is closed.
		if n, err := store.CountAdminWebAuthnCredentials(r.Context(), accountID); err != nil {
			log.Printf("[superadmin] enroll/begin count credentials: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		} else if n > 0 {
			jsonError(w, "an admin passkey is already registered; enrol additional keys from the console", http.StatusConflict)
			return
		}

		creation, _, err := store.BeginAdminWebAuthnRegistration(r.Context(), accountID)
		if err != nil {
			if isAdminWebAuthnNotConfigured(err) {
				jsonError(w, "WebAuthn not configured on this server", http.StatusServiceUnavailable)
				return
			}
			log.Printf("[superadmin] enroll/begin: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		auditAction(r.Context(), al, accountID, "admin.enroll.begin", accountID, map[string]string{"ip": remoteIP(r)})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(creation)
	}
}

// HandleAdminWebAuthnRegisterFinish handles
// POST /api/superadmin/webauthn/register/finish.
// Body: the raw attestation from navigator.credentials.create. On success the
// admin credential is persisted and the operator can log into the console.
func HandleAdminWebAuthnRegisterFinish(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID := AdminAccountIDFromCtx(r.Context())
		if accountID == "" {
			jsonError(w, "forbidden", http.StatusForbidden)
			return
		}

		// Bootstrap-only: re-check under the same rule as begin (defence in depth
		// against a begin/finish pair racing an already-enrolled account).
		if n, err := store.CountAdminWebAuthnCredentials(r.Context(), accountID); err != nil {
			log.Printf("[superadmin] enroll/finish count credentials: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		} else if n > 0 {
			jsonError(w, "an admin passkey is already registered; enrol additional keys from the console", http.StatusConflict)
			return
		}

		if err := store.FinishAdminWebAuthnRegistration(r.Context(), accountID, r); err != nil {
			if isAdminWebAuthnNotConfigured(err) {
				jsonError(w, "WebAuthn not configured on this server", http.StatusServiceUnavailable)
				return
			}
			log.Printf("[superadmin] enroll/finish: %v", err)
			jsonError(w, "WebAuthn registration failed", http.StatusBadRequest)
			return
		}

		auditAction(r.Context(), al, accountID, "admin.enroll.success", accountID, map[string]string{"ip": remoteIP(r)})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}
