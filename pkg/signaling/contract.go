// Package signaling implements a minimal in-memory WebRTC signaling store for
// the vulos.cloud control-plane.
//
// Two enrolled ULIDs negotiate a peer-to-peer connection by exchanging an SDP
// offer, SDP answer, and ICE candidates through short-lived sessions (60 s
// TTL). If P2P fails the caller falls back to the relay fabric.
//
// Sessions are stored only in memory and are intentionally lost on restart;
// the WebRTC layer re-negotiates automatically.
package signaling

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SessionTTL is the maximum lifetime of a signaling session.
const SessionTTL = 60 * time.Second

// Sentinel errors. Route handlers map these to HTTP status codes.
var (
	ErrSessionNotFound = errors.New("signaling: session not found")
	ErrSessionExpired  = errors.New("signaling: session expired")
	ErrNotOwner        = errors.New("signaling: caller does not own from_ulid")
)

// ICECandidate is a single ICE candidate exchanged during negotiation.
type ICECandidate struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid"`
	SDPMLineIndex int    `json:"sdp_mline_index"`
}

// Offer is the payload sent by the initiating ULID.
type Offer struct {
	FromULID   string         `json:"from_ulid"`
	ToULID     string         `json:"to_ulid"`
	SDP        string         `json:"sdp"`
	Candidates []ICECandidate `json:"candidates"`
}

// Answer is the payload sent by the responding ULID.
type Answer struct {
	SessionToken string         `json:"session_token"`
	SDP          string         `json:"sdp"`
	Candidates   []ICECandidate `json:"candidates"`
}

// Session holds all state for one signaling exchange.
type Session struct {
	Token       string
	FromULID    string
	ToULID      string
	OfferSDP    string
	OfferICE    []ICECandidate
	AnswerSDP   string
	AnswerICE   []ICECandidate
	Answered    bool
	ExpiresAt   time.Time
	answerReady chan struct{} // closed once AnswerSDP is stored
}

// Store is the signaling persistence contract.
// The only required implementation is MemSignalingStore (in-memory, no disk).
type Store interface {
	// CreateOffer stores a new offer and returns the generated session token.
	// Returns ErrNotOwner when the caller's accountID does not own fromULID
	// (ownership is verified externally before calling; the store itself just
	// records the session).
	CreateOffer(ctx context.Context, offer Offer) (sessionToken string, err error)

	// StoreAnswer records the answering SDP + ICE candidates against the session
	// identified by sessionToken. Returns ErrSessionNotFound or ErrSessionExpired.
	StoreAnswer(ctx context.Context, answer Answer) error

	// PollAnswer blocks until an answer is available, the session expires, or
	// ctx is cancelled. On success it returns the answered Session.
	// Returns ErrSessionNotFound, ErrSessionExpired, or ctx.Err().
	PollAnswer(ctx context.Context, sessionToken string) (Session, error)

	// GetSession returns the session without blocking.
	// Returns ErrSessionNotFound or ErrSessionExpired.
	GetSession(ctx context.Context, sessionToken string) (Session, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemSignalingStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemSignalingStore is the in-memory signaling Store. Sessions are lost on
// restart; that is acceptable because WebRTC re-negotiates automatically.
// A background goroutine cleans up expired sessions every 30 seconds.
type MemSignalingStore struct {
	mu       sync.Mutex
	sessions map[string]*Session

	stopOnce sync.Once
	stop     chan struct{}
}

// NewMemSignalingStore returns an initialised store and starts the background
// expiry sweeper. Call Close to stop the sweeper goroutine.
func NewMemSignalingStore() *MemSignalingStore {
	s := &MemSignalingStore{
		sessions: make(map[string]*Session),
		stop:     make(chan struct{}),
	}
	go s.sweeper()
	return s
}

// Close stops the background sweeper.
func (s *MemSignalingStore) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *MemSignalingStore) sweeper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.evictExpired()
		case <-s.stop:
			return
		}
	}
}

func (s *MemSignalingStore) evictExpired() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
}

// newToken generates a cryptographically random URL-safe session token.
func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("signaling: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateOffer implements Store.
func (s *MemSignalingStore) CreateOffer(_ context.Context, offer Offer) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	sess := &Session{
		Token:       token,
		FromULID:    offer.FromULID,
		ToULID:      offer.ToULID,
		OfferSDP:    offer.SDP,
		OfferICE:    offer.Candidates,
		ExpiresAt:   time.Now().Add(SessionTTL),
		answerReady: make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[token] = sess
	s.mu.Unlock()
	return token, nil
}

// StoreAnswer implements Store.
func (s *MemSignalingStore) StoreAnswer(_ context.Context, answer Answer) error {
	s.mu.Lock()
	sess, ok := s.sessions[answer.SessionToken]
	if !ok {
		s.mu.Unlock()
		return ErrSessionNotFound
	}
	if time.Now().After(sess.ExpiresAt) {
		delete(s.sessions, answer.SessionToken)
		s.mu.Unlock()
		return ErrSessionExpired
	}
	// Write the answer fields.
	sess.AnswerSDP = answer.SDP
	sess.AnswerICE = answer.Candidates
	sess.Answered = true
	ch := sess.answerReady
	s.mu.Unlock()

	// Signal any waiting pollers (non-blocking; channel is only closed once).
	select {
	case <-ch:
		// already closed — double answer; harmless
	default:
		close(ch)
	}
	return nil
}

// PollAnswer implements Store. It blocks until an answer is available, the
// session TTL elapses, or ctx is cancelled.
func (s *MemSignalingStore) PollAnswer(ctx context.Context, sessionToken string) (Session, error) {
	s.mu.Lock()
	sess, ok := s.sessions[sessionToken]
	if !ok {
		s.mu.Unlock()
		return Session{}, ErrSessionNotFound
	}
	if time.Now().After(sess.ExpiresAt) {
		delete(s.sessions, sessionToken)
		s.mu.Unlock()
		return Session{}, ErrSessionExpired
	}
	if sess.Answered {
		snap := *sess
		s.mu.Unlock()
		return snap, nil
	}
	// Capture the channel and remaining TTL while holding the lock so we don't
	// race with a concurrent expiry sweep.
	ch := sess.answerReady
	ttl := time.Until(sess.ExpiresAt)
	s.mu.Unlock()

	select {
	case <-ch:
		// Answer arrived; re-read under lock.
		s.mu.Lock()
		sess2, ok2 := s.sessions[sessionToken]
		if !ok2 {
			s.mu.Unlock()
			return Session{}, ErrSessionExpired
		}
		snap := *sess2
		s.mu.Unlock()
		return snap, nil

	case <-time.After(ttl):
		return Session{}, ErrSessionExpired

	case <-ctx.Done():
		return Session{}, ctx.Err()
	}
}

// GetSession implements Store.
func (s *MemSignalingStore) GetSession(_ context.Context, sessionToken string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionToken]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	if time.Now().After(sess.ExpiresAt) {
		delete(s.sessions, sessionToken)
		return Session{}, ErrSessionExpired
	}
	return *sess, nil
}

// compile-time assertion: MemSignalingStore satisfies Store.
var _ Store = (*MemSignalingStore)(nil)
