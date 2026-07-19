// MINST-06: Multi-instance notifications — fan-out + dedup.
//
// NotifyFanout fans an OS notification out to all online account instances via
// the vulos-relay S2S messaging channel, ensuring the user sees it on every
// device.  Dedup is enforced by a seen_notifications SQLite table (7-day TTL)
// so each instance delivers the notification to its local UI at most once.
//
// Priority mapping:
//   - P0 / P1: fan out immediately (synchronous relay POST per instance)
//   - P2 / P3: buffered into a 30-second batch window; the batch is
//     deduplicated before the relay POST so redundant sends are skipped.
//
// Relay endpoint: POST {VULOS_RELAY_BASE_URL}/api/s2s/notify
// Override VULOS_RELAY_BASE_URL for dev / self-hosted deployments.
// Default: https://relay.vulos.org
//
// NOTE (seam P2-4): the base-URL env var is VULOS_RELAY_BASE_URL — the SAME
// var the rest of the OS reachability layer (peering federation/resolve, relay
// client) reads. It was previously VULOS_RELAY_URL here, which nothing else
// set, so the fanout always fell back to the default host.
//
// Cross-instance delivery contract (completed): the fanout POSTs a
// relayEnvelope {target_ulid, target, sender, notification} with an
// `Authorization: Bearer <VULOS_RELAY_TOKEN>` header (the box's own relay
// token — the SAME credential the tunnel agent uses, so the relay can
// authenticate the sender). `sender` is this box's own tunnel name
// (VULOS_RELAY_NAME); `target` is the destination instance's tunnel name
// (derived from its registry EndpointURL host). The relay authorizes the
// bearer against `sender`, resolves `target` via its tunnel registry, verifies
// the target belongs to the SAME account (cross-tenant fail-closed), and
// forwards the BARE Notification to the target box's /api/notify/receive over
// the existing tunnel. When the box has no tunnel identity (VULOS_RELAY_NAME /
// VULOS_RELAY_TOKEN unset) or the target has no derivable tunnel name, the send
// is skipped — best-effort, non-fatal (the OS already delivered locally).
//
// All relay calls degrade gracefully: if the relay is unreachable the fanout
// logs a warning and continues — the notification is NOT retried (the OS
// already delivered it locally; cross-instance delivery is best-effort).
//
// Wiring: create a NotifyFanout with NewNotifyFanout, start the batch worker
// with go fanout.RunBatcher(ctx), and call fanout.Notify(ctx, n) for every
// OS notification.
//
// HTTP endpoints are exposed via RegisterNotifyHandlers(mux, fanout) — do NOT
// import from main.go; wire from a routes_*.go file in cmd/server.
package multiinstance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultRelayBaseURL is the production vulos-relay base URL.
const defaultRelayBaseURL = "https://relay.vulos.org"

// relayBaseURL returns the effective relay base URL (env override or default).
// Unified on VULOS_RELAY_BASE_URL (seam P2-4) so it matches the rest of the OS
// reachability layer instead of the orphaned VULOS_RELAY_URL.
func relayBaseURL() string {
	if v := os.Getenv("VULOS_RELAY_BASE_URL"); v != "" {
		return v
	}
	return defaultRelayBaseURL
}

// seenTTL is how long a notification_id is remembered in the dedup table.
const seenTTL = 7 * 24 * time.Hour

// batchWindow is the accumulation period for P2/P3 notifications.
const batchWindow = 30 * time.Second

// fanoutConcurrency caps the number of in-flight relay POSTs during a fan-out.
// A bounded worker pool means one slow/stalled peer cannot serialise the whole
// batch behind it (audit P1-6): with N online instances × M notifications the
// previous nested loop blocked on each POST in turn, so a single peer at the
// httpClient timeout (10s) stalled every other delivery. 16 is a sane default
// for the expected handful-to-dozens of instances; it can be tuned later.
const fanoutConcurrency = 16

// Notification is the cross-instance notification payload.
// Priority 0/1 are fanned out immediately; 2/3 are batched.
type Notification struct {
	// ID is the unique notification ID used for dedup (required).
	ID string `json:"id"`
	// Title is the human-readable notification title.
	Title string `json:"title"`
	// Body is the notification body text.
	Body string `json:"body"`
	// Priority is 0 (highest) – 3 (lowest), matching the OS notification levels.
	Priority int `json:"priority"`
	// CreatedAt is when the notification was originally created on this instance.
	CreatedAt time.Time `json:"created_at"`
}

