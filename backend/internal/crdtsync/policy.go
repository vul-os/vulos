package crdtsync

import (
	"fmt"
	"sort"
	"strings"
)

// ── which data syncs ─────────────────────────────────────────────────────────
//
// The engine will faithfully replicate ANYTHING it is handed. That is precisely
// why the decision about what it is handed lives here, as an ALLOW-LIST, in
// code, next to the reasoning — and why Open refuses to build a Store with an
// empty one.
//
// A deny-list would be the wrong shape: it fails OPEN. Every table added to the
// database later would replicate by default, and the first person to add a
// credential column would ship it to every box on the LAN without noticing. An
// allow-list fails closed — a new table syncs only when someone writes it down
// here and says why.
//
// The refusals below are recorded in the same table as the approvals on
// purpose. "We thought about sessions and decided no" is a fact worth keeping
// where the next person will read it; a domain that is simply absent looks like
// an oversight.

// Domain names for replicated SQL tables are "sql:<table>" — see
// sqlcrdt.Domain, which must agree with these constants (sqlcrdt has a test
// asserting it does).
const (
	// DomainReminders replicates the assistant's reminders table.
	DomainReminders = "sql:reminders"
)

// DomainDecision records one domain and whether it replicates, with the
// reasoning. Refusals carry Sync=false and are kept deliberately.
type DomainDecision struct {
	// Domain is the crdtsync domain name.
	Domain string
	// Sync is whether this domain replicates between boxes.
	Sync bool
	// Reason is why. For a refusal, what would go wrong if it did sync.
	Reason string
}

// Decisions is the per-domain policy for this box.
//
// Approved (Sync=true) domains are what SyncableDomains hands to Open. Refused
// domains are documentation with teeth: DecisionFor reports them, so a wiring
// mistake that tries to replicate one can be told exactly why it is refused
// rather than just "not allowed".
var Decisions = []DomainDecision{
	{
		Domain: DomainReminders,
		Sync:   true,
		Reason: "User-authored content whose whole value is being the same everywhere: a reminder set on one box should fire wherever you are. " +
			"No column is a credential (id, user_id, text, remind_at, created_at, done), the table has a stable TEXT primary key, and " +
			"RemindersStore holds no in-memory cache — merged rows are visible to the next read with no reload hook. " +
			"It is also the case column granularity exists for: marking one done on your phone while editing its text on your laptop must not lose either edit.",
	},
	{
		Domain: "sql:sessions",
		Sync:   false,
		Reason: "Sessions are per-DEVICE authentication state, not user data. Replicating a bearer token multiplies the blast radius of every box: " +
			"one compromised instance would hand the attacker live sessions on all of them, and revoking a session on one box could not be relied on " +
			"to revoke it elsewhere (a stale replica would resurrect the tombstone's loser). Sessions must stay where they were minted.",
	},
	{
		Domain: "sql:users",
		Sync:   false,
		Reason: "The users row carries the password hash inside its JSON blob. Column-level exclusion cannot reach inside a blob, so there is no safe " +
			"partial version of this table; and a merge that resolved a password change the wrong way would be an authentication bug, not a lost edit. " +
			"Auth material needs a deliberate, audited propagation path, not a general CRDT.",
	},
	{
		Domain: "sql:recovery_blobs",
		Sync:   false,
		Reason: "Encrypted recovery kits. Same argument as users, one step worse: the entire point of a recovery blob is that it exists in few places.",
	},
	{
		Domain: "sql:master_key_blobs",
		Sync:   false,
		Reason: "Per-user master-key envelopes. Replicating key material to every box on the LAN defeats the reason it is enveloped at all.",
	},
	{
		Domain: "sql:local_api_keys",
		Sync:   false,
		Reason: "API key hashes. A credential store; see users.",
	},
	{
		Domain: "sql:profiles",
		Sync:   false,
		Reason: "WANTED but not yet safe. Profiles are the obvious settings domain, but the row is a single JSON `data` blob holding AIAPIKey and " +
			"PinHash alongside Theme and Locale — column-level Exclude cannot strip a field from inside a blob. Syncing it needs a field-level bridge " +
			"that projects the safe subset (there is prior art in routes_export.go's safeProfileExport). Until that exists, syncing this table would " +
			"ship API keys to every peer. Refused on those grounds, not on principle.",
	},
	{
		Domain: "sql:app_registry",
		Sync:   false,
		Reason: "Already replicated by internal/multiinstance/appsync over the same fabric transport. Two engines converging the same table would " +
			"fight: each would observe the other's writes as local edits and restamp them, and the pair would never settle.",
	},
	{
		Domain: "sql:storagemode",
		Sync:   false,
		Reason: "Node-local hardware configuration. Boxes deliberately differ here — replicating one box's storage mode onto another describes " +
			"hardware that box does not have.",
	},
	{
		Domain: "sql:push_subscriptions",
		Sync:   false,
		Reason: "Per-device push endpoints and their keys. Meaningless on another box and credential-bearing.",
	},
	{
		Domain: "sql:acctsec_sensitive_actions",
		Sync:   false,
		Reason: "A security audit trail. Its value depends on being an append-only local record of what happened ON THIS BOX; a mergeable audit log " +
			"is one an attacker can edit from a second box.",
	},
	{
		Domain: "sql:cgroup_slices",
		Sync:   false,
		Reason: "Node-local resource limits, tied to this machine's CPU and memory. See storagemode.",
	},
}

// SyncableDomains returns the approved domains, sorted. This is what the wiring
// passes to Open.
func SyncableDomains() []string {
	var out []string
	for _, d := range Decisions {
		if d.Sync {
			out = append(out, d.Domain)
		}
	}
	sort.Strings(out)
	return out
}

// DecisionFor returns the recorded decision for a domain, if there is one.
func DecisionFor(domain string) (DomainDecision, bool) {
	for _, d := range Decisions {
		if d.Domain == domain {
			return d, true
		}
	}
	return DomainDecision{}, false
}

// ErrDomainNotAllowed is returned for any operation on a domain outside the
// Store's allow-list. It names the reason when the domain was explicitly
// refused, so the failure explains itself.
type ErrDomainNotAllowed struct {
	Domain string
	Reason string
}

func (e *ErrDomainNotAllowed) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("crdtsync: domain %q is not replicated: %s", e.Domain, e.Reason)
	}
	return fmt.Sprintf("crdtsync: domain %q is not in this replica's allow-list", e.Domain)
}

// allowSet builds the lookup used on every operation.
func allowSet(domains []string) map[string]bool {
	m := make(map[string]bool, len(domains))
	for _, d := range domains {
		if d = strings.TrimSpace(d); d != "" {
			m[d] = true
		}
	}
	return m
}

// checkDomain is the enforcement point. Every path that could introduce state
// into this replica — local writes, merged deltas, applied snapshots — goes
// through it, so a domain that is not on the list cannot arrive by any route,
// including from a peer that asks for it by name.
func (s *Store) checkDomain(domain string) error {
	if s.allowed[domain] {
		return nil
	}
	if d, ok := DecisionFor(domain); ok && !d.Sync {
		return &ErrDomainNotAllowed{Domain: domain, Reason: d.Reason}
	}
	return &ErrDomainNotAllowed{Domain: domain}
}
