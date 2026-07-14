// Package auth — cloudunified.go
//
// UNIFIED-SIGNIN: one Vulos account signs you in to the cloud AND to this OS.
//
// POST /api/auth/cloud/login {email, password, totp_code?, profile_name?}
//
// The OS backend acts as the broker-driving client so the browser never
// touches the cloud directly:
//
//  1. CP password login via services/cloudclient (PoW + Origin + cookie jar),
//     surfacing structured steps the UI can prompt on:
//     {"step":"email_verification_required" | "totp_required" |
//     "passkey_required" | "enrollment_required"}.
//  2. Broker pubkey pinning: VULOS_CLOUD_BROKER_PUBKEY env wins; otherwise the
//     key is TOFU-pinned to disk on first login and a LATER key change served
//     by the cloud REFUSES the login (a silent overwrite would let a
//     man-in-the-middle swap the broker key — fail closed instead).
//  3. Device identity: the box's cloud-enrolled ULID (RFC 8628 grant via
//     services/cloudenroll). Not enrolled → "enrollment_required", and the
//     companion endpoints /api/auth/cloud/enroll/{start,status} drive the
//     user-code approval flow.
//  4. CP mints a 120 s device-bound login token; the OS validates it through
//     the SAME verifier path as POST /api/auth/cloudlogin (signature, expiry,
//     device binding, replay dedup — nothing bypassed) and mints the OS
//     session.
package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// ─── Device-enrollment seam ───────────────────────────────────────────────────

// CloudEnrollStatus is a snapshot of the box's RFC 8628 enrollment grant.
type CloudEnrollStatus struct {
	// State: "unavailable" | "idle" | "pending" | "approved" | "error".
	State           string `json:"state"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ULID            string `json:"ulid,omitempty"`
	Error           string `json:"error,omitempty"`
}

// CloudEnrollment is the seam to the box's device-enrollment machinery
// (implemented over services/cloudenroll.Manager and wired in main.go).
type CloudEnrollment interface {
	// Identity returns the enrolled device identity; ulid == "" (with nil err)
	// means the box is not enrolled.
	Identity() (ulid, account string, err error)
	// Begin starts (or joins) an enrollment grant, returning the user_code +
	// verification_uri for the owner-approval screen.
	Begin(ctx context.Context) (userCode, verificationURI string, err error)
	// Status snapshots the grant state machine.
	Status() CloudEnrollStatus
}

// CloudLoginClient is the CP client surface handleCloudUnifiedLogin drives.
// Implemented by *cloudclient.Client; a seam so tests can fault-inject.
type CloudLoginClient interface {
	Login(ctx context.Context, email, password, totpCode string) (*CloudLoginOutcome, error)
	BrokerPubkey(ctx context.Context) (ed25519.PublicKey, error)
	IssueLoginToken(ctx context.Context, ulid, profileName string) (payload []byte, sigB64 string, err error)
}

// CloudLoginOutcome mirrors cloudclient.LoginOutcome without importing it
// (kept dependency-light; the adapter in cloudunified_client.go converts).
type CloudLoginOutcome struct {
	Step string
	User json.RawMessage
}

// ─── Broker pubkey pinning ───────────────────────────────────────────────────

// ErrBrokerKeyMismatch — the cloud served a broker key DIFFERENT from the one
// pinned on this device. Refuse: a silently accepted key change is exactly the
// shape of a broker-key-swap attack. Recovery is a deliberate operator action
// (re-enroll / set VULOS_CLOUD_BROKER_PUBKEY).
var ErrBrokerKeyMismatch = errors.New("cloudlogin: cloud broker key does not match the key pinned on this device")

// ensureBrokerPubkey returns the broker pubkey to verify with:
//   - VULOS_CLOUD_BROKER_PUBKEY env holding an INLINE base64 key → that key,
//     always (never fetched, never written) — the operator override wins.
//   - a key is pinned on disk (the env-configured path, or the well-known
//     path) → fetch the cloud's current key and REQUIRE it to match
//     (ErrBrokerKeyMismatch otherwise — never silently overwrite a pin; a
//     changed key is the shape of a broker-key-swap attack). If the fetch
//     itself fails we proceed with the pinned key — verification still fails
//     closed on a bad signature.
//   - nothing pinned yet → fetch and TOFU-pin (WriteBrokerPubkey) once.
func ensureBrokerPubkey(ctx context.Context, fetch func(context.Context) (ed25519.PublicKey, error)) (ed25519.PublicKey, error) {
	if env := strings.TrimSpace(os.Getenv("VULOS_CLOUD_BROKER_PUBKEY")); env != "" {
		if brokerEnvIsInlineKey(env) {
			return LoadBrokerPubkey() // inline env key: override wins; error if malformed
		}
		// env is a PATH: treat it as the pin file location (fall through).
	}

	pinned, err := LoadBrokerPubkey()
	switch {
	case err == nil:
		served, ferr := fetch(ctx)
		if ferr != nil {
			log.Printf("[cloudlogin] broker pubkey fetch failed (%v) — using pinned key", ferr)
			return pinned, nil
		}
		if subtle.ConstantTimeCompare(pinned, served) != 1 {
			log.Printf("[cloudlogin] SECURITY: cloud served a DIFFERENT broker key than the pinned one — refusing (no silent overwrite)")
			return nil, ErrBrokerKeyMismatch
		}
		return pinned, nil

	case errors.Is(err, ErrNoBrokerPubkey):
		served, ferr := fetch(ctx)
		if ferr != nil {
			return nil, ferr
		}
		if werr := WriteBrokerPubkey(served); werr != nil {
			// Pin failure is not fatal for THIS login (we hold the key in hand),
			// but it must be loud: next boot would re-TOFU.
			log.Printf("[cloudlogin] warning: could not pin broker pubkey: %v", werr)
		} else {
			log.Printf("[cloudlogin] broker pubkey TOFU-pinned from cloud")
		}
		return served, nil

	default:
		return nil, err
	}
}

// PinBrokerPubkeyAtEnrollment pins the cloud login-broker pubkey to disk at
// owner-approved ENROLLMENT time, closing the first-login TOFU window: by the
// time login #1 runs, ensureBrokerPubkey finds a pinned key and takes the
// mismatch-checked (fail-closed) branch instead of trust-on-first-use.
//
// Enrollment is the strictly-better trust anchor (the account owner has just
// approved this device in-browser against their cloud account), so pinning the
// broker key at that same moment is stronger than pinning it lazily on the
// first login. `fetch` is an UNAUTHENTICATED read of the CP's active broker
// pubkey (GET /api/profile/broker/pubkey), reachable during enrollment.
//
// Behaviour deliberately mirrors ensureBrokerPubkey's env handling: an INLINE
// VULOS_CLOUD_BROKER_PUBKEY override wins and there is nothing to pin (disk is
// ignored by LoadBrokerPubkey), so this is a no-op in that case. Otherwise the
// fetched key is written via the SAME WriteBrokerPubkey path ensureBrokerPubkey
// uses. Returning an error is NON-FATAL to enrollment — the caller logs loudly
// and falls back to today's first-login TOFU.
func PinBrokerPubkeyAtEnrollment(ctx context.Context, fetch func(context.Context) (ed25519.PublicKey, error)) error {
	if env := strings.TrimSpace(os.Getenv("VULOS_CLOUD_BROKER_PUBKEY")); env != "" {
		if brokerEnvIsInlineKey(env) {
			// Inline env override wins; LoadBrokerPubkey never reads disk. Nothing
			// to pin — leaving the override authoritative, not weakening it.
			return nil
		}
		// env is a PATH: it is the pin-file location; fall through and write it.
	}
	served, err := fetch(ctx)
	if err != nil {
		return fmt.Errorf("cloudlogin: fetch broker pubkey at enrollment: %w", err)
	}
	if err := WriteBrokerPubkey(served); err != nil {
		return fmt.Errorf("cloudlogin: pin broker pubkey at enrollment: %w", err)
	}
	return nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// cloudUnifiedLoginRequest is the wire format for POST /api/auth/cloud/login.
type cloudUnifiedLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code,omitempty"`
	// ProfileName names the OS profile the cloud token is minted for; defaults
	// to the local part of the email.
	ProfileName string `json:"profile_name,omitempty"`
}

