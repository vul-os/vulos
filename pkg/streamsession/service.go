package streamsession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// MaxSignalBytes is the hard cap (in bytes) on the payload of one SDP/ICE
// signal POST. This is the PRIMARY enforcement of the "media never transits
// the CP" invariant: any payload above this size is rejected at the handler
// edge and is NEVER forwarded to the relay. SDP offers/answers and ICE
// candidates are kilobytes; a media packet (or worse, a stream of them)
// would not fit.
//
// 64 KiB is generous for a single VULOS-STREAM/1 signaling frame (offer +
// candidates after SDP munging rarely exceeds 16 KiB) while staying well
// below any RTP/RTCP burst a misbehaving client could try to smuggle.
const MaxSignalBytes = 64 * 1024

// ErrSignalTooLarge is returned by Signal when a forwarded payload exceeds
// MaxSignalBytes. Maps to HTTP 413 in the handler.
var ErrSignalTooLarge = errors.New("streamsession: signal payload exceeds MaxSignalBytes (media bytes not permitted)")

// ErrUpstreamRelay is returned when the relay HTTPS POST returns non-2xx or
// the request itself fails (connect/transport). Maps to HTTP 502 in the
// handler.
var ErrUpstreamRelay = errors.New("streamsession: relay signaling upstream error")

// GPUAttacher is the cross-package seam used to allocate / release a
// cloud-GPU host. gpusku.Service satisfies this shape via Attach / Detach;
// a fake satisfies it in tests. Kept narrow so streamsession does NOT
// import gpusku/ directly (avoids any future cycle through the billing
// adapters and keeps the unit tests dep-free).
type GPUAttacher interface {
	// Attach the gpuType to ulid for accountID. Returning err aborts session
	// allocation.
	Attach(ctx context.Context, accountID, ulid, gpuType string) error
	// Detach the GPU from ulid for accountID. Errors are LOGGED by callers
	// but do NOT fail the teardown — the session row is the source of truth
	// for what was active; a missed detach is reconciled by the gpusku meter
	// no longer seeing the attachment after the next tick.
	Detach(ctx context.Context, accountID, ulid string) error
}

// NoopGPUAttacher is the default GPUAttacher — useful for dev wiring with no
// cloud GPU and for BYO-only paths. Both methods are no-ops.
type NoopGPUAttacher struct{}

func (NoopGPUAttacher) Attach(_ context.Context, _, _, _ string) error { return nil }
func (NoopGPUAttacher) Detach(_ context.Context, _, _ string) error    { return nil }

// RelaySignaler is the cross-package seam used to forward a signaling frame
// to the relay's VULOS-STREAM/1 ingress. The default implementation
// (httpRelaySignaler) posts to {endpoint}/stream/signal as HTTPS. Tests inject
// a fake to assert the cloud is a thin pass-through (payload bytes are
// forwarded VERBATIM and never parsed).
type RelaySignaler interface {
	ForwardSignal(ctx context.Context, endpoint string, sess Session, payload []byte) error
	// NotifyTeardown tells the relay the session is over so it can drop any
	// open TURN slot. Best-effort; errors are logged at the caller.
	NotifyTeardown(ctx context.Context, endpoint string, sess Session) error
}

// HTTPRelaySignaler is the production RelaySignaler. It POSTs the opaque
// payload bytes to {endpoint}/stream/signal with the session/host/account
// metadata in headers — never in the body, never parsed by us.
type HTTPRelaySignaler struct {
	// Client is the HTTPS client used for relay calls. A reasonable timeout
	// is required — signaling is on the user-perceived latency path.
	Client *http.Client
}

// NewHTTPRelaySignaler returns a signaler with a 5s timeout client.
func NewHTTPRelaySignaler() *HTTPRelaySignaler {
	return &HTTPRelaySignaler{Client: &http.Client{Timeout: 5 * time.Second}}
}

// ForwardSignal implements RelaySignaler.
func (h *HTTPRelaySignaler) ForwardSignal(ctx context.Context, endpoint string, sess Session, payload []byte) error {
	if endpoint == "" {
		return fmt.Errorf("%w: empty relay endpoint", ErrUpstreamRelay)
	}
	url := endpoint + "/stream/signal"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrUpstreamRelay, err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Vulos-Stream-Session", sess.ID)
	req.Header.Set("X-Vulos-Stream-Host", sess.HostULID)
	req.Header.Set("X-Vulos-Stream-Account", sess.AccountID)
	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: do: %v", ErrUpstreamRelay, err)
	}
	defer resp.Body.Close()
	// Drain so connections can be reused; we don't read the response body
	// into the client either — the relay's ack is fire-and-forget at this
	// layer (the inverse leg arrives as a separate signal POST from the
	// peer through normal signaling).
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d", ErrUpstreamRelay, resp.StatusCode)
	}
	return nil
}

