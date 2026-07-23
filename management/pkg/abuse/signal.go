package abuse

import "time"

// SignalKind identifies which detector tripped a Signal.
type SignalKind string

const (
	KindSignup  SignalKind = "signup"  // burst of new accounts from one IP/account
	KindSend    SignalKind = "send"    // burst of outbound messages
	KindLogin   SignalKind = "login"   // burst of login attempts (success or fail)
	KindFanout  SignalKind = "fanout"  // a single send addressed to many recipients
	KindContent SignalKind = "content" // content-classifier score above threshold
	KindCSAM    SignalKind = "csam"    // CSAM hash matcher returned a match
)

// Categories accepted on abuse_reports.category (validated at the API edge).
const (
	CategoryPhishing   = "phishing"
	CategorySpam       = "spam"
	CategoryCSAM       = "csam"
	CategoryFraud      = "fraud"
	CategoryHarassment = "harassment"
	CategoryMalware    = "malware"
	CategoryOther      = "other"
)

// Status values for abuse_reports.status.
const (
	StatusOpen          = "open"
	StatusInvestigating = "investigating"
	StatusActioned      = "actioned"
	StatusDismissed     = "dismissed"
)

// Signal is the unit of detector output. Score is a unitless 0..1+ value; the
// detector compares it against Config.WarnThreshold / StrongThreshold to
// decide what (if anything) to do.
type Signal struct {
	Kind      SignalKind
	AccountID string
	IP        string
	Score     float64
	Reason    string
	At        time.Time
}

// IsStrong returns true when the signal's score crosses the strong threshold.
// The auto-suspend path uses this; everything else just logs/queues the signal
// for the human team.
func (s Signal) IsStrong(cfg Config) bool {
	return s.Score >= cfg.StrongThreshold
}

// IsWarn returns true when the signal is at or above the warn threshold but
// below the strong threshold.
func (s Signal) IsWarn(cfg Config) bool {
	return s.Score >= cfg.WarnThreshold && s.Score < cfg.StrongThreshold
}

// validCategory reports whether c is one of the documented category values.
func validCategory(c string) bool {
	switch c {
	case CategoryPhishing, CategorySpam, CategoryCSAM, CategoryFraud,
		CategoryHarassment, CategoryMalware, CategoryOther:
		return true
	}
	return false
}

// validStatus reports whether s is one of the four lifecycle states.
func validStatus(s string) bool {
	switch s {
	case StatusOpen, StatusInvestigating, StatusActioned, StatusDismissed:
		return true
	}
	return false
}
