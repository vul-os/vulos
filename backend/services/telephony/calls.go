package telephony

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var reCallPath = regexp.MustCompile(`/org/freedesktop/ModemManager1/Call/\d+`)

// maxCallLog bounds the box's own call history so a busy line can't grow the
// process without limit. Mirrors maxVirtualMsgs' rationale for the virtual line.
const maxCallLog = 200

// Call is a call-history entry (matches the phone app's expected fields).
//
// Direction is one of "incoming" / "outgoing" / "missed" — the same vocabulary
// the Android call-log bridge emits, so one Recents list can render rows from
// either source without a translation table.
type Call struct {
	ID        string `json:"id,omitempty"`
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

// CallLog returns the box's own call history, newest first.
//
// ModemManager keeps NO persistent call log of its own — a Call object exists
// only while the call does and is dropped the moment it ends — so if the box
// does not record its own calls there is nothing to show and Recents is
// permanently empty on real hardware. This is that record: PlaceCall logs each
// outgoing call as it is dialled, and the poll loop (pollCalls) logs inbound
// calls, answered or missed, as it observes them.
//
// Never nil, so the wire always carries `[]` rather than `null`.
func (s *Service) CallLog() []Call {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	out := make([]Call, len(s.clog))
	copy(out, s.clog)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out
}

// recordCall appends an entry to the bounded call log.
func (s *Service) recordCall(c Call) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.clog = append(s.clog, c)
	if len(s.clog) > maxCallLog {
		s.clog = s.clog[len(s.clog)-maxCallLog:]
	}
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
	// Record the outgoing call NOW rather than waiting for the poll loop to
	// observe it. The poll deliberately ignores outgoing call objects when they
	// end (see pollCalls) so the two paths cannot double-log the same call, and
	// the user sees the call in Recents the instant it is placed — as on a phone.
	s.recordCall(Call{ID: path, Number: strings.TrimSpace(number), Direction: "outgoing", TS: time.Now().Unix()})
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
	if _, err := mmcli("-o", path, "--accept"); err != nil {
		return err
	}
	s.noteAnswered(path)
	return nil
}

// ─── the call that is happening RIGHT NOW ─────────────────────────────────────

// ActiveCall is the snapshot of the call currently on the modem, if any. It is
// the ONLY way a UI can know a call is in progress, and it exists because there
// was no such way: PlaceCall returned `ok` the instant mmcli accepted the dial
// and then the box went silent. A caller could start a call and had nothing to
// show for it — no ringing state, no "you are on a call", and no honest basis
// for a Hang up button, since nothing could say whether there was anything left
// to hang up. Everything here is READ from ModemManager; none of it is inferred
// from the fact that a dial request was made.
type ActiveCall struct {
	Active bool `json:"active"`
	// Number is the far end, when the modem reports one. Withheld numbers and
	// some networks give nothing, so "" is a normal answer, not a failure.
	Number string `json:"number,omitempty"`
	// Direction is ModemManager's own: "incoming" or "outgoing".
	Direction string `json:"direction,omitempty"`
	// State is ModemManager's own call state, passed through verbatim rather
	// than collapsed into a Vulos vocabulary: "dialing", "ringing-out",
	// "ringing-in", "active", "held", "waiting". A UI that wants to say
	// "Ringing…" versus "On a call" needs the difference, and inventing a
	// coarser vocabulary here would throw it away.
	State string `json:"state,omitempty"`
}

// terminalCallStates are the states in which a Call object is NOT a call in
// progress. ModemManager can leave a `terminated` object listed briefly after a
// call ends, and reports `unknown` for an object it has not filled in yet;
// treating either as live would leave a stale "on a call" bar on screen with a
// Hang up button that hangs up nothing.
var terminalCallStates = map[string]bool{"": true, "unknown": true, "terminated": true}

// ActiveCall reports the call currently on the modem. No modem, no mmcli, no
// call objects, or only terminated ones ⇒ {Active:false} — the same clean
// "nothing here" the rest of this service returns rather than an error, because
// a box with no GSM is not a broken box.
//
// Honest limitation: with several calls up (one held, one active) this reports
// the first non-terminal one mmcli lists. Vulos has no multi-call UI, so there
// is nothing yet for a richer answer to feed.
func (s *Service) ActiveCall() ActiveCall {
	id := s.modemIndex()
	if id == "" {
		return ActiveCall{}
	}
	out, err := mmcli("-m", id, "--voice-list-calls")
	if err != nil {
		// A transient mmcli failure is NOT "the call ended". Reporting inactive
		// here would tear an in-call bar down mid-call on a single hiccup, and
		// the user would lose the only Hang up button they have.
		return ActiveCall{}
	}
	for _, p := range reCallPath.FindAllString(out, -1) {
		number, direction, state, ok := s.callState(p)
		if !ok || terminalCallStates[strings.TrimSpace(state)] {
			continue
		}
		return ActiveCall{Active: true, Number: number, Direction: direction, State: strings.TrimSpace(state)}
	}
	return ActiveCall{}
}

