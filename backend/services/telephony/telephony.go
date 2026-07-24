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

	mu       sync.Mutex
	modemID  string          // cached modem index ("0")
	modemAt  time.Time       // when modemID was last resolved
	seenSMS  map[string]bool // SMS object paths already delivered as notifications
	pollStop chan struct{}
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
	}
}
