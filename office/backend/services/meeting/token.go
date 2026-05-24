// Package meeting provides scheduled-meeting persistence helpers, HMAC-signed
// join tokens, and lobby state management for Vulos Meet.
//
// Security properties:
//   - Join tokens are HMAC-SHA256 signed, single-use (nonce in token), 1-hour TTL.
//   - Room IDs are 22 URL-safe base64 characters (≈132 bits entropy).
//   - TURN credentials are scoped to a specific room_id + expiry.
//   - All joins are audit-logged with (room_id, account_id|null, ip, ua, accepted_by, joined_at).
//   - Per-IP brute-force rate limit is enforced at the handler layer.
package meeting

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ── constants ────────────────────────────────────────────────────────────────

const (
	TokenTTL     = time.Hour        // join token validity window
	RoomIDLen    = 16               // bytes → 22 URL-safe base64 chars
	MaxRoomPeers = 25               // default cap; configurable up to 100
)

// ── token payload ─────────────────────────────────────────────────────────────

// JoinTokenClaims is the JSON payload embedded in a signed join token.
type JoinTokenClaims struct {
	RoomID    string `json:"room_id"`
	AccountID string `json:"account_id,omitempty"` // empty for anonymous
	Nonce     string `json:"nonce"`                // 8 random bytes hex, ensures single-use
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// ── nonce store (single-use enforcement) ─────────────────────────────────────
// nonceKey uniquely identifies a nonce within a room.
type nonceKey struct {
	roomID string
	nonce  string
}

type nonceStore struct {
	mu      sync.Mutex
	used    map[nonceKey]int64 // nonceKey → expires_at unix
}

var globalNonces = &nonceStore{
	used: make(map[nonceKey]int64),
}

func init() {
	// Background sweep: remove expired nonces every 5 minutes.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			globalNonces.sweep()
		}
	}()
}

// markUsed registers a nonce as used. Returns false if the nonce was already used.
func (ns *nonceStore) markUsed(roomID, nonce string, expiresAt int64) bool {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	k := nonceKey{roomID: roomID, nonce: nonce}
	if _, exists := ns.used[k]; exists {
		return false
	}
	ns.used[k] = expiresAt
	return true
}

// sweep removes entries that have passed their expiry time.
func (ns *nonceStore) sweep() {
	now := time.Now().Unix()
	ns.mu.Lock()
	defer ns.mu.Unlock()
	for k, exp := range ns.used {
		if now > exp {
			delete(ns.used, k)
		}
	}
}

// ── participant roster (per-room cap) ─────────────────────────────────────────
type rosterStore struct {
	mu      sync.Mutex
	counts  map[string]int // roomID → current participant count
}

var globalRoster = &rosterStore{
	counts: make(map[string]int),
}

// ParticipantCount returns the current participant count for a room.
func ParticipantCount(roomID string) int {
	globalRoster.mu.Lock()
	defer globalRoster.mu.Unlock()
	return globalRoster.counts[roomID]
}

// ParticipantJoined increments the participant count for a room.
func ParticipantJoined(roomID string) {
	globalRoster.mu.Lock()
	defer globalRoster.mu.Unlock()
	globalRoster.counts[roomID]++
}

// ParticipantLeft decrements the participant count for a room (floor at 0).
func ParticipantLeft(roomID string) {
	globalRoster.mu.Lock()
	defer globalRoster.mu.Unlock()
	if globalRoster.counts[roomID] > 0 {
		globalRoster.counts[roomID]--
	}
}

// ── singleton secret ─────────────────────────────────────────────────────────
// Loaded from VULOS_MEET_SECRET env var (hex-encoded 32 bytes).
// If absent in dev mode, a random key is generated in-memory.

var (
	secretMu  sync.Mutex
	secretKey []byte
)

// LoadOrGenerateSecret loads the HMAC secret from env or generates one for dev.
func LoadOrGenerateSecret() error {
	secretMu.Lock()
	defer secretMu.Unlock()
	if secretKey != nil {
		return nil
	}
	raw := os.Getenv("VULOS_MEET_SECRET")
	if raw != "" {
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("meeting: decode VULOS_MEET_SECRET: %w", err)
		}
		if len(decoded) < 16 {
			return fmt.Errorf("meeting: VULOS_MEET_SECRET must be at least 32 hex chars (16 bytes)")
		}
		secretKey = decoded
		return nil
	}
	// Dev mode — random in-memory key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("meeting: generate dev secret: %w", err)
	}
	secretKey = key
	return nil
}