// ─── inbound-call polling → call log + WS + notification ──────────────────────

// liveCall is what the poll loop remembers about a call object it has seen, so
// that when the object disappears (the call ended — ModemManager drops it) the
// entry can be finalised with the right direction and answered/missed outcome.
type liveCall struct {
	number   string
	incoming bool
	answered bool
	startTS  int64
}

// noteAnswered marks a tracked inbound call as answered, so that when it ends it
// is logged as a received call rather than a missed one.
func (s *Service) noteAnswered(path string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if c, ok := s.seenCall[path]; ok {
		c.answered = true
		s.seenCall[path] = c
	}
}

// callState reads a single ModemManager Call object's number/direction/state.
func (s *Service) callState(path string) (number, direction, state string, ok bool) {
	out, err := mmcli("-o", path, "-K")
	if err != nil || out == "" {
		return "", "", "", false
	}
	kv := parseKV(out)
	return kv["call.properties.number"], kv["call.properties.direction"], kv["call.properties.state"], true
}

// pollCalls is one tick of inbound-call tracking. ModemManager exposes a Call
// object while a call exists and removes it when the call ends, so "ended" is
// observed as a path that was present last tick and is gone now — that
// transition is what writes the history entry.
//
// Only INBOUND calls are logged here; outgoing ones are logged by PlaceCall at
// dial time and pre-marked seen, so no call is recorded twice.
//
// Honest limitation: at a 10s tick a call that both arrives and ends inside one
// interval is never observed and so is never logged. That is a real gap, not a
// silent one — a missed call shorter than the poll interval will not appear.
func (s *Service) pollCalls() {
	id := s.modemIndex()
	if id == "" {
		return
	}
	out, err := mmcli("-m", id, "--voice-list-calls")
	if err != nil {
		// Transient failure (or a modem with no voice support): do NOT treat the
		// empty result as "every tracked call ended", or a single hiccup would
		// flush every in-progress call into the log as missed.
		return
	}
	present := map[string]bool{}
	for _, p := range reCallPath.FindAllString(out, -1) {
		present[p] = true
	}

	// New/updated objects.
	for p := range present {
		s.cmu.Lock()
		known, seen := s.seenCall[p]
		s.cmu.Unlock()
		number, direction, state, ok := s.callState(p)
		if !ok {
			continue
		}
		if !seen {
			known = liveCall{number: number, incoming: direction == "incoming", startTS: time.Now().Unix()}
			if known.incoming {
				s.hub.broadcast(map[string]any{"type": "call_incoming", "from": number, "ts": known.startTS})
			}
		}
		if known.number == "" {
			known.number = number
		}
		if state == "active" {
			known.answered = true
		}
		s.cmu.Lock()
		if s.seenCall == nil {
			s.seenCall = map[string]liveCall{}
		}
		s.seenCall[p] = known
		s.cmu.Unlock()
	}

	// Objects that vanished since the last tick: the call ended.
	s.cmu.Lock()
	var ended []struct {
		path string
		c    liveCall
	}
	for p, c := range s.seenCall {
		if !present[p] {
			ended = append(ended, struct {
				path string
				c    liveCall
			}{p, c})
			delete(s.seenCall, p)
		}
	}
	s.cmu.Unlock()

	for _, e := range ended {
		if !e.c.incoming {
			continue // outgoing: already logged by PlaceCall
		}
		dir := "missed"
		var dur int64
		if e.c.answered {
			dir = "incoming"
			dur = time.Now().Unix() - e.c.startTS
		}
		number := e.c.number
		s.recordCall(Call{ID: e.path, Number: number, Direction: dir, TS: e.c.startTS, Duration: dur})
		s.hub.broadcast(map[string]any{"type": "call_ended", "from": number, "direction": dir, "ts": e.c.startTS})
		if dir == "missed" && s.notifier != nil {
			from := number
			if from == "" {
				from = "Unknown"
			}
			s.notifier.Send("Missed call", "From "+from, "phone")
		}
	}
}
