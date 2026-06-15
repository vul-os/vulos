// Package passkeys — qrlogin.go
//
// LOGINISO-02: QR / phone-approval login for kiosk / streamed clients.
//
// Flow:
//  1. Kiosk calls POST /api/auth/qr/begin  → receives { challenge_id, qr_data }.
//     qr_data is a URL-safe string the kiosk encodes into a QR code.
//  2. An already-authenticated phone (with a valid session cookie) calls
//     POST /api/auth/qr/approve  → { challenge_id, nonce, approved: true }.
//     The challenge is resolved and a scoped session token is minted.
//  3. Kiosk polls GET /api/auth/qr/poll?id=<challenge_id> until it gets
//     { approved: true, session_token: "..." }, then sets the session cookie.
//
// Security properties:
//   - Single-use: a challenge can only be approved once; further polls/approvals
//     after resolution are rejected.
//   - Expiry: challenges expire after qrChallengeTTL (2 minutes).
//   - Nonce binding (QRSEC-01): Begin() generates a random nonce that is
//     embedded in qr_data. The approving device must echo the nonce in its
//     Approve() call. A shoulder-surfer who sees only the on-screen QR image
//     but does not have access to the decoded payload bytes cannot construct a
//     valid approval request without the nonce.
//   - The session token minted is identical to a normal auth.CreateSession token
//     so no special server-side scoping is required; the kiosk gets a normal OS
//     session.
//   - Residual risk: a device that can both photograph and decode the QR code
//     (e.g. a camera aimed at the screen) still obtains the nonce. Full
//     device-key binding (signing the approval with the phone's TPM key) is
//     deferred; the nonce-echo is the minimum viable improvement.
package passkeys

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"vulos/backend/services/auth"
)

const (
	qrChallengeTTL = 2 * time.Minute
	// qrDataPrefix is prepended to the challenge_id in the qr_data payload so
	// the phone app can route it correctly.
	qrDataPrefix = "vulos://qr-login/"
)

// ErrQRChallengeNotFound is returned when a challenge_id is unknown.
var ErrQRChallengeNotFound = errors.New("qrlogin: challenge not found or expired")

// ErrQRChallengeExpired is returned when the challenge TTL has passed.
var ErrQRChallengeExpired = errors.New("qrlogin: challenge expired")

// ErrQRChallengeAlreadyUsed is returned when a single-use challenge has
// already been resolved.
var ErrQRChallengeAlreadyUsed = errors.New("qrlogin: challenge already used")

// ErrQRNonceMismatch is returned when the approver supplies the wrong nonce.
// This indicates a shoulder-surfer attack: the approver knows the challenge_id
// (e.g. from seeing it in the URL or a brief glimpse of the QR image) but
// does not hold the decoded QR payload bytes containing the nonce.
var ErrQRNonceMismatch = errors.New("qrlogin: nonce mismatch")

// qrChallenge holds the server-side state for one QR approval challenge.
type qrChallenge struct {
	mu           sync.Mutex
	id           string
	nonce        string    // QRSEC-01: random value embedded in qr_data; approver must echo
	expiresAt    time.Time
	approvedByID string // userID of the approving phone (empty until approved)
	sessionToken string // minted on approval, handed to the kiosk on poll
	used         bool   // true once approved or expired+polled
}

// QRLoginService manages QR login challenges.
// Construct it with NewQRLoginService.
type QRLoginService struct {
	mu         sync.Mutex
	challenges map[string]*qrChallenge
	store      *auth.Store
}

// NewQRLoginService returns a ready QRLoginService backed by store.
func NewQRLoginService(store *auth.Store) *QRLoginService {
	return &QRLoginService{
		challenges: make(map[string]*qrChallenge),
		store:      store,
	}
}

// QRBeginResult is the response payload for POST /api/auth/qr/begin.
type QRBeginResult struct {
	ChallengeID string    `json:"challenge_id"`
	QRData      string    `json:"qr_data"`   // encode this into a QR image
	ExpiresAt   time.Time `json:"expires_at"` // when the challenge expires
}

