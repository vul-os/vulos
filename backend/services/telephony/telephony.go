// Package telephony is the box-side GSM service (TELE-01): SMS + calls via a
// USB LTE modem / SIM plugged into the box, spoken through ModemManager's `mmcli`
// CLI (the same "shell out to a CLI, no Go D-Bus lib" approach used by
// services/auth/fingerprint.go). It backs the `phone` app's /api/telephony/*
// endpoints and fires a sovereign notification (services/notify) on each inbound
// SMS, so an OTP / bank alert reaches the user even with the app closed.
//
// Everything is HARDWARE-GATED: with no `mmcli` binary or no modem present,
// IsAvailable() is false and every endpoint returns a clean "unavailable" state
// rather than erroring — the box simply has no GSM attached.
//
// Why mmcli key-value (`-K`) rather than JSON: `-K` output ("a.b.c : value") is
// stable across ModemManager versions; the JSON shape has drifted. We parse it
// into a flat map and, for path lists, regex the well-known object paths — robust
// to formatting quirks.
package telephony

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Notifier is the sovereign-notification seam. The service depends only on this
// tiny interface (not on services/notify), so the core stays decoupled; the
// server injects an adapter over notify.Service. An inbound SMS calls Send.
type Notifier interface {
	Send(title, body string, source string)
}

// Service is the box telephony service. Safe for concurrent use.
type Service struct {
	notifier Notifier
	// ownerID returns the box owner's user id — the box's single SIM is treated
	// as THAT user's line. The whole /api/telephony surface (and the inbound-SMS
	// notification/WS feed) is scoped to the owner so another account on a
	// multi-user box can't read their SMS/OTPs. Returns "" when unresolved (e.g.
	// no admin yet) — then, fail-closed, telephony is denied to everyone.
	ownerID func() string
	hub     *wsHub

	// provider is the SECOND / THROWAWAY-number line: a swappable virtual-number
	// backend (BYO, env-configured) that is entirely separate from the box SIM
	// above. Never nil — an unconfigured box gets a noopProvider that fails closed
	// (Configured()==false, every action returns ErrNoProvider), so telephony never
	// silently dials or sends over a default vendor.
	provider Provider

	mu       sync.Mutex
	modemID  string          // cached modem index ("0")
	modemAt  time.Time       // when modemID was last resolved
	seenSMS  map[string]bool // SMS object paths already delivered as notifications
	pollStop chan struct{}

	// vmu guards the virtual-line message log. The provider (unlike the SIM's
	// ModemManager inbox) keeps no queryable store on the box, so the box records
	// the virtual line's own sent/received messages here to back the over-internet
	// threads view. Bounded to maxVirtualMsgs.
	vmu   sync.Mutex
	vmsgs []sms

	// cmu guards the call log and the in-flight-call tracking map. ModemManager
	// keeps NO call history of its own — a Call object exists only for as long as
	// the call does — so unless the box records its own calls there is nothing for
	// the phone app's Recents list to show, ever. clog is that record (bounded to
	// maxCallLog); seenCall tracks the call objects currently on the modem so that
	// one vanishing can be turned into a history entry. See calls.go.
	cmu      sync.Mutex
	clog     []Call
	seenCall map[string]liveCall
}

// New creates the service. notifier may be nil (no notifications fired). ownerID
// resolves the owning user; nil is treated as "no owner" (telephony denied).
func New(notifier Notifier, ownerID func() string) *Service {
	if ownerID == nil {
		ownerID = func() string { return "" }
	}
	return &Service{
		notifier: notifier,
		ownerID:  ownerID,
		hub:      newWSHub(),
		seenSMS:  map[string]bool{},
		provider: providerFromEnv(),
	}
}

// isOwner reports whether userID is the box owner (fail-closed: false when the
// owner is unresolved or userID is empty).
func (s *Service) isOwner(userID string) bool {
	owner := s.ownerID()
	return owner != "" && userID == owner
}

// ─── mmcli plumbing ───────────────────────────────────────────────────────────

// mmcliPresent reports whether the ModemManager CLI is installed.
func mmcliPresent() bool {
	_, err := exec.LookPath("mmcli")
	return err == nil
}

