package tunnel

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// grants.go — who may claim which tunnel name on this relay.
//
// # The relay never runs open
//
// There is no anonymous or first-come registration. A relay with no grant
// store refuses to start, rather than starting in a permissive mode nobody
// chose. An open relay is an open proxy: it would let anyone publish
// arbitrary content under the operator's domain and TLS certificate, and
// would make the operator the visible origin of whatever traffic resulted.
// The failure mode of "refuses to boot" is a confused operator; the failure
// mode of "boots open" is an operator who is now hosting someone else's abuse.
//
// # A grant binds a token to specific NAMES
//
// A token authorises a fixed set of names, checked on every registration.
// This is what makes per-box tokens meaningful: box1's grant cannot claim
// box2's name, so a token stolen from one box cannot impersonate another,
// and revoking one box does not disturb the rest.
//
// # Rotation and expiry, without a flag day
//
//   - PreviousToken is accepted alongside Token for as long as the operator
//     leaves it in place, so a token can be rotated by writing the new one,
//     reloading the relay, then updating boxes at leisure, then dropping the
//     old one. Without this, rotation means simultaneous edits on the relay
//     and every box — which in practice means rotation never happens.
//   - ExpiresAt makes a grant self-revoking. A token that leaks after the
//     operator has stopped paying attention stops working on its own.

// Grant authorises one token to claim a fixed set of tunnel names.
type Grant struct {
	// Token is the bearer secret. SECRET — never logged, never returned.
	Token string `json:"token"`
	// Names are the tunnel names this token may claim. Required, non-empty:
	// a grant with no names authorises nothing, and silently accepting one
	// would produce a token that appears configured but can claim nothing.
	Names []string `json:"names"`
	// PreviousToken is accepted alongside Token during a rotation window.
	// Optional.
	PreviousToken string `json:"previous_token,omitempty"`
	// ExpiresAt is an RFC 3339 timestamp after which this grant authorises
	// nothing. Optional; empty means no expiry.
	ExpiresAt string `json:"expires_at,omitempty"`
	// Label is an operator-facing note ("kitchen pi"). Never used for authz.
	Label string `json:"label,omitempty"`

	expiresAt time.Time // parsed form of ExpiresAt
}

// Revocations lists credentials that must stop working immediately, without
// waiting for a grants-file edit to propagate.
//
// It is a SEPARATE file from the grants on purpose: revoking is the urgent
// operation, done under pressure, possibly by someone who did not write the
// grants file. Making it a small append-only list of things to refuse — rather
// than an edit to the list of things to allow — means an operator revoking at
// 3am cannot accidentally delete a working grant while removing a bad one.
type Revocations struct {
	// Tokens are exact bearer values to refuse.
	Tokens []string `json:"tokens,omitempty"`
	// Names are tunnel names to refuse regardless of which grant claims them.
	Names []string `json:"names,omitempty"`
}

// GrantStore answers "may this token claim this name" and is safe for
// concurrent use. Reload swaps the whole set atomically.
type GrantStore struct {
	mu sync.RWMutex
	// byToken maps a SHA-256 of the token to its grant. Hashing means a heap
	// dump or a stray print of this map does not disclose live credentials,
	// and lookups stay O(1).
	byToken map[string]*Grant
	revoked map[string]struct{} // hashed tokens
	revName map[string]struct{}
	nowFn   func() time.Time
}

// hashToken is the lookup key for a bearer token. SHA-256 is used because the
// value is high-entropy (operators are told to generate it with `openssl rand
// -hex 32`), so this is a lookup key, NOT a password hash — a slow KDF would
// buy nothing against a 256-bit random value and would cost a KDF per request.
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// Fingerprint is a short, non-reversible identifier for a token, safe to log
// and to store alongside a live session so the relay can tell whether two
// registrations came from the SAME credential without retaining the credential.
func Fingerprint(tok string) string {
	return hashToken(tok)[:12]
}

