package telephony

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// pollInterval is how often we scan the modem's SMS list for new inbound
// messages. ModemManager has no simple push over mmcli, so we poll + diff.
const pollInterval = 10 * time.Second

// sms is one parsed message from mmcli.
type sms struct {
	Path      string
	Number    string
	Body      string
	Direction string // "incoming" | "outgoing"
	State     string // received | sent | receiving | ...
	TS        int64  // unix seconds
}

func (s *Service) listSMSPaths(modemID string) ([]string, error) {
	out, err := mmcli("-m", modemID, "--messaging-list-sms")
	if err != nil {
		return nil, err
	}
	return reSMSPath.FindAllString(out, -1), nil
}

func (s *Service) getSMS(path string) (sms, bool) {
	out, err := mmcli("-s", path, "-K")
	if err != nil {
		return sms{}, false
	}
	return parseSMS(path, parseKV(out)), true
}

// parseSMS builds an sms from an mmcli `-s -K` key-value map. Split out for tests.
func parseSMS(path string, kv map[string]string) sms {
	// pdu-type "deliver" = a message we RECEIVED; "submit" = one we SENT.
	dir := "outgoing"
	if kv["sms.properties.pdu-type"] == "deliver" {
		dir = "incoming"
	}
	return sms{
		Path:      path,
		Number:    kv["sms.content.number"],
		Body:      kv["sms.content.text"],
		Direction: dir,
		State:     kv["sms.properties.state"],
		TS:        parseMMTime(kv["sms.properties.timestamp"]),
	}
}

// parseMMTime parses ModemManager's timestamp to unix seconds; 0 on failure.
func parseMMTime(v string) int64 {
	if v == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// allSMS returns every message on the modem. The bool is false on a transient
// failure (no modem, or the list call errored) — callers must distinguish that
// from a genuinely-empty inbox (e.g. the poll loop must NOT prune its seen-set on
// a transient error, or it would re-notify every existing message next tick).
func (s *Service) allSMS() ([]sms, bool) {
	id := s.modemIndex()
	if id == "" {
		return nil, false
	}
	paths, err := s.listSMSPaths(id)
	if err != nil {
		return nil, false
	}
	var out []sms
	for _, p := range paths {
		if m, ok := s.getSMS(p); ok {
			out = append(out, m)
		}
	}
	return out, true
}

// ─── threads / messages (phone-app shapes) ────────────────────────────────────

// Thread is a conversation summary (matches apps/phone's expected fields).
type Thread struct {
	Number      string `json:"number"`
	ContactName string `json:"contact_name,omitempty"`
	LastBody    string `json:"last_body"`
	LastTS      int64  `json:"last_ts"`
	UnreadCount int    `json:"unread_count"`
}

// Threads groups all SMS by number into conversations, newest first.
func (s *Service) Threads() []Thread {
	msgs, _ := s.allSMS()
	return groupThreads(msgs)
}

// groupThreads is the pure grouping logic (split out for tests).
func groupThreads(msgs []sms) []Thread {
	byNum := map[string]*Thread{}
	for _, m := range msgs {
		if m.Number == "" {
			continue
		}
		t := byNum[m.Number]
		if t == nil {
			t = &Thread{Number: m.Number}
			byNum[m.Number] = t
		}
		if m.TS >= t.LastTS {
			t.LastTS = m.TS
			t.LastBody = m.Body
		}
		// ModemManager leaves an inbound message "received" until it is marked
		// read; treat that as unread.
		if m.Direction == "incoming" && m.State == "received" {
			t.UnreadCount++
		}
	}
	out := make([]Thread, 0, len(byNum))
	for _, t := range byNum {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS > out[j].LastTS })
	return out
}

// Message is one message in a thread (matches apps/phone's expected fields).
type Message struct {
	Direction string `json:"direction"`
	Body      string `json:"body"`
	TS        int64  `json:"ts"`
	Status    string `json:"status,omitempty"`
}