// mmcli runs `mmcli <args...>` and returns trimmed stdout, or an error. A 5s
// timeout bounds a WEDGED modem — USB LTE modems are exactly the hardware class
// that hangs (firmware crash, USB dropout mid-D-Bus-call), and every caller
// (including the poll loop and the request handlers) runs mmcli synchronously,
// so a hang without this would stall them indefinitely.
func mmcli(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mmcli", args...).Output()
	return strings.TrimSpace(string(out)), err
}

var (
	reModemPath = regexp.MustCompile(`/org/freedesktop/ModemManager1/Modem/(\d+)`)
	reSMSPath   = regexp.MustCompile(`/org/freedesktop/ModemManager1/SMS/\d+`)
	// A dialable number: optional leading +, then digits/space/()- only. This
	// matters for SECURITY: the number is interpolated into mmcli's comma-joined
	// `number=…,text=…` dictionary arg, so an unvalidated number containing a comma
	// (e.g. "+1,text=evil") could inject dictionary keys and redirect/alter the
	// message. Restricting to real phone-number characters closes that.
	reValidNumber = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{2,31}$`)
)

// validNumber reports whether n is a safe, dialable phone number.
func validNumber(n string) bool {
	return reValidNumber.MatchString(strings.TrimSpace(n))
}

// parseKV turns mmcli `-K` output ("a.b.c : value" lines) into a flat map.
// Values are trimmed; the sentinel "--" (mmcli's "unset") becomes "".
func parseKV(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if val == "--" {
			val = ""
		}
		if key != "" {
			m[key] = val
		}
	}
	return m
}

// modemIndex resolves (and briefly caches) the first modem's index. Empty string
// means no modem present.
func (s *Service) modemIndex() string {
	s.mu.Lock()
	if s.modemID != "" && time.Since(s.modemAt) < 10*time.Second {
		id := s.modemID
		s.mu.Unlock()
		return id
	}
	s.mu.Unlock()

	out, err := mmcli("-L")
	if err != nil {
		return ""
	}
	match := reModemPath.FindStringSubmatch(out)
	if match == nil {
		return ""
	}
	s.mu.Lock()
	s.modemID = match[1]
	s.modemAt = time.Now()
	s.mu.Unlock()
	return match[1]
}

// ─── availability + status ────────────────────────────────────────────────────

// IsAvailable reports whether GSM telephony is usable (mmcli installed AND a
// modem present). Hardware gate for the whole service.
func (s *Service) IsAvailable() bool {
	return mmcliPresent() && s.modemIndex() != ""
}

// Status is the modem snapshot the phone app shows.
type Status struct {
	Available bool   `json:"available"`
	State     string `json:"state,omitempty"`          // e.g. "registered", "connected"
	Signal    int    `json:"signal_quality,omitempty"` // 0-100
	Operator  string `json:"operator,omitempty"`
	Number    string `json:"number,omitempty"` // own number (often blank; SIMs seldom expose it)

	// Voice reports whether this modem can carry CALLS as well as data/SMS. Many
	// USB LTE sticks are data/SMS-only, and on those a dialler is a button that
	// can only ever fail. Exposing the capability lets the phone app say so up
	// front instead of offering a dial pad that errors on every press.
	Voice bool `json:"voice"`
}

// Status returns the current modem state, or {Available:false} when no GSM.
func (s *Service) Status() Status {
	if !mmcliPresent() {
		return Status{Available: false}
	}
	id := s.modemIndex()
	if id == "" {
		return Status{Available: false}
	}
	out, err := mmcli("-m", id, "-K")
	if err != nil {
		return Status{Available: false}
	}
	kv := parseKV(out)
	return Status{
		Available: true,
		State:     kv["modem.generic.state"],
		Signal:    atoiSafe(kv["modem.generic.signal-quality.value"]),
		Operator:  firstNonEmpty(kv["modem.3gpp.operator-name"], kv["modem.3gpp.operator-code"]),
		Number:    kv["modem.generic.own-numbers.value[1]"],
		Voice:     s.HasVoice(),
	}
}
