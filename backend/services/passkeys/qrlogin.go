// Package passkeys — qrlogin.go
//
// LOGINISO-02: QR / phone-approval login for kiosk / streamed clients.
//
// Flow:
//  1. Kiosk calls POST /api/auth/qr/begin  → receives { challenge_id, qr_data }.
//     qr_data is a URL-safe string the kiosk encodes into a QR code.
//  2. An already-authenticated phone (with a valid session cookie) calls
//     POST /api/auth/qr/approve  → { challenge_id, approved: true }.
//     The challenge is resolved and a scoped session token is minted.
//  3. Kiosk polls GET /api/auth/qr/poll?id=<challenge_id> until it gets
//     { approved: true, session_token: "..." }, then sets the session cookie.
//
// Security properties:
//   - Single-use: a challenge can only be approved once; further polls/approvals
//     after resolution are rejected.
//   - Expiry: challenges expire after qrChallengeTTL (2 minutes).
//   - The session token minted is identical to a normal auth.CreateSession token
//     so no special server-side scoping is required; the kiosk gets a normal OS
//     session.
//   - qr_data encodes only the challenge_id + expiry, never a secret. The
//     real secret lives server-side; the approving phone sends the challenge_id
//     back over an authenticated channel.
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

// qrChallenge holds the server-side state for one QR approval challenge.
type qrChallenge struct {
	mu           sync.Mutex
	id           string
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
func (s *QRLoginService) Begin() (*QRBeginResult, error) {
	id, err := qrRandID()
	if err != nil {
		return nil, fmt.Errorf("qrlogin: generate id: %w", err)
	}

	exp := time.Now().Add(qrChallengeTTL)
	ch := &qrChallenge{
		id:        id,
		expiresAt: exp,
	}

	s.mu.Lock()
	s.challenges[id] = ch
	s.mu.Unlock()

	// qr_data encodes just the challenge_id inside a custom URL so the phone
	// app can deep-link straight to the approval flow.
	qrPayload, err := json.Marshal(map[string]string{
		"challenge_id": id,
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
// On success it mints an auth session for the same user and stores it in the
// challenge so the kiosk can retrieve it via Poll.
func (s *QRLoginService) Approve(challengeID, approverUserID string) error {
	if approverUserID == "" {
		return fmt.Errorf("qrlogin: approver must be authenticated")
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
type QRPollResult struct {
	Pending      bool   `json:"pending"`       // true while waiting
	Approved     bool   `json:"approved"`      // true when the phone approved
	SessionToken string `json:"session_token"` // set when Approved=true
	Expired      bool   `json:"expired"`       // true when challenge is past TTL
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
		// Approved — return the token and remove the challenge.
		token := ch.sessionToken
		s.mu.Lock()
		delete(s.challenges, challengeID)
		s.mu.Unlock()
		return &QRPollResult{Approved: true, SessionToken: token}, nil
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

// qrRandID generates a URL-safe random challenge id.
func qrRandID() (string, error) {
	b := make([]byte, 18)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
