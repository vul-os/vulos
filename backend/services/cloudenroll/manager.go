// manager.go — UNIFIED-SIGNIN: a small state machine over Enroller so the OS
// HTTP layer can drive RFC 8628 device enrollment asynchronously.
//
// Enroller.Enroll blocks until the fleet owner approves the grant in the cloud
// console, which can take minutes — far longer than an HTTP request should
// live. Manager runs the grant in a background goroutine and exposes:
//
//   - Begin  — start (or join) an enrollment; returns the user_code +
//     verification_uri as soon as the CP hands them out, so the UI can show
//     "enter ABCD-1234 at <uri>" immediately.
//   - Status — poll the grant state (idle | pending | approved | error).
//   - Identity — the enrolled identity (cached, or loaded from disk).
//
// One grant at a time: a second Begin while a grant is pending returns the
// SAME user_code (idempotent), so a UI retry never orphans a grant.
package cloudenroll

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ManagerStatus is a snapshot of the enrollment state machine.
type ManagerStatus struct {
	// State is one of "idle", "pending", "approved", "error".
	State           string
	UserCode        string
	VerificationURI string
	ULID            string
	Error           string
}

// Manager drives asynchronous device enrollment over an Enroller.
type Manager struct {
	e *Enroller
	// OnEnrolled, when non-nil, is invoked (on the enroll goroutine) with the
	// fresh identity after a successful grant — main.go uses it to hot-wire the
	// device cert into the integrations client without a restart.
	OnEnrolled func(*Identity)

	// beginTimeout bounds how long Begin waits for the CP to hand out the
	// user_code. Tests may shorten it.
	beginTimeout time.Duration

	mu       sync.Mutex
	state    string // "idle" | "pending" | "approved" | "error"
	userCode string
	uri      string
	errMsg   string
	ident    *Identity
}

// NewManager wraps e. onEnrolled may be nil.
func NewManager(e *Enroller, onEnrolled func(*Identity)) *Manager {
	return &Manager{e: e, OnEnrolled: onEnrolled, beginTimeout: 20 * time.Second, state: "idle"}
}

// Identity returns the enrolled device identity: the in-memory one from a
// just-completed grant, else the on-disk identity. ulid == "" means the box is
// not enrolled (and err == nil).
func (m *Manager) Identity() (ulid, account string, err error) {
	m.mu.Lock()
	if m.ident != nil {
		u, a := m.ident.ULID, m.ident.Account
		m.mu.Unlock()
		return u, a, nil
	}
	m.mu.Unlock()

	id, err := m.e.Load()
	if err != nil {
		return "", "", err
	}
	if id == nil {
		return "", "", nil
	}
	m.mu.Lock()
	m.ident = id
	m.state = "approved"
	m.mu.Unlock()
	return id.ULID, id.Account, nil
}

// ErrAlreadyEnrolled is returned by Begin when the box already holds an identity.
var ErrAlreadyEnrolled = errors.New("cloudenroll: device is already enrolled")

// Begin starts a new enrollment grant (or joins the pending one) and returns
// the user_code + verification_uri for the owner-approval screen. It does NOT
// wait for approval — poll Status for that.
func (m *Manager) Begin(ctx context.Context) (userCode, verificationURI string, err error) {
	// Already enrolled? (Also loads from disk.)
	if ulid, _, ierr := m.Identity(); ierr == nil && ulid != "" {
		return "", "", ErrAlreadyEnrolled
	}

	m.mu.Lock()
	if m.state == "pending" && m.userCode != "" {
		uc, uri := m.userCode, m.uri
		m.mu.Unlock()
		return uc, uri, nil
	}
	m.state = "pending"
	m.userCode, m.uri, m.errMsg = "", "", ""
	m.mu.Unlock()

	// The grant outlives this HTTP request: run it on a background context.
	// The CP-advertised expires_in bounds the poll loop, so no leak.
	codeCh := make(chan [2]string, 1)
	errCh := make(chan error, 1)
	go func() {
		id, enrollErr := m.e.Enroll(context.Background(), func(uc, uri string) {
			m.mu.Lock()
			m.userCode, m.uri = uc, uri
			m.mu.Unlock()
			codeCh <- [2]string{uc, uri}
		})
		m.mu.Lock()
		if enrollErr != nil {
			m.state = "error"
			m.errMsg = enrollErr.Error()
			m.mu.Unlock()
			errCh <- enrollErr // pre-notify failures unblock Begin immediately
			return
		}
		m.ident = id
		m.state = "approved"
		cb := m.OnEnrolled
		m.mu.Unlock()
		if cb != nil {
			cb(id)
		}
	}()

	select {
	case pair := <-codeCh:
		return pair[0], pair[1], nil
	case enrollErr := <-errCh:
		return "", "", enrollErr
	case <-time.After(m.beginTimeout):
		return "", "", errors.New("cloudenroll: timed out waiting for the cloud to issue a user code")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// Status returns a snapshot of the enrollment state machine.
func (m *Manager) Status() ManagerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := ManagerStatus{
		State:           m.state,
		UserCode:        m.userCode,
		VerificationURI: m.uri,
		Error:           m.errMsg,
	}
	if m.ident != nil {
		s.ULID = m.ident.ULID
	}
	return s
}