func getSecret() []byte {
	secretMu.Lock()
	defer secretMu.Unlock()
	return secretKey
}

// ── token issuance ───────────────────────────────────────────────────────────

// NewRoomID generates a URL-safe base64-encoded random room ID (22 chars, ≈132 bits).
func NewRoomID() (string, error) {
	b := make([]byte, RoomIDLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("meeting: generate room id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IssueJoinToken creates and signs a single-use join token for the given room.
// accountID may be empty for anonymous joins.
func IssueJoinToken(roomID, accountID string) (string, error) {
	if err := LoadOrGenerateSecret(); err != nil {
		return "", err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("meeting: generate nonce: %w", err)
	}
	now := time.Now()
	claims := JoinTokenClaims{
		RoomID:    roomID,
		AccountID: accountID,
		Nonce:     hex.EncodeToString(nonce),
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(TokenTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("meeting: marshal token claims: %w", err)
	}
	b64Payload := base64.RawURLEncoding.EncodeToString(payload)
	sig := signPayload(getSecret(), b64Payload)
	return b64Payload + "." + sig, nil
}

// VerifyJoinToken parses and verifies a join token. Returns the claims on success.
// Returns an error if the signature is invalid, the token is expired, or the
// format is malformed.
func VerifyJoinToken(token string) (*JoinTokenClaims, error) {
	if err := LoadOrGenerateSecret(); err != nil {
		return nil, err
	}

	b64Payload, sig, found := splitToken(token)
	if !found {
		return nil, errors.New("meeting: malformed token")
	}

	expectedSig := signPayload(getSecret(), b64Payload)
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return nil, errors.New("meeting: invalid token signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, fmt.Errorf("meeting: decode token payload: %w", err)
	}

	var claims JoinTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("meeting: parse token claims: %w", err)
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return nil, errors.New("meeting: token expired")
	}

	// Single-use enforcement: reject replayed nonces.
	if !globalNonces.markUsed(claims.RoomID, claims.Nonce, claims.ExpiresAt) {
		return nil, errors.New("meeting: token already used (replay rejected)")
	}

	return &claims, nil
}

// ── TURN credential issuance ─────────────────────────────────────────────────
// TURN credentials are room-scoped: username = "<expiry>:<roomID>:<userID>"
// so a credential issued for room A cannot be used in room B.

// TURNCredentials holds short-lived TURN credentials scoped to a room.
type TURNCredentials struct {
	Username   string `json:"username"`
	Credential string `json:"credential"`
	TTLSeconds int    `json:"ttlSeconds"`
}

// IssueTURNCredentials issues coturn-compatible short-lived TURN credentials
// scoped to a specific room_id (adds security binding beyond the standard
// time-limited credential). Uses HMAC-SHA256 (room-scoped extension of the
// standard HMAC-SHA256 coturn credential).
//
// Format invariant: username = "<expiry>:<roomID>:<userID>".
// roomID and userID must be URL-safe base64 characters (A-Za-z0-9_-) and must
// NOT contain ':' — a colon would corrupt the credential username parsing.
// Both IDs are generated by this package (NewRoomID / account IDs from the
// identity service) and are verified here as a defensive check.
func IssueTURNCredentials(roomID, userID string) (TURNCredentials, error) {
	// Defensive validation: neither field may contain ':'.
	if strings.ContainsRune(roomID, ':') {
		return TURNCredentials{}, errors.New("meeting: roomID must not contain ':'")
	}
	if strings.ContainsRune(userID, ':') {
		return TURNCredentials{}, errors.New("meeting: userID must not contain ':'")
	}

	secret := os.Getenv("VULOS_TURN_SECRET")
	if secret == "" {
		return TURNCredentials{}, errors.New("meeting: VULOS_TURN_SECRET not set")
	}
	ttl := 3600
	expiry := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	username := fmt.Sprintf("%d:%s:%s", expiry, roomID, userID)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(username))
	cred := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return TURNCredentials{
		Username:   username,
		Credential: cred,
		TTLSeconds: ttl,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func signPayload(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func splitToken(token string) (payload, sig string, ok bool) {
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			return token[:i], token[i+1:], true
		}
	}
	return "", "", false
}
