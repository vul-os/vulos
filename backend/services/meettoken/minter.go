// Package meettoken is the self-host (no-cloud-CP) minter for VULOS-MEET/1
// join tokens.
//
// # WHY THIS EXISTS
//
// In the cloud topology the Meet browser calls POST /api/meet/token and the
// control plane (vulos-cloud MEET-CP-01) mints a LiveKit-compatible token that
// the vulos-meet SFU wrapper (internal/wrap.Validator) then validates. The SFU
// only ever VALIDATES — it never mints. On a sovereign box there is no CP, so
// nothing mints the token and Meet cannot start a call.
//
// This package closes that gap: the OS backend (which serves /api on a
// self-host box) mints the SAME VULOS-MEET/1 token shape the wrap.Validator
// accepts, signed with the LOCAL LiveKit (api_key, api_secret) pair. The SFU is
// configured with that same pair, so a token minted here verifies both at the
// wrap.Validator admission seam AND at LiveKit Server's own signaling verifier.
//
// # BYTE-COMPATIBILITY
//
// The validator parses the token with github.com/go-jose/go-jose/v3/jwt
// (jwt.ParseSigned + token.Claims) and reads a livekit auth.ClaimGrants shape:
// the top-level JWT claims (iss/sub/nbf/exp) merged with a `name` claim (the
// tenant audience) and a `video` grant carrying {room, roomJoin, canPublish,
// canSubscribe}. To guarantee a byte-identical wire format WITHOUT pulling the
// whole github.com/livekit/protocol module (which drags in nats/redis/psrpc/
// twirp/zap — a 400+ package footprint inappropriate for a sovereign box), we
// sign with the SAME go-jose JOSE library the validator uses and replicate only
// the tiny ClaimGrants/VideoGrant JSON structs (exact json tags copied from
// livekit/protocol/auth@v1.46.0/auth/grants.go). Same signer, same claim names,
// same HS256 alg ⇒ the "two implementations, two bugs" risk the spec warns
// about is avoided at the byte level.
//
// See vulos-meet/spec/TOKEN.md and vulos-meet/internal/wrap/{auth,tenant}.go
// for the canonical validation rules this minter targets.
package meettoken

import (
	"errors"
	"fmt"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
)

// DefaultTenantSeparator MUST match vulos-meet wrap.DefaultTenantSeparator.
// The room grant is `<tenant><sep><rest>` and the validator splits on this
// exact byte. It is a single ASCII byte that cannot appear in a tenant id.
const DefaultTenantSeparator = ":"

// MaxTenantIDLen mirrors wrap.MaxTenantIDLen — the validator rejects a room
// whose tenant prefix is longer than this, so we reject at mint too.
const MaxTenantIDLen = 63

// DefaultTTL is the join-token lifetime when the minter is built with ttl<=0.
// 15 minutes matches the cloud CP's TokenTTLDefault: long enough to establish a
// call, short enough that a leaked token's blast radius is small. The token is
// only needed to JOIN; the SFU keeps the media session alive after join.
const DefaultTTL = 15 * time.Minute

// MaxTTL is the hard ceiling. vulos-meet's validator independently rejects any
// token whose remaining lifetime exceeds MEET_MAX_TOKEN_TTL (default 6h), so a
// longer TTL here would simply be rejected downstream. We clamp to keep the two
// sides consistent and the blast radius bounded.
const MaxTTL = 6 * time.Hour

// Errors surfaced to the HTTP layer. The route maps these to stable statuses.
var (
	// ErrNotConfigured means the LiveKit signing secrets are unset. The route
	// maps this to 503 (Meet not configured) — never a 500/crash.
	ErrNotConfigured = errors.New("meettoken: LiveKit signing secrets not configured (set LIVEKIT_API_KEY + LIVEKIT_API_SECRET)")
	// ErrEmptyTenant / ErrEmptyRoom / ErrInvalidTenant / ErrInvalidRoom are
	// input-validation failures → 400.
	ErrEmptyTenant   = errors.New("meettoken: tenant is empty")
	ErrEmptyRoom     = errors.New("meettoken: room name is empty")
	ErrInvalidTenant = errors.New("meettoken: tenant id contains invalid characters")
	ErrInvalidRoom   = errors.New("meettoken: room name contains the tenant separator")
)

