package relayscale

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// controller.go — the periodic CONTROL LOOP that closes the relay-autoscaling
// feedback loop. Where policy.go decides desired state and actuator.go is a
// stateless one-shot converge, the Controller is the STATEFUL periodic driver:
// it reads the freshest observed demand (the same DemandStore the /observe
// endpoint fills), computes desired counts per region, and converges the fleet
// through the injected RelayProvisioner — with the safety a real control loop
// needs:
//
//   - ACTUATION GATING. Only a self-actuating provisioner (kubernetes /
//     firecracker / proxmox / a commercial managed multi-provider) is driven.
//     For manual / external / unset the Controller is ADVISORY: it computes and
//     surfaces desired counts but never calls the seam (Actuated=false), exactly
//     like the demand API's published state.
//   - GRACEFUL DRAIN. Scale-down never yanks a live node. A victim is CORDONed
//     (best-effort, via the optional Cordoner seam so the router stops sending it
//     new connections), marked draining, and only Destroyed once its drain window
//     has elapsed on a later tick — connections bleed off first.
//   - ANTI-FLAP. Per-region, per-direction cooldowns: scale-up is eager (short
//     cooldown), scale-down is conservative (a cooldown at least as long as the
//     drain window). The policy's Step already bounds how far one pass moves.
//   - FAIL-CLOSED. Any provider error (List / Provision / Destroy) HOLDS: the
//     Controller logs, records the error for the status surface, and makes no
//     further changes this pass rather than thrashing.
//   - IDEMPOTENT. Re-running Tick with unchanged inputs performs no work: a node
//     already draining is not re-cordoned, a region within cooldown is skipped.
//
// It is provider-agnostic — it drives only the RelayProvisioner (+ optional
// Cordoner) seam and names no substrate, so the OSS controller works for every
// injected provisioner including the commercial one from vulos-cloud.

// Controller loop defaults.
const (
	// DefaultInterval is the reconcile cadence.
	DefaultInterval = 30 * time.Second
	// DefaultScaleUpCooldown is the minimum gap between scale-UP actions in a
	// region. Short — scale-up is eager (load is already high).
	DefaultScaleUpCooldown = 30 * time.Second
	// DefaultScaleDownCooldown is the minimum gap between scale-DOWN actions in a
	// region. Long — scale-down is conservative and must clear the drain window so
	// a region is never repeatedly bled.
	DefaultScaleDownCooldown = 5 * time.Minute
	// DefaultDrainTimeout is how long a cordoned node drains before it is
	// destroyed.
	DefaultDrainTimeout = 2 * time.Minute
)

// Cordoner is an OPTIONAL RelayProvisioner extension. A provisioner that can
// CORDON an instance — tell the router/geo-DNS to stop steering NEW connections
// to it, so its established connections bleed off — implements it, and the
// Controller calls Cordon before starting a node's drain window. Provisioners
// that cannot cordon simply do not implement it; the Controller then drains by
// time alone. Kept separate from RelayProvisioner so adding it breaks no existing
// implementation (mirrors the popLister upgrade pattern).
type Cordoner interface {
	Cordon(ctx context.Context, inst Instance) error
}

// ControllerConfig tunes the loop. Zero fields take the package defaults.
type ControllerConfig struct {
	Interval          time.Duration
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration
	DrainTimeout      time.Duration
}

func (c *ControllerConfig) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.ScaleUpCooldown <= 0 {
		c.ScaleUpCooldown = DefaultScaleUpCooldown
	}
	if c.ScaleDownCooldown <= 0 {
		c.ScaleDownCooldown = DefaultScaleDownCooldown
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = DefaultDrainTimeout
	}
	// Keep the invariant that a region is never re-bled while a drain is still in
	// flight: the scale-down cooldown must be at least the drain window.
	if c.ScaleDownCooldown < c.DrainTimeout {
		c.ScaleDownCooldown = c.DrainTimeout
	}
}

// Controller-visible action labels (RegionStatus.LastAction).
const (
	actAdvisory = "advisory" // provisioner does not actuate; desired is published only
	actScaleUp  = "scale_up" // provisioned node(s) this pass
	actDrain    = "drain"    // began draining victim(s) toward destroy
	actDestroy  = "destroy"  // destroyed a fully-drained node
	actCooldown = "cooldown" // wanted to act but the region is within its cooldown
	actHold     = "hold"     // desired == current (or nothing safe to do)
)

