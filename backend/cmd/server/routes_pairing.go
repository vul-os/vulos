package main

// routes_pairing.go — SECURE add-a-device pairing + compromised-device removal.
//
// Routes registered:
//
//	POST /api/pairing/issue    — owner + step-up. An already-trusted owner
//	  device mints a short-lived, single-use, SIGNED pairing ticket (short-code
//	  + QR). No storage secrets are read or embedded. The issuer's device-key
//	  fingerprint is stamped on the ticket (resolved from THIS box's devicekey
//	  identity, never trusted from the client) so a self-pair is refused at
//	  redeem time.
//	POST /api/pairing/claim    — NO owner/step-up gate: the brand-new device has
//	  no session yet, so possession of the valid single-use ticket IS the
//	  bearer authorisation (exactly like POST /api/setup/join-code). Best-effort
//	  IP rate-limiting slows brute force. The new device submits its OWN public
//	  key; it is enrolled and gets a box-signed attestation. It receives only
//	  NON-SECRET connection hints — never the account access/secret keys.
//	GET  /api/pairing/devices  — owner. Lists enrolled devices (public keys +
//	  audit only; nothing secret).
//	POST /api/pairing/revoke   — owner + step-up. Removes a COMPROMISED paired
//	  device by fleet-quorum break-glass revocation of ITS device key
//	  (devicekey.BreakGlassRevokePubKey). The revocation is enforced everywhere
//	  the process-wide checker is wired (IsDeviceKeyRevoked) and propagated
//	  fleet-wide via the RevocationStore gossip surface — a revoked device is
//	  locked out. The local paired-device record is marked revoked to match.
//
// Trust anchors, by design:
//   - PAIRING (granting trust) is anchored on the OWNER SESSION + STEP-UP on the
//     existing trusted device. That is the whole point of "approve a new device
//     from an existing trusted device" — the human on device A vouches.
//   - REMOVAL (revoking trust, when the device to remove may be compromised) is
//     anchored on a fleetid QUORUM of OTHER boxes, because the owner session may
//     itself be on the device being removed. No single box can self-authorise a
//     revocation (fleetid self-exclusion + threshold >= 2). For 1-2 box fleets a
//     quorum cannot be met by design; the flow surfaces that as 403 and the
//     operator uses the off-box recovery anchor.
//
// Owner + step-up gating reuses dkRequireOwnerAndStepup / registryRoster /
// selfFleetVulaID / breakGlassQuorumRequest from routes_devicekey_lifecycle.go,
// so the fail-closed behaviour (nil authStore, no session, non-owner, missing
// step-up) is identical to the existing device-key lifecycle routes.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/pairing"
)

// pairingDeps bundles what registerPairingRoutes needs. KeyStore and
// RevocationStore may be nil (the box has no key store yet); the affected routes
// then reply 503 rather than doing anything unsafe. Registry may be nil — only
// the break-glass revoke path needs it (503 otherwise).
type pairingDeps struct {
	DBDir           string // directory holding the JSON stores (typically <data>/db)
	KeyStore        devicekey.KeyStore
	RevocationStore *devicekey.RevocationStore
	AuthStore       *auth.Store
	Registry        *multiinstance.Registry
}