// NotifyTeardown implements RelaySignaler.
func (h *HTTPRelaySignaler) NotifyTeardown(ctx context.Context, endpoint string, sess Session) error {
	if endpoint == "" {
		return nil
	}
	url := endpoint + "/stream/teardown"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("%w: build teardown: %v", ErrUpstreamRelay, err)
	}
	req.Header.Set("X-Vulos-Stream-Session", sess.ID)
	req.Header.Set("X-Vulos-Stream-Host", sess.HostULID)
	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: do teardown: %v", ErrUpstreamRelay, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: teardown status %d", ErrUpstreamRelay, resp.StatusCode)
	}
	return nil
}

// Service orchestrates session lifecycle + pass-through signaling.
type Service struct {
	store         *Store
	gpus          GPUAttacher
	relay         RelaySignaler
	relayEndpoint string
}

// NewService constructs a Service. Any of gpus / relay may be nil — sensible
// defaults are substituted (no-op GPU attacher, real HTTP signaler).
// relayEndpoint is the HTTPS base URL of the configured relay (e.g.
// https://relay.vulos.org); the signaler appends /stream/signal.
func NewService(store *Store, gpus GPUAttacher, relay RelaySignaler, relayEndpoint string) *Service {
	if gpus == nil {
		gpus = NoopGPUAttacher{}
	}
	if relay == nil {
		relay = NewHTTPRelaySignaler()
	}
	return &Service{
		store:         store,
		gpus:          gpus,
		relay:         relay,
		relayEndpoint: relayEndpoint,
	}
}

// StartParams is the input to StartSession. Exactly one of GPUType or
// BYOGPUBoxID must be set: GPUType → allocate a cloud GPU instance and
// attach it (charges the per-GPU-hour meter); BYOGPUBoxID → use a registered
// BYO GPU box (no cloud charge).
type StartParams struct {
	AccountID   string
	HostULID    string // the managed-instance ulid that will host the streaming server
	GPUType     string // cloud-GPU catalogue key ('a10' | 'l40s' | 'a100-40gb' | 'a100-80gb'); empty for BYO
	BYOGPUBoxID string // BYO GPU box ulid; empty for cloud-GPU

	// Gaming marks this as a GPU cloud-gaming session (GAME-SESSION-01). Gaming
	// sessions are subject to the one-active-per-account policy and the per-tier
	// concurrent GPU-session cap (GamingSessionCap below). Billing is UNCHANGED —
	// a gaming session bills exactly like any other cloud_gpu session via the
	// gpusku per-GPU-hour meter on the host_ulid attachment (no double-bill).
	Gaming bool
	// GamingSessionCap is the caller's per-tier concurrent gaming-session cap
	// (from gpusku.SessionCapForTier — resolved at the route so the service does
	// not import gpusku). Only consulted when Gaming is true. A value <= 0 means
	// "not entitled" and the start is rejected (ErrGamingNotEntitled); the route
	// passes the tier cap so free-tier (cap 0) cannot start a gaming session at
	// all, and paid tiers are bounded to their concurrent cap.
	GamingSessionCap int
}

// ErrGamingNotEntitled is returned when a gaming StartSession is requested by an
// account whose tier cap is 0 (e.g. free tier). Maps to HTTP 402 at the route.
var ErrGamingNotEntitled = errors.New("streamsession: GPU gaming not available on this tier")

// ErrGamingSessionLimit is returned when the account already has GamingSessionCap
// active gaming sessions. Maps to HTTP 409 at the route.
var ErrGamingSessionLimit = errors.New("streamsession: concurrent gaming session limit reached")

// StartSession allocates the host (GPU attach for cloud, mark-as-host for
// BYO), records a Session row in 'allocating' → 'active', and returns the
// Session with the relay endpoint baked in (so the client knows where its
// peer will be reachable for signaling).
//
// Allocation order:
//  1. Generate session id.
//  2. Insert row in 'allocating'.
//  3. Attach GPU (cloud only).
//  4. Promote to 'active'. On any step failure, mark 'failed' and return err.
func (s *Service) StartSession(ctx context.Context, p StartParams) (Session, error) {
	if p.AccountID == "" {
		return Session{}, errors.New("streamsession: account_id required")
	}
	if (p.GPUType == "" && p.BYOGPUBoxID == "") || (p.GPUType != "" && p.BYOGPUBoxID != "") {
		return Session{}, errors.New("streamsession: exactly one of gpu_type or byo_gpu_box_id required")
	}

	// GAME-SESSION-01: gaming entitlement + concurrency gate. Enforced BEFORE any
	// allocation so we never attach (and start billing) a GPU for a session the
	// account is not entitled to run. A tier cap of 0 (free tier) → not entitled.
	// Otherwise the account's non-terminal gaming sessions must be below the cap.
	// The count is state-guarded (only allocating/active count) so a torn-down
	// session frees the slot.
	if p.Gaming {
		if p.GamingSessionCap <= 0 {
			return Session{}, ErrGamingNotEntitled
		}
		active, err := s.store.CountActiveGamingByAccount(ctx, p.AccountID)
		if err != nil {
			return Session{}, err
		}
		if active >= p.GamingSessionCap {
			return Session{}, ErrGamingSessionLimit
		}
	}

	var (
		kind    HostKind
		hostUL  string
		gpuType string
	)
	if p.GPUType != "" {
		kind = HostKindCloudGPU
		gpuType = p.GPUType
		hostUL = p.HostULID
		if hostUL == "" {
			return Session{}, errors.New("streamsession: host_ulid required for cloud_gpu allocation")
		}
	} else {
		kind = HostKindBYOGPU
		hostUL = p.BYOGPUBoxID
	}

	id, err := NewID()
	if err != nil {
		return Session{}, err
	}
	sess := Session{
		ID:            id,
		AccountID:     p.AccountID,
		HostKind:      kind,
		HostULID:      hostUL,
		GPUType:       gpuType,
		RelayEndpoint: s.relayEndpoint,
		Gaming:        p.Gaming,
		State:         StateAllocating,
	}
	sess, err = s.store.Insert(ctx, sess)
	if err != nil {
		return Session{}, err
	}

	if kind == HostKindCloudGPU {
		if err := s.gpus.Attach(ctx, p.AccountID, hostUL, gpuType); err != nil {
			// Mark failed but leave row for audit; surface the error to the
			// caller. We do NOT call Detach because Attach never succeeded.
			_ = s.store.SetState(ctx, sess.ID, StateFailed)
			return Session{}, fmt.Errorf("streamsession: gpu attach: %w", err)
		}
	}

	if err := s.store.SetState(ctx, sess.ID, StateActive); err != nil {
		// Best-effort rollback of the GPU attach so we don't leave a charging
		// host with no session row.
		if kind == HostKindCloudGPU {
			_ = s.gpus.Detach(ctx, p.AccountID, hostUL)
		}
		return Session{}, err
	}
	sess.State = StateActive
	return sess, nil
}