// newCloudLoginClient constructs the CP client for a request; overridable in
// tests (and by main.go if it ever needs a custom transport).
var newCloudLoginClient = defaultCloudLoginClient

// handleCloudUnifiedLogin is POST /api/auth/cloud/login.
//
// Responses:
//   - 200 CloudSessionInfo (+ OS session cookie)   — signed in.
//   - 200 {"step": "..."}                          — UI must prompt:
//     totp_required | email_verification_required | passkey_required |
//     enrollment_required (extra: enroll_available bool).
//   - 401 {"error":"invalid_credentials"|"invalid_totp"| verifier errors}
//   - 403 {"error":"cloud_reauth_required"|...}    — CP refused the mint.
//   - 429 / 502 / 503 for throttles, cloud failures, pin mismatch.
func (h *Handler) handleCloudUnifiedLogin(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	var req cloudUnifiedLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		writeErr(w, 400, "email and password are required")
		return
	}

	client := newCloudLoginClient()
	ctx := r.Context()

	// ── 1. Cloud password (+ TOTP) login ────────────────────────────────────
	outcome, err := client.Login(ctx, req.Email, req.Password, req.TOTPCode)
	if err != nil {
		h.writeCloudLoginErr(w, ip, err)
		return
	}
	if outcome.Step != "" {
		// Structured intermediate step — not a failure; the UI prompts.
		writeJSON(w, map[string]any{"step": outcome.Step})
		return
	}

	// ── 2. Device identity (enrollment) ─────────────────────────────────────
	if h.CloudEnroll == nil {
		writeJSON(w, map[string]any{"step": "enrollment_required", "enroll_available": false})
		return
	}
	ulid, _, err := h.CloudEnroll.Identity()
	if err != nil {
		log.Printf("[cloudlogin] enrollment identity load: %v", err)
		writeErr(w, 503, "device enrollment state unavailable")
		return
	}
	if ulid == "" {
		writeJSON(w, map[string]any{"step": "enrollment_required", "enroll_available": true})
		return
	}

	// ── 3. Broker pubkey (pinned; TOFU once; mismatch refuses) ──────────────
	pubkey, err := ensureBrokerPubkey(ctx, client.BrokerPubkey)
	if err != nil {
		if errors.Is(err, ErrBrokerKeyMismatch) {
			writeErr(w, 503, "cloud broker key mismatch — refusing to sign in (re-enroll this device or contact your admin)")
			return
		}
		log.Printf("[cloudlogin] broker pubkey: %v", err)
		writeErr(w, 502, "could not obtain the cloud broker key")
		return
	}

	// ── 4. Mint the device-bound login token ────────────────────────────────
	profileName := strings.TrimSpace(req.ProfileName)
	if profileName == "" {
		profileName = req.Email
		if at := strings.Index(profileName, "@"); at > 0 {
			profileName = profileName[:at]
		}
	}
	payload, sigB64, err := client.IssueLoginToken(ctx, ulid, profileName)
	if err != nil {
		h.writeCloudIssueErr(w, err)
		return
	}

	// ── 5. Validate through the SAME verifier path as /api/auth/cloudlogin ──
	// (signature → expiry → required fields → device binding → replay dedup).
	// The offline wrapper additionally records the grace-period cache so a
	// later offline login keeps working.
	verifier := NewCloudOfflineVerifier(h.store, pubkey, 0)
	info, err := verifier.Login(payload, sigB64)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeCloudVerifierErr(w, err)
		return
	}

	h.limiter.RecordSuccess(ip)
	h.store.Flush()
	http.SetCookie(w, sessionCookie(r, info.SessionToken))
	writeJSON(w, info)
}

