package abuse

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// AuditRecorder is the narrow seam the detector uses to hash-chain
// suspend/reinstate events into the existing tamper-evident audit log. The
// signature matches auditlog.Logger.Record so the real logger plugs in
// directly; a no-op default exists for unit tests.
type AuditRecorder interface {
	Record(ctx context.Context, actor, action, target string, metadata map[string]string) error
}

// noopAudit is the zero-value default; Record is a no-op.
type noopAudit struct{}

func (noopAudit) Record(context.Context, string, string, string, map[string]string) error {
	return nil
}

// Sink is invoked for every emitted signal (post-threshold). It is called
// synchronously from the detector goroutine that produced the signal — keep
// implementations cheap.
type Sink func(context.Context, Signal)

// Detector holds the in-memory sliding-window counters and the suspend policy.
// All methods are safe for concurrent use.
type Detector struct {
	cfg        Config
	store      *Store
	classifier ContentClassifier
	csam       CSAMHashMatcher
	audit      AuditRecorder
	sink       Sink
	clock      func() time.Time

	mu       sync.Mutex
	signupIP map[string]*window
	sendAcct map[string]*window
	sendIP   map[string]*window
	loginIP  map[string]*window
}

// NewDetector constructs a Detector. store is the only required dependency;
// classifier / csam / audit / sink may all be nil and will be defaulted to
// safe no-ops.
func NewDetector(cfg Config, store *Store, classifier ContentClassifier, csam CSAMHashMatcher, audit AuditRecorder, sink Sink) *Detector {
	if classifier == nil {
		classifier = NoopClassifier{}
	}
	if csam == nil {
		csam = NoopCSAMMatcher{}
	}
	if audit == nil {
		audit = noopAudit{}
	}
	if sink == nil {
		sink = func(context.Context, Signal) {}
	}
	return &Detector{
		cfg:        cfg.withDefaults(),
		store:      store,
		classifier: classifier,
		csam:       csam,
		audit:      audit,
		sink:       sink,
		clock:      time.Now,
		signupIP:   map[string]*window{},
		sendAcct:   map[string]*window{},
		sendIP:     map[string]*window{},
		loginIP:    map[string]*window{},
	}
}

// Config returns the effective configuration (defaults applied).
func (d *Detector) Config() Config { return d.cfg }

// ─── per-event observation entrypoints ───────────────────────────────────────

// ObserveSignup records a signup attempt from ip and optional accountID and
// returns any Signal that crossed the warn or strong threshold (nil otherwise).
func (d *Detector) ObserveSignup(ctx context.Context, accountID, ip string) *Signal {
	n := d.tick(d.signupIP, ip)
	score := ratio(n, d.cfg.SignupBurstPerIP)
	return d.maybeEmit(ctx, KindSignup, accountID, ip, score,
		fmt.Sprintf("signups=%d/%s on ip", n, d.cfg.Window))
}

// ObserveSend records an outbound send by accountID from ip. recipients is the
// fan-out count for this message — passing >0 also runs the fan-out detector.
// Returns the highest-scoring Signal that crossed a threshold (nil if none).
func (d *Detector) ObserveSend(ctx context.Context, accountID, ip string, recipients int) *Signal {
	var best *Signal

	// burst per account
	if accountID != "" {
		n := d.tick(d.sendAcct, accountID)
		score := ratio(n, d.cfg.SendBurstPerAccount)
		if sig := d.maybeEmit(ctx, KindSend, accountID, ip, score,
			fmt.Sprintf("sends=%d/%s on account", n, d.cfg.Window)); sig != nil {
			best = sig
		}
	}
	// burst per ip
	if ip != "" {
		n := d.tick(d.sendIP, ip)
		score := ratio(n, d.cfg.SendBurstPerIP)
		if sig := d.maybeEmit(ctx, KindSend, accountID, ip, score,
			fmt.Sprintf("sends=%d/%s on ip", n, d.cfg.Window)); sig != nil {
			if best == nil || sig.Score > best.Score {
				best = sig
			}
		}
	}
	// fan-out
	if recipients > 0 {
		score := fanoutScore(recipients, d.cfg)
		if sig := d.maybeEmit(ctx, KindFanout, accountID, ip, score,
			fmt.Sprintf("recipients=%d on single send", recipients)); sig != nil {
			if best == nil || sig.Score > best.Score {
				best = sig
			}
		}
	}
	return best
}

