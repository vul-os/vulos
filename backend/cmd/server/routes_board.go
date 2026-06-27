package main

// routes_board.go — GET /api/board/token?room=<boardId>
//
// Mints a short-lived auth token for the Vulos Board sync server. Board is a
// gateway-proxied web app (served at /app/board/); the actual realtime sync
// server lives in a separate repo (board-ui) and authorizes its websocket
// connections with an HMAC `?token=` query parameter. The OS is the self-host
// control plane (seam A) and is the intended token minter, so the board client
// fetches a token from here before opening its websocket.
//
// Token contract (MUST match the board sync server exactly):
//
//	token   = base64url(payloadJSON) + "." + base64url(HMAC_SHA256(secret, base64url(payloadJSON)))
//	          using RawURLEncoding (no padding) for BOTH segments.
//	payload = {"exp":<unix seconds>,"room":"<board id>"}
//	secret  = env BOARD_AUTH_SECRET (the same value the board sync server holds).
//
// The board sync server recomputes the HMAC over the received payload segment
// and compares it (constant-time) against the received signature segment, then
// checks `exp` and that `room` matches the room the client is joining. This
// endpoint only SIGNS; verification is the board server's job.
//
// Env gating: if BOARD_AUTH_SECRET is unset, the board server runs in open/dev
// mode (no token required), so we return {"token":""} (no error) to keep local
// builds working. The secret is never logged and never returned to the client.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// boardTokenTTL is the lifetime of a minted board token. Short by design — the
// client fetches a fresh token each time it (re)opens a board websocket.
const boardTokenTTL = time.Hour

// boardTokenPayload is the JSON signed into a board auth token. Field order and
// names are part of the wire contract with the board sync server.
type boardTokenPayload struct {
	Exp  int64  `json:"exp"`
	Room string `json:"room"`
}

// signBoardToken builds the `payload.signature` token per the board contract.
// Both segments use base64url with no padding (RawURLEncoding). The secret is
// never included in the output.
func signBoardToken(secret string, payload boardTokenPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	payloadSeg := base64.RawURLEncoding.EncodeToString(raw)

	mac := hmac.New(sha256.New, []byte(secret))
	// HMAC is computed over the base64url-encoded payload segment (not the raw
	// JSON) — this matches the board server's verification input exactly.
	mac.Write([]byte(payloadSeg))
	sigSeg := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return payloadSeg + "." + sigSeg, nil
}

// validBoardRoom sanitizes a requested room/board id. Board ids are opaque
// identifiers; we allow a conservative URL/websocket-safe charset and cap the
// length so a hostile value can't be smuggled into the signed payload.
func validBoardRoom(room string) bool {
	if room == "" || len(room) > 128 {
		return false
	}
	for _, c := range room {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// registerBoardRoutes wires GET /api/board/token onto mux. It uses the same
// session auth as other /api/* control-plane endpoints: the global auth
// middleware injects X-User-ID, and an empty value means "not signed in".
func registerBoardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/board/token", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}

		room := r.URL.Query().Get("room")
		if !validBoardRoom(room) {
			writeErr(w, 400, "invalid room")
			return
		}

		// Open/dev mode: no secret configured → board server requires no token.
		// Hand back an empty token (not an error) so the client still connects.
		secret := os.Getenv("BOARD_AUTH_SECRET")
		if secret == "" {
			writeJSON(w, map[string]any{"token": ""})
			return
		}

		exp := time.Now().Add(boardTokenTTL).Unix()
		token, err := signBoardToken(secret, boardTokenPayload{Exp: exp, Room: room})
		if err != nil {
			// Never surface the secret or signing internals to the client.
			writeErr(w, 500, "failed to mint token")
			return
		}

		writeJSON(w, map[string]any{"token": token, "exp": exp})
	})
}