// Minter mints VULOS-MEET/1 tokens with the local LiveKit key/secret.
//
// It reads the api_key/api_secret at construction; a rotation requires a
// restart (the self-host box reads them from env like vulos-meet's own binary
// does — see vulos-meet/internal/wrap/config.go LiveKitAPIKeyEnv). Zero value is
// not usable; build with NewMinter.
type Minter struct {
	apiKey    string
	apiSecret string
	sep       string
	ttl       time.Duration
	nowFn     func() time.Time
}

// NewMinter builds a Minter. apiKey/apiSecret are the shared LiveKit signing
// pair; both MUST be non-empty or NewMinter returns ErrNotConfigured so the
// caller can register a 503-only handler instead of a broken minter. ttl<=0
// falls back to DefaultTTL; ttl>MaxTTL is clamped to MaxTTL.
func NewMinter(apiKey, apiSecret string, ttl time.Duration) (*Minter, error) {
	apiKey = strings.TrimSpace(apiKey)
	apiSecret = strings.TrimSpace(apiSecret)
	if apiKey == "" || apiSecret == "" {
		return nil, ErrNotConfigured
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	return &Minter{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		sep:       DefaultTenantSeparator,
		ttl:       ttl,
		nowFn:     time.Now,
	}, nil
}

// MintParams is the input to Mint. TenantID and UserID are SERVER-DERIVED from
// the authenticated session (never the request body); RoomName comes from the
// request. CanPublish/CanSubscribe default to a full-media participant when both
// are false (matching the CP minter's convenience default).
type MintParams struct {
	// TenantID becomes the JWT `name` claim (tenant audience) AND the prefix of
	// the `video.room` grant. The validator rejects unless name == prefix(room).
	// On a self-host box this is the session user's id (1:1 user↔tenant, same
	// convention as the cloud CP).
	TenantID string
	// UserID becomes JWT `sub` / LiveKit identity.
	UserID string
	// RoomName is the per-tenant room name from the request. It is combined with
	// TenantID to form the qualified room id `<tenant><sep><roomName>`.
	RoomName string
	// CanPublish / CanSubscribe are the media grants. Both-false ⇒ both-true
	// (normal participant convenience default).
	CanPublish   bool
	CanSubscribe bool
	// Host adds the RoomAdmin grant — the HOST authority every vulos-meet
	// meeting control gates on (record, mute-others, remove, mute-all, end,
	// lock, waiting-room admit, spotlight, lower-hands, AI summary), and the one
	// the browser reads off the token to decide whether to offer the control.
	// Without it the host role does not exist at runtime, however complete the
	// UI looks.
	//
	// The caller decides, and on this box the answer follows from the 1:1
	// user↔tenant binding: the token's tenant audience IS the session user, and
	// the validator only accepts a token whose room is prefixed by that tenant,
	// so every room a user can mint for is a room inside their own tenant. The
	// box owner is host of their own rooms; there is no cross-tenant participant
	// this could over-grant to. It is room-scoped regardless (never
	// RoomList/RoomCreate).
	Host bool
}

// MintResult is the minted token plus the derived room id and absolute expiry.
type MintResult struct {
	Token     string
	RoomID    string
	ExpiresAt time.Time
}

// videoGrant is the exact wire shape of livekit/protocol/auth.VideoGrant for
// the fields the validator (and LiveKit Server) read. json tags are copied
// verbatim; omitempty on the pointer bools distinguishes explicit-false from
// unset exactly as livekit does.
type videoGrant struct {
	RoomJoin     bool   `json:"roomJoin,omitempty"`
	Room         string `json:"room,omitempty"`
	RoomAdmin    bool   `json:"roomAdmin,omitempty"`
	CanPublish   *bool  `json:"canPublish,omitempty"`
	CanSubscribe *bool  `json:"canSubscribe,omitempty"`
}

// claimGrants is the subset of livekit/protocol/auth.ClaimGrants we emit. The
// validator reads Name (tenant audience) and Video. Other fields are omitted
// (omitempty) so the serialized claim set is byte-identical to what the CP
// minter produces for the same inputs.
type claimGrants struct {
	Name  string      `json:"name,omitempty"`
	Video *videoGrant `json:"video,omitempty"`
}

// QualifyRoom builds `<tenant><sep><roomName>` with the same discipline as
// wrap.Tenant.QualifyRoom: non-empty tenant + room, tenant charset enforced,
// and roomName must not already contain the separator.
func (m *Minter) QualifyRoom(tenant, roomName string) (string, error) {
	if err := validateTenant(tenant, m.sep); err != nil {
		return "", err
	}
	if roomName == "" {
		return "", ErrEmptyRoom
	}
	if strings.Contains(roomName, m.sep) {
		return "", ErrInvalidRoom
	}
	return tenant + m.sep + roomName, nil
}

// Mint issues a fresh VULOS-MEET/1 token. The returned token verifies against
// vulos-meet's wrap.Validator (and LiveKit Server) because it is signed with
// the same HS256 secret, carries iss==apiKey, a `name`==tenant audience, and a
// `video.room` prefixed by that same tenant — the binding the validator
// enforces.
func (m *Minter) Mint(p MintParams) (MintResult, error) {
	if m == nil || m.apiKey == "" || m.apiSecret == "" {
		return MintResult{}, ErrNotConfigured
	}
	roomID, err := m.QualifyRoom(p.TenantID, p.RoomName)
	if err != nil {
		return MintResult{}, err
	}
	if p.UserID == "" {
		return MintResult{}, errors.New("meettoken: user id is empty")
	}

	// Both-false ⇒ both-true: a normal participant that did not specify grants
	// gets full media. This matches the CP minter's convenience default.
	canPub := p.CanPublish || (!p.CanPublish && !p.CanSubscribe)
	canSub := p.CanSubscribe || (!p.CanPublish && !p.CanSubscribe)

	now := m.nowFn()
	expiresAt := now.Add(m.ttl)

	grants := claimGrants{
		Name: p.TenantID, // tenant audience — MUST byte-equal the room prefix
		Video: &videoGrant{
			Room:         roomID,
			RoomJoin:     true,
			RoomAdmin:    p.Host,
			CanPublish:   &canPub,
			CanSubscribe: &canSub,
		},
	}

	// Standard JWT claims. iss==apiKey is what ParseAPIToken reads to identify
	// the signing key, and Verify enforces iss==apiKey + exp/nbf. NumericDate is
	// seconds-precision, identical to livekit's SetValidFor output.
	std := jwt.Claims{
		Issuer:    m.apiKey,
		Subject:   p.UserID,
		NotBefore: jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(expiresAt),
	}

	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: []byte(m.apiSecret)},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return MintResult{}, fmt.Errorf("meettoken: build signer: %w", err)
	}
	// Merge standard claims + grants into one flat claim set, exactly as
	// livekit's AccessToken.ToJWT does (jwt.Signed(...).Claims(cl).Claims(&grant)).
	tok, err := jwt.Signed(sig).Claims(std).Claims(&grants).CompactSerialize()
	if err != nil {
		return MintResult{}, fmt.Errorf("meettoken: serialize jwt: %w", err)
	}
	if tok == "" {
		return MintResult{}, errors.New("meettoken: serializer returned empty token")
	}
	return MintResult{Token: tok, RoomID: roomID, ExpiresAt: expiresAt.UTC()}, nil
}

// validateTenant enforces the same tenant-id rules as wrap.Tenant.validateTenant:
// non-empty, <= MaxTenantIDLen, ASCII [A-Za-z0-9_-] only, and must not contain
// the separator. A tenant that fails here would be rejected by the validator, so
// we reject early with a typed error.
func validateTenant(tenant, sep string) error {
	if tenant == "" {
		return ErrEmptyTenant
	}
	if len(tenant) > MaxTenantIDLen {
		return ErrInvalidTenant
	}
	for i := 0; i < len(tenant); i++ {
		c := tenant[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_'
		if !ok {
			return ErrInvalidTenant
		}
	}
	if strings.Contains(tenant, sep) {
		return ErrInvalidTenant
	}
	return nil
}