// ObserveLogin records a login attempt from ip (success or failure both count
// — credential stuffing tells on volume, not outcome).
func (d *Detector) ObserveLogin(ctx context.Context, accountID, ip string) *Signal {
	if ip == "" {
		return nil
	}
	n := d.tick(d.loginIP, ip)
	score := ratio(n, d.cfg.LoginBurstPerIP)
	return d.maybeEmit(ctx, KindLogin, accountID, ip, score,
		fmt.Sprintf("logins=%d/%s on ip", n, d.cfg.Window))
}

// ObserveContent runs the body through the ContentClassifier and emits a
// Signal if the score crosses a threshold. The classifier's score is taken as-
// is (clamped to [0,1+]).
func (d *Detector) ObserveContent(ctx context.Context, accountID, ip string, body []byte) (*Signal, error) {
	score, err := d.classifier.Classify(ctx, body)
	if err != nil {
		return nil, err
	}
	return d.maybeEmit(ctx, KindContent, accountID, ip, score, "content-classifier"), nil
}

// ObserveCSAM runs hash through the CSAMHashMatcher. A match is ALWAYS a
// Strong signal regardless of cfg thresholds and triggers Suspend; the report
// is queued for the human/legal team — this package never reports to
// authorities (see /docs/RISKS-HUMAN-TEAM.md §2).
func (d *Detector) ObserveCSAM(ctx context.Context, accountID, ip string, hash []byte) (*Signal, error) {
	matched, err := d.csam.Match(ctx, hash)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, nil
	}
	sig := Signal{
		Kind:      KindCSAM,
		AccountID: accountID,
		IP:        ip,
		Score:     d.cfg.StrongThreshold, // always strong
		Reason:    "csam-hash-match",
		At:        d.clock().UTC(),
	}
	d.handle(ctx, sig)
	// Also queue an abuse_reports row so the human team sees it on the queue.
	// WAVE34-5: a CSAM report is a MANDATORY-REPORTING obligation. A swallowed
	// FileReport error means a hash-matched item never reaches the human+legal
	// queue — a compliance gap that must NOT fail open. On failure we (a) write a
	// durable entry into the tamper-evident audit log as a fallback record, and
	// (b) return the error to the caller so it can retry / alert. The Signal is
	// still returned (the suspend already ran via d.handle) alongside the error.
	if d.store != nil && accountID != "" {
		if _, err := d.store.FileReport(ctx, "system:csam-detector", accountID, CategoryCSAM,
			"automated CSAM hash-match — human + legal review required",
			map[string]string{"ip": ip}); err != nil {
			// Durable fallback: the audit log is hash-chained and separate from the
			// abuse_reports table, so the mandatory-report obligation is still recorded
			// even when the primary queue write failed.
			_ = d.audit.Record(ctx, "system:csam-detector", "abuse.csam_report_failed", accountID, map[string]string{
				"ip":    ip,
				"error": err.Error(),
			})
			return &sig, fmt.Errorf("abuse: CSAM FileReport failed (mandatory-reporting gap) for account=%s: %w", accountID, err)
		}
	}
	return &sig, nil
}

// ─── internals ───────────────────────────────────────────────────────────────

// maybeEmit constructs a Signal at (kind, accountID, ip, score, reason) and,
// if score crosses warn or strong, returns it after running side effects
// (audit log, suspend on strong, sink callback, signal-log persistence).
func (d *Detector) maybeEmit(ctx context.Context, kind SignalKind, accountID, ip string, score float64, reason string) *Signal {
	if score < d.cfg.WarnThreshold {
		return nil
	}
	sig := Signal{
		Kind:      kind,
		AccountID: accountID,
		IP:        ip,
		Score:     score,
		Reason:    reason,
		At:        d.clock().UTC(),
	}
	d.handle(ctx, sig)
	return &sig
}

