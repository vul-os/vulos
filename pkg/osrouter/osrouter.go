// Package osrouter implements the vulos.cloud OS routing model (PART A): it
// resolves a request on the `os.<domain>` plane to a concrete box in the user's
// cluster.
//
// Model
//
//	os.<domain>            — session-routed: resolve the caller's logical OS
//	                         identity (account → org) → the BEST box in that
//	                         org's cluster (healthy / least-loaded / nearest).
//	                         When the account belongs to >1 org, serve an
//	                         org/OS CHOOSER instead.
//	<org>.os.<domain>      — direct org handle: skip the chooser, route to the
//	                         best box in THAT org's cluster.
//	<box-id>.os.<domain>   — direct box handle: route to exactly that box.
//
// v1 is a cluster-of-1 (a single enrolled box per org), but the directory +
// best-box selection are written for N boxes so the model is correct as clusters
// grow — SelectBest ranks an arbitrary set.
//
// The `os` plane is a DIFFERENT ORIGIN from the apex (`vulos.org`), and the
// console session cookie is host-scoped to the apex (see internal/auth.Session).
// So os.<domain> never receives the session cookie directly: the apex resolves
// the decision (where the session is valid) and hands the os plane an
// AUDIENCE-BOUND router token scoped to the chosen org's box (see token.go).
//
// This package is dependency-light: it talks to a narrow Directory interface so
// it is fully unit-testable with an in-memory fake and does not couple to the
// orgadmin / routing / fleet stores (the wiring layer supplies the adapter).
package osrouter

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/idents"
)

// ── Host classification ─────────────────────────────────────────────────────────

// HostKind classifies a request Host relative to the OS plane.
type HostKind int

const (
	// HostNotOS is any host that is not the OS apex or a single-label OS instance.
	HostNotOS HostKind = iota
	// HostOSApex is exactly `os.<domain>` — session-routed (chooser or best box).
	HostOSApex
	// HostOrgHandle is `<org-slug>.os.<domain>` — direct org handle.
	HostOrgHandle
	// HostBoxID is `<box-id>.os.<domain>` — direct box handle (box-id is a ULID).
	HostBoxID
)

// HostClass is the parsed classification of a request Host.
type HostClass struct {
	Kind HostKind
	// Label is the single sub-label for org/box handles ("" for the apex / not-OS).
	// For HostBoxID it is the canonical (uppercase) ULID; for HostOrgHandle it is
	// the lowercased org slug.
	Label string
}

// ClassifyHost parses host relative to osHost (e.g. "os.vulos.org"). A :port is
// stripped and matching is case-insensitive. Only ZERO or ONE sub-label before
// the OS host is accepted (no deeper nesting) — fail closed to HostNotOS.
func ClassifyHost(host, osHost string) HostClass {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	osHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(osHost), "."))
	if host == "" || osHost == "" {
		return HostClass{Kind: HostNotOS}
	}
	if host == osHost {
		return HostClass{Kind: HostOSApex}
	}
	suffix := "." + osHost
	if !strings.HasSuffix(host, suffix) {
		return HostClass{Kind: HostNotOS}
	}
	label := strings.TrimSuffix(host, suffix)
	// Exactly one sub-label — reject nested `a.b.os.<domain>` and empty labels.
	if label == "" || strings.Contains(label, ".") {
		return HostClass{Kind: HostNotOS}
	}
	// A box id is a ULID; try that first (a 26-char ULID would also pass ValidName,
	// so ULID wins the disambiguation). Otherwise it must be a valid org slug.
	if _, err := idents.ParseULID(label); err == nil {
		// Canonical box-id spelling is uppercase (parity with routing.CanonULID).
		return HostClass{Kind: HostBoxID, Label: strings.ToUpper(label)}
	}
	if idents.ValidName(label) {
		return HostClass{Kind: HostOrgHandle, Label: label}
	}
	return HostClass{Kind: HostNotOS}
}

// ── Cluster model + best-box selection ──────────────────────────────────────────

// Box is one enrolled box (a Vulos OS instance) in an org's cluster.
type Box struct {
	// ID is the box identifier; in v1 it is the canonical (uppercase) ULID.
	ID string
	// OrgID is the org whose cluster this box belongs to.
	OrgID string
	// Healthy reports whether the box is currently reachable/serving.
	Healthy bool
	// LastSeen is the last heartbeat time (zero = unknown, trusted via Healthy).
	LastSeen time.Time
	// LoadScore is a lower-is-better load metric (0 = unknown/idle).
	LoadScore float64
	// Lat, Lon are the box's approximate location (for nearest selection).
	Lat, Lon float64
	// Region is the box's home region (informational).
	Region string
}

// healthWindow bounds how stale a heartbeat may be before a box is considered
// unhealthy regardless of its Healthy flag. A zero LastSeen is trusted (a store
// that has not recorded a heartbeat yet relies on the Healthy flag).
const healthWindow = 90 * time.Second

