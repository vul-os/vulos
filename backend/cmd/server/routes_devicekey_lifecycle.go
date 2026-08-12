package main

// routes_devicekey_lifecycle.go — device-key ROTATION + REVOCATION HTTP API.
//
// Routes registered:
//
//	POST /api/auth/device/rotate       — owner + step-up. Normal rotation
//	  needs nothing else; break_glass=true additionally requires a quorum
//	  vouch bundle (fleetid.VerifyQuorum, action=identity-recovery) from OTHER
//	  boxes in the fleet — see devicekey.BreakGlassRotate.
//	POST /api/auth/device/revoke       — owner + step-up. Same normal /
//	  break-glass split as rotate, targeting this box's CURRENT device key.
//	GET  /api/auth/device/revocations  — public (no owner/step-up gate,
//	  mirroring the existing GET /api/auth/device/identity and
//	  GET /api/instances/{ulid}/apps endpoints): the propagation pull surface.
//	  A peer box fetches this, verifies every entry against ITS OWN roster
//	  (devicekey.MergeRevocationBatch), and merges what verifies — see
//	  devicekey/propagation.go's package doc for why no additional transport
//	  trust is needed.
//
// The hard rule this file's break-glass paths defer to entirely:
// devicekey.BreakGlassRotate / BreakGlassRevoke require fleetid.VerifyQuorum
// to succeed BEFORE touching any key material, and there is no exported way
// to skip that check (see devicekey/keystore.go's forceInstallIdentity doc
// comment). This handler cannot, and does not try to, replicate or bypass
// that logic — it only gathers the request, resolves this box's own
// fleet-fabric subject id when the caller omits it, and forwards to the
// devicekey package.
//
// Owner + step-up gating mirrors routes_stepup.go / routes_instances_manage.go
// exactly: X-User-ID is read from the header the auth middleware stamps from
// the validated session (never trusted from the client), and stepup.Require
// enforces a fresh password re-proof for this destructive action.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/fleetid"
	"vulos/backend/services/peering"
	"vulos/backend/services/stepup"
)

// registryRoster adapts *multiinstance.Registry (this box's own view of its
// fleet — read-only here) to fleetid.Roster: a peer counts toward quorum only
// when it is a member of THIS box's own roster, exactly like fleetid's design
// intends (a compromised peer cannot fabricate roster membership). Vulos IDs
// are derived from each instance's fabric Ed25519 public key, NOT from any
// device's ECDSA identity key — the two are deliberately different key
// spaces (see rotation.go's QuorumSubjectID doc comment).
type registryRoster struct{ reg *multiinstance.Registry }

func (r registryRoster) IsRostered(vulosID string) (ed25519.PublicKey, bool) {
	if r.reg == nil {
		return nil, false
	}
	insts, err := r.reg.List()
	if err != nil {
		return nil, false
	}
	for _, in := range insts {
		pub, ok := decodeInstancePubKey(in.Ed25519PublicKey)
		if !ok {
			continue
		}
		if peering.EncodeVulosID(pub) == vulosID {
			return pub, true
		}
	}
	return nil, false
}

// IsRevoked implements fleetid.RevocationOracle so a peer whose FABRIC key was
// revoked (FABRIC-KEY-01, internal/multiinstance/rotation.go RevokePeer) never
// counts toward a device-key quorum either — the two revocation mechanisms are
// independent stores but a revoked fleet member should not get to vouch for
// anything.
func (r registryRoster) IsRevoked(vulosID string) bool {
	if r.reg == nil {
		return false
	}
	insts, err := r.reg.List()
	if err != nil {
		return false
	}
	for _, in := range insts {
		pub, ok := decodeInstancePubKey(in.Ed25519PublicKey)
		if !ok {
			continue
		}
		if peering.EncodeVulosID(pub) == vulosID {
			return in.Revoked
		}
	}
	return false
}

func decodeInstancePubKey(b64 string) (ed25519.PublicKey, bool) {
	if b64 == "" {
		return nil, false
	}
	pub, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(pub), true
}