// handle runs the side effects for an above-threshold Signal. It is split from
// maybeEmit so CSAM (which bypasses the warn-gate) can reuse it.
func (d *Detector) handle(ctx context.Context, sig Signal) {
	// Persist forensic copy (best-effort).
	if d.store != nil {
		_ = d.store.LogSignal(ctx, sig)
	}
	// Strong signal → auto-suspend.  Suspend reports changed=false when the
	// account is already actively suspended; we only emit an audit entry on
	// the state transition so repeated strong signals don't churn the chain.
	if sig.IsStrong(d.cfg) && sig.AccountID != "" && d.store != nil {
		if changed, err := d.store.Suspend(ctx, sig.AccountID, fmt.Sprintf("%s:%s", sig.Kind, sig.Reason), sig.Score, true); err == nil && changed {
			_ = d.audit.Record(ctx, "system:abuse-detector", "abuse.auto_suspend", sig.AccountID, map[string]string{
				"kind":   string(sig.Kind),
				"ip":     sig.IP,
				"score":  fmt.Sprintf("%.3f", sig.Score),
				"reason": sig.Reason,
			})
		}
	}
	d.sink(ctx, sig)
}

// ReinstateByHuman is a convenience that wraps store.Reinstate with an audit
// entry — call sites in the route layer use this so the audit chain stays
// consistent.
func (d *Detector) ReinstateByHuman(ctx context.Context, accountID, actor string) error {
	if err := d.store.Reinstate(ctx, accountID, actor); err != nil {
		return err
	}
	_ = d.audit.Record(ctx, "human:"+actor, "abuse.reinstate", accountID, nil)
	return nil
}

// ManualSuspend wraps store.Suspend (auto=false) with an audit entry.
// If the account is already actively suspended the call is a no-op and no
// duplicate audit entry is written.
func (d *Detector) ManualSuspend(ctx context.Context, accountID, reason, actor string) error {
	changed, err := d.store.Suspend(ctx, accountID, reason, d.cfg.StrongThreshold, false)
	if err != nil {
		return err
	}
	if changed {
		_ = d.audit.Record(ctx, "human:"+actor, "abuse.manual_suspend", accountID, map[string]string{
			"reason": reason,
		})
	}
	return nil
}

// ─── sliding window ──────────────────────────────────────────────────────────

// window is a fixed-resolution sliding-window counter. We use a ring of
// timestamps and prune anything older than cfg.Window on every tick. For our
// thresholds (tens-to-hundreds per minute) this is fine; a real production
// build at higher rates would switch to a hashed-bucket approximator.
type window struct {
	stamps []time.Time
}

func (d *Detector) tick(m map[string]*window, key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clock()
	w := m[key]
	if w == nil {
		w = &window{}
		m[key] = w
	}
	// Prune.
	cutoff := now.Add(-d.cfg.Window)
	i := 0
	for i < len(w.stamps) && w.stamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		w.stamps = w.stamps[i:]
	}
	w.stamps = append(w.stamps, now)
	return len(w.stamps)
}

// ratio returns count/threshold clamped to [0, 1.5] so a 50% overshoot stays
// representable while runaway counts don't blow up downstream math.
func ratio(count, threshold int) float64 {
	if threshold <= 0 {
		return 0
	}
	r := float64(count) / float64(threshold)
	if r > 1.5 {
		r = 1.5
	}
	return r
}

// fanoutScore maps a recipient count to a score using the warn/strong bands.
func fanoutScore(recipients int, cfg Config) float64 {
	switch {
	case recipients >= cfg.FanoutRecipientsStrong:
		return cfg.StrongThreshold
	case recipients >= cfg.FanoutRecipientsWarn:
		// linear ramp from WarnThreshold at warn-count to just-below-strong at strong-count
		span := cfg.FanoutRecipientsStrong - cfg.FanoutRecipientsWarn
		if span <= 0 {
			return cfg.WarnThreshold
		}
		frac := float64(recipients-cfg.FanoutRecipientsWarn) / float64(span)
		return cfg.WarnThreshold + frac*(cfg.StrongThreshold-cfg.WarnThreshold)
	default:
		return 0
	}
}
