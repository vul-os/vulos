// Package cloudstatus aggregates the health of the Vulos CLOUD services we
// operate — and, separately, the per-account status of the things a signed-in
// user owns.
//
// ARCHITECTURE (2026-07): the cloud is NOT a multi-product host. Products run on
// the user's own box. The cloud operates a small, fixed set of services:
//
//   - Control Plane   — this process; health == its Postgres is reachable.
//   - Relay (EU PoP)  — the EU point-of-presence of the reverse-tunnel fabric.
//   - Relay (JHB PoP) — the Johannesburg point-of-presence.
//   - Provisioning    — the box/cell provisioner (in-process with the CP).
//
// (Post box-federated pivot, 2026-07-15: the cloud runs NO application. Mail is
// box-level via lilmail and comms — Talk/Meet — are third-party, so there is no
// central Meet SFU to health-check here.)
//
// Two surfaces consume this package:
//
//	PublicSnapshot  — unauthenticated overall health of the cloud services above.
//	                  NO tenant data, NO secrets, NO internal URLs/IPs. Every
//	                  check is fail-safe: a failing probe yields degraded/down/
//	                  unknown, it never panics or leaks the underlying error.
//	AccountSnapshot — the status of the things ONE account owns (its boxes'
//	                  reachability, its relay usage/health, its provisioned
//	                  services, recent events). The accountID is supplied by the
//	                  caller from the authenticated session — this package never
//	                  reads an identifier from request input, so it cannot be
//	                  driven to another tenant's resources (no IDOR).
//
// All external sources are narrow interfaces so the aggregator is decoupled from
// cpdb/routing/fleet/relayusage and is trivially testable. Every source is
// optional (nil-tolerant): a nil or failing source degrades its own section to
// "unknown"/empty and never takes the snapshot down.
package cloudstatus

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Health states, worst-last. Kept as bare strings to match the JSON contract the
// frontend renders (operational|degraded|down|unknown).
const (
	StatusOperational = "operational"
	StatusDegraded    = "degraded"
	StatusDown        = "down"
	StatusUnknown     = "unknown"
)

// ─── external source interfaces (all optional / nil-tolerant) ────────────────

// DBPinger reports whether the control-plane database is reachable. Satisfied by
// *auth.Store (its Ping method).
type DBPinger interface {
	Ping(ctx context.Context) error
}

// PoP is a minimal relay point-of-presence view (no IP — the public page must
// not leak infrastructure addresses).
type PoP struct {
	ID      string
	Region  string
	Healthy bool
}

// PoPSource lists the relay PoP pool. Satisfied by an adapter over routing.Store.
type PoPSource interface {
	ListPoPs(ctx context.Context) ([]PoP, error)
}

// Device is one enrolled box/device owned by an account.
type Device struct {
	ULID          string
	Name          string
	Health        string
	LastHeartbeat *time.Time
}

// DeviceSource lists the boxes an account owns. Satisfied by an adapter over
// fleet.Store (ListByAccount) — always account-scoped.
type DeviceSource interface {
	Devices(ctx context.Context, accountID string) ([]Device, error)
}

// RelayUsage is an account's current-month relayed traffic.
type RelayUsage struct {
	Bytes    int64
	Sessions int
}

// RelayUsageSource returns an account's relay usage. Satisfied by an adapter over
// relayusage.Store (UsageThisMonth).
type RelayUsageSource interface {
	Usage(ctx context.Context, accountID string) (RelayUsage, error)
}

// Probe performs a fail-safe reachability check of a fixed, config-derived URL.
// It MUST return false on any error and MUST NOT surface the error. Because the
// URL always comes from configuration (never request input), a probe can never
// be turned into an SSRF oracle.
type Probe func(ctx context.Context, url string) bool

// ─── aggregator ──────────────────────────────────────────────────────────────

// Config wires an Aggregator. Every field is optional.
type Config struct {
	DB             DBPinger
	PoPs           PoPSource
	Devices        DeviceSource
	Relay          RelayUsageSource
	Probe          Probe         // nil ⇒ a default HTTP probe is used
	ProvisionURL   string        // "" ⇒ Provisioning tied to CP DB health
	PublicCacheTTL time.Duration // 0 ⇒ 10s
	HeartbeatFresh time.Duration // 0 ⇒ 10m; a box seen within this window is reachable
	Now            func() time.Time
}

// Aggregator produces the public + per-account status snapshots.
type Aggregator struct {
	cfg   Config
	now   func() time.Time
	probe Probe
	ttl   time.Duration
	fresh time.Duration

	mu       sync.Mutex
	cache    *PublicStatus
	cachedAt time.Time
}

