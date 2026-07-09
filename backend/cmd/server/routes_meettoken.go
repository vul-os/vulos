// routes_meettoken.go — SELF-HOST Meet join-token minter (self-host P1).
//
// In the cloud topology the Meet browser's POST /api/meet/token is served by
// the control plane (vulos-cloud MEET-CP-01). On a sovereign box there is no CP
// and the OS backend serves /api, so this file registers a LOCAL minter that
// produces the SAME VULOS-MEET/1 token the vulos-meet SFU validates.
//
// Authz posture (self-host):
//   - The OS auth middleware (services/auth Handler.Middleware) validates the
//     session and injects X-User-ID. This handler requires that header — an
//     unauthenticated request never reaches a mint (401).
//   - Tenant AND identity are derived from the session user id, NEVER from the
//     request body. The body's `room` selects the room; the body's `name` (a
//     display name) is ignored for the tenant binding. A caller therefore cannot
//     spoof another tenant by crafting the body.
//   - 1:1 user↔tenant, matching the cloud CP convention (the session user id is
//     both the tenant audience and the LiveKit identity). Multi-tenant and
//     waiting-room are cloud concerns; a self-host box is single-owner.
//
// If the LiveKit signing secrets are unset the minter is nil and the route
// returns a clear 503 ("Meet not configured") — never a crash.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"vulos/backend/services/meettoken"
)

// LiveKit signing-credential env vars. These are the CANONICAL names — the SAME
// pair vulos-meet's own binary reads (vulos-meet/internal/wrap/config.go
// LiveKitAPIKeyEnv / LiveKitAPISecretEnv) and the SAME pair the cloud CP mints
// with. On a self-host box the operator sets both on the OS backend AND on the
// co-located vulos-meet SFU so mint and validate share one secret.
const (
	livekitAPIKeyEnv    = "LIVEKIT_API_KEY"
	livekitAPISecretEnv = "LIVEKIT_API_SECRET"
	// meetMintTTLEnv optionally overrides the join-token lifetime (a Go duration
	// like "15m"). Unset/invalid ⇒ meettoken.DefaultTTL. Clamped to MaxTTL.
	meetMintTTLEnv = "MEET_MINT_TTL"
)

