package telephony

import (
	"fmt"
	"regexp"
	"strings"
)

var reCallPath = regexp.MustCompile(`/org/freedesktop/ModemManager1/Call/\d+`)

// Call is a call-history entry (matches apps/phone's expected fields).
type Call struct {
	Number    string `json:"number"`
	Direction string `json:"direction"`
	TS        int64  `json:"ts"`
	Duration  int64  `json:"duration"`
}

// HasVoice reports whether the modem exposes voice support. Many data/SMS-only
// USB modems do not — the phone app degrades gracefully when this is false.
func (s *Service) HasVoice() bool {
	id := s.modemIndex()
	if id == "" {
		return false
	}
	out, err := mmcli("-m", id, "--voice-status")
	return err == nil && out != ""
}

// CallLog returns recent calls. ModemManager keeps no PERSISTENT call log (Call
// objects are dropped when a call ends), so this returns the currently-known
// calls only — usually empty. A persistent log (recording our own placed/received
// calls) is a documented follow-up.
func (s *Service) CallLog() []Call {
	return []Call{}
}

// PlaceCall dials `number` (best-effort — fails on data/SMS-only modems). The
// modem owns the audio path; this just initiates the call.
func (s *Service) PlaceCall(number string) error {
	id := s.modemIndex()
	if id == "" {
		return fmt.Errorf("telephony: no modem")
	}
	if !validNumber(number) {
		return fmt.Errorf("telephony: invalid number")
	}
	out, err := mmcli("-m", id, "--voice-create-call=number="+strings.TrimSpace(number))
	if err != nil {
		return fmt.Errorf("telephony: voice unavailable: %w", err)
	}
	path := reCallPath.FindString(out)
	if path == "" {
		return fmt.Errorf("telephony: call not created")
	}
	if _, err := mmcli("-o", path, "--start"); err != nil {
		return fmt.Errorf("telephony: call start: %w", err)
	}
	return nil
}

// Hangup ends all active calls.
func (s *Service) Hangup() error {
	id := s.modemIndex()
	if id == "" {
		return fmt.Errorf("telephony: no modem")
	}
	_, err := mmcli("-m", id, "--voice-hangup-all")
	return err
}

// Accept answers the first ringing call (best-effort).
func (s *Service) Accept() error {
	id := s.modemIndex()
	if id == "" {
		return fmt.Errorf("telephony: no modem")
	}
	out, err := mmcli("-m", id, "--voice-list-calls")
	if err != nil {
		return err
	}
	path := reCallPath.FindString(out)
	if path == "" {
		return fmt.Errorf("telephony: no call to accept")
	}
	_, err = mmcli("-o", path, "--accept")
	return err
}
