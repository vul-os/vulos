// board.go — BOARD-01: hosted Vulos Board room-token mint.
//
//	GET /api/board/token?room=<id> — session-gated; returns an HMAC room token.
//
// Vulos Board is a native in-shell surface in Vulos Workspace (collaborative
// whiteboard). The board server authenticates a client by an HMAC token passed
// as ?token= on its connection:
//
//	token = base64url(payloadJSON) + "." +
//	        base64url(HMAC_SHA256(BOARD_AUTH_SECRET, base64url(payloadJSON)))
//
// with payload {"exp":<unix>,"room":"<id>"} and RawURLEncoding (no padding) for
// every base64url segment. This mirrors the verifier the OSS board server runs.
//
// The token is signed with BOARD_AUTH_SECRET (sourced via the SecretsProvider).
// When the secret is unset the endpoint returns an EMPTY token (200) so the
// shell degrades gracefully instead of erroring — board simply stays
// unauthenticated until the secret is wired.
package cproutes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// boardTokenTTL is the short lifetime stamped into the room token's exp claim.
// Kept short because the token is minted on demand each time the shell opens a
// board room; the board server only needs it long enough to establish the
// session.
const boardTokenTTL = 5 * time.Minute

// boardHandlers holds dependencies for the board room-token mint.
type boardHandlers struct {
	auth   *auth.Store
	secret func() string    // BOARD_AUTH_SECRET resolver ("" when unset → empty token)
	now    func() time.Time // injectable clock for tests
}

// RegisterBoard wires GET /api/board/token into mux. Session-gated: only a
// logged-in Workspace user can mint a room token. secret resolves the signing
// key at request time (so a rotation / late-set env var is picked up live).
func RegisterBoard(mux *http.ServeMux, authStore *auth.Store, secret func() string) {
	h := &boardHandlers{
		auth:   authStore,
		secret: secret,
		now:    func() time.Time { return time.Now().UTC() },
	}
	mux.HandleFunc("GET /api/board/token", h.mintRoomToken)
}

// mintRoomToken: session-gated mint of a board room token for ?room=<id>.
func (h *boardHandlers) mintRoomToken(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}

	room := r.URL.Query().Get("room")
	if room == "" {
		httpx.Err(w, http.StatusBadRequest, "missing room")
		return
	}
	if !validBoardRoom(room) {
		httpx.Err(w, http.StatusBadRequest, "invalid room")
		return
	}

	secret := ""
	if h.secret != nil {
		secret = h.secret()
	}
	// Secret unset → empty token (200). The shell degrades gracefully; board
	// stays unauthenticated until BOARD_AUTH_SECRET is configured.
	if secret == "" {
		httpx.JSON(w, map[string]any{"token": "", "room": room})
		return
	}

	exp := h.now().Add(boardTokenTTL)
	token, err := signBoardToken(secret, room, exp)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not mint board token")
		return
	}
	httpx.JSON(w, map[string]any{
		"token":      token,
		"room":       room,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"expires_in": int(boardTokenTTL.Seconds()),
	})
}

// maxBoardRoomLen bounds the room id length. It comfortably fits the largest
// room shape the shells mint — an "<email>:default" default room, where the
// email can be RFC-max (254) plus the ":default" suffix — while still rejecting
// an oversized value that could bloat the signed token, the echoed JSON
// response, or the board server's URL path / storage key.
const maxBoardRoomLen = 320

// validBoardRoom reports whether room is a safe board collaboration key.
//
// The board room is an OPAQUE collaboration key, NOT a structured identifier.
// The OSS board sync server (board-ui/server-go) binds a minted token to a room
// by FULL-STRING equality — its verifier requires claims.room to equal the room
// taken verbatim from the WebSocket URL path (/ws/<room>); it never splits the
// room on ":" into a "<tenant>:<room>" pair. So — unlike vulos-meet's LiveKit
// rooms, where meetalloc must reject ":" to protect the "<tenant>:<room>" prefix
// invariant (see internal/meetalloc/tenant.go) — a ":" here has no prefix
// meaning and enables no prefix-confusion. That lets us accept the
// "<email>:default" per-account default room Vulos Workspace mints
// (BoardSurface.jsx), whose id is the account email and therefore legitimately
// contains "@", ".", "-", "_", "+" and ":".
//
// The room flows into exactly three sinks, each made injection-safe by the
// bounded allowlist below:
//   - the signed JSON token payload (a JSON string value),
//   - the JSON response body (echoed back to the caller),
//   - the board server's WebSocket URL path segment /ws/<room> and, from there,
//     its storage key board/<safeRoom>.bin — where the server independently
//     strips it to [A-Za-z0-9._-] and treats "."/".." as un-storable.
//
// Allowing ONLY [A-Za-z0-9] plus [@ . _ - : +] rejects whitespace, control
// bytes, NUL, CR, LF, and the path separators "/" and "\" (and every non-ASCII
// byte), so nothing can smuggle a query separator, a response/log-splitting
// newline, or a path traversal into any sink. ".." is rejected outright as
// defence-in-depth (no legitimate room — simple id, channel id, or RFC-valid
// email — contains it).
func validBoardRoom(room string) bool {
	if room == "" || len(room) > maxBoardRoomLen {
		return false
	}
	if strings.Contains(room, "..") {
		return false
	}
	for i := 0; i < len(room); i++ {
		c := room[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '@' || c == '.' || c == '_' ||
			c == '-' || c == ':' || c == '+'
		if !ok {
			return false
		}
	}
	return true
}

// boardTokenPayload is the JSON claim set the board server verifies.
type boardTokenPayload struct {
	Exp  int64  `json:"exp"`
	Room string `json:"room"`
}

// signBoardToken builds base64url(payload) "." base64url(HMAC_SHA256(secret,
// base64url(payload))) using RawURLEncoding for every segment — the exact wire
// format the OSS board server's verifier expects.
func signBoardToken(secret, room string, exp time.Time) (string, error) {
	payload, err := json.Marshal(boardTokenPayload{Exp: exp.Unix(), Room: room})
	if err != nil {
		return "", err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encPayload))
	encSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encPayload + "." + encSig, nil
}