// RegionStatus is one region's control-loop state, surfaced to the superadmin
// read endpoint.
type RegionStatus struct {
	Region       string     `json:"region"`
	Current      int        `json:"current"`
	Desired      int        `json:"desired"`
	Draining     int        `json:"draining"`
	LastAction   string     `json:"last_action"`
	LastActionAt *time.Time `json:"last_action_at,omitempty"`
	Reason       string     `json:"reason"`
}

// ControllerStatus is the whole control-loop snapshot (GET .../controller).
type ControllerStatus struct {
	GeneratedAt time.Time `json:"generated_at"`
	Provisioner string    `json:"provisioner"`
	// Actuated reports whether the Controller drives the seam. False in
	// manual/external mode — the desired counts are advisory.
	Actuated  bool           `json:"actuated"`
	Regions   []RegionStatus `json:"regions"`
	LastError string         `json:"last_error,omitempty"`
}

// regionState is the Controller's private per-region bookkeeping.
type regionState struct {
	current      int
	desired      int
	reason       string
	lastAction   string
	lastActionAt time.Time // zero => no action yet (cooldown never blocks the first)
}

// drainRec tracks one node draining toward destroy.
type drainRec struct {
	inst      Instance
	destroyAt time.Time
}

// Controller is the periodic relay-scaling control loop. Construct with
// NewController and Run it in a goroutine. Tick is safe to call serially; do not
// call it concurrently with itself (Run guarantees serial ticks).
type Controller struct {
	prov   RelayProvisioner
	policy Policy
	spec   SpecFunc
	store  *DemandStore
	cfg    ControllerConfig
	log    *slog.Logger
	now    func() time.Time // test clock hook

	mu        sync.Mutex
	status    map[string]*regionState
	draining  map[string]drainRec // instance ID -> drain record
	lastError string
	lastPass  time.Time
}

// NewController builds the control loop. A nil provisioner defaults to
// ManualProvisioner (advisory); a nil store gets a fresh one; a nil logger uses
// slog.Default(). Config zero-fields take defaults.
func NewController(prov RelayProvisioner, policy Policy, spec SpecFunc, store *DemandStore, cfg ControllerConfig, log *slog.Logger) *Controller {
	if prov == nil {
		prov = NewManualProvisioner()
	}
	if store == nil {
		store = NewDemandStore(0, nil)
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.withDefaults()
	return &Controller{
		prov:     prov,
		policy:   policy,
		spec:     spec,
		store:    store,
		cfg:      cfg,
		log:      log,
		now:      time.Now,
		status:   map[string]*regionState{},
		draining: map[string]drainRec{},
	}
}

// Run drives Tick on the configured interval until ctx is cancelled. One
// immediate tick runs first so status is populated promptly.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	c.log.Info("relayscale/controller: started",
		"provisioner", c.prov.Name(), "actuated", c.prov.Enabled(),
		"interval", c.cfg.Interval, "drain", c.cfg.DrainTimeout)
	c.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			c.log.Info("relayscale/controller: stopping", "provisioner", c.prov.Name())
			return
		case <-t.C:
			c.Tick(ctx)
		}
	}
}

// Tick runs one reconcile pass: read demand, compute desired, and (when the
// provisioner actuates) converge the fleet with drain + cooldown. It never
// returns an error — provider failures are logged, recorded on the status
// surface, and HELD (no thrash).
func (c *Controller) Tick(ctx context.Context) {
	now := c.now()
	loads := c.store.Snapshot()
	plans := c.policy.Plan(loads)

	c.mu.Lock()
	c.lastPass = now
	c.mu.Unlock()

	// Advisory mode: compute + surface desired, actuate nothing.
	if !c.prov.Enabled() {
		c.mu.Lock()
		for _, p := range plans {
			st := c.stateFor(p.Region)
			st.current, st.desired, st.reason, st.lastAction = p.Current, p.Desired, p.Reason, actAdvisory
		}
		c.lastError = ""
		c.mu.Unlock()
		return
	}

	// Actuated. Snapshot the fleet; a List error HOLDS the whole pass (no thrash).
	existing, err := c.prov.List(ctx)
	if err != nil {
		c.mu.Lock()
		c.lastError = err.Error()
		c.mu.Unlock()
		c.log.Warn("relayscale/controller: list failed; holding", "provisioner", c.prov.Name(), "err", err)
		return
	}
	c.mu.Lock()
	c.lastError = ""
	c.mu.Unlock()

	byRegion := map[string][]Instance{}
	for _, in := range existing {
		byRegion[in.Region] = append(byRegion[in.Region], in)
	}

	// 1) Converge each region (eager up, conservative drained down), cooldown-gated.
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return
		}
		c.reconcileRegion(ctx, p, byRegion[p.Region], now)
	}

	// 2) Finish drains whose window elapsed → Destroy. Run AFTER reconcile so the
	// terminal "destroy" lifecycle label is the freshest for a region (a reconcile
	// that could only report "cooldown"/"hold" for the same region does not mask
	// it). A drain STARTED this pass is never due yet (destroyAt = now + window),
	// so this cannot short-circuit the drain.
	c.reapDrains(ctx, now)
}

