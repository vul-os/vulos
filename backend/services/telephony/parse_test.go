package telephony

import "testing"

// Sample mmcli `-s <path> -K` output for a RECEIVED message.
const smsReceivedKV = `
sms.dbus-path                          : /org/freedesktop/ModemManager1/SMS/3
sms.content.number                     : +15551234567
sms.content.text                       : Your code is 448291
sms.properties.pdu-type                : deliver
sms.properties.state                   : received
sms.properties.timestamp               : 2026-07-23T09:41:12+02:00
sms.properties.smsc                    : --
`

const smsSentKV = `
sms.content.number                     : +15559999999
sms.content.text                       : on my way
sms.properties.pdu-type                : submit
sms.properties.state                   : sent
sms.properties.timestamp               : 2026-07-23T09:00:00+00:00
`

func TestParseKV(t *testing.T) {
	kv := parseKV(smsReceivedKV)
	if kv["sms.content.number"] != "+15551234567" {
		t.Errorf("number: %q", kv["sms.content.number"])
	}
	if kv["sms.content.text"] != "Your code is 448291" {
		t.Errorf("text: %q", kv["sms.content.text"])
	}
	// The "--" sentinel must become empty.
	if kv["sms.properties.smsc"] != "" {
		t.Errorf(`smsc "--" should be empty, got %q`, kv["sms.properties.smsc"])
	}
}

func TestParseSMS_Direction(t *testing.T) {
	rx := parseSMS("/SMS/3", parseKV(smsReceivedKV))
	if rx.Direction != "incoming" {
		t.Errorf("deliver pdu should be incoming, got %q", rx.Direction)
	}
	if rx.Number != "+15551234567" || rx.Body != "Your code is 448291" {
		t.Errorf("fields: %+v", rx)
	}
	if rx.TS == 0 {
		t.Error("timestamp did not parse")
	}

	tx := parseSMS("/SMS/4", parseKV(smsSentKV))
	if tx.Direction != "outgoing" {
		t.Errorf("submit pdu should be outgoing, got %q", tx.Direction)
	}
}

func TestParseMMTime(t *testing.T) {
	if got := parseMMTime("2026-07-23T09:41:12+02:00"); got == 0 {
		t.Error("RFC3339 timestamp failed")
	}
	if got := parseMMTime(""); got != 0 {
		t.Errorf("empty should be 0, got %d", got)
	}
	if got := parseMMTime("not a time"); got != 0 {
		t.Errorf("garbage should be 0, got %d", got)
	}
}

func TestGroupThreads(t *testing.T) {
	msgs := []sms{
		{Number: "+1", Body: "hi", Direction: "incoming", State: "received", TS: 100},
		{Number: "+1", Body: "later reply", Direction: "outgoing", State: "sent", TS: 200},
		{Number: "+1", Body: "unread ping", Direction: "incoming", State: "received", TS: 150},
		{Number: "+2", Body: "otp 1234", Direction: "incoming", State: "received", TS: 300},
		{Number: "", Body: "no number — dropped", Direction: "incoming", TS: 400},
	}
	threads := groupThreads(msgs)
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads (empty number dropped), got %d", len(threads))
	}
	// Newest-first: +2 (ts 300) before +1 (latest ts 200).
	if threads[0].Number != "+2" {
		t.Errorf("expected +2 first (newest), got %q", threads[0].Number)
	}
	// +1's latest body is the ts=200 outgoing "later reply".
	var t1 *Thread
	for i := range threads {
		if threads[i].Number == "+1" {
			t1 = &threads[i]
		}
	}
	if t1 == nil || t1.LastBody != "later reply" || t1.LastTS != 200 {
		t.Errorf("+1 last message wrong: %+v", t1)
	}
	// Two inbound "received" on +1 → unread 2.
	if t1.UnreadCount != 2 {
		t.Errorf("+1 unread = %d, want 2", t1.UnreadCount)
	}
}

func TestModemPathRegex(t *testing.T) {
	out := "/org/freedesktop/ModemManager1/Modem/0 [Quectel] EC25"
	m := reModemPath.FindStringSubmatch(out)
	if m == nil || m[1] != "0" {
		t.Errorf("modem index not parsed from %q: %v", out, m)
	}
	smsOut := "/org/freedesktop/ModemManager1/SMS/3\n/org/freedesktop/ModemManager1/SMS/7"
	if got := reSMSPath.FindAllString(smsOut, -1); len(got) != 2 {
		t.Errorf("expected 2 SMS paths, got %v", got)
	}
}