// NotifyFanout fans out OS notifications to all online instances.
// It is safe for concurrent use.
type NotifyFanout struct {
	reg        *Registry
	httpClient *http.Client

	// batch collects P2/P3 notifications between flush cycles.
	mu    sync.Mutex
	batch []Notification
}

// NewNotifyFanout creates a NotifyFanout backed by reg.
func NewNotifyFanout(reg *Registry) *NotifyFanout {
	return &NotifyFanout{
		reg: reg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Notify fans out n to all online account instances.
// P0/P1 notifications are sent immediately; P2/P3 are buffered.
//
// Notify is idempotent per notification ID: if the same ID has already been
// sent (or received back from the relay), a second Notify call is a no-op.
// This prevents duplicate fan-out when the caller retries or when the same
// notification is triggered on multiple code paths.
func (nf *NotifyFanout) Notify(ctx context.Context, n Notification) error {
	if n.ID == "" {
		return fmt.Errorf("notify: notification ID must not be empty")
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}

	// Dedup check: if already seen, skip fan-out silently.
	alreadySeen, err := nf.isSeen(n.ID)
	if err != nil {
		log.Printf("[notifyfanout] isSeen check %s: %v", n.ID, err)
	}
	if alreadySeen {
		return nil
	}

	// Mark as seen now so a concurrent or subsequent Notify call for the same
	// ID is skipped, and so that the notification is not re-delivered locally
	// if it bounces back from the relay.
	if err := nf.markSeen(n.ID); err != nil {
		log.Printf("[notifyfanout] markSeen %s: %v", n.ID, err)
	}

	if n.Priority <= 1 {
		// Immediate fan-out.
		return nf.fanOutNow(ctx, []Notification{n})
	}

	// Buffer for batch send.
	nf.mu.Lock()
	nf.batch = append(nf.batch, n)
	nf.mu.Unlock()
	return nil
}

// purgeInterval is how often RunBatcher sweeps expired seen_notifications rows.
// The TTL itself is 7 days (see markSeen); sweeping once a day is plenty.
const purgeInterval = 24 * time.Hour

// RunBatcher starts the P2/P3 batch worker using the default batchWindow tick.
// It blocks until ctx is cancelled. Call this in a dedicated goroutine.
//
// It also periodically calls PurgeExpiredSeen so the seen_notifications dedup
// table doesn't grow unboundedly — that method existed but nothing ever
// invoked it (deadcode-flagged as unreachable) until this wiring.
func (nf *NotifyFanout) RunBatcher(ctx context.Context) {
	nf.runBatcherWithTick(ctx, batchWindow, purgeInterval)
}

// runBatcherWithTick is the internal implementation that accepts configurable
// tick durations so tests can use shorter intervals without changing the
// constants.
func (nf *NotifyFanout) runBatcherWithTick(ctx context.Context, tick, purgeTick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	purgeTicker := time.NewTicker(purgeTick)
	defer purgeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Flush any remaining notifications before exit.
			nf.flushBatch(context.Background())
			return
		case <-ticker.C:
			nf.flushBatch(ctx)
		case <-purgeTicker.C:
			if err := nf.PurgeExpiredSeen(); err != nil {
				log.Printf("[notifyfanout] PurgeExpiredSeen: %v", err)
			}
		}
	}
}

// FlushBatch immediately flushes accumulated P2/P3 notifications.
// Intended for use in tests and graceful shutdown; not needed in normal operation.
func (nf *NotifyFanout) FlushBatch(ctx context.Context) {
	nf.flushBatch(ctx)
}

// flushBatch deduplicates and fans out the accumulated P2/P3 batch.
func (nf *NotifyFanout) flushBatch(ctx context.Context) {
	nf.mu.Lock()
	if len(nf.batch) == 0 {
		nf.mu.Unlock()
		return
	}
	pending := nf.batch
	nf.batch = nil
	nf.mu.Unlock()

	// Dedup within the batch by ID (last-write wins — preserves order).
	seen := make(map[string]struct{}, len(pending))
	deduped := pending[:0]
	for _, n := range pending {
		if _, ok := seen[n.ID]; ok {
			continue
		}
		seen[n.ID] = struct{}{}
		deduped = append(deduped, n)
	}

	if err := nf.fanOutNow(ctx, deduped); err != nil {
		log.Printf("[notifyfanout] batch flush: %v", err)
	}
}