// Thread returns the messages exchanged with `number`, oldest first.
func (s *Service) ThreadFor(number string) []Message {
	msgs, _ := s.allSMS()
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

// Send creates and sends an SMS via the modem.
//
// SECURITY: mmcli's --messaging-create-sms takes a comma-joined key=value string
// whose parser is last-value-wins, so an unquoted comma in the body could inject
// a SECOND `number=` and silently REDIRECT the message. We therefore QUOTE the
// text value and escape `\`/`"` inside it, so no comma or quote in the body can
// break out of the value and add a dictionary key. Control characters are
// rejected outright (they can't appear in a legitimate SMS and could mangle the
// argument).
func (s *Service) Send(to, body string) error {
	id := s.modemIndex()
	if id == "" {
		return fmt.Errorf("telephony: no modem")
	}
	if !validNumber(to) {
		return fmt.Errorf("telephony: invalid recipient number")
	}
	if body == "" {
		return fmt.Errorf("telephony: empty body")
	}
	if strings.ContainsAny(body, "\x00\r\n") {
		return fmt.Errorf("telephony: message contains control characters")
	}
	esc := strings.ReplaceAll(body, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	out, err := mmcli("-m", id, `--messaging-create-sms=number=`+strings.TrimSpace(to)+`,text="`+esc+`"`)
	if err != nil {
		return fmt.Errorf("telephony: create sms: %w", err)
	}
	path := reSMSPath.FindString(out)
	if path == "" {
		return fmt.Errorf("telephony: sms not created")
	}
	if _, err := mmcli("-s", path, "--send"); err != nil {
		return fmt.Errorf("telephony: send: %w", err)
	}
	return nil
}

// ─── inbound-SMS polling → WS + notification ──────────────────────────────────

// StartPolling begins the inbound-SMS scan loop (idempotent). Pre-existing
// messages at first scan are NOT re-notified; only genuinely new ones fire.
func (s *Service) StartPolling() {
	s.mu.Lock()
	if s.pollStop != nil {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.pollStop = stop
	s.mu.Unlock()
	go s.pollLoop(stop)
}

// StopPolling ends the scan loop.
func (s *Service) StopPolling() {
	s.mu.Lock()
	if s.pollStop != nil {
		close(s.pollStop)
		s.pollStop = nil
	}
	s.mu.Unlock()
}

func (s *Service) pollLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	primed := false // first pass just records what already exists
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		if !s.IsAvailable() {
			continue
		}
		msgs, ok := s.allSMS()
		if !ok {
			// Transient mmcli failure — do NOT wipe seenSMS or advance `primed`,
			// or every existing inbound message would look "new" next tick and
			// get re-notified. Retry on the next tick.
			continue
		}
		current := make(map[string]bool)
		for _, m := range msgs {
			if m.Direction != "incoming" {
				continue
			}
			current[m.Path] = true
			s.mu.Lock()
			isNew := !s.seenSMS[m.Path]
			s.mu.Unlock()
			if isNew && primed {
				s.onIncomingSMS(m)
			}
		}
		// Prune the seen-set to only currently-present inbound messages so it can't
		// grow without bound; SMS paths are monotonic, so a pruned (deleted) path
		// won't reappear and re-notify.
		s.mu.Lock()
		s.seenSMS = current
		s.mu.Unlock()
		primed = true
	}
}

func (s *Service) onIncomingSMS(m sms) {
	// Live push to any open phone-app clients.
	s.hub.broadcast(map[string]any{"type": "sms", "from": m.Number, "body": m.Body, "ts": m.TS})
	// Sovereign notification — reaches the user even with the app closed (feeds
	// the same webpush stack as every other Vulos notification).
	if s.notifier != nil {
		preview := m.Body
		if len(preview) > 120 {
			preview = preview[:120]
		}
		from := m.Number
		if from == "" {
			from = "Unknown"
		}
		s.notifier.Send("SMS from "+from, preview, "phone")
	}
}