// Signal forwards an opaque SDP/ICE payload to the relay for this session.
// The payload is forwarded VERBATIM — the cloud never parses it. This is the
// MEDIA-NEVER-TRANSITS-CP enforcement point:
//   - payload size is capped at MaxSignalBytes (ErrSignalTooLarge above).
//   - the cloud reads at most MaxSignalBytes into memory (the handler uses
//     io.LimitReader at the HTTP edge).
//   - the cloud's only outbound is an HTTPS POST to {RelayEndpoint}/stream/signal.
//
// Returns ErrSessionNotFound, ErrSignalTooLarge, or ErrUpstreamRelay.
func (s *Service) Signal(ctx context.Context, sessionID, callerAccount string, payload []byte) error {
	if len(payload) > MaxSignalBytes {
		return ErrSignalTooLarge
	}
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.AccountID != callerAccount {
		// Pretend it does not exist to the caller (no leak of foreign session ids).
		return ErrSessionNotFound
	}
	if sess.State != StateActive {
		return fmt.Errorf("streamsession: session %s not active (state=%s)", sessionID, sess.State)
	}
	return s.relay.ForwardSignal(ctx, sess.RelayEndpoint, sess, payload)
}

// Teardown closes the session: detach the GPU (if cloud_gpu — stops the
// per-GPU-hour meter), notify the relay to drop the session, and mark the
// row torn_down. The relay notify is best-effort.
//
// Returns ErrSessionNotFound when the id is unknown or the caller does not
// own it.
func (s *Service) Teardown(ctx context.Context, sessionID, callerAccount string) (Session, error) {
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if sess.AccountID != callerAccount {
		return Session{}, ErrSessionNotFound
	}
	// Idempotency: a second teardown is a no-op (still returns the session).
	if sess.State == StateTornDown {
		return sess, nil
	}

	if sess.HostKind == HostKindCloudGPU {
		if err := s.gpus.Detach(ctx, sess.AccountID, sess.HostULID); err != nil {
			// WAVE34-5: do NOT silently discard. A failed detach means the GPU may
			// still be attached and METERING after teardown — a stuck attachment is
			// real money. Surface it loudly so the gpusku reconciler / an operator
			// can catch a detach that the next meter tick does not resolve. We still
			// proceed to mark the row torn down (the row is the source of truth), but
			// the failure is now visible instead of erased.
			log.Printf("[streamsession] WARNING (billing-leak-risk): GPU Detach failed for session=%s account=%s host=%s — GPU may still be attached and metering; reconcile: %v",
				sess.ID, sess.AccountID, sess.HostULID, err)
		}
	}

	// Best-effort relay notify. Errors are not fatal.
	_ = s.relay.NotifyTeardown(ctx, sess.RelayEndpoint, sess)

	if err := s.store.SetState(ctx, sess.ID, StateTornDown); err != nil {
		return Session{}, err
	}
	sess.State = StateTornDown
	now := time.Now().UTC()
	sess.ClosedAt = &now
	return sess, nil
}

// Get exposes the underlying Store.Get for handler reuse + ownership checks.
func (s *Service) Get(ctx context.Context, sessionID, callerAccount string) (Session, error) {
	sess, err := s.store.Get(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if sess.AccountID != callerAccount {
		return Session{}, ErrSessionNotFound
	}
	return sess, nil
}

// List exposes ListByAccount for handler reuse.
func (s *Service) List(ctx context.Context, accountID string, limit int) ([]Session, error) {
	return s.store.ListByAccount(ctx, accountID, limit)
}
