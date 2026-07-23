// routes_enroll.go — bare-metal enrollment HTTP routes (RFC 8628 Device
// Authorization Grant) for the vulos.cloud control-plane.
//
// Routes registered:
//
//	POST /enroll/start   — device starts grant (body: {device_pubkey})
//	POST /enroll/poll    — device polls for approval (body: {device_code})
//	POST /enroll/approve — logged-in user approves a grant (session required)
//	POST /enroll/deny    — logged-in user denies a grant (session required)
//
// See BOOT_API.md for the full HTTP surface and error→status map.
//
// Do NOT edit main.go — BOOT-04 owns that file and will call
// RegisterBootEnrollRoutes when wiring the full server.
package cproutes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/enroll"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// RegisterBootEnrollRoutes wires bare-metal enrollment endpoints into mux.
//
// Parameters:
//
//	st           — enroll store (MemStore or SQLStore)
//	signer       — device cert signer
//	routingStore — routing store for binding ULID → account (management scope)
//	authStore    — for session-based approve/deny gate
//	fleetStore   — fleet store for auto-creating device rows on approve (nil-safe:
//	               if nil, enroll succeeds and a log line is emitted instead)
func RegisterBootEnrollRoutes(
	mux *http.ServeMux,
	st enroll.Store,
	signer enroll.CertSigner,
	routingStore routing.Store,
	authStore *auth.Store,
	fleetStore fleet.Store,
) {
	// Fresh rate limiters per registration so tests using separate muxes do
	// not share state. Production creates one mux per process.
	startLim := newIPRateLimiter(time.Minute, 10)
	pollLim := newIPRateLimiter(time.Minute, 60)

	mux.HandleFunc("POST /enroll/start", enrollStartHandler(st, startLim))
	mux.HandleFunc("POST /enroll/poll", enrollPollHandler(st, pollLim))
	mux.HandleFunc("POST /enroll/approve", enrollApproveHandler(st, signer, routingStore, authStore, fleetStore))
	mux.HandleFunc("POST /enroll/deny", enrollDenyHandler(st, authStore))
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-IP rate-limiter
// ─────────────────────────────────────────────────────────────────────────────

type rateBucket struct {
	count   int
	resetAt time.Time
}

type ipRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	maxReqs int
	entries map[string]*rateBucket
}

func newIPRateLimiter(window time.Duration, maxReqs int) *ipRateLimiter {
	return &ipRateLimiter{
		window:  window,
		maxReqs: maxReqs,
		entries: make(map[string]*rateBucket),
	}
}

