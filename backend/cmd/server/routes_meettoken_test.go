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

	// REGRESSION (red-team): a caller cannot smuggle a FOREIGN tenant prefix into
	// the room grant. `<tenant>:<room>` is built server-side from the session user;
	// a room value that itself contains the ':' separator (e.g. "victim:secret")
	// would, if naively concatenated, yield "attacker:victim:secret" — but the SFU
	// validator splits on the FIRST separator, so the tenant prefix always stays
	// the session user. The minter rejects a room containing the separator outright
	// (400) so no ambiguity can ever reach the wire.
	t.Run("foreign-tenant-via-room rejected", func(t *testing.T) {
		body := `{"room":"victim-tenant:secret-room"}`
		req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(body))
		req.Header.Set("X-User-ID", "attacker")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("room-with-separator: code=%d want 400 (must not mint a foreign-prefixed room)", rec.Code)
		}
	})

	// REGRESSION (red-team): an attacker-supplied X-User-ID header is meaningless
	// at THIS layer only because the auth middleware strips it upstream; here we
	// prove the handler binds strictly to whatever X-User-ID the (trusted) middleware
	// injected. Two different injected identities produce two different, correctly
	// bound tokens — never a shared/forged tenant.
	t.Run("identity strictly follows injected X-User-ID", func(t *testing.T) {
		for _, uid := range []string{"alice", "bob"} {
			req := httptest.NewRequest(http.MethodPost, "/api/meet/token", strings.NewReader(`{"room":"standup"}`))
			req.Header.Set("X-User-ID", uid)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("uid=%s: code=%d want 200", uid, rec.Code)
			}
			var resp struct {
				Token  string `json:"token"`
				RoomID string `json:"room_id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			std, g := mtDecode(t, resp.Token)
			if std.Subject != uid || g.Name != uid || resp.RoomID != uid+":standup" {
				t.Fatalf("uid=%s: identity/tenant leaked: sub=%q name=%q room=%q", uid, std.Subject, g.Name, resp.RoomID)
			}
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
