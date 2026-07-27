package telephony

// virtual.go is the box-side state for the second/throwaway-number (Provider)
// line: a bounded log of the line's own sent/received messages plus the read/send
// operations the phone app calls over the internet. The SIM line reads its inbox
// straight from ModemManager; the virtual line has no such on-box store, so we
// keep our own record here (fed by VirtualSend and the inbound webhook) to back
// the threads view from any browser reaching the box.

import (
	"sort"
	"strings"
	"time"
)

// maxVirtualMsgs bounds the in-memory virtual-line log so a chatty number can't
// grow it without limit; oldest messages fall off.
const maxVirtualMsgs = 500

// VirtualStatus is the second-number snapshot the phone app shows alongside the
// SIM Status. Configured==false means no provider is wired (the app renders a
// "set up a second number" state); nothing is dialed or sent in that case.
type VirtualStatus struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider,omitempty"`
	Number     string `json:"number,omitempty"`
	CanCall    bool   `json:"can_call"`
}

// VirtualStatus reports the second-number line's state.
func (s *Service) VirtualStatus() VirtualStatus {
	p := s.provider
	st := VirtualStatus{
		Configured: p.Configured(),
		Provider:   p.Name(),
		Number:     p.Number(),
	}
	if hp, ok := p.(*httpProvider); ok {
		st.CanCall = hp.callURL != ""
	}
	return st
}

// VirtualSend sends an SMS over the second-number provider and, on success,
// records it on the virtual line + pushes it to any open WS clients (so the
// browser reflects the sent message immediately). Fails closed with ErrNoProvider
// when unconfigured.
func (s *Service) VirtualSend(to, body string) error {
	if err := s.provider.SendSMS(to, body); err != nil {
		return err
	}
	m := sms{
		Number:    strings.TrimSpace(to),
		Body:      body,
		Direction: "outgoing",
		State:     "sent",
		TS:        time.Now().Unix(),
	}
	s.appendVirtual(m)
	s.hub.broadcast(map[string]any{
		"type": "sms", "line": "virtual", "direction": "outgoing",
		"to": m.Number, "body": m.Body, "ts": m.TS,
	})
	return nil
}

// VirtualPlaceCall initiates a call on the second-number line. Returns the
// provider call id when known. Fails closed when unconfigured / call-incapable.
func (s *Service) VirtualPlaceCall(to string) (string, error) {
	return s.provider.PlaceCall(to)
}

// VirtualThreads groups the virtual line's recorded messages into conversations,
// reusing the SIM line's grouping logic.
func (s *Service) VirtualThreads() []Thread {
	return groupThreads(s.virtualSnapshot())
}

// VirtualThreadFor returns the virtual-line messages exchanged with `number`,
// oldest first.
func (s *Service) VirtualThreadFor(number string) []Message {
	msgs := s.virtualSnapshot()
	var out []Message
	for _, m := range msgs {
		if m.Number != number {
			continue
		}
		out = append(out, Message{Direction: m.Direction, Body: m.Body, TS: m.TS, Status: m.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// appendVirtual records a message on the virtual line, trimming to the newest
// maxVirtualMsgs.
func (s *Service) appendVirtual(m sms) {
	s.vmu.Lock()
	defer s.vmu.Unlock()
	s.vmsgs = append(s.vmsgs, m)
	if len(s.vmsgs) > maxVirtualMsgs {
		s.vmsgs = s.vmsgs[len(s.vmsgs)-maxVirtualMsgs:]
	}
}

// virtualSnapshot returns a copy of the virtual-line log for read-only use.
func (s *Service) virtualSnapshot() []sms {
	s.vmu.Lock()
	defer s.vmu.Unlock()
	out := make([]sms, len(s.vmsgs))
	copy(out, s.vmsgs)
	return out
}

// inboundEvent is the JSON shape the provider webhook delivers. `type` is "sms"
// (default) or "call".
type inboundEvent struct {
	Type string `json:"type"`
	From string `json:"from"`
	To   string `json:"to"`
	Body string `json:"body"`
	TS   int64  `json:"ts"`
}

// ingestInbound records + fans out a verified inbound provider event. For SMS it
// mirrors the SIM line's onIncomingSMS behaviour: live WS push AND an owner-
// targeted sovereign notification (so an OTP to the second number reaches the
// user with the app closed). For a call it pushes a ring event + notification.
func (s *Service) ingestInbound(ev inboundEvent) {
	ts := ev.TS
	if ts == 0 {
		ts = time.Now().Unix()
	}
	from := ev.From
	if from == "" {
		from = "Unknown"
	}
	switch ev.Type {
	case "call":
		s.hub.broadcast(map[string]any{"type": "call", "line": "virtual", "from": ev.From, "ts": ts})
		if s.notifier != nil {
			s.notifier.Send("Call from "+from, "Incoming call on your second number", "phone")
		}
	default: // "sms" or empty
		s.appendVirtual(sms{Number: ev.From, Body: ev.Body, Direction: "incoming", State: "received", TS: ts})
		s.hub.broadcast(map[string]any{
			"type": "sms", "line": "virtual", "direction": "incoming",
			"from": ev.From, "body": ev.Body, "ts": ts,
		})
		if s.notifier != nil {
			preview := ev.Body
			if len(preview) > 120 {
				preview = preview[:120]
			}
			s.notifier.Send("SMS from "+from, preview, "phone")
		}
	}
}