func (l *ipRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.entries[ip]
	if !ok || now.After(b.resetAt) {
		l.entries[ip] = &rateBucket{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	if b.count >= l.maxReqs {
		return false
	}
	b.count++
	return true
}

// defaultEnrollStartLimiter and defaultEnrollPollLimiter are the production
// rate limiters. Tests call RegisterBootEnrollRoutes with a fresh ServeMux
// and the handlers capture fresh per-call limiters via the closure, so
// tests create fresh limiters via RegisterBootEnrollRoutes each time.

// remoteIPForEnroll extracts the client IP from the request, stripping port.
func remoteIPForEnroll(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ─────────────────────────────────────────────────────────────────────────────
// ULID generation for new device IDs
// ─────────────────────────────────────────────────────────────────────────────

const enrollCrockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newEnrollULID generates a random 26-char Crockford base32 ULID suitable as
// a device identifier. Uses crypto/rand. Panics on rand failure.
func newEnrollULID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", err
		}
		b[i] = enrollCrockfordAlphabet[n.Int64()]
	}
	// Ensure the ULID fits in 128 bits: the first character must be 0-7 in
	// Crockford (first char encodes only 3 bits of the 128-bit value).
	for b[0] > '7' {
		n, err := rand.Int(rand.Reader, big.NewInt(8))
		if err != nil {
			return "", err
		}
		b[0] = enrollCrockfordAlphabet[n.Int64()]
	}
	return string(b), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// pubKeyForUserCode — optional store extension
// ─────────────────────────────────────────────────────────────────────────────

// pubKeyStore is an optional interface that stores may implement to expose the
// raw device public key for a user_code. Both SQLStore and MemStore implement
// this via PubKeyForUserCode.
type pubKeyStore interface {
	PubKeyForUserCode(userCode string) ([]byte, error)
}

func getPubKeyForEnrollApprove(ctx context.Context, st enroll.Store, userCode string) ([]byte, error) {
	if pks, ok := st.(pubKeyStore); ok {
		return pks.PubKeyForUserCode(userCode)
	}
	return nil, errors.New("enroll: store does not support PubKeyForUserCode")
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /enroll/start
// ─────────────────────────────────────────────────────────────────────────────

const (
	enrollGrantTTL      = 15 * time.Minute
	enrollGrantInterval = 5 // polling interval in seconds (RFC 8628 §3.5)
)

type enrollStartRequest struct {
	DevicePubKey string `json:"device_pubkey"` // base64-encoded ed25519 public key
}

func enrollStartHandler(st enroll.Store, lim *ipRateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIPForEnroll(r)
		if !lim.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var req enrollStartRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		if req.DevicePubKey == "" {
			httpx.Err(w, http.StatusBadRequest, enroll.ErrInvalidPubKey.Error())
			return
		}

		// Accept standard base64 or raw URL-safe base64.
		pubKey, err := base64.StdEncoding.DecodeString(req.DevicePubKey)
		if err != nil {
			pubKey, err = base64.RawURLEncoding.DecodeString(req.DevicePubKey)
			if err != nil {
				httpx.Err(w, http.StatusBadRequest, enroll.ErrInvalidPubKey.Error())
				return
			}
		}
		if len(pubKey) != ed25519.PublicKeySize {
			httpx.Err(w, http.StatusBadRequest, enroll.ErrInvalidPubKey.Error())
			return
		}

		now := time.Now().UTC()
		g := enroll.EnrollGrant{
			DeviceCode:      enroll.GenDeviceCode(),
			UserCode:        enroll.GenUserCode(),
			VerificationURI: env.SiteURL() + "/activate",
			Interval:        enrollGrantInterval,
			ExpiresIn:       int(enrollGrantTTL.Seconds()),
			ExpiresAt:       now.Add(enrollGrantTTL),
		}

		if err := st.StartGrant(r.Context(), g, pubKey); err != nil {
			httpx.Err(w, bootEnrollErrStatus(err), err.Error())
			return
		}

		httpx.JSON(w, g)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /enroll/poll
// ─────────────────────────────────────────────────────────────────────────────

type enrollPollRequest struct {
	DeviceCode string `json:"device_code"`
}

func enrollPollHandler(st enroll.Store, lim *ipRateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIPForEnroll(r)
		if !lim.Allow(ip) {
			// RFC 8628 §3.5: slow_down is a 200 with an error field.
			httpx.JSON(w, map[string]string{"error": "slow_down"})
			return
		}

		var req enrollPollRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		if req.DeviceCode == "" {
			httpx.Err(w, http.StatusBadRequest, "device_code is required")
			return
		}

		result, err := st.PollGrant(r.Context(), req.DeviceCode)
		if err != nil {
			switch {
			case errors.Is(err, enroll.ErrAuthPending):
				httpx.JSON(w, map[string]string{"error": "authorization_pending"})
			case errors.Is(err, enroll.ErrSlowDown):
				httpx.JSON(w, map[string]string{"error": "slow_down"})
			case errors.Is(err, enroll.ErrExpired):
				httpx.Err(w, http.StatusGone, "expired_token")
			case errors.Is(err, enroll.ErrAccessDenied):
				httpx.Err(w, http.StatusForbidden, "access_denied")
			case errors.Is(err, enroll.ErrUnknownDeviceCode):
				httpx.Err(w, http.StatusNotFound, err.Error())
			case errors.Is(err, enroll.ErrAlreadyConsumed):
				httpx.Err(w, http.StatusConflict, "already_consumed")
			default:
				httpx.Err(w, http.StatusInternalServerError, err.Error())
			}
			return
		}

		httpx.JSON(w, result)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /enroll/approve
// ─────────────────────────────────────────────────────────────────────────────

type enrollApproveRequest struct {
	UserCode string `json:"user_code"`
}

func enrollApproveHandler(
	st enroll.Store,
	signer enroll.CertSigner,
	routingStore routing.Store,
	authStore *auth.Store,
	fleetStore fleet.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession already wrote 401
		}

		var req enrollApproveRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		if req.UserCode == "" {
			httpx.Err(w, http.StatusBadRequest, "user_code is required")
			return
		}

		// Validate the user_code exists (also confirms it hasn't expired).
		if _, err := st.GetByUserCode(r.Context(), req.UserCode); err != nil {
			httpx.Err(w, bootEnrollErrStatus(err), err.Error())
			return
		}

		// Retrieve the device public key (required for cert signing).
		pubKey, err := getPubKeyForEnrollApprove(r.Context(), st, req.UserCode)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "could not retrieve device public key")
			return
		}

		// Generate a fresh ULID for this device.
		ulid, err := newEnrollULID()
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "ulid generation failed")
			return
		}

		// Bind ULID → account in routing store (management scope; default mode=fabric).
		if _, err := routingStore.Enroll(r.Context(), ulid, u.ID, routing.ModeFabric); err != nil {
			httpx.Err(w, http.StatusInternalServerError, "routing enroll failed: "+err.Error())
			return
		}

		// Mint device certificate.
		deviceCert, err := signer.SignDeviceCert(pubKey, u.ID, ulid)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "cert signing failed: "+err.Error())
			return
		}

		// Persist approval.
		if err := st.ApproveGrant(r.Context(), req.UserCode, u.ID, ulid, deviceCert); err != nil {
			httpx.Err(w, bootEnrollErrStatus(err), err.Error())
			return
		}

		// Auto-create fleet device row. Non-fatal: a failure here must not
		// prevent the device from receiving its cert.
		if fleetStore == nil {
			log.Printf("enroll: fleet store not wired; skipping device row creation for ulid=%s", ulid)
		} else {
			d := fleet.Device{
				ULID:      ulid,
				AccountID: u.ID,
				Name:      "",
				Channel:   "stable",
				Health:    "unknown",
				CreatedAt: time.Now().UTC(),
			}
			if upsertErr := fleetStore.UpsertDevice(r.Context(), d); upsertErr != nil {
				log.Printf("enroll: fleet UpsertDevice failed for ulid=%s (non-fatal): %v", ulid, upsertErr)
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /enroll/deny
// ─────────────────────────────────────────────────────────────────────────────

type enrollDenyRequest struct {
	UserCode string `json:"user_code"`
}

func enrollDenyHandler(st enroll.Store, authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession already wrote 401
		}

		var req enrollDenyRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		if req.UserCode == "" {
			httpx.Err(w, http.StatusBadRequest, "user_code is required")
			return
		}

		if err := st.DenyGrant(r.Context(), req.UserCode); err != nil {
			httpx.Err(w, bootEnrollErrStatus(err), err.Error())
			return
		}

		_ = u
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error → HTTP status mapping
// ─────────────────────────────────────────────────────────────────────────────

func bootEnrollErrStatus(err error) int {
	switch {
	case errors.Is(err, enroll.ErrInvalidPubKey):
		return http.StatusBadRequest // 400
	case errors.Is(err, enroll.ErrAuthPending):
		return http.StatusOK // 200 (with {"error":"authorization_pending"})
	case errors.Is(err, enroll.ErrSlowDown):
		return http.StatusOK // 200 (with {"error":"slow_down"})
	case errors.Is(err, enroll.ErrExpired):
		return http.StatusGone // 410
	case errors.Is(err, enroll.ErrAccessDenied):
		return http.StatusForbidden // 403
	case errors.Is(err, enroll.ErrUnknownDeviceCode),
		errors.Is(err, enroll.ErrUnknownUserCode):
		return http.StatusNotFound // 404
	case errors.Is(err, enroll.ErrAlreadyConsumed):
		return http.StatusConflict // 409
	default:
		return http.StatusInternalServerError // 500
	}
}

// Ensure strings is used (imported for any future cohort/split logic in
// this file, matching the convention in routes_ota.go).
var _ = strings.Contains