// Fresh reports whether the box is healthy AND its heartbeat (if recorded) is
// within the health window relative to now.
func (b Box) Fresh(now time.Time) bool {
	if !b.Healthy {
		return false
	}
	if b.LastSeen.IsZero() {
		return true
	}
	return now.Sub(b.LastSeen) <= healthWindow
}

// ErrNoHealthyBox is returned when a cluster has no currently-serving box.
var ErrNoHealthyBox = errors.New("osrouter: no healthy box in cluster")

// SelectBest picks the best box for a cluster. Ranking (deterministic):
//  1. only FRESH (healthy + recent heartbeat) boxes are eligible;
//  2. least-loaded first (lower LoadScore);
//  3. then nearest to (clientLat, clientLon) by great-circle-ish distance;
//  4. then stable tiebreak by ID.
//
// A cluster-of-1 trivially returns its single fresh box. Returns ErrNoHealthyBox
// when no box is eligible.
func SelectBest(boxes []Box, clientLat, clientLon float64) (Box, error) {
	return selectBestAt(boxes, clientLat, clientLon, time.Now())
}

func selectBestAt(boxes []Box, clientLat, clientLon float64, now time.Time) (Box, error) {
	eligible := make([]Box, 0, len(boxes))
	for _, b := range boxes {
		if b.Fresh(now) {
			eligible = append(eligible, b)
		}
	}
	if len(eligible) == 0 {
		return Box{}, ErrNoHealthyBox
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		bi, bj := eligible[i], eligible[j]
		if bi.LoadScore != bj.LoadScore {
			return bi.LoadScore < bj.LoadScore
		}
		di := dist(bi.Lat, bi.Lon, clientLat, clientLon)
		dj := dist(bj.Lat, bj.Lon, clientLat, clientLon)
		if di != dj {
			return di < dj
		}
		return bi.ID < bj.ID
	})
	return eligible[0], nil
}

// dist is a cheap monotonic-in-true-distance metric (sufficient for nearest
// selection over small fleets; dependency-free).
func dist(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := lat1 - lat2
	dLon := lon1 - lon2
	return dLat*dLat + dLon*dLon
}

// ── Directory (the org↔box + membership seam) ───────────────────────────────────

// Org is a logical OS identity (an org, or a personal "org of one").
type Org struct {
	ID   string
	Slug string
	Name string
	Role string // caller's role in the org (owner/admin/member), when known
}

// ErrNotFound is returned by the Directory when an org/box handle is unknown.
var ErrNotFound = errors.New("osrouter: not found")

// Directory bridges osrouter to the org membership model + the box cluster
// registry. The wiring layer supplies a concrete adapter over orgadmin + routing
// / fleet; tests use MemDirectory.
type Directory interface {
	// OrgsForAccount lists the orgs the account belongs to (chooser + 0/1/N).
	// Order is stable (the caller's preferred default should sort first, but the
	// Router does not rely on order — it resolves the default explicitly).
	OrgsForAccount(ctx context.Context, accountID string) ([]Org, error)
	// ActiveOrg returns the account's last-selected/default org id, or "" if none.
	ActiveOrg(ctx context.Context, accountID string) (string, error)
	// BoxesForOrg returns the candidate boxes in an org's cluster (v1: 0 or 1).
	BoxesForOrg(ctx context.Context, orgID string) ([]Box, error)
	// BoxByID resolves a direct box handle (canonical ULID) → Box (ErrNotFound).
	BoxByID(ctx context.Context, boxID string) (Box, error)
}

// ── Router decision ─────────────────────────────────────────────────────────────

// DecisionKind is the outcome of resolving an OS-plane request.
type DecisionKind int

const (
	// DecideLogin: no/invalid session — bounce to the apex login with a return-to.
	DecideLogin DecisionKind = iota
	// DecideChooser: the account belongs to >1 org and none is pinned/default —
	// serve the org/OS picker (Orgs is populated).
	DecideChooser
	// DecideRoute: resolved to a single best box — Org, Box and BoxHost are set.
	DecideRoute
	// DecideNoBox: the resolved org has no enrolled/healthy box yet.
	DecideNoBox
	// DecideNotFound: unknown org/box handle.
	DecideNotFound
	// DecideForbidden: the session is not a member of the pinned org / not entitled
	// to the pinned box (cross-org isolation).
	DecideForbidden
)

func (k DecisionKind) String() string {
	switch k {
	case DecideLogin:
		return "login"
	case DecideChooser:
		return "chooser"
	case DecideRoute:
		return "route"
	case DecideNoBox:
		return "no_box"
	case DecideNotFound:
		return "not_found"
	case DecideForbidden:
		return "forbidden"
	default:
		return "unknown"
	}
}

