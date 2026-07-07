package meettoken

import (
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3/jwt"
)

const (
	testKey    = "APItestkey1234567890"
	testSecret = "supersecretsigningkeythatislongenough"
)

// verifyLikeWrapValidator reproduces vulos-meet/internal/wrap.Validator.Validate
// step-for-step against the minted token, using the SAME go-jose parse path the
// real validator uses. If this passes, the real SFU validator passes (they read
// identical bytes). See vulos-meet/spec/TOKEN.md §4 for the normative rules.
//
// It returns the parsed (tenant, room, identity, canPublish, canSubscribe) or a
// descriptive error naming the exact rule that failed.
func verifyLikeWrapValidator(t *testing.T, raw, apiKey, apiSecret, sep string, maxTTL time.Duration) (tenant, room, identity string, canPub, canSub bool) {
	t.Helper()

	// (1) JWT parse.
	parsed, err := jwt.ParseSigned(raw)
	if err != nil {
		t.Fatalf("rule 1 (parse): %v", err)
	}

	// (2) API-key match: read iss without verification, must equal apiKey.
	var unverified jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		t.Fatalf("rule 2 (unsafe claims): %v", err)
	}
	if unverified.Issuer != apiKey {
		t.Fatalf("rule 2 (api key): iss=%q want %q", unverified.Issuer, apiKey)
	}

	// (3) Signature + time: verify HS256 against apiSecret and validate iss/exp/nbf.
	var std jwt.Claims
	var grants claimGrants
	if err := parsed.Claims([]byte(apiSecret), &std, &grants); err != nil {
		t.Fatalf("rule 3 (signature): %v", err)
	}
	if err := std.Validate(jwt.Expected{Issuer: apiKey, Time: time.Now()}); err != nil {
		t.Fatalf("rule 3 (iss/exp/nbf): %v", err)
	}

	// MAX-TTL clamp (defense in depth): remaining lifetime <= maxTTL.
	if maxTTL > 0 && std.Expiry != nil {
		if time.Until(std.Expiry.Time()) > maxTTL {
			t.Fatalf("max-ttl: remaining lifetime exceeds %s", maxTTL)
		}
	}

	// (4) Grants present + room present.
	if grants.Video == nil {
		t.Fatalf("rule 4 (grants): no video grant")
	}
	if grants.Video.Room == "" {
		t.Fatalf("rule 4 (room): empty room")
	}
	if !grants.Video.RoomJoin {
		t.Fatalf("roomJoin must be true")
	}

	// (5) Room well-formed: `<tenant><sep><rest>` with both non-empty.
	i := strings.Index(grants.Video.Room, sep)
	if i < 0 {
		t.Fatalf("rule 5 (room prefix): room %q missing separator", grants.Video.Room)
	}
	tenantFromRoom := grants.Video.Room[:i]
	rest := grants.Video.Room[i+len(sep):]
	if tenantFromRoom == "" || rest == "" {
		t.Fatalf("rule 5 (room prefix): empty tenant or rest in %q", grants.Video.Room)
	}

	// (6) Tenant audience present and matches the room prefix.
	if grants.Name == "" {
		t.Fatalf("rule 6 (tenant audience): name empty")
	}
	if grants.Name != tenantFromRoom {
		t.Fatalf("rule 6 (tenant binding): name=%q != room prefix=%q", grants.Name, tenantFromRoom)
	}

	cp := true
	if grants.Video.CanPublish != nil {
		cp = *grants.Video.CanPublish
	}
	cs := true
	if grants.Video.CanSubscribe != nil {
		cs = *grants.Video.CanSubscribe
	}
	return tenantFromRoom, rest, std.Subject, cp, cs
}