// writeCloudLoginErr maps cloudclient login errors to HTTP responses.
func (h *Handler) writeCloudLoginErr(w http.ResponseWriter, ip string, err error) {
	switch {
	case errors.Is(err, errInvalidCloudCredentials):
		h.limiter.RecordFailure(ip)
		writeErr(w, 401, "invalid_credentials")
	case errors.Is(err, errInvalidCloudTOTP):
		h.limiter.RecordFailure(ip)
		writeErr(w, 401, "invalid_totp")
	case errors.Is(err, errCloudRateLimited):
		writeErr(w, 429, "the cloud is rate limiting sign-in attempts — try again shortly")
	default:
		log.Printf("[cloudlogin] cloud login failed: %v", err)
		writeErr(w, 502, "could not reach Vulos Cloud — check your network connection")
	}
}

// writeCloudIssueErr maps token-mint errors to HTTP responses.
func (h *Handler) writeCloudIssueErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errCloudReauthRequired):
		writeErr(w, 403, "cloud_reauth_required")
	case errors.Is(err, errCloudIssueForbidden):
		writeErr(w, 403, "the cloud refused to authorize this device sign-in: "+err.Error())
	case errors.Is(err, errCloudRateLimited):
		writeErr(w, 429, "too many device sign-in attempts — try again later")
	default:
		log.Printf("[cloudlogin] token issue failed: %v", err)
		writeErr(w, 502, "could not obtain a sign-in token from Vulos Cloud")
	}
}

// writeCloudVerifierErr maps verifier errors exactly like handleCloudLogin.
func writeCloudVerifierErr(w http.ResponseWriter, err error) {
	switch err {
	case ErrTokenExpired:
		writeErr(w, 401, "cloud token has expired")
	case ErrBadSignature:
		writeErr(w, 401, "invalid cloud token signature")
	case ErrDeviceMismatch:
		writeErr(w, 401, "cloud token is not valid for this device")
	case ErrTokenReplay:
		writeErr(w, 401, "cloud token has already been used")
	case ErrNoBrokerPubkey:
		writeErr(w, 503, "cloud login not configured on this device")
	default:
		writeErr(w, 401, err.Error())
	}
}

// handleCloudEnrollStart is POST /api/auth/cloud/enroll/start.
//
// Setup-time and unauthenticated by design (like /api/setup/join): the grant
// only completes when the ACCOUNT OWNER approves the user_code in the cloud
// console, so an anonymous caller gains nothing but a code the owner will
// ignore. One grant runs at a time (repeat calls join it).
func (h *Handler) handleCloudEnrollStart(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}
	if h.CloudEnroll == nil {
		writeErr(w, 503, "device enrollment is not available on this box")
		return
	}
	userCode, uri, err := h.CloudEnroll.Begin(r.Context())
	if err != nil {
		log.Printf("[cloudenroll] begin: %v", err)
		writeErr(w, 502, "could not start device enrollment: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{
		"user_code":        userCode,
		"verification_uri": uri,
	})
}

// handleCloudEnrollStatus is GET /api/auth/cloud/enroll/status.
func (h *Handler) handleCloudEnrollStatus(w http.ResponseWriter, r *http.Request) {
	if h.CloudEnroll == nil {
		writeJSON(w, CloudEnrollStatus{State: "unavailable"})
		return
	}
	writeJSON(w, h.CloudEnroll.Status())
}