// Decision is the resolved routing outcome.
type Decision struct {
	Kind    DecisionKind
	Orgs    []Org  // populated for DecideChooser
	Org     Org    // resolved org (DecideRoute / DecideNoBox)
	Box     Box    // resolved box (DecideRoute)
	BoxHost string // `<box-id>.os.<domain>` — the router-token audience (DecideRoute)
}

// ResolveInput carries everything the Router needs for one decision.
type ResolveInput struct {
	// AccountID is the authenticated caller ("" ⇒ no valid session ⇒ DecideLogin).
	AccountID string
	// Host is the request Host (classified against OSHost).
	Host string
	// OSHost is the deployment's OS host (env.OSHost()).
	OSHost string
	// PreferOrgID pins a specific org for the apex plane (e.g. from ?org=… or a
	// remembered choice). Ignored when the account is not a member.
	PreferOrgID string
	// ClientLat, ClientLon are the caller's approximate location for nearest
	// best-box selection.
	ClientLat, ClientLon float64
}

// Router resolves OS-plane requests to boxes.
type Router struct {
	dir Directory
	now func() time.Time
}

// NewRouter returns a Router backed by dir.
func NewRouter(dir Directory) *Router {
	return &Router{dir: dir, now: time.Now}
}

// SetNow overrides the clock (tests).
func (r *Router) SetNow(f func() time.Time) { r.now = f }

// Resolve computes the routing Decision for in.
func (r *Router) Resolve(ctx context.Context, in ResolveInput) (Decision, error) {
	class := ClassifyHost(in.Host, in.OSHost)
	if class.Kind == HostNotOS {
		// Not an OS-plane host; the caller should not have dispatched here.
		return Decision{Kind: DecideNotFound}, nil
	}
	// Every OS-plane decision requires an authenticated identity.
	if in.AccountID == "" {
		return Decision{Kind: DecideLogin}, nil
	}

	// The account's org set drives every membership check.
	orgs, err := r.dir.OrgsForAccount(ctx, in.AccountID)
	if err != nil {
		return Decision{}, err
	}
	memberOf := make(map[string]Org, len(orgs))
	for _, o := range orgs {
		memberOf[o.ID] = o
	}

	switch class.Kind {
	case HostBoxID:
		box, err := r.dir.BoxByID(ctx, class.Label)
		if errors.Is(err, ErrNotFound) {
			return Decision{Kind: DecideNotFound}, nil
		}
		if err != nil {
			return Decision{}, err
		}
		org, ok := memberOf[box.OrgID]
		if !ok {
			// Cross-org isolation: not a member of the box's org.
			return Decision{Kind: DecideForbidden}, nil
		}
		if !box.Fresh(r.now()) {
			return Decision{Kind: DecideNoBox, Org: org}, nil
		}
		return r.routeTo(org, box, in.OSHost), nil

	case HostOrgHandle:
		// Resolve the handle within the CALLER'S OWN orgs: a matching slug is
		// inherently a membership proof, and a non-match returns NotFound without
		// disclosing whether some OTHER account's org has that slug (no enumeration).
		for _, o := range orgs {
			if strings.EqualFold(o.Slug, class.Label) {
				return r.resolveOrgToBox(ctx, o, in)
			}
		}
		return Decision{Kind: DecideNotFound}, nil

	default: // HostOSApex — session-routed with chooser
		if len(orgs) == 0 {
			return Decision{Kind: DecideNoBox}, nil
		}
		// Resolve the target org: explicit preference → active/default → the sole
		// org → otherwise a chooser.
		if in.PreferOrgID != "" {
			if m, ok := memberOf[in.PreferOrgID]; ok {
				return r.resolveOrgToBox(ctx, m, in)
			}
			// A preference the caller is not a member of is ignored (fall through).
		}
		if active, _ := r.dir.ActiveOrg(ctx, in.AccountID); active != "" {
			if m, ok := memberOf[active]; ok {
				return r.resolveOrgToBox(ctx, m, in)
			}
		}
		if len(orgs) == 1 {
			return r.resolveOrgToBox(ctx, orgs[0], in)
		}
		return Decision{Kind: DecideChooser, Orgs: orgs}, nil
	}
}

// resolveOrgToBox selects the best box in org's cluster (or DecideNoBox).
func (r *Router) resolveOrgToBox(ctx context.Context, org Org, in ResolveInput) (Decision, error) {
	boxes, err := r.dir.BoxesForOrg(ctx, org.ID)
	if err != nil {
		return Decision{}, err
	}
	best, err := selectBestAt(boxes, in.ClientLat, in.ClientLon, r.now())
	if errors.Is(err, ErrNoHealthyBox) {
		return Decision{Kind: DecideNoBox, Org: org}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	return r.routeTo(org, best, in.OSHost), nil
}

func (r *Router) routeTo(org Org, box Box, osHost string) Decision {
	return Decision{
		Kind:    DecideRoute,
		Org:     org,
		Box:     box,
		BoxHost: strings.ToLower(box.ID) + "." + osHost,
	}
}