func TestMint_ValidatesLikeWrapValidator(t *testing.T) {
	m, err := NewMinter(testKey, testSecret, 0)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	res, err := m.Mint(MintParams{TenantID: "user-abc", UserID: "user-abc", RoomName: "standup"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if res.RoomID != "user-abc:standup" {
		t.Fatalf("RoomID=%q want user-abc:standup", res.RoomID)
	}

	tenant, room, identity, cp, cs := verifyLikeWrapValidator(t, res.Token, testKey, testSecret, ":", 6*time.Hour)
	if tenant != "user-abc" {
		t.Fatalf("tenant=%q want user-abc", tenant)
	}
	if room != "standup" {
		t.Fatalf("room=%q want standup", room)
	}
	if identity != "user-abc" {
		t.Fatalf("identity=%q want user-abc", identity)
	}
	if !cp || !cs {
		t.Fatalf("default grants should be full media, got canPublish=%v canSubscribe=%v", cp, cs)
	}
}

func TestMint_RoomBinding_TenantIsServerDerived(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	// A malicious room name cannot smuggle a different tenant: the tenant prefix
	// is always the server-derived TenantID, and a roomName containing ':' is
	// rejected (would produce an ambiguous prefix).
	if _, err := m.Mint(MintParams{TenantID: "alice", UserID: "alice", RoomName: "evil:room"}); err == nil {
		t.Fatalf("expected error for room name containing separator")
	}
	// A normal mint binds room prefix to the tenant.
	res, err := m.Mint(MintParams{TenantID: "alice", UserID: "alice", RoomName: "room"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	tenant, _, _, _, _ := verifyLikeWrapValidator(t, res.Token, testKey, testSecret, ":", 6*time.Hour)
	if tenant != "alice" {
		t.Fatalf("tenant binding broken: got %q", tenant)
	}
}

func TestMint_WrongSecretFailsVerification(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	res, _ := m.Mint(MintParams{TenantID: "u", UserID: "u", RoomName: "r"})
	parsed, err := jwt.ParseSigned(res.Token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var std jwt.Claims
	var grants claimGrants
	if err := parsed.Claims([]byte("wrong-secret"), &std, &grants); err == nil {
		t.Fatalf("verification with wrong secret must fail")
	}
}

func TestMint_TTLBounds(t *testing.T) {
	// ttl<=0 → DefaultTTL.
	m, _ := NewMinter(testKey, testSecret, 0)
	if m.ttl != DefaultTTL {
		t.Fatalf("ttl=%s want default %s", m.ttl, DefaultTTL)
	}
	// ttl>MaxTTL clamped.
	m2, _ := NewMinter(testKey, testSecret, 24*time.Hour)
	if m2.ttl != MaxTTL {
		t.Fatalf("ttl=%s want clamped %s", m2.ttl, MaxTTL)
	}
	// The minted token's remaining lifetime respects the ceiling the validator
	// enforces (a token minted at MaxTTL passes a MaxTTL clamp; one longer would
	// not — but we clamp so it always passes).
	res, _ := m2.Mint(MintParams{TenantID: "u", UserID: "u", RoomName: "r"})
	verifyLikeWrapValidator(t, res.Token, testKey, testSecret, ":", MaxTTL+time.Minute)
}

func TestMint_ExplicitGrants(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	// Subscribe-only observer: publish=false, subscribe=true.
	res, err := m.Mint(MintParams{TenantID: "u", UserID: "u", RoomName: "r", CanPublish: false, CanSubscribe: true})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	_, _, _, cp, cs := verifyLikeWrapValidator(t, res.Token, testKey, testSecret, ":", 6*time.Hour)
	if cp {
		t.Fatalf("canPublish should be false for observer")
	}
	if !cs {
		t.Fatalf("canSubscribe should be true for observer")
	}
}

func TestNewMinter_SecretsUnset(t *testing.T) {
	if _, err := NewMinter("", testSecret, 0); err != ErrNotConfigured {
		t.Fatalf("empty key: got %v want ErrNotConfigured", err)
	}
	if _, err := NewMinter(testKey, "", 0); err != ErrNotConfigured {
		t.Fatalf("empty secret: got %v want ErrNotConfigured", err)
	}
	if _, err := NewMinter("  ", "  ", 0); err != ErrNotConfigured {
		t.Fatalf("whitespace: got %v want ErrNotConfigured", err)
	}
}

func TestMint_InvalidInputs(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	cases := []struct {
		name string
		p    MintParams
		want error
	}{
		{"empty tenant", MintParams{TenantID: "", UserID: "u", RoomName: "r"}, ErrEmptyTenant},
		{"empty room", MintParams{TenantID: "u", UserID: "u", RoomName: ""}, ErrEmptyRoom},
		{"tenant with separator", MintParams{TenantID: "a:b", UserID: "u", RoomName: "r"}, ErrInvalidTenant},
		{"room with separator", MintParams{TenantID: "u", UserID: "u", RoomName: "a:b"}, ErrInvalidRoom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Mint(tc.p); err != tc.want {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	// Empty user id (not one of the typed sentinels but must error).
	if _, err := m.Mint(MintParams{TenantID: "u", UserID: "", RoomName: "r"}); err == nil {
		t.Fatalf("empty user id must error")
	}
}