// Begin creates a new QR challenge and returns the data the kiosk should render
// as a QR code.
//
// QRSEC-01: a random nonce is generated and embedded in qr_data. The approving
// phone must include this nonce in its Approve() call. This prevents an attacker
// who knows the challenge_id (e.g. from a partial glimpse of the screen or the
// poll URL) from approving without access to the full decoded QR payload.
func (s *QRLoginService) Begin() (*QRBeginResult, error) {
	id, err := qrRandID()
	if err != nil {
		return nil, fmt.Errorf("qrlogin: generate id: %w", err)
	}
	nonce, err := qrRandID()
	if err != nil {
		return nil, fmt.Errorf("qrlogin: generate nonce: %w", err)
	}

	exp := time.Now().Add(qrChallengeTTL)
	ch := &qrChallenge{
		id:        id,
		nonce:     nonce,
		expiresAt: exp,
	}

	s.mu.Lock()
	s.challenges[id] = ch
	s.mu.Unlock()

	// qr_data encodes the challenge_id + nonce + expiry inside a custom URL so
	// the phone app can deep-link straight to the approval flow.
	// The nonce is intentionally opaque to the kiosk: the kiosk only displays
	// the QR code; it never needs to read the nonce value itself.
	qrPayload, err := json.Marshal(map[string]string{
		"challenge_id": id,
		"nonce":        nonce,
		"expires_at":   exp.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("qrlogin: marshal qr_data: %w", err)
	}

	return &QRBeginResult{
		ChallengeID: id,
		QRData:      qrDataPrefix + base64.RawURLEncoding.EncodeToString(qrPayload),
		ExpiresAt:   exp,
	}, nil
}

// Approve is called by the authenticated phone to approve a challenge.
// approverUserID must be a valid, already-authenticated user ID (taken from the
// X-User-ID header set by the auth middleware).
//
// QRSEC-01: the caller must supply the nonce that was embedded in the QR code
// payload. This value is only obtainable by decoding the QR image; knowing
// the challenge_id alone (e.g. from a shoulder-surfing glimpse) is not enough.
//
// On success it mints an auth session for the same user and stores it in the
// challenge so the kiosk can retrieve it via Poll.
func (s *QRLoginService) Approve(challengeID, approverUserID, nonce string) error {
	if approverUserID == "" {
		return fmt.Errorf("qrlogin: approver must be authenticated")
	}
	if nonce == "" {
		return ErrQRNonceMismatch
	}

	s.mu.Lock()
	ch, ok := s.challenges[challengeID]
	s.mu.Unlock()
	if !ok {
		return ErrQRChallengeNotFound
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	if time.Now().After(ch.expiresAt) {
		return ErrQRChallengeExpired
	}
	if ch.used {
		return ErrQRChallengeAlreadyUsed
	}

	// QRSEC-01: verify the nonce echoed by the approving device matches the
	// value that was embedded in the QR payload.
	if nonce != ch.nonce {
		return ErrQRNonceMismatch
	}

	// Look up the approver in the auth store.
	u, ok := s.store.GetUser(approverUserID)
	if !ok || u == nil {
		return fmt.Errorf("qrlogin: approver user not found")
	}

	// Mint a session for the approver; the kiosk logs in as the same user.
	sess := s.store.CreateSession(u, "qr-kiosk")
	s.store.Flush()

	ch.approvedByID = approverUserID
	ch.sessionToken = sess.Token
	ch.used = true

	return nil
}

// QRPollResult is the response payload for GET /api/auth/qr/poll.
// The session token is intentionally NOT included in the body — it is delivered
// only via an httponly cookie set by the HTTP handler so it is never readable
// by JavaScript running in the kiosk page.
type QRPollResult struct {
	Pending  bool   `json:"pending"`  // true while waiting
	Approved bool   `json:"approved"` // true when the phone approved
	Expired  bool   `json:"expired"`  // true when challenge is past TTL
	// sessionToken is the minted token; it is not exported to JSON.
	// The HTTP handler reads it and sets the cookie, then discards it.
	sessionToken string
}

// Poll returns the current state of a challenge. The kiosk calls this
// repeatedly (short-poll) until Approved or Expired.
//
// Once Approved is returned, the challenge is cleaned up from memory.
func (s *QRLoginService) Poll(challengeID string) (*QRPollResult, error) {
	s.mu.Lock()
	ch, ok := s.challenges[challengeID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrQRChallengeNotFound
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.used && ch.sessionToken != "" {
		// Approved — return the token via the unexported field so the HTTP handler
		// can set it as an httponly cookie without leaking it in the JSON body.
		token := ch.sessionToken
		s.mu.Lock()
		delete(s.challenges, challengeID)
		s.mu.Unlock()
		return &QRPollResult{Approved: true, sessionToken: token}, nil
	}

	if time.Now().After(ch.expiresAt) {
		// Expired without approval — clean up.
		s.mu.Lock()
		delete(s.challenges, challengeID)
		s.mu.Unlock()
		return &QRPollResult{Expired: true}, nil
	}

	return &QRPollResult{Pending: true}, nil
}

// SessionToken returns the minted session token (only populated when Approved is true).
// Callers should set this as an httponly cookie rather than including it in a JSON body.
func (r *QRPollResult) SessionToken() string { return r.sessionToken }

// qrRandID generates a URL-safe random challenge id.
func qrRandID() (string, error) {
	b := make([]byte, 18)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