// fanOutNow sends notifications to all currently-online instances.
// It does NOT filter by seen_notifications — that dedup gate is for inbound
// notifications only (ReceiveNotification).  The local markSeen in Notify just
// prevents re-delivery if the same ID bounces back from the relay.
func (nf *NotifyFanout) fanOutNow(ctx context.Context, notifications []Notification) error {
	if len(notifications) == 0 {
		return nil
	}

	// Collect online instances.
	instances, err := nf.reg.List()
	if err != nil {
		return fmt.Errorf("fanout: list instances: %w", err)
	}

	// Build the (instance, notification) job list up front so we know the work
	// size and can size the pool against it.
	type job struct {
		inst Instance
		n    Notification
	}
	var jobs []job
	for _, inst := range instances {
		if inst.Status != StatusOnline || inst.EndpointURL == "" {
			continue
		}
		for _, n := range notifications {
			jobs = append(jobs, job{inst: inst, n: n})
		}
	}
	if len(jobs) == 0 {
		return nil
	}

	// Bounded-concurrency fan-out (audit P1-6): a semaphore caps in-flight relay
	// POSTs so one slow peer can't stall the batch, while still bounding total
	// goroutines. We collect the first error under a mutex; per-POST failures are
	// best-effort and logged.
	concurrency := fanoutConcurrency
	if len(jobs) < concurrency {
		concurrency = len(jobs)
	}
	sem := make(chan struct{}, concurrency)
	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		lastErr error
	)
	for _, j := range jobs {
		// Respect ctx cancellation: stop scheduling new work promptly.
		select {
		case <-ctx.Done():
			errMu.Lock()
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			errMu.Unlock()
			wg.Wait()
			return lastErr
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if relayErr := nf.sendToRelay(ctx, j.inst, j.n); relayErr != nil {
				log.Printf("[notifyfanout] relay to %s: %v", j.inst.ULID, relayErr)
				errMu.Lock()
				lastErr = relayErr
				errMu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	return lastErr
}

// relayEnvelope is the wrapper POSTed to the relay's /api/s2s/notify route. It
// carries the routing metadata the relay needs to authenticate the sender and
// forward the BARE Notification to the target box over its existing tunnel.
//
// The relay uses Sender + the Authorization bearer to authenticate (the token
// must be authorized for the Sender name — the box's own tunnel), and Target to
// look up the destination tunnel session (registry.lookup). It then forwards
// ONLY the embedded Notification (a bare object) to the target's
// /api/notify/receive — which is why RegisterNotifyHandlers decodes a bare
// Notification, not this wrapper. TargetULID is informational (logging/dedup
// correlation); Target is the routable identifier.
type relayEnvelope struct {
	// TargetULID is the target instance's registry ULID (informational).
	TargetULID string `json:"target_ulid"`
	// Target is the target instance's relay tunnel NAME — the routable identifier
	// the relay resolves via registry.lookup to reach the box over its tunnel.
	Target string `json:"target"`
	// Sender is THIS box's own relay tunnel name; the relay authorizes the bearer
	// token against it (the same grant that authorizes this box's tunnel).
	Sender string `json:"sender"`
	// Notification is the bare payload the relay forwards verbatim to the target's
	// /api/notify/receive.
	Notification Notification `json:"notification"`
}

// tunnelNameForInstance derives the target instance's relay tunnel NAME from its
// registry EndpointURL. The relay routes by the leftmost DNS label of the tunnel
// host (<name>.<relay-domain>), so the tunnel name is that leftmost label of the
// instance's public endpoint host. Returns "" when no routable name can be
// derived (empty/relative endpoint, or a bare host with no subdomain label) — in
// which case the relay send is skipped (the relay could not route it anyway).
func tunnelNameForInstance(inst Instance) string {
	raw := strings.TrimSpace(inst.EndpointURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	// The tunnel name is the leftmost subdomain label. A host with no dot (a bare
	// hostname) or a leading empty label is not a routable tunnel target.
	label, _, ok := strings.Cut(host, ".")
	if !ok || label == "" {
		return ""
	}
	return strings.ToLower(label)
}

// sendToRelay POSTs a single notification to the vulos-relay S2S endpoint for
// the target instance.  Failures are logged but do not panic.
//
// The relay both authenticates the sender (via the box's own VULOS_RELAY_TOKEN
// bearer, authorized against the box's own VULOS_RELAY_NAME) and routes to the
// target box over its existing tunnel (via the derived target tunnel name). When
// either the sender's own tunnel identity or the target's routable name is
// missing, the relay could neither authenticate nor route the send, so it is
// skipped (best-effort, non-fatal — local delivery already happened).
func (nf *NotifyFanout) sendToRelay(ctx context.Context, target Instance, n Notification) error {
	targetName := tunnelNameForInstance(target)
	if targetName == "" {
		// No routable tunnel name for this target — the relay cannot forward it.
		// Best-effort: skip silently (local delivery already occurred).
		return nil
	}

	// The box's own relay identity: the SAME name + token the tunnel agent uses.
	// Without them the relay cannot authenticate this sender, so skip.
	senderName := strings.TrimSpace(os.Getenv("VULOS_RELAY_NAME"))
	relayToken := strings.TrimSpace(os.Getenv("VULOS_RELAY_TOKEN"))
	if senderName == "" || relayToken == "" {
		return nil
	}

	env := relayEnvelope{
		TargetULID:   target.ULID,
		Target:       targetName,
		Sender:       senderName,
		Notification: n,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	endpoint := relayBaseURL() + "/api/s2s/notify"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+relayToken)

	resp, err := nf.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: status %d", endpoint, resp.StatusCode)
	}
	return nil
}

// ReceiveNotification processes an inbound notification that arrived from
// another instance via the relay.  If the ID has already been seen, the
// notification is silently dropped (dedup).  Otherwise it is marked seen and
// returned so the caller can deliver it to the local OS notification system.
//
// Returns (n, true) when the notification should be delivered locally;
// (Notification{}, false) when it should be dropped.
func (nf *NotifyFanout) ReceiveNotification(n Notification) (Notification, bool) {
	if n.ID == "" {
		return Notification{}, false
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}

	alreadySeen, err := nf.isSeen(n.ID)
	if err != nil {
		log.Printf("[notifyfanout] ReceiveNotification isSeen %s: %v", n.ID, err)
	}
	if alreadySeen {
		return Notification{}, false
	}

	if mErr := nf.markSeen(n.ID); mErr != nil {
		log.Printf("[notifyfanout] ReceiveNotification markSeen %s: %v", n.ID, mErr)
	}
	return n, true
}

// PurgeExpiredSeen removes seen_notifications rows whose TTL has elapsed.
// Safe to call periodically (e.g. daily).
func (nf *NotifyFanout) PurgeExpiredSeen() error {
	_, err := nf.reg.db.Exec(
		`DELETE FROM seen_notifications WHERE expires_at < ?`,
		formatTime(time.Now().UTC()),
	)
	return err
}

// markSeen inserts notification_id into seen_notifications with a 7-day TTL.
func (nf *NotifyFanout) markSeen(id string) error {
	now := time.Now().UTC()
	_, err := nf.reg.db.Exec(`
		INSERT INTO seen_notifications (notification_id, seen_at, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(notification_id) DO NOTHING`,
		id,
		formatTime(now),
		formatTime(now.Add(seenTTL)),
	)
	return err
}

// isSeen returns true if notification_id is present (and not expired) in
// seen_notifications.
func (nf *NotifyFanout) isSeen(id string) (bool, error) {
	var count int
	err := nf.reg.db.QueryRow(`
		SELECT COUNT(*) FROM seen_notifications
		WHERE notification_id = ? AND expires_at > ?`,
		id, formatTime(time.Now().UTC()),
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RegisterNotifyHandlers wires the notification fan-out endpoints into mux.
//
// Routes registered:
//
//	POST /api/notify/fanout   — fan out a notification to all online instances
//	POST /api/notify/receive  — inbound notification from relay (dedup + local deliver)
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterNotifyHandlers(mux, fanout)
func RegisterNotifyHandlers(mux *http.ServeMux, nf *NotifyFanout) {
	// POST /api/notify/fanout
	mux.HandleFunc("POST /api/notify/fanout", func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		if n.ID == "" {
			http.Error(w, `{"error":"notification id required"}`, http.StatusUnprocessableEntity)
			return
		}
		if err := nf.Notify(r.Context(), n); err != nil {
			log.Printf("[notifyfanout] handler fanout: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
	})

	// POST /api/notify/receive
	mux.HandleFunc("POST /api/notify/receive", func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		delivered, ok := nf.ReceiveNotification(n)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			// Duplicate — silently drop.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "duplicate"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "delivered",
			"notification": delivered,
		})
	})
}