// selfFleetVulosID resolves THIS box's own fleet-fabric Vulos ID — the subject a
// peer quorum vouches FOR during break-glass — from the registry's owner-role
// entry (self-registered by multiinstance.AppSync.SetIdentity with
// Role=RoleOwner; see appsync.go). Returns "" if no such entry is found (e.g.
// the fabric identity has not been set up yet), which the handler surfaces as
// a clear error rather than guessing.
func selfFleetVulosID(reg *multiinstance.Registry) string {
	if reg == nil {
		return ""
	}
	insts, err := reg.List()
	if err != nil {
		return ""
	}
	for _, in := range insts {
		if in.Role != multiinstance.RoleOwner {
			continue
		}
		if pub, ok := decodeInstancePubKey(in.Ed25519PublicKey); ok {
			return peering.EncodeVulosID(pub)
		}
	}
	return ""
}

// dkOwnerCheck mirrors the adminCheck passed to devicekey.RegisterHandlers in
// main.go exactly (AUTH-10c): X-User-ID is the auth middleware's validated
// stamp, never a client-supplied value.
func dkOwnerCheck(authStore *auth.Store, r *http.Request) bool {
	if authStore == nil {
		return false
	}
	p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
	return p != nil && p.Role == auth.RoleAdmin
}

// dkRequireOwnerAndStepup fails closed: nil authStore, no session, non-owner
// role, or a missing/expired step-up token are all rejected. Returns "" and
// writes the error response itself when the gate fails; returns the verified
// userID on success.
func dkRequireOwnerAndStepup(w http.ResponseWriter, r *http.Request, authStore *auth.Store) (userID string, ok bool) {
	userID = r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	if !dkOwnerCheck(authStore, r) {
		writeErr(w, http.StatusForbidden, "owner required")
		return "", false
	}
	if err := stepup.Require(r); err != nil {
		writeErr(w, http.StatusForbidden, "step-up required: "+err.Error())
		return "", false
	}
	return userID, true
}

// dkLifecycleDeps bundles what registerDeviceKeyLifecycleRoutes needs.
// reg may be nil (e.g. registered before the shared instance registry is
// constructed) — normal self-rotate/self-revoke and the GET propagation
// endpoint keep working; only the break-glass paths degrade to a clear 503.
type dkLifecycleDeps struct {
	KeyStore        devicekey.KeyStore
	RevocationStore *devicekey.RevocationStore
	AuthStore       *auth.Store
	Registry        *multiinstance.Registry // may be nil; see above
}

// breakGlassQuorumRequest is the shared shape of the break-glass fields on
// both the rotate and revoke request bodies.
type breakGlassQuorumRequest struct {
	RequestID       string              `json:"request_id"`
	QuorumSubjectID string              `json:"quorum_subject_id,omitempty"` // optional; defaults to this box's own fleet identity
	QuorumCerts     []fleetid.VouchCert `json:"quorum_certs"`
	Threshold       int                 `json:"threshold,omitempty"` // optional; fleetid enforces a floor of 2 regardless
}

// resolveSubjectID returns req.QuorumSubjectID if set, else this box's own
// resolved fleet Vulos ID, else an error the handler surfaces as 503/400.
func (req breakGlassQuorumRequest) resolveSubjectID(reg *multiinstance.Registry) (string, error) {
	if req.QuorumSubjectID != "" {
		return req.QuorumSubjectID, nil
	}
	id := selfFleetVulosID(reg)
	if id == "" {
		return "", errNoFleetIdentity
	}
	return id, nil
}

var errNoFleetIdentity = fleetIdentityError{}

type fleetIdentityError struct{}

func (fleetIdentityError) Error() string {
	return "no fleet-fabric identity resolved for this box (and none supplied as quorum_subject_id)"
}

