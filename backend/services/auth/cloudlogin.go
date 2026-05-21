// Package auth — cloudlogin.go
//
// CLOGIN-01: Validates a cloud-signed login token issued by the Vulos Cloud
// login-broker and creates an OS session from it.
//
// Token format (JSON, Ed25519-signed via services/signing):
//
//	{
//	  "account_id":  "<cloud-account ULID>",
//	  "ulid":        "<device ULID this token is bound to>",
//	  "email":       "<cloud account email>",
//	  "name":        "<display name>",
//	  "expires_at":  "<RFC3339>",
//	  "issued_at":   "<RFC3339>"
//	}
//
// The cloud signs canonical(payload) with the login-broker Ed25519 key baked
// at enrollment.  The OS validates against that baked pubkey (loaded from
// VULOS_CLOUD_BROKER_PUBKEY env or a well-known on-disk path) and, on success,
// finds-or-creates a local User and returns a session.
//
// The cloud is NOT a runtime dependency: token validation is purely local.
package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/services/signing"
)

// ─── Well-known broker-pubkey path ───────────────────────────────────────────

// defaultBrokerPubkeyPath is the on-disk location where the cloud-managed
// enrollment writes the login-broker Ed25519 public key (raw 32 bytes, base64
// encoded, one line).  Can be overridden via VULOS_CLOUD_BROKER_PUBKEY env.
const defaultBrokerPubkeyPath = "/var/lib/vulos/cloud/broker.pub"

// LoadBrokerPubkey reads the login-broker Ed25519 public key from disk or the
// VULOS_CLOUD_BROKER_PUBKEY environment variable.
//
//   - VULOS_CLOUD_BROKER_PUBKEY env → value is either a raw base64-encoded
//     32-byte key (no newlines) or a path to a file containing such a key.
//   - Otherwise, defaultBrokerPubkeyPath is used.
//
// Returns ErrNoBrokerPubkey if no key file is found — callers should treat
// this as "cloud login not configured on this device".
func LoadBrokerPubkey() (ed25519.PublicKey, error) {
	// 1. Check env override.
	if env := os.Getenv("VULOS_CLOUD_BROKER_PUBKEY"); env != "" {
		// If the value looks like a path (contains / or starts with .),
		// treat it as a file path; otherwise try inline base64.
		looksLikePath := len(env) > 0 && (env[0] == '/' || env[0] == '.')
		if looksLikePath {
			return readPubkeyFile(env)
		}
		return decodePubkeyB64(env)
	}

	// 2. Well-known path.
	return readPubkeyFile(defaultBrokerPubkeyPath)
}

// ErrNoBrokerPubkey is returned when no broker public key is configured on
// this device (i.e. it has never been enrolled with Vulos Cloud).
var ErrNoBrokerPubkey = errors.New("cloud login broker pubkey not configured on this device")

func readPubkeyFile(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoBrokerPubkey
		}
		return nil, fmt.Errorf("cloudlogin: read broker pubkey %s: %w", path, err)
	}
	return decodePubkeyB64(strings.TrimSpace(string(data)))
}

func decodePubkeyB64(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Try URL-safe base64 too.
		raw, err = base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("cloudlogin: decode broker pubkey base64: %w", err)
		}
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cloudlogin: broker pubkey must be %d bytes, got %d",
			ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// WriteBrokerPubkey persists a raw Ed25519 public key to the well-known broker
