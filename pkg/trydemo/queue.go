package trydemo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// session tracks one connected client.
type session struct {
	token       string
	ip          string
	role        Role
	joinedAt    time.Time
	lastSeen    time.Time // for heartbeat expiry
	lastInput   time.Time // for idle-kill (driver only)
	driverStart time.Time // when this session became the driver
}

// inMemQueue is the concrete in-memory Queue implementation.
type inMemQueue struct {
	mu sync.Mutex

	cfg       Config
	store     Store
	costGuard CostGuard

	// sessions maps token → session.
	sessions map[string]*session

	// driver token ("" if none).
	driverToken string

	// spectators: ordered list of tokens (order doesn't matter for spectators,
	// but we need a stable set for count).
	spectators []string

	// waitQueue: ordered list of tokens waiting to become driver.
	waitQueue []string
}

// newQueue creates a new in-memory Queue.
func newQueue(cfg Config, s Store, cg CostGuard) Queue {
	return &inMemQueue{
		cfg:       cfg,
		store:     s,
		costGuard: cg,
		sessions:  make(map[string]*session),
	}
}

// maxSpectators returns the maximum number of spectator slots.
func (q *inMemQueue) maxSpectators() int {
	return q.cfg.MaxViewers - 1 // 1 reserved for driver
}

func (q *inMemQueue) Join(ctx context.Context, ip string) (string, Role, int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Enforce per-IP concurrent session limit.
	concurrent := 0
	for _, s := range q.sessions {
		if s.ip == ip {
			concurrent++
		}
	}
	if concurrent >= q.cfg.PerIPConcurrent {
		return "", RoleNone, 0, ErrPerIPLimitReached
	}

	token := uuid.New().String()
	now := time.Now()
	sess := &session{
		token:    token,
		ip:       ip,
		joinedAt: now,
		lastSeen: now,
	}

	// Decide the role.
	if q.driverToken == "" {
		// Check cooldown before promoting to driver.
		lastClaim, err := q.store.LastDriverClaim(ctx, ip)
		if err != nil {
			return "", RoleNone, 0, fmt.Errorf("queue: join: check cooldown: %w", err)
		}
		cooldown := time.Duration(q.cfg.PerIPDriverCooldownSecs) * time.Second
		if !lastClaim.IsZero() && time.Since(lastClaim) < cooldown {
			// Cooldown active — drop into spectator or queue.
			sess.role = q.assignNonDriverRole(token, sess)
			q.sessions[token] = sess
			pos := q.positionOf(token)
			return token, sess.role, pos, nil
		}

		// Check cost cap.
		engaged, err := q.costGuard.Engaged(ctx)
		if err != nil {
			return "", RoleNone, 0, fmt.Errorf("queue: join: cost guard: %w", err)
		}
		if !engaged {
			// Promote directly to driver.
			sess.role = RoleDriver
			sess.driverStart = now
			sess.lastInput = now
			q.driverToken = token
			q.sessions[token] = sess
			if err := q.store.RecordDriverClaim(ctx, ip, now); err != nil {
				// Non-fatal: log but continue.
				_ = q.store.LogEvent(ctx, "cap_engaged", ip, token, "record_claim_failed", now)
			}
			_ = q.store.LogEvent(ctx, "driver_claim", ip, token, "join_direct", now)
			return token, RoleDriver, 0, nil
		}
	}

	// Assign spectator or queue slot.
	sess.role = q.assignNonDriverRole(token, sess)
	q.sessions[token] = sess
	pos := q.positionOf(token)
	return token, sess.role, pos, nil
}

// assignNonDriverRole assigns RoleSpectator or RoleQueued and updates the
// internal lists. Called with q.mu held.
func (q *inMemQueue) assignNonDriverRole(token string, sess *session) Role {
	if len(q.spectators) < q.maxSpectators() {
		sess.role = RoleSpectator
		q.spectators = append(q.spectators, token)
		return RoleSpectator
	}
	sess.role = RoleQueued
	q.waitQueue = append(q.waitQueue, token)
	return RoleQueued
}

// positionOf returns the 1-based queue position for a queued token, or 0.
// Called with q.mu held.
func (q *inMemQueue) positionOf(token string) int {
	for i, t := range q.waitQueue {
		if t == token {
			return i + 1
		}
	}
	return 0
}

func (q *inMemQueue) Heartbeat(_ context.Context, token string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	sess, ok := q.sessions[token]
	if !ok {
		return ErrUnknownToken
	}
	sess.lastSeen = time.Now()
	return nil
}

func (q *inMemQueue) RecordInput(_ context.Context, token string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	sess, ok := q.sessions[token]
	if !ok {
		return ErrUnknownToken
	}
	if sess.role != RoleDriver {
		return ErrNotDriver
	}
	sess.lastInput = time.Now()
	return nil
}

func (q *inMemQueue) Leave(ctx context.Context, token string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.removeLocked(ctx, token, "leave")
}