// registerDeviceKeyLifecycleRoutes wires the device-key rotation/revocation
// HTTP API into mux. Safe to call with deps.Registry == nil (see
// dkLifecycleDeps doc); deps.KeyStore and deps.RevocationStore must be
// non-nil or every route replies 503.
func registerDeviceKeyLifecycleRoutes(mux *http.ServeMux, deps dkLifecycleDeps) {
	mux.HandleFunc("POST /api/auth/device/rotate", func(w http.ResponseWriter, r *http.Request) {
		if deps.KeyStore == nil {
			writeErr(w, http.StatusServiceUnavailable, "device key store unavailable")
			return
		}
		if _, ok := dkRequireOwnerAndStepup(w, r, deps.AuthStore); !ok {
			return
		}

		var body struct {
			Reason     string `json:"reason"`
			BreakGlass bool   `json:"break_glass"`
			breakGlassQuorumRequest
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		var (
			cert *devicekey.RotationCert
			err  error
		)
		if !body.BreakGlass {
			cert, err = deps.KeyStore.Rotate(body.Reason)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "rotate failed: "+err.Error())
				return
			}
		} else {
			if body.RequestID == "" || len(body.QuorumCerts) == 0 {
				writeErr(w, http.StatusBadRequest, "break-glass rotation requires request_id and a non-empty quorum_certs bundle from OTHER boxes")
				return
			}
			subjectID, serr := body.resolveSubjectID(deps.Registry)
			if serr != nil {
				writeErr(w, http.StatusServiceUnavailable, serr.Error())
				return
			}
			candidate, cerr := devicekey.GenerateCandidateKey()
			if cerr != nil {
				writeErr(w, http.StatusInternalServerError, "generate candidate key: "+cerr.Error())
				return
			}
			threshold := body.Threshold
			if threshold <= 0 {
				threshold = fleetid.MinThreshold
			}
			cert, err = devicekey.BreakGlassRotate(deps.KeyStore, candidate, body.Reason, subjectID, body.RequestID, body.QuorumCerts, registryRoster{deps.Registry}, threshold, time.Now())
			if err != nil {
				// Deliberately 403, not 500: an insufficient/invalid quorum is an
				// authorization failure, not a server error — and, per the hard
				// rule, no key material was touched.
				writeErr(w, http.StatusForbidden, "break-glass rotation denied: "+err.Error())
				return
			}
		}

		writeJSON(w, map[string]any{
			"method":      cert.Method,
			"old_pub_key": cert.OldPubKey,
			"new_pub_key": cert.NewPubKey,
			"issued_at":   cert.IssuedAt,
			"cert":        cert,
		})
	})

	mux.HandleFunc("POST /api/auth/device/revoke", func(w http.ResponseWriter, r *http.Request) {
		if deps.KeyStore == nil || deps.RevocationStore == nil {
			writeErr(w, http.StatusServiceUnavailable, "device key revocation unavailable")
			return
		}
		if _, ok := dkRequireOwnerAndStepup(w, r, deps.AuthStore); !ok {
			return
		}

		var body struct {
			Reason     string `json:"reason"`
			BreakGlass bool   `json:"break_glass"`
			breakGlassQuorumRequest
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		var (
			cert *devicekey.DeviceRevocationCert
			err  error
		)
		if !body.BreakGlass {
			cert, err = devicekey.SelfRevoke(deps.KeyStore, deps.RevocationStore, body.Reason)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "revoke failed: "+err.Error())
				return
			}
		} else {
			if body.RequestID == "" || len(body.QuorumCerts) == 0 {
				writeErr(w, http.StatusBadRequest, "break-glass revocation requires request_id and a non-empty quorum_certs bundle from OTHER boxes")
				return
			}
			subjectID, serr := body.resolveSubjectID(deps.Registry)
			if serr != nil {
				writeErr(w, http.StatusServiceUnavailable, serr.Error())
				return
			}
			threshold := body.Threshold
			if threshold <= 0 {
				threshold = fleetid.MinThreshold
			}
			cert, err = devicekey.BreakGlassRevoke(deps.KeyStore, deps.RevocationStore, body.Reason, subjectID, body.RequestID, body.QuorumCerts, registryRoster{deps.Registry}, threshold, time.Now())
			if err != nil {
				writeErr(w, http.StatusForbidden, "break-glass revocation denied: "+err.Error())
				return
			}
		}

		writeJSON(w, map[string]any{
			"fingerprint": cert.Fingerprint,
			"method":      cert.Method,
			"revoked_at":  cert.IssuedAt,
			"cert":        cert,
		})
	})

	// GET is intentionally UNGATED (no owner/step-up check), mirroring
	// GET /api/auth/device/identity and GET /api/instances/{ulid}/apps: this is
	// the machine-to-machine propagation pull, not a user-facing read. Every
	// entry is self-verifying (see devicekey/revocation.go), so exposing the
	// list costs nothing a holder of a valid cert couldn't already prove.
	mux.HandleFunc("GET /api/auth/device/revocations", func(w http.ResponseWriter, r *http.Request) {
		if deps.RevocationStore == nil {
			writeJSON(w, devicekey.RevocationBatch{Revocations: []*devicekey.DeviceRevocationCert{}})
			return
		}
		writeJSON(w, devicekey.RevocationBatch{Revocations: deps.RevocationStore.List()})
	})
}
