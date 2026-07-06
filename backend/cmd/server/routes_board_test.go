package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/env"
)

// TestSignBoardToken_MatchesContract reproduces the board sync server's
// verification: split on ".", recompute HMAC_SHA256(secret, payloadSeg) over the
// base64url payload segment, constant-time compare, then decode the payload and
// check the claims. This proves our minter and the server agree on the wire shape.
func TestSignBoardToken_MatchesContract(t *testing.T) {
	const secret = "test-board-secret"
	want := boardTokenPayload{Exp: 1_900_000_000, Room: "room-42"}

	tok, err := signBoardToken(secret, want)
	if err != nil {
		t.Fatalf("signBoardToken: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		t.Fatalf("token must have exactly 2 dot-separated segments, got %d: %q", len(parts), tok)
	}
	payloadSeg, sigSeg := parts[0], parts[1]

	// Both segments must be RawURLEncoding (no padding).
	if strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token contains non-RawURLEncoding chars: %q", tok)
	}

	// Server-side signature recomputation.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadSeg))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(wantSig), []byte(sigSeg)) {
		t.Fatalf("signature mismatch:\n got %q\nwant %q", sigSeg, wantSig)
	}

	// Payload round-trips to the claims we signed.
	raw, err := base64.RawURLEncoding.DecodeString(payloadSeg)
	if err != nil {
		t.Fatalf("payload not RawURLEncoding: %v", err)
	}
	var got boardTokenPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if got.Room != want.Room || got.Exp != want.Exp {
		t.Fatalf("claims mismatch: got %+v want %+v", got, want)
	}
}

func TestValidBoardRoom(t *testing.T) {
	ok := []string{"room-42", "a", "A_b.C-9", strings.Repeat("x", 128)}
	bad := []string{"", "has space", "slash/here", "semi;colon", strings.Repeat("x", 129)}
	for _, r := range ok {
		if !validBoardRoom(r) {
			t.Errorf("validBoardRoom(%q) = false, want true", r)
		}
	}
	for _, r := range bad {
		if validBoardRoom(r) {
			t.Errorf("validBoardRoom(%q) = true, want false", r)
		}
	}
}

func TestBoardTokenEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	registerBoardRoutes(mux, env.EnvLocal)

	// Unauthenticated → 401.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board/token?room=r1", nil))
	if rec.Code != 401 {
		t.Errorf("no X-User-ID: got %d want 401", rec.Code)
	}

	// Invalid room → 400.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/board/token?room=bad%20room", nil)
	req.Header.Set("X-User-ID", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("invalid room: got %d want 400", rec.Code)
	}

	// No secret configured → 200 with empty token (open/dev mode).
	t.Setenv("BOARD_AUTH_SECRET", "")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/board/token?room=r1", nil)
	req.Header.Set("X-User-ID", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dev mode: got %d want 200", rec.Code)
	}
	var devResp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &devResp)
	if devResp["token"] != "" {
		t.Errorf("dev mode token = %v, want empty", devResp["token"])
	}

	// Secret set → 200 with a non-empty token + exp.
	t.Setenv("BOARD_AUTH_SECRET", "s3cr3t")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/board/token?room=r1", nil)
	req.Header.Set("X-User-ID", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("minting: got %d want 200", rec.Code)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if tok, _ := resp["token"].(string); tok == "" || !strings.Contains(tok, ".") {
		t.Errorf("expected signed token, got %v", resp["token"])
	}
	if _, ok := resp["exp"]; !ok {
		t.Errorf("expected exp in response, got %v", resp)
	}
}

// TestBoardTokenEndpoint_ProdFailsClosed is the wave-34 fail-open regression:
// in VULOS_ENV=prod, an unset BOARD_AUTH_SECRET must NOT hand out an "open-mode"
// empty token (which would sanction anonymous collab connections). The route
// must refuse (503) instead.
func TestBoardTokenEndpoint_ProdFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	registerBoardRoutes(mux, env.EnvProd)

	// Prod + no secret → 503, and definitely NO token in the body.
	t.Setenv("BOARD_AUTH_SECRET", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/board/token?room=r1", nil)
	req.Header.Set("X-User-ID", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("prod + no secret: got %d want 503 (fail closed)", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if _, hasToken := body["token"]; hasToken {
		t.Errorf("prod + no secret must NOT return a token, got %v", body)
	}

	// Prod WITH a secret still mints a real token.
	t.Setenv("BOARD_AUTH_SECRET", "prod-secret")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/board/token?room=r1", nil)
	req.Header.Set("X-User-ID", "u1")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("prod + secret: got %d want 200", rec.Code)
	}
	var ok map[string]any
	json.Unmarshal(rec.Body.Bytes(), &ok)
	if tok, _ := ok["token"].(string); tok == "" || !strings.Contains(tok, ".") {
		t.Errorf("prod + secret: expected signed token, got %v", ok["token"])
	}
}