// New builds an Aggregator from cfg. Safe to call with a zero Config.
func New(cfg Config) *Aggregator {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	probe := cfg.Probe
	if probe == nil {
		probe = defaultProbe
	}
	ttl := cfg.PublicCacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	fresh := cfg.HeartbeatFresh
	if fresh <= 0 {
		fresh = 10 * time.Minute
	}
	return &Aggregator{cfg: cfg, now: now, probe: probe, ttl: ttl, fresh: fresh}
}

/* ─── public snapshot ────────────────────────────────────────────────────── */

// Component is one row of the public status page. Detail is a SHORT, secret-free
// human summary (e.g. "2/2 PoPs healthy") — never an error string, URL or IP.
type Component struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// PublicStatus is the GET /api/cloud/status body.
type PublicStatus struct {
	GeneratedAt string      `json:"generated_at"`
	Overall     string      `json:"overall"`
	Components  []Component `json:"components"`
}

// PublicSnapshot returns the cloud-service health, served from a short TTL cache
// so a burst of status-page loads does not fan out into repeated DB pings and
// probes. The whole build is wrapped so a panic in any check can never crash the
// caller — it degrades to an all-unknown snapshot instead.
func (a *Aggregator) PublicSnapshot(ctx context.Context) (out PublicStatus) {
	a.mu.Lock()
	if a.cache != nil && a.now().Sub(a.cachedAt) < a.ttl {
		cached := *a.cache
		a.mu.Unlock()
		return cached
	}
	a.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			out = a.unknownPublic()
		}
	}()

	out = a.computePublic(ctx)

	a.mu.Lock()
	snap := out
	a.cache = &snap
	a.cachedAt = a.now()
	a.mu.Unlock()
	return out
}

func (a *Aggregator) computePublic(ctx context.Context) PublicStatus {
	comps := []Component{
		a.controlPlane(ctx),
		a.relayComponent(ctx, "relay_eu", "Relay — EU", euRegions),
		a.relayComponent(ctx, "relay_jhb", "Relay — Johannesburg", jhbRegions),
		a.provisioning(ctx),
	}
	return PublicStatus{
		GeneratedAt: a.now().UTC().Format(time.RFC3339),
		Overall:     overall(comps),
		Components:  comps,
	}
}

func (a *Aggregator) unknownPublic() PublicStatus {
	ids := [][3]string{
		{"control_plane", "Control Plane", ""},
		{"relay_eu", "Relay — EU", ""},
		{"relay_jhb", "Relay — Johannesburg", ""},
		{"provisioning", "Provisioning", ""},
	}
	comps := make([]Component, 0, len(ids))
	for _, v := range ids {
		comps = append(comps, Component{ID: v[0], Name: v[1], Status: StatusUnknown, Detail: "status unavailable"})
	}
	return PublicStatus{GeneratedAt: a.now().UTC().Format(time.RFC3339), Overall: StatusUnknown, Components: comps}
}