// removeLocked removes a session and cleans up lists. Called with q.mu held.
func (q *inMemQueue) removeLocked(ctx context.Context, token, reason string) error {
	sess, ok := q.sessions[token]
	if !ok {
		return nil // idempotent
	}
	delete(q.sessions, token)

	switch sess.role {
	case RoleDriver:
		q.driverToken = ""
		_ = q.store.LogEvent(ctx, "driver_release", sess.ip, token, reason, time.Now())
	case RoleSpectator:
		q.spectators = removeString(q.spectators, token)
	case RoleQueued:
		q.waitQueue = removeString(q.waitQueue, token)
	}
	return nil
}

func (q *inMemQueue) Promote(ctx context.Context) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.driverToken != "" {
		return "", nil // seat occupied
	}
	if len(q.waitQueue) == 0 {
		// Also check spectators — if a spectator is at the front, promote them.
		// (Spectators watch, then take the wheel when driver leaves.)
		// Actually per the plan, waitQueue is where you wait to drive.
		// Spectators join the waitQueue explicitly or are auto-promoted.
		// Here: no one in wait queue, no-op.
		return "", nil
	}

	// Check cost cap.
	engaged, err := q.costGuard.Engaged(ctx)
	if err != nil {
		return "", fmt.Errorf("queue: promote: cost guard: %w", err)
	}
	if engaged {
		_ = q.store.LogEvent(ctx, "cap_engaged", "", "", "promote_blocked", time.Now())
		return "", nil
	}

	// Pop head of queue.
	head := q.waitQueue[0]
	q.waitQueue = q.waitQueue[1:]

	sess, ok := q.sessions[head]
	if !ok {
		// Session already gone; try again next tick (caller loops).
		return "", nil
	}

	// Remove from spectators if present (shouldn't be, but defensive).
	q.spectators = removeString(q.spectators, head)

	now := time.Now()
	sess.role = RoleDriver
	sess.driverStart = now
	sess.lastInput = now
	sess.lastSeen = now
	q.driverToken = head

	if err := q.store.RecordDriverClaim(ctx, sess.ip, now); err != nil {
		_ = q.store.LogEvent(ctx, "cap_engaged", sess.ip, head, "record_claim_failed", now)
	}
	_ = q.store.LogEvent(ctx, "driver_claim", sess.ip, head, "promoted_from_queue", now)
	return head, nil
}

func (q *inMemQueue) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	snap := Snapshot{
		DriverActive:   q.driverToken != "",
		SpectatorCount: len(q.spectators),
		QueueLength:    len(q.waitQueue),
	}
	if q.driverToken != "" {
		if sess, ok := q.sessions[q.driverToken]; ok {
			elapsed := int(time.Since(sess.driverStart).Seconds())
			remaining := q.cfg.DriverSessionSecs - elapsed
			if remaining < 0 {
				remaining = 0
			}
			snap.DriverSecsLeft = remaining
		}
	}
	return snap
}

func (q *inMemQueue) RoleOf(token string) Role {
	q.mu.Lock()
	defer q.mu.Unlock()
	sess, ok := q.sessions[token]
	if !ok {
		return RoleNone
	}
	return sess.role
}

func (q *inMemQueue) SecsLeft() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.driverToken == "" {
		return 0
	}
	sess, ok := q.sessions[q.driverToken]
	if !ok {
		return 0
	}
	elapsed := int(time.Since(sess.driverStart).Seconds())
	remaining := q.cfg.DriverSessionSecs - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// driverExpired returns true if the driver's session has exceeded the cap.
// Called with q.mu held.
func (q *inMemQueue) driverExpired() (string, bool) {
	if q.driverToken == "" {
		return "", false
	}
	sess, ok := q.sessions[q.driverToken]
	if !ok {
		return "", false
	}
	return q.driverToken, time.Since(sess.driverStart) >= time.Duration(q.cfg.DriverSessionSecs)*time.Second
}

// driverIdle returns true if the driver has been idle past the idle-kill window.
// Called with q.mu held.
func (q *inMemQueue) driverIdle() (string, bool) {
	if q.driverToken == "" {
		return "", false
	}
	sess, ok := q.sessions[q.driverToken]
	if !ok {
		return "", false
	}
	return q.driverToken, time.Since(sess.lastInput) >= time.Duration(q.cfg.DriverIdleSecs)*time.Second
}

// viewerCount returns total active sessions (driver + spectators + queued watchers).
// Called with q.mu held.
func (q *inMemQueue) viewerCount() int {
	return len(q.sessions)
}

// removeString removes the first occurrence of s from the slice.
func removeString(ss []string, s string) []string {
	for i, v := range ss {
		if v == s {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
}

// Sentinel errors returned by Queue methods.
var (
	ErrPerIPLimitReached = errors.New("trydemo: per-IP concurrent session limit reached")
	ErrUnknownToken      = errors.New("trydemo: unknown session token")
	ErrNotDriver         = errors.New("trydemo: token is not the driver")
)
