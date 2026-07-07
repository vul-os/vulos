package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v3/jwt"
)

const (
	mtTestKey    = "APItestkey1234567890"
	mtTestSecret = "supersecretsigningkeythatislongenough"
)

// mtGrants mirrors the minted claim shape enough to assert on the tenant binding.
type mtGrants struct {
	Name  string `json:"name"`
	Video struct {
		Room         string `json:"room"`
		RoomJoin     bool   `json:"roomJoin"`
		CanPublish   *bool  `json:"canPublish"`
		CanSubscribe *bool  `json:"canSubscribe"`
	} `json:"video"`
}

func mtDecode(t *testing.T, token string) (jwt.Claims, mtGrants) {
	t.Helper()
	parsed, err := jwt.ParseSigned(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	var std jwt.Claims
	var g mtGrants
	if err := parsed.Claims([]byte(mtTestSecret), &std, &g); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	return std, g
}

func TestMeetToken_ConfiguredMintsAndSessionGates(t *testing.T) {
	t.Setenv("LIVEKIT_API_KEY", mtTestKey)
	t.Setenv("LIVEKIT_API_SECRET", mtTestSecret)

	mux := http.NewServeMux()
	if ok := registerMeetTokenRoutes(mux); !ok {
		t.Fatalf("expected minter to be configured")
	}

	// Unauthenticated (no X-User-ID) → 401.
	t.Run("unauth 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(`{"room":"standup"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d want 401 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	// Authenticated → 200, token binds tenant to the SESSION user, not the body.
	t.Run("session-derived tenant", func(t *testing.T) {
		// The body attempts to spoof a different tenant via `name`; it must be ignored.
		body := `{"room":"standup","name":"attacker-tenant"}`
		req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(body))
		req.Header.Set("X-User-ID", "real-user")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Token     string `json:"token"`
			RoomID    string `json:"room_id"`
			Waiting   bool   `json:"waiting"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Token == "" {
			t.Fatalf("empty token")
		}
		if resp.Waiting {
			t.Fatalf("self-host mint must not be a waiting token")
		}
		if resp.RoomID != "real-user:standup" {
			t.Fatalf("room_id=%q want real-user:standup", resp.RoomID)
		}
		std, g := mtDecode(t, resp.Token)
		if std.Subject != "real-user" {
			t.Fatalf("identity=%q want real-user", std.Subject)
		}
		if std.Issuer != mtTestKey {
			t.Fatalf("iss=%q want %q", std.Issuer, mtTestKey)
		}
		// The tenant audience (name) MUST be the session user, NOT the body's name.
		if g.Name != "real-user" {
			t.Fatalf("tenant audience=%q — body spoof leaked (want real-user)", g.Name)
		}
		if g.Video.Room != "real-user:standup" {
			t.Fatalf("video.room=%q want real-user:standup", g.Video.Room)
		}
		if !g.Video.RoomJoin {
			t.Fatalf("roomJoin must be true")
		}
		// Tenant binding: name == room prefix (the invariant the SFU enforces).
		if !strings.HasPrefix(g.Video.Room, g.Name+":") {
			t.Fatalf("tenant binding broken: room=%q name=%q", g.Video.Room, g.Name)
		}
	})

	// Missing room → 400.
	t.Run("missing room 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(`{}`))
		req.Header.Set("X-User-ID", "u")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code=%d want 400", rec.Code)
		}
	})
}

func TestMeetToken_SecretsUnset503(t *testing.T) {
	t.Setenv("LIVEKIT_API_KEY", "")
	t.Setenv("LIVEKIT_API_SECRET", "")

	mux := http.NewServeMux()
	if ok := registerMeetTokenRoutes(mux); ok {
		t.Fatalf("expected minter to be unconfigured")
	}
	// The route still exists and answers 503 (not a 404, not a crash), even for an
	// authenticated caller.
	req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(`{"room":"r"}`))
	req.Header.Set("X-User-ID", "u")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503 (body=%s)", rec.Code, rec.Body.String())
	}
}