// registerPairingRoutes wires the pairing + revoke HTTP API into mux. Safe to
// call with nil KeyStore/RevocationStore/Registry (the dependent routes degrade
// to a clear 503).
func registerPairingRoutes(mux *http.ServeMux, deps pairingDeps) {
	rl := newJCRateLimiter() // shared limiter shape with the join-code setup path

	// POST /api/pairing/issue — owner + step-up.
	mux.HandleFunc("POST /api/pairing/issue", func(w http.ResponseWriter, r *http.Request) {
		if deps.KeyStore == nil {
			writeErr(w, http.StatusServiceUnavailable, "device key store unavailable")
			return
		}
		if _, ok := dkRequireOwnerAndStepup(w, r, deps.AuthStore); !ok {
			return
		}
		var body struct {
			DeviceName string `json:"device_name"`
			TTLSeconds int    `json:"ttl_seconds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		// Resolve the issuing device's fingerprint from THIS box's own device
		// identity — never from the client. This becomes the ticket's
		// IssuerDeviceID and is what the self-pair refusal compares against.
		der, err := deps.KeyStore.DeviceIdentity()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read device identity: "+err.Error())
			return
		}
		issuerFP := devicekey.Fingerprint(der)

		ttl := time.Duration(body.TTLSeconds) * time.Second // clamped inside Issue
		ticket, err := pairing.Issue(deps.DBDir, issuerFP, body.DeviceName, ttl)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "issue pairing ticket: "+err.Error())
			return
		}
		authorityPub, _ := pairing.AuthorityPublicKey(deps.DBDir)

		writeJSON(w, map[string]any{
			"short_code":           ticket.ShortCode,
			"token":                ticket.Token,
			"qr_payload":           pairing.QRPayload(ticket, "", ""),
			"issuer_device_id":     ticket.IssuerDeviceID,
			"expires_at":           ticket.ExpiresAt.UTC().Format(time.RFC3339),
			"authority_public_key": authorityPub,
		})
	})

	// POST /api/pairing/claim — no session (the new device has none); the
	// single-use ticket is the bearer authorisation. Rate-limited by IP.
	mux.HandleFunc("POST /api/pairing/claim", func(w http.ResponseWriter, r *http.Request) {
		ip := jcExtractIP(r)
		if rl.limited(ip) {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		var body struct {
			Token           string `json:"token"`
			DeviceName      string `json:"device_name"`
			DevicePublicKey string `json:"device_public_key"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			rl.record(ip)
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.Token == "" || body.DevicePublicKey == "" {
			rl.record(ip)
			writeErr(w, http.StatusBadRequest, "token and device_public_key are required")
			return
		}

		res, err := pairing.Claim(deps.DBDir, body.Token, body.DeviceName, body.DevicePublicKey)
		if err != nil {
			rl.record(ip)
			switch err {
			case pairing.ErrExpired:
				writeErr(w, http.StatusGone, "pairing ticket expired")
			case pairing.ErrInvalid:
				writeErr(w, http.StatusNotFound, "pairing ticket not found or already used")
			case pairing.ErrTampered:
				writeErr(w, http.StatusBadRequest, "pairing ticket signature invalid (tampered or forged)")
			case pairing.ErrSelfPair:
				writeErr(w, http.StatusConflict, "a device may not pair with itself")
			case pairing.ErrBadDeviceKey:
				writeErr(w, http.StatusBadRequest, "device public key missing or malformed")
			default:
				writeErr(w, http.StatusInternalServerError, "claim pairing ticket: "+err.Error())
			}
			return
		}
		rl.reset(ip)
		writeJSON(w, res)
	})

	// GET /api/pairing/devices — owner. A read: no step-up required.
	mux.HandleFunc("GET /api/pairing/devices", func(w http.ResponseWriter, r *http.Request) {
		if !dkOwnerCheck(deps.AuthStore, r) {
			if r.Header.Get("X-User-ID") == "" {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			writeErr(w, http.StatusForbidden, "owner required")
			return
		}
		devices, err := pairing.ListPairedDevices(deps.DBDir)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "list paired devices: "+err.Error())
			return
		}
		if devices == nil {
			devices = []pairing.PairedDevice{}
		}
		writeJSON(w, map[string]any{"devices": devices})
	})

	// POST /api/pairing/revoke — owner + step-up. Break-glass, quorum-gated
	// removal of a compromised paired device.
	mux.HandleFunc("POST /api/pairing/revoke", func(w http.ResponseWriter, r *http.Request) {
		if deps.RevocationStore == nil {
			writeErr(w, http.StatusServiceUnavailable, "device key revocation unavailable")
			return
		}
		if _, ok := dkRequireOwnerAndStepup(w, r, deps.AuthStore); !ok {
			return
		}
		var body struct {
			DeviceID string `json:"device_id"`
			Reason   string `json:"reason"`
			breakGlassQuorumRequest
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.DeviceID == "" {
			writeErr(w, http.StatusBadRequest, "device_id is required")
			return
		}
		if body.RequestID == "" || len(body.QuorumCerts) == 0 {
			writeErr(w, http.StatusBadRequest, "compromised-device removal requires request_id and a non-empty quorum_certs bundle from OTHER boxes")
			return
		}

		dev, ok, err := pairing.GetPairedDevice(deps.DBDir, body.DeviceID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "lookup device: "+err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "no such paired device")
			return
		}
		targetDER, derr := base64.StdEncoding.DecodeString(dev.PublicKey)
		if derr != nil {
			writeErr(w, http.StatusInternalServerError, "stored device public key is not decodable")
			return
		}

		subjectID, serr := body.resolveSubjectID(deps.Registry)
		if serr != nil {
			writeErr(w, http.StatusServiceUnavailable, serr.Error())
			return
		}
		threshold := body.Threshold
		if threshold <= 0 {
			threshold = 2 // fleetid enforces a floor of 2 regardless
		}

		cert, err := devicekey.BreakGlassRevokePubKey(deps.RevocationStore, targetDER, body.Reason, subjectID, body.RequestID, body.QuorumCerts, registryRoster{deps.Registry}, threshold, time.Now())
		if err != nil {
			// 403, not 500: an insufficient/invalid/self-vouched quorum is an
			// authorization failure, and no revocation was recorded.
			writeErr(w, http.StatusForbidden, "compromised-device removal denied: "+err.Error())
			return
		}

		// Reflect the authoritative revocation in the local audit store.
		_ = pairing.MarkRevoked(deps.DBDir, body.DeviceID)

		writeJSON(w, map[string]any{
			"device_id":   body.DeviceID,
			"fingerprint": cert.Fingerprint,
			"method":      cert.Method,
			"revoked_at":  cert.IssuedAt,
			"cert":        cert,
		})
	})
}