// NewGrantStore builds a store from grants and revocations, validating both.
func NewGrantStore(grants []Grant, rev Revocations) (*GrantStore, error) {
	s := &GrantStore{
		byToken: make(map[string]*Grant, len(grants)),
		revoked: make(map[string]struct{}, len(rev.Tokens)),
		revName: make(map[string]struct{}, len(rev.Names)),
		nowFn:   time.Now,
	}
	if len(grants) == 0 {
		return nil, errors.New("no grants configured — a relay never runs open; add at least one {\"token\",\"names\"} entry")
	}
	for i := range grants {
		g := grants[i]
		if strings.TrimSpace(g.Token) == "" {
			return nil, fmt.Errorf("grant %d: token is required", i)
		}
		if len(g.Names) == 0 {
			return nil, fmt.Errorf("grant %d: names is required and must be non-empty (a grant with no names authorises nothing)", i)
		}
		for _, n := range g.Names {
			if err := ValidName(n); err != nil {
				return nil, fmt.Errorf("grant %d: %w", i, err)
			}
		}
		if g.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, g.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("grant %d: expires_at %q is not RFC 3339: %w", i, g.ExpiresAt, err)
			}
			g.expiresAt = t
		}
		h := hashToken(g.Token)
		if _, dup := s.byToken[h]; dup {
			return nil, fmt.Errorf("grant %d: duplicate token", i)
		}
		s.byToken[h] = &g
		if g.PreviousToken != "" {
			ph := hashToken(g.PreviousToken)
			if ph == h {
				return nil, fmt.Errorf("grant %d: previous_token equals token", i)
			}
			if _, dup := s.byToken[ph]; dup {
				return nil, fmt.Errorf("grant %d: previous_token collides with another grant's token", i)
			}
			s.byToken[ph] = &g
		}
	}
	for _, t := range rev.Tokens {
		if strings.TrimSpace(t) != "" {
			s.revoked[hashToken(t)] = struct{}{}
		}
	}
	for _, n := range rev.Names {
		if strings.TrimSpace(n) != "" {
			s.revName[n] = struct{}{}
		}
	}
	return s, nil
}

// LoadGrantsFile reads a JSON array of grants from path.
//
// SECURITY: the file holds bearer tokens, so a world-accessible mode is
// refused rather than warned about — same rule, and same reasoning, as the
// box-side endpoints file.
func LoadGrantsFile(path string) ([]Grant, error) {
	raw, err := readSecretFile(path, "grants")
	if err != nil {
		return nil, err
	}
	return parseGrants(raw)
}

// ParseGrants decodes a JSON array of grants from an inline value (for
// platforms whose secret channel is the environment rather than a file).
func ParseGrants(raw string) ([]Grant, error) { return parseGrants([]byte(raw)) }

func parseGrants(raw []byte) ([]Grant, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var gs []Grant
	if err := dec.Decode(&gs); err != nil {
		return nil, fmt.Errorf("parse grants: %w (expected a JSON array of {\"token\",\"names\":[...]} objects)", err)
	}
	return gs, nil
}

// LoadRevocationsFile reads the revocation list. A missing file is NOT an
// error: revocations are optional, and a relay must not fail to start because
// nobody has ever revoked anything.
func LoadRevocationsFile(path string) (Revocations, error) {
	if path == "" {
		return Revocations{}, nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Revocations{}, nil
	}
	raw, err := readSecretFile(path, "revocations")
	if err != nil {
		return Revocations{}, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var rev Revocations
	if err := dec.Decode(&rev); err != nil {
		return Revocations{}, fmt.Errorf("parse revocations: %w", err)
	}
	return rev, nil
}

// readSecretFile reads a file that holds credentials, refusing permissive modes.
func readSecretFile(path, what string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s file: %w", what, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s file: %s is a directory", what, path)
	}
	if mode := fi.Mode().Perm(); mode&0o007 != 0 {
		return nil, fmt.Errorf(
			"%s file %s is world-accessible (mode %#o) and holds bearer tokens — run: chmod 600 %s",
			what, path, mode, path)
	}
	return os.ReadFile(path)
}

// AuthResult is what a successful lookup yields. It deliberately does NOT
// carry the token: callers need to know who was authorised, never what the
// credential was.
type AuthResult struct {
	// Fingerprint identifies the credential without disclosing it.
	Fingerprint string
	// Names the credential may claim.
	Names []string
	// Label is the operator's note for this grant.
	Label string
}

// Authorization failure reasons, distinguished so the relay can answer with
// the right protocol code — but note Authorize itself never reveals WHICH
// check failed to the peer; see server.go.
var (
	// ErrNoSuchToken: the presented token matches no grant.
	ErrNoSuchToken = errors.New("token not recognised")
	// ErrTokenRevoked: the token is on the revocation list.
	ErrTokenRevoked = errors.New("token revoked")
	// ErrGrantExpired: the grant's expires_at has passed.
	ErrGrantExpired = errors.New("grant expired")
	// ErrNameNotGranted: the token is valid but not for this name.
	ErrNameNotGranted = errors.New("name not authorised for this token")
	// ErrNameRevoked: the name is on the revocation list.
	ErrNameRevoked = errors.New("name revoked")
)

