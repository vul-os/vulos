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
	// GrowOnly makes this domain a grow-only set: entries may be ADDED, never
	// removed and never modified.
	//
	// It exists for the security audit trail, and it is what makes replicating
	// one safe. A plain LWW domain gives every peer an EDIT primitive: a
	// rostered box that is later compromised could overwrite or tombstone an
	// entry recording its own compromise, on every other box, and the merge
	// would faithfully converge on the attacker's version. Grow-only removes
	// that primitive from the algebra rather than trying to police it:
	//
	//	- a tombstone in this domain is REFUSED, locally and from a peer;
	//	- a register is immutable once written — merge keeps the FIRST writer
	//	  (lowest stamp), not the last.
	//
	// First-writer-wins is still a max/min over a total order, so it keeps
	// every property convergence relies on: commutative, associative,
	// idempotent, and independent of arrival order.
	//
	// The residual, stated plainly: a rostered peer can still PRE-EMPT a key it
	// predicts, by writing that key first with a lower stamp. For an audit log
	// whose keys carry a random event id that is not reachable, and it is a
	// strictly smaller power than "edit any existing entry". It is not a
	// general-purpose defence for a domain with guessable keys.
	GrowOnly bool
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
		Sync:   true,
		Reason: "The account itself, and the reason a fleet feels like one machine: a person logs into any of their own boxes with the credentials " +
			"they set once. That requires the password hash to replicate. Withholding it leaves the account present on box B and unusable there, " +
			"which pushes people into creating a second account by hand with a different password — multiplying weak passwords instead of copies of " +
			"one strong hash. bcrypt at DefaultCost is designed to be STORED, and its cost factor is a defence that does not weaken with the number " +
			"of copies; an attacker who reads this table already owns that box. " +
			"RESIDUAL, accepted on the record rather than hidden: more copies means more machines a hash can be stolen from, and in a fleet with one " +
			"internet-facing box that box becomes the weakest link for every account. The mitigation is the one that always applied — a strong " +
			"password and bcrypt's cost factor. " +
			"Free-form Preferences are NOT replicated wholesale: secret-named keys stay local by the same token rule Profile.Settings uses, so a " +
			"future feature cannot leak a token into the replicated half by accident (auth/user_secrets.go).",
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
		Sync:   true,
		Reason: "Settings are what people most expect to follow them between their own boxes: theme, locale, timezone, display name, avatar, AI " +
			"provider/model, initiative, and free-form preferences. " +
			"This was refused while the row was a single JSON blob holding AIAPIKey and PinHash alongside Theme and Locale, because column-level " +
			"Exclude cannot strip a field from inside a blob. That is fixed at the STORAGE layer (auth/profile_secrets.go): credentials are written " +
			"to a separate profile_secrets table and stitched back on load, so the bytes in the replicated row never contain them. A json:\"-\" tag " +
			"would not have done — the requirement is about what is WRITTEN, not what is encoded. " +
			"PinHash stays local because a PIN belongs to a machine someone is standing at, not to an account; AIAPIKey stays local because it is a " +
			"bearer secret that can be spent. Free-form Settings keys that name a secret are held back by a token rule, so a future feature cannot " +
			"leak a token into the replicated half by accident.",
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
		Domain:   "sql:acctsec_sensitive_actions",
		Sync:     true,
		GrowOnly: true,
		Reason: "A security audit trail, replicated as a GROW-ONLY set. Under close-to-identical instances it SHOULD be on every box: an attacker " +
			"who compromises one machine and erases its local log no longer erases the evidence, because the others hold it and grow-only means " +
			"they cannot be told to forget. It was refused while the only algebra was last-writer-wins, which hands every rostered peer an EDIT " +
			"primitive — a compromised box could overwrite or tombstone the entry recording its own compromise, everywhere. GrowOnly removes that: " +
			"tombstones refused locally, from a peer op, and from a peer snapshot; merge keeps the FIRST writer, so an entry is immutable once " +
			"written. It was refused a second time for a schema reason now fixed: the primary key was a per-box AUTOINCREMENT integer, and the " +
			"session extension keys on the primary key, so two machines gave the same key to different events and a merge dropped one. Migrations " +
			"0002/0003 make a random event_id the primary key. That also closes grow-only's residual — first-writer-wins lets a peer suppress an " +
			"entry whose key it can PREDICT, and a random id leaves nothing to predict.",
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

// GrowOnlyDomains returns the approved domains that are grow-only sets.
func GrowOnlyDomains() []string {
	var out []string
	for _, d := range Decisions {
		if d.Sync && d.GrowOnly {
			out = append(out, d.Domain)
		}
	}
	sort.Strings(out)
	return out
}

// IsGrowOnly reports whether a domain is a grow-only set.
func IsGrowOnly(domain string) bool {
	d, ok := DecisionFor(domain)
	return ok && d.GrowOnly
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

// ErrGrowOnlyViolation is returned when an operation would remove or modify an
// entry in a grow-only domain. It is a refusal, not a fault: an honest peer
// never produces one, so it is worth naming loudly rather than folding into a
// generic merge error.
type ErrGrowOnlyViolation struct {
	Domain string
	Op     string
}

func (e *ErrGrowOnlyViolation) Error() string {
	return fmt.Sprintf("crdtsync: domain %q is grow-only: %s is not permitted (entries may be added, never removed or modified)", e.Domain, e.Op)
}