// reapDrains destroys nodes whose drain window has elapsed. A Destroy error keeps
// the node in the draining set for a later retry (fail-closed: never lose track).
func (c *Controller) reapDrains(ctx context.Context, now time.Time) {
	c.mu.Lock()
	var due []drainRec
	for _, dr := range c.draining {
		if !now.Before(dr.destroyAt) {
			due = append(due, dr)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].inst.ID < due[j].inst.ID })

	for _, dr := range due {
		if ctx.Err() != nil {
			return
		}
		if err := c.prov.Destroy(ctx, dr.inst); err != nil {
			c.mu.Lock()
			c.lastError = err.Error()
			c.mu.Unlock()
			c.log.Warn("relayscale/controller: destroy of drained node failed; will retry",
				"region", dr.inst.Region, "id", dr.inst.ID, "err", err)
			continue // keep draining; retry next tick
		}
		c.mu.Lock()
		delete(c.draining, dr.inst.ID)
		st := c.stateFor(dr.inst.Region)
		st.lastAction, st.lastActionAt = actDestroy, now
		c.mu.Unlock()
		c.log.Info("relayscale/controller: destroyed drained relay", "region", dr.inst.Region, "id", dr.inst.ID)
	}
}

// reconcileRegion applies one region's plan: provision on positive delta (eager),
// begin draining victims on negative delta (conservative), both cooldown-gated.
func (c *Controller) reconcileRegion(ctx context.Context, p RegionPlan, existing []Instance, now time.Time) {
	c.mu.Lock()
	st := c.stateFor(p.Region)
	st.current, st.desired, st.reason = p.Current, p.Desired, p.Reason
	lastAt := st.lastActionAt
	c.mu.Unlock()

	delta := p.Delta()
	switch {
	case delta > 0:
		if !lastAt.IsZero() && now.Sub(lastAt) < c.cfg.ScaleUpCooldown {
			c.setAction(p.Region, actCooldown, time.Time{})
			return
		}
		c.scaleUp(ctx, p, delta, now)
	case delta < 0:
		if !lastAt.IsZero() && now.Sub(lastAt) < c.cfg.ScaleDownCooldown {
			c.setAction(p.Region, actCooldown, time.Time{})
			return
		}
		c.scaleDown(ctx, p, existing, -delta, now)
	default:
		c.setAction(p.Region, actHold, time.Time{})
	}
}

// scaleUp provisions up to n nodes. A provider error holds the region (breaks the
// loop, no thrash); a manual/external signal that slips through Enabled() is
// treated as advisory.
func (c *Controller) scaleUp(ctx context.Context, p RegionPlan, n int, now time.Time) {
	provisioned := 0
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		spec := RelaySpec{}
		if c.spec != nil {
			spec = c.spec(p.Region)
		}
		inst, err := c.prov.Provision(ctx, p.Region, spec)
		if err != nil {
			if errors.Is(err, ErrManualMode) || errors.Is(err, ErrExternalMode) {
				c.setAction(p.Region, actAdvisory, time.Time{})
				return
			}
			c.mu.Lock()
			c.lastError = err.Error()
			c.mu.Unlock()
			c.log.Warn("relayscale/controller: provision failed; holding region",
				"region", p.Region, "err", err)
			break // hold — do not thrash the remaining requested nodes
		}
		provisioned++
		c.log.Info("relayscale/controller: provisioned relay", "region", p.Region, "id", inst.ID)
	}
	if provisioned > 0 {
		c.setAction(p.Region, actScaleUp, now)
	}
}