// Authorize reports whether tok may claim name.
//
// The token comparison is constant-time. That matters less than it would for
// a password — the map lookup is already on a hash — but the final equality
// check against the stored value is the one place a timing signal could
// distinguish a near-miss, and making it constant-time costs nothing.
func (s *GrantStore) Authorize(tok, name string) (*AuthResult, error) {
	if s == nil {
		return nil, ErrNoSuchToken
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	h := hashToken(tok)
	if _, bad := s.revoked[h]; bad {
		return nil, ErrTokenRevoked
	}
	if _, bad := s.revName[name]; bad {
		return nil, ErrNameRevoked
	}

	g, ok := s.byToken[h]
	if !ok {
		return nil, ErrNoSuchToken
	}
	// Re-verify the raw value in constant time. The hash lookup above already
	// establishes this, so this guards only against a hash collision, which
	// is not a realistic attack on SHA-256 — it is here because an
	// authorisation path that skips the final comparison is a pattern that
	// goes wrong when someone later swaps the hash for something weaker.
	if subtle.ConstantTimeCompare([]byte(tok), []byte(g.Token)) != 1 &&
		(g.PreviousToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(g.PreviousToken)) != 1) {
		return nil, ErrNoSuchToken
	}
	if !g.expiresAt.IsZero() && s.nowFn().After(g.expiresAt) {
		return nil, ErrGrantExpired
	}
	for _, n := range g.Names {
		if n == name {
			return &AuthResult{
				Fingerprint: h[:12],
				Names:       append([]string(nil), g.Names...),
				Label:       g.Label,
			}, nil
		}
	}
	return nil, ErrNameNotGranted
}

// Lookup reports whether a token is recognised at all, without checking a
// name. The relay uses it to reject an unauthorised upgrade BEFORE allocating
// a WebSocket — the name is not known until the register frame arrives, and
// an unrecognised token should never get that far.
func (s *GrantStore) Lookup(tok string) (*AuthResult, error) {
	if s == nil {
		return nil, ErrNoSuchToken
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	h := hashToken(tok)
	if _, bad := s.revoked[h]; bad {
		return nil, ErrTokenRevoked
	}
	g, ok := s.byToken[h]
	if !ok {
		return nil, ErrNoSuchToken
	}
	if !g.expiresAt.IsZero() && s.nowFn().After(g.expiresAt) {
		return nil, ErrGrantExpired
	}
	return &AuthResult{Fingerprint: h[:12], Names: append([]string(nil), g.Names...), Label: g.Label}, nil
}

// GrantsName reports whether ANY live grant authorises name.
//
// This is the question an automatic-TLS integration needs answered: "should a
// certificate be issued for <name>.<domain>?" It is deliberately answerable
// BEFORE the box has ever connected, so a certificate is ready when it does —
// gating issuance on a live tunnel would mean the first request after a box
// comes up fails while ACME runs.
//
// It leaks whether a name is provisioned, so callers must serve it only where
// that is acceptable: the relay exposes it on the LOOPBACK admin listener, for
// a TLS terminator running on the same host.
func (s *GrantStore) GrantsName(name string) bool {
	if s == nil || ValidName(name) != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, bad := s.revName[name]; bad {
		return false
	}
	now := s.nowFn()
	for h, g := range s.byToken {
		if _, bad := s.revoked[h]; bad {
			continue
		}
		if !g.expiresAt.IsZero() && now.After(g.expiresAt) {
			continue
		}
		for _, n := range g.Names {
			if n == name {
				return true
			}
		}
	}
	return false
}

// Reload atomically swaps in a new grant/revocation set. In-flight sessions
// are NOT torn down here; the caller sweeps them (see Server.sweepRevoked),
// because deciding what to do about live traffic is policy, not storage.
func (s *GrantStore) Reload(grants []Grant, rev Revocations) error {
	fresh, err := NewGrantStore(grants, rev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byToken, s.revoked, s.revName = fresh.byToken, fresh.revoked, fresh.revName
	return nil
}

// Revoked reports whether a live session's credential or name has since been
// revoked or expired. The relay calls this on a timer so a revocation takes
// effect on ESTABLISHED tunnels, not just on new ones — a revocation that
// only applied to future connections would leave a compromised box connected
// indefinitely, since its tunnel never reconnects while it is working.
func (s *GrantStore) Revoked(fingerprint, name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, bad := s.revName[name]; bad {
		return true
	}
	for h, g := range s.byToken {
		if h[:12] != fingerprint {
			continue
		}
		if _, bad := s.revoked[h]; bad {
			return true
		}
		if !g.expiresAt.IsZero() && s.nowFn().After(g.expiresAt) {
			return true
		}
		for _, n := range g.Names {
			if n == name {
				return false // still authorised
			}
		}
		return true // grant no longer covers this name
	}
	return true // the credential is gone from the store entirely
}