// registerMeetTokenRoutes wires POST /api/meet/token onto mux for self-host.
//
// It reads the LiveKit signing pair from the environment and builds a minter.
// When the secrets are unset it registers a 503-only handler (Meet not
// configured) so the endpoint answers coherently instead of 404-ing (which the
// meet client interprets as "no CP" and silently degrades). Returns whether a
// working minter was configured (for the startup log).
func registerMeetTokenRoutes(mux *http.ServeMux) bool {
	apiKey := os.Getenv(livekitAPIKeyEnv)
	apiSecret := os.Getenv(livekitAPISecretEnv)

	var ttl time.Duration
	if raw := os.Getenv(meetMintTTLEnv); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			ttl = d
		}
	}

	minter, err := meettoken.NewMinter(apiKey, apiSecret, ttl)
	if err != nil {
		// Secrets unset → register a 503-only handler. Fail-safe: a self-host box
		// without LiveKit configured tells the client Meet is unavailable rather
		// than crashing or silently minting nothing.
		mux.HandleFunc("POST /api/meet/token", func(w http.ResponseWriter, r *http.Request) {
			writeMeetTokenErr(w, http.StatusServiceUnavailable,
				"Meet is not configured on this instance (set LIVEKIT_API_KEY and LIVEKIT_API_SECRET)")
		})
		return false
	}

	mux.HandleFunc("POST /api/meet/token", func(w http.ResponseWriter, r *http.Request) {
		// Session gate: the OS auth middleware injects X-User-ID after validating
		// the session. No header ⇒ unauthenticated ⇒ 401. This is the same authz
		// seam every other self-host data route uses.
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeMeetTokenErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req struct {
			Room string `json:"room"`
			// name is the browser-supplied DISPLAY name — deliberately NOT used for
			// the tenant binding. Tenant is server-derived from the session below.
			Name string `json:"name"`
			// publish/subscribe are optional media-grant overrides. Absent ⇒ full
			// media (normal participant).
			Publish   *bool `json:"publish"`
			Subscribe *bool `json:"subscribe"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Room == "" {
			writeMeetTokenErr(w, http.StatusBadRequest, "room required")
			return
		}

		canPub := true
		if req.Publish != nil {
			canPub = *req.Publish
		}
		canSub := true
		if req.Subscribe != nil {
			canSub = *req.Subscribe
		}

		// Tenant AND identity are the session user id — server-derived, never from
		// the body. The room comes from the request; the tenant binding does not.
		res, err := minter.Mint(meettoken.MintParams{
			TenantID:     userID,
			UserID:       userID,
			RoomName:     req.Room,
			CanPublish:   canPub,
			CanSubscribe: canSub,
		})
		if err != nil {
			switch {
			case errors.Is(err, meettoken.ErrNotConfigured):
				writeMeetTokenErr(w, http.StatusServiceUnavailable, "Meet is not configured on this instance")
			case errors.Is(err, meettoken.ErrEmptyTenant),
				errors.Is(err, meettoken.ErrEmptyRoom),
				errors.Is(err, meettoken.ErrInvalidTenant),
				errors.Is(err, meettoken.ErrInvalidRoom):
				writeMeetTokenErr(w, http.StatusBadRequest, err.Error())
			default:
				log.Printf("[meettoken] mint error: %v", err)
				writeMeetTokenErr(w, http.StatusInternalServerError, "internal")
			}
			return
		}

		// SFU Phase 2 allocation: if a self-host SFU host is registered with the
		// relay, resolve its VERIFIED serverUrl (direct-first) and hand it back as
		// meet_url so a BIG call (escalated past the mesh cap) connects to media on
		// the operator's own infra. Inert by default: with no relay configured or no
		// host registered this stays "" and the client keeps its Phase-1 behavior
		// (the co-located SFU it is already serving). Never fatal — a resolve error
		// degrades to "".
		meetURL := resolveSFUServerURL(r.Context())

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":   res.Token,
			"room_id": res.RoomID,
			// meet_url is the resolved self-host SFU serverUrl for a big call, or ""
			// when none is registered (the client then uses its co-located SFU).
			"meet_url":   meetURL,
			"expires_at": res.ExpiresAt.UTC().Format(time.RFC3339),
			// waiting/waiting_room are cloud-only (waiting-room lobby). A self-host
			// box mints a normal full-media join token, so both are false — the meet
			// client's meetToken.js treats absent/false `waiting` as a normal join.
			"waiting":      false,
			"waiting_room": false,
		})
	})
	return true
}

// sfuResolveTimeout bounds the allocation lookup so a slow/unreachable relay
// never stalls a token mint — a timeout degrades to "" (client uses its own SFU).
const sfuResolveTimeout = 3 * time.Second

// meetHostRelayBaseURL resolves the relay base URL the SFU-host registry lives on.
//
// It reads the dedicated MEET_HOST_RELAY_URL override first, and DEFAULTS to the
// canonical relay URL (VULOS_RELAY_BASE_URL) when that override is unset. This is a
// deliberate default so an operator who configures the reachability relay via the
// canonical VULOS_RELAY_BASE_URL does NOT silently disable SFU-host resolve by
// forgetting to also set the meet-specific override. Set MEET_HOST_RELAY_URL only
// to point SFU-host resolution at a DIFFERENT relay than the rest of reachability.
func meetHostRelayBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("MEET_HOST_RELAY_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("VULOS_RELAY_BASE_URL")), "/")
}

// resolveSFUServerURL asks the relay's SFU-host registry
// (GET {relay}/api/meet/host/resolve, relay = meetHostRelayBaseURL()) for a reachable, VERIFIED
// self-host SFU endpoint to escalate a big call to (SFU Phase 2 allocation,
// direct-first). It returns the endpoint, or "" when:
//   - no relay is configured (VULOS_RELAY_BASE_URL unset) — inert default,
//   - the registry is empty / registration is disabled (available=false),
//   - the relay is unreachable or slow (degrade, never block the mint).
//
// The relay only ever returns an endpoint it independently verified (reachable +
// ownership-proven via directprobe), so a box cannot smuggle an endpoint it does
// not control into a client's join.
func resolveSFUServerURL(ctx context.Context) string {
	base := meetHostRelayBaseURL()
	if base == "" {
		return ""
	}
	// Scope the lookup to THIS box's own tunnel name. The relay is shared across
	// many accounts, so an unscoped resolve would hand back whatever host is first
	// in the registry — possibly ANOTHER tenant's SFU endpoint, routing this box's
	// meeting media to a stranger's server. With no name we cannot scope safely, so
	// we degrade to "" (client keeps its co-located SFU) rather than risk a
	// cross-tenant endpoint.
	name := strings.TrimSpace(os.Getenv("VULOS_RELAY_NAME"))
	if name == "" {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, sfuResolveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, base+"/api/meet/host/resolve?name="+url.QueryEscape(name), nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Available bool   `json:"available"`
		ServerURL string `json:"server_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return ""
	}
	if !out.Available {
		return ""
	}
	return out.ServerURL
}

// writeMeetTokenErr writes a JSON {error} body with no token contents (the meet
// client reads {error} on non-2xx).
func writeMeetTokenErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