// scaleDown begins draining n victims: cordon (best-effort) then mark draining so
// reapDrains destroys them once the window elapses. Already-draining nodes are
// excluded so re-ticking is idempotent.
func (c *Controller) scaleDown(ctx context.Context, p RegionPlan, existing []Instance, n int, now time.Time) {
	c.mu.Lock()
	candidates := make([]Instance, 0, len(existing))
	for _, in := range existing {
		if _, draining := c.draining[in.ID]; !draining {
			candidates = append(candidates, in)
		}
	}
	c.mu.Unlock()

	victims := pickVictims(candidates, n)
	if len(victims) == 0 {
		c.setAction(p.Region, actHold, time.Time{})
		return
	}
	started := 0
	for _, v := range victims {
		if ctx.Err() != nil {
			break
		}
		// Best-effort cordon so the router stops steering NEW connections here;
		// established connections then bleed off during the drain window. A cordon
		// failure degrades to a time-only drain (still safe — Destroy drains too).
		if cd, ok := c.prov.(Cordoner); ok {
			if err := cd.Cordon(ctx, v); err != nil {
				c.log.Warn("relayscale/controller: cordon failed; draining by time only",
					"region", p.Region, "id", v.ID, "err", err)
			}
		}
		c.mu.Lock()
		c.draining[v.ID] = drainRec{inst: v, destroyAt: now.Add(c.cfg.DrainTimeout)}
		c.mu.Unlock()
		started++
		c.log.Info("relayscale/controller: draining relay before destroy",
			"region", p.Region, "id", v.ID, "drain", c.cfg.DrainTimeout)
	}
	if started > 0 {
		c.setAction(p.Region, actDrain, now)
	}
}

// setAction records the region's latest action label; a non-zero at also advances
// the cooldown clock. A zero at (cooldown/hold) leaves lastActionAt untouched so a
// cooldown window keeps counting from the real action.
func (c *Controller) setAction(region, action string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.stateFor(region)
	st.lastAction = action
	if !at.IsZero() {
		st.lastActionAt = at
	}
}

// stateFor returns (creating if needed) the region's bookkeeping. Caller holds mu.
func (c *Controller) stateFor(region string) *regionState {
	st := c.status[region]
	if st == nil {
		st = &regionState{}
		c.status[region] = st
	}
	return st
}

// Status returns the current control-loop snapshot for the superadmin surface.
func (c *Controller) Status() ControllerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	drainByRegion := map[string]int{}
	for _, dr := range c.draining {
		drainByRegion[dr.inst.Region]++
	}
	seen := map[string]bool{}
	regions := make([]RegionStatus, 0, len(c.status)+len(drainByRegion))
	for r, st := range c.status {
		seen[r] = true
		rs := RegionStatus{
			Region:     r,
			Current:    st.current,
			Desired:    st.desired,
			Draining:   drainByRegion[r],
			LastAction: st.lastAction,
			Reason:     st.reason,
		}
		if !st.lastActionAt.IsZero() {
			t := st.lastActionAt
			rs.LastActionAt = &t
		}
		regions = append(regions, rs)
	}
	// Any region with draining nodes but no status row yet (edge case).
	for r, n := range drainByRegion {
		if !seen[r] {
			regions = append(regions, RegionStatus{Region: r, Draining: n})
		}
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].Region < regions[j].Region })

	return ControllerStatus{
		GeneratedAt: c.now().UTC(),
		Provisioner: c.prov.Name(),
		Actuated:    c.prov.Enabled(),
		Regions:     regions,
		LastError:   c.lastError,
	}
}

// ServeStatus handles GET {prefix}/controller — the superadmin read surface.
func (c *Controller) ServeStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, c.Status())
}

// Register mounts the controller's read surface: GET {prefix}/controller.
func (c *Controller) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/controller", c.ServeStatus)
}

// ControllerConfigFromEnv builds a ControllerConfig from RELAY_SCALE_* env,
// applying defaults for any unset value.
func ControllerConfigFromEnv() ControllerConfig {
	return controllerConfigFromEnv(os.Getenv)
}

func controllerConfigFromEnv(getenv func(string) string) ControllerConfig {
	return ControllerConfig{
		Interval:          durOr(getenv("RELAY_SCALE_INTERVAL")),
		ScaleUpCooldown:   durOr(getenv("RELAY_SCALE_UP_COOLDOWN")),
		ScaleDownCooldown: durOr(getenv("RELAY_SCALE_DOWN_COOLDOWN")),
		DrainTimeout:      durOr(getenv("RELAY_SCALE_DRAIN_TIMEOUT")),
	}
}

// durOr parses a Go duration, returning 0 (→ default) on empty/garbage.
func durOr(s string) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil {
		return d
	}
	return 0
}