func (a *Aggregator) controlPlane(ctx context.Context) Component {
	c := Component{ID: "control_plane", Name: "Control Plane"}
	if a.cfg.DB == nil {
		c.Status, c.Detail = StatusUnknown, "no database probe configured"
		return c
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := a.cfg.DB.Ping(pctx); err != nil {
		c.Status, c.Detail = StatusDown, "database unreachable"
		return c
	}
	c.Status, c.Detail = StatusOperational, "database reachable"
	return c
}

func (a *Aggregator) provisioning(ctx context.Context) Component {
	c := Component{ID: "provisioning", Name: "Provisioning"}
	// If an explicit health URL is configured, probe it. Otherwise provisioning
	// runs in-process with the control plane, so its health tracks the CP DB.
	if url := strings.TrimSpace(a.cfg.ProvisionURL); url != "" {
		if a.probe(ctx, url) {
			c.Status, c.Detail = StatusOperational, "provisioner reachable"
		} else {
			c.Status, c.Detail = StatusDown, "provisioner unreachable"
		}
		return c
	}
	if a.cfg.DB == nil {
		c.Status, c.Detail = StatusUnknown, "no database probe configured"
		return c
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := a.cfg.DB.Ping(pctx); err != nil {
		c.Status, c.Detail = StatusDown, "provisioning database unreachable"
		return c
	}
	c.Status, c.Detail = StatusOperational, "accepting provisioning requests"
	return c
}

// relayComponent classifies the PoPs whose region matches the given aliases.
func (a *Aggregator) relayComponent(ctx context.Context, id, name string, aliases []string) Component {
	c := Component{ID: id, Name: name}
	if a.cfg.PoPs == nil {
		c.Status, c.Detail = StatusUnknown, "no PoP source"
		return c
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pops, err := a.cfg.PoPs.ListPoPs(pctx)
	if err != nil {
		c.Status, c.Detail = StatusUnknown, "PoP status unavailable"
		return c
	}
	total, healthy := 0, 0
	for _, p := range pops {
		if !regionMatches(p.Region, aliases) {
			continue
		}
		total++
		if p.Healthy {
			healthy++
		}
	}
	c.Status, c.Detail = classifyPoPs(healthy, total)
	return c
}

// classifyPoPs turns a healthy/total PoP count into a status + secret-free detail.
func classifyPoPs(healthy, total int) (status, detail string) {
	switch {
	case total == 0:
		return StatusUnknown, "no PoP in this region"
	case healthy == 0:
		return StatusDown, "0/" + itoa(total) + " PoPs healthy"
	case healthy < total:
		return StatusDegraded, itoa(healthy) + "/" + itoa(total) + " PoPs healthy"
	default:
		return StatusOperational, itoa(healthy) + "/" + itoa(total) + " PoPs healthy"
	}
}

/* ─── account snapshot ───────────────────────────────────────────────────── */

// AccountBox is one box the account owns, with a derived reachability flag.
type AccountBox struct {
	ULID      string `json:"ulid"`
	Name      string `json:"name"`
	Reachable bool   `json:"reachable"`
	Health    string `json:"health"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// AccountRelay is the account's relay usage + fabric health.
type AccountRelay struct {
	Bytes    int64  `json:"bytes"`
	Sessions int    `json:"sessions"`
	Period   string `json:"period"`
	Health   string `json:"health"` // ok | degraded | unavailable
}

// AccountService is one provisioned service the account owns.
type AccountService struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// AccountEvent is one recent, real signal about the account's resources.
type AccountEvent struct {
	At   string `json:"at"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// AccountStatus is the GET /api/account/status body. It carries ONLY the caller's
// own resources — no account identifier is echoed and no other tenant's data can
// appear because every source is queried with the session-supplied accountID.
type AccountStatus struct {
	GeneratedAt string           `json:"generated_at"`
	Overall     string           `json:"overall"`
	Boxes       []AccountBox     `json:"boxes"`
	Relay       AccountRelay     `json:"relay"`
	Services    []AccountService `json:"services"`
	Events      []AccountEvent   `json:"events"`
}

// AccountSnapshot returns the status of the resources owned by accountID. The
// accountID MUST be the authenticated caller's own id (supplied by the route from
// the session). A blank accountID yields an empty, safe snapshot. The build is
// panic-guarded so a misbehaving source can never crash the request.
func (a *Aggregator) AccountSnapshot(ctx context.Context, accountID string) (out AccountStatus) {
	out = AccountStatus{
		GeneratedAt: a.now().UTC().Format(time.RFC3339),
		Overall:     StatusOperational,
		Boxes:       []AccountBox{},
		Services:    []AccountService{},
		Events:      []AccountEvent{},
		Relay:       AccountRelay{Period: a.now().UTC().Format("2006-01"), Health: "unavailable"},
	}
	if strings.TrimSpace(accountID) == "" {
		return out
	}
	defer func() {
		if r := recover(); r != nil {
			// Never surface partial/corrupt state on panic — return the safe zero.
			out = AccountStatus{
				GeneratedAt: a.now().UTC().Format(time.RFC3339),
				Overall:     StatusUnknown,
				Boxes:       []AccountBox{}, Services: []AccountService{}, Events: []AccountEvent{},
				Relay: AccountRelay{Period: a.now().UTC().Format("2006-01"), Health: "unavailable"},
			}
		}
	}()

	worst := 0
	rank := map[string]int{StatusOperational: 0, StatusUnknown: 1, StatusDegraded: 2, StatusDown: 3}
	bump := func(s string) {
		if r, ok := rank[s]; ok && r > worst {
			worst = r
		}
	}

	// ── Boxes (account-scoped) ────────────────────────────────────────────────
	if a.cfg.Devices != nil {
		dctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		devices, err := a.cfg.Devices.Devices(dctx, accountID)
		cancel()
		if err == nil {
			now := a.now().UTC()
			for _, d := range devices {
				reachable := boxReachable(d, now, a.fresh)
				box := AccountBox{
					ULID:      d.ULID,
					Name:      d.Name,
					Reachable: reachable,
					Health:    normalizeHealth(d.Health, reachable),
				}
				if d.LastHeartbeat != nil {
					box.LastSeen = d.LastHeartbeat.UTC().Format(time.RFC3339)
				}
				out.Boxes = append(out.Boxes, box)
				out.Services = append(out.Services, AccountService{
					Name:   serviceName(d),
					Status: box.Health,
				})
				if !reachable {
					bump(StatusDegraded)
				}
				// A real, per-box event: last time we heard from it.
				if d.LastHeartbeat != nil {
					out.Events = append(out.Events, AccountEvent{
						At:   d.LastHeartbeat.UTC().Format(time.RFC3339),
						Kind: "heartbeat",
						Text: serviceName(d) + " last checked in",
					})
				}
			}
		}
	}

	// ── Relay usage + fabric health (account-scoped usage; global PoP health) ──
	if a.cfg.Relay != nil {
		rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		if u, err := a.cfg.Relay.Usage(rctx, accountID); err == nil {
			out.Relay.Bytes = u.Bytes
			out.Relay.Sessions = u.Sessions
		}
		cancel()
	}
	out.Relay.Health = a.relayFabricHealth(ctx)
	if out.Relay.Health != "unavailable" {
		out.Services = append(out.Services, AccountService{Name: "Relay tunnel", Status: relayHealthToStatus(out.Relay.Health)})
	}
	if out.Relay.Health == "degraded" {
		bump(StatusDegraded)
	}

	// Newest events first, capped.
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].At > out.Events[j].At })
	if len(out.Events) > 25 {
		out.Events = out.Events[:25]
	}
	out.Overall = []string{StatusOperational, StatusOperational, StatusDegraded, StatusDown}[worst]
	return out
}

// relayFabricHealth summarises the PoP pool for the account view: "ok" when any
// PoP is healthy, "degraded" when PoPs exist but none are healthy, "unavailable"
// when there is no PoP source.
func (a *Aggregator) relayFabricHealth(ctx context.Context) string {
	if a.cfg.PoPs == nil {
		return "unavailable"
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pops, err := a.cfg.PoPs.ListPoPs(pctx)
	if err != nil {
		return "unavailable"
	}
	if len(pops) == 0 {
		return "unavailable"
	}
	for _, p := range pops {
		if p.Healthy {
			return "ok"
		}
	}
	return "degraded"
}

/* ─── helpers ────────────────────────────────────────────────────────────── */

var (
	euRegions  = []string{"eu", "europe", "fra", "ams", "de-", "nl-", "fr-", "eu-", "-eu", "frankfurt", "amsterdam"}
	jhbRegions = []string{"jhb", "joh", "africa", "af-", "-af", "za", "south-africa", "johannesburg", "cpt"}
)

func regionMatches(region string, aliases []string) bool {
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return false
	}
	for _, a := range aliases {
		if r == a || strings.Contains(r, a) {
			return true
		}
	}
	return false
}

// overall folds component statuses into a single headline. "unknown" is treated
// as no-worse-than operational so a not-yet-configured component does not scream
// an outage; a real "down"/"degraded" still surfaces.
func overall(comps []Component) string {
	worst := 0
	rank := map[string]int{StatusOperational: 0, StatusUnknown: 1, StatusDegraded: 2, StatusDown: 3}
	for _, c := range comps {
		if r, ok := rank[c.Status]; ok && r > worst {
			worst = r
		}
	}
	return []string{StatusOperational, StatusOperational, StatusDegraded, StatusDown}[worst]
}

func boxReachable(d Device, now time.Time, fresh time.Duration) bool {
	h := strings.ToLower(strings.TrimSpace(d.Health))
	if h == "down" || h == "offline" || h == "unreachable" || h == "crashed" {
		return false
	}
	if d.LastHeartbeat != nil {
		return now.Sub(d.LastHeartbeat.UTC()) <= fresh
	}
	// No heartbeat yet: only trust an explicit healthy signal.
	return h == "ok" || h == "healthy"
}

func normalizeHealth(h string, reachable bool) string {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "ok", "healthy":
		if reachable {
			return StatusOperational
		}
		return StatusDegraded
	case "down", "offline", "unreachable", "crashed":
		return StatusDown
	case "degraded", "warn", "warning":
		return StatusDegraded
	case "":
		if reachable {
			return StatusOperational
		}
		return StatusUnknown
	default:
		if reachable {
			return StatusOperational
		}
		return StatusUnknown
	}
}

func serviceName(d Device) string {
	if strings.TrimSpace(d.Name) != "" {
		return d.Name
	}
	if strings.TrimSpace(d.ULID) != "" {
		return "Box " + d.ULID
	}
	return "Box"
}

func relayHealthToStatus(h string) string {
	switch h {
	case "ok":
		return StatusOperational
	case "degraded":
		return StatusDegraded
	default:
		return StatusUnknown
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// defaultProbe is the fail-safe HTTP reachability check used when Config.Probe is
// nil. It GETs the (config-supplied) URL with a short timeout and treats any 2xx/
// 3xx as reachable; every error path returns false and is swallowed.
func defaultProbe(ctx context.Context, url string) bool {
	u := strings.TrimSpace(url)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