// pubkey path.  Called at enrollment time.
func WriteBrokerPubkey(pub ed25519.PublicKey) error {
	path := defaultBrokerPubkeyPath
	if env := os.Getenv("VULOS_CLOUD_BROKER_PUBKEY"); env != "" {
		if _, err := os.Stat(filepath.Dir(env)); err == nil {
			path = env
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("cloudlogin: mkdir for broker pubkey: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(pub)
	return os.WriteFile(path, []byte(b64+"\n"), 0600)
}

// ─── Token types ─────────────────────────────────────────────────────────────

// CloudLoginToken is the JSON payload of a cloud-issued login token.
type CloudLoginToken struct {
	AccountID string    `json:"account_id"`
	ULID      string    `json:"ulid"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
	IssuedAt  time.Time `json:"issued_at"`
}

// CloudLoginRequest is the wire format sent by the frontend to
// POST /api/auth/cloudlogin.
type CloudLoginRequest struct {
	// Token is the canonical JSON payload exactly as the cloud produced it.
	Token []byte `json:"token"`
	// Signature is the Ed25519 signature (base64) over Token.
	Signature string `json:"signature"`
}

// CloudSessionInfo is returned to the frontend on a successful cloud login.
type CloudSessionInfo struct {
	SessionToken string    `json:"session_token"`
	UserID       string    `json:"user_id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	AccountID    string    `json:"account_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ─── Verifier ────────────────────────────────────────────────────────────────

// CloudLoginVerifier validates cloud-signed login tokens and issues OS sessions.
type CloudLoginVerifier struct {
	store  *Store
	pubkey ed25519.PublicKey // may be nil if cloud login is not configured
}

// NewCloudLoginVerifier creates a verifier.  pubkey may be nil; in that case
// every call to Login returns ErrNoBrokerPubkey.
func NewCloudLoginVerifier(store *Store, pubkey ed25519.PublicKey) *CloudLoginVerifier {
	return &CloudLoginVerifier{store: store, pubkey: pubkey}
}

// Login validates a CloudLoginRequest and, on success, returns a CloudSessionInfo
// with a new OS session token.
//
//   - tokenBytes must be the exact canonical JSON bytes the cloud signed.
//   - sigB64 is the base64 (std or raw-URL) encoded Ed25519 signature.
//
// The function returns one of:
//   - (info, nil)           — success; OS session created.
//   - (_, ErrNoBrokerPubkey) — cloud not enrolled on this device.
//   - (_, ErrTokenExpired)   — token is past its expires_at.
//   - (_, ErrBadSignature)   — signature does not verify.
func (v *CloudLoginVerifier) Login(tokenBytes []byte, sigB64 string) (*CloudSessionInfo, error) {
	if v.pubkey == nil {
		return nil, ErrNoBrokerPubkey
	}

	// 1. Decode signature.
	sig, err := decodeSig(sigB64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}

	// 2. Verify Ed25519 signature over the canonical token bytes.
	if !signing.Verify(v.pubkey, tokenBytes, sig) {
		return nil, ErrBadSignature
	}

	// 3. Parse the token payload.
	var tok CloudLoginToken
	if err := json.Unmarshal(tokenBytes, &tok); err != nil {
		return nil, fmt.Errorf("cloudlogin: parse token: %w", err)
	}

	// 4. Validate expiry.
	if time.Now().After(tok.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	// 5. Validate required fields.
	if tok.AccountID == "" || tok.Email == "" {
		return nil, errors.New("cloudlogin: token missing account_id or email")
	}

	log.Printf("[cloudlogin] valid token: account=%s email=%s expires=%s",
		tok.AccountID, tok.Email, tok.ExpiresAt.Format(time.RFC3339))

	// 6. Find or create a local User linked to this cloud account.
	user := v.store.FindOrCreateUser("cloud", tok.AccountID, tok.Email, tok.Name, "")

	// 7. Create OS session.
	sess := v.store.CreateSession(user, "cloud:"+tok.AccountID)

	return &CloudSessionInfo{
		SessionToken: sess.Token,
		UserID:       user.ID,
		Email:        tok.Email,
		Name:         tok.Name,
		AccountID:    tok.AccountID,
		ExpiresAt:    sess.ExpiresAt,
	}, nil
}

// Sentinel errors for cloud login.
var (
	ErrTokenExpired = errors.New("cloudlogin: token has expired")
	ErrBadSignature = errors.New("cloudlogin: signature verification failed")
)

// decodeSig decodes a base64 (standard or raw-URL) encoded signature.
func decodeSig(b64 string) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(b64)
	if err == nil {
		return sig, nil
	}
	sig, err = base64.RawURLEncoding.DecodeString(b64)
	if err == nil {
		return sig, nil
	}
	return nil, fmt.Errorf("cloudlogin: decode sig base64: %w", err)
}

// ─── IsCloudEnrolled ─────────────────────────────────────────────────────────

// IsCloudEnrolled reports whether this OS instance has a baked broker pubkey,
// which is the proxy for "is this device enrolled with Vulos Cloud."
func IsCloudEnrolled() bool {
	_, err := LoadBrokerPubkey()
	return err == nil
}

// enrollmentFlagPath is a simple sentinel file written at enrollment time.
const enrollmentFlagPath = "/var/lib/vulos/cloud/enrolled"

// WriteEnrollmentFlag creates the enrollment sentinel file.
func WriteEnrollmentFlag() error {
	if err := os.MkdirAll(filepath.Dir(enrollmentFlagPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(enrollmentFlagPath, []byte("enrolled\n"), 0600)
}
