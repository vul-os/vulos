// board_test.go — tests for GET /api/board/token (BOARD-01).
package cproutes

// COORDINATOR: shared test helpers openTestAuthStore, signupAndSession live in a
// package-main test file — they must be available in package cproutes to compile.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// boardTestMux wires the board route onto a fresh mux with an in-memory auth
// store, reusing the shared test helpers (openTestAuthStore/signupAndSession).
func boardTestMux(t *testing.T, secret string) (*http.ServeMux, *http.Cookie) {
	t.Helper()
	as := openTestAuthStore(t)
	mux := http.NewServeMux()
	RegisterBoard(mux, as, func() string { return secret })
	_, cookie := signupAndSession(t, as, "board@example.test", "correct horse battery staple")
	return mux, cookie
}

func getBoard(t *testing.T, mux *http.ServeMux, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestBoardTokenRequiresSession verifies the mint is session-gated.
func TestBoardTokenRequiresSession(t *testing.T) {
	mux, _ := boardTestMux(t, "s3cr3t")
	rr := getBoard(t, mux, "/api/board/token?room=r1", nil)
	if rr.Code == http.StatusOK {
		t.Fatalf("unauthenticated mint returned 200; want a non-OK status")
	}
}

// TestBoardTokenMissingRoom verifies a missing room yields 400.
func TestBoardTokenMissingRoom(t *testing.T) {
	mux, cookie := boardTestMux(t, "s3cr3t")
	rr := getBoard(t, mux, "/api/board/token", cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing room = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestBoardTokenEmptySecret verifies an unset BOARD_AUTH_SECRET yields an empty
// token (200), so the shell degrades gracefully.
func TestBoardTokenEmptySecret(t *testing.T) {
	mux, cookie := boardTestMux(t, "")
	rr := getBoard(t, mux, "/api/board/token?room=r1", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty-secret mint = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["token"] != "" {
		t.Fatalf("token = %v, want empty", got["token"])
	}
}

// TestBoardTokenSignedFormat verifies the minted token is exactly
// base64url(payload).base64url(HMAC_SHA256(secret, base64url(payload))) with
// RawURLEncoding, and that the payload carries the room and a future exp.
func TestBoardTokenSignedFormat(t *testing.T) {
	const secret = "board-signing-secret"
	mux, cookie := boardTestMux(t, secret)
	rr := getBoard(t, mux, "/api/board/token?room=room-42", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("mint = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Token string `json:"token"`
		Room  string `json:"room"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Room != "room-42" {
		t.Fatalf("room = %q, want room-42", got.Room)
	}

	parts := strings.Split(got.Token, ".")
	if len(parts) != 2 {
		t.Fatalf("token has %d segments, want 2: %q", len(parts), got.Token)
	}
	// Recompute the HMAC over the encoded payload segment and compare.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(wantSig)) {
		t.Fatalf("signature mismatch: got %q want %q", parts[1], wantSig)
	}
	// Decode the payload and check the claims.
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("payload not RawURLEncoding: %v", err)
	}
	var p boardTokenPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if p.Room != "room-42" {
		t.Fatalf("payload room = %q, want room-42", p.Room)
	}
	if p.Exp <= time.Now().Unix() {
		t.Fatalf("payload exp = %d is not in the future", p.Exp)
	}
}

// TestBoardTokenEmailDefaultRoom is the parity fix (BOARD-01): Vulos Workspace's
// per-account default board room is "<email>:default" (BoardSurface.jsx), so the
// mint MUST accept the email-shaped room — containing "@", ".", "-", "+" and the
// ":" separator — and sign it VERBATIM (no prefix stripping / normalization), so
// the token's room claim exactly matches the room the board server sees on the
// WebSocket path. Before the fix an over-strict validator rejected it → 400 →
// the shell connected token-less → the SECURE prod board server refused the
// upgrade → the default board silently failed to sync.
func TestBoardTokenEmailDefaultRoom(t *testing.T) {
	const secret = "board-signing-secret"
	mux, cookie := boardTestMux(t, secret)
	const room = "alice+ci@example.co.uk:default"
	rr := getBoard(t, mux, "/api/board/token?room="+url.QueryEscape(room), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("email-default mint = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Token string `json:"token"`
		Room  string `json:"room"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Echoed room must be the input verbatim — no prefix-confusion / stripping.
	if got.Room != room {
		t.Fatalf("echoed room = %q, want %q (verbatim)", got.Room, room)
	}
	// Signature must verify AND the signed payload's room must equal the full
	// input — the board server matches this claim by full-string equality.
	parts := strings.Split(got.Token, ".")
	if len(parts) != 2 {
		t.Fatalf("token has %d segments, want 2: %q", len(parts), got.Token)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(wantSig)) {
		t.Fatalf("signature mismatch for email-default room")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("payload not RawURLEncoding: %v", err)
	}
	var p boardTokenPayload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if p.Room != room {
		t.Fatalf("signed payload room = %q, want %q (no prefix stripping)", p.Room, room)
	}
}

// TestBoardTokenRejectsUnsafeRoom verifies the mint still refuses rooms that
// could smuggle an injection, a path traversal, or an oversized value into the
// signed token, the JSON response, or the board server's URL path / storage key.
// Legitimate simple ids and channel ids remain accepted (covered above).
func TestBoardTokenRejectsUnsafeRoom(t *testing.T) {
	mux, cookie := boardTestMux(t, "s3cr3t")
	bad := map[string]string{
		"path-slash":      "a/b",
		"path-backslash":  `a\b`,
		"path-traversal":  "../../etc/passwd",
		"dotdot":          "room..default",
		"space":           "a b",
		"newline":         "a\nb",
		"carriage-return": "a\rb",
		"tab":             "a\tb",
		"nul":             "a\x00b",
		"control":         "a\x07b",
		"query-amp":       "a&x=1",
		"query-hash":      "a#frag",
		"percent":         "a%2fb",
		"non-ascii":       "café",
		"angle":           "a<b>",
		"oversize":        strings.Repeat("a", 321),
	}
	for name, room := range bad {
		t.Run(name, func(t *testing.T) {
			rr := getBoard(t, mux, "/api/board/token?room="+url.QueryEscape(room), cookie)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("room %q (%s) = %d, want 400; body=%s", room, name, rr.Code, rr.Body.String())
			}
			// A rejected mint must not leak a signed token.
			if strings.Contains(rr.Body.String(), "\"token\"") {
				t.Fatalf("rejected room %q returned a token: %s", room, rr.Body.String())
			}
		})
	}
}

// TestBoardTokenAcceptsMaxLenRoom verifies the length cap admits an
// RFC-max-email default room (254-char email + ":default") but rejects one byte
// beyond the bound.
func TestBoardTokenAcceptsMaxLenRoom(t *testing.T) {
	mux, cookie := boardTestMux(t, "s3cr3t")
	atCap := strings.Repeat("a", maxBoardRoomLen)
	if rr := getBoard(t, mux, "/api/board/token?room="+url.QueryEscape(atCap), cookie); rr.Code != http.StatusOK {
		t.Fatalf("at-cap room (%d chars) = %d, want 200", len(atCap), rr.Code)
	}
	overCap := strings.Repeat("a", maxBoardRoomLen+1)
	if rr := getBoard(t, mux, "/api/board/token?room="+url.QueryEscape(overCap), cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("over-cap room (%d chars) = %d, want 400", len(overCap), rr.Code)
	}
}
