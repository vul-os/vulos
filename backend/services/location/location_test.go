package location

import (
	"math"
	"strings"
	"testing"
	"time"
)

// ─── validation ────────────────────────────────────────────────────────────

func TestSetPosition_Validation(t *testing.T) {
	cases := []struct {
		name    string
		pos     Position
		wantErr bool
	}{
		{"valid", Position{Lat: 40.7128, Lng: -74.0060}, false},
		{"valid boundary lat 90", Position{Lat: 90, Lng: 0}, false},
		{"valid boundary lat -90", Position{Lat: -90, Lng: 0}, false},
		{"valid boundary lng 180", Position{Lat: 0, Lng: 180}, false},
		{"valid boundary lng -180", Position{Lat: 0, Lng: -180}, false},
		{"lat too high", Position{Lat: 90.0001, Lng: 0}, true},
		{"lat too low", Position{Lat: -90.0001, Lng: 0}, true},
		{"lng too high", Position{Lat: 0, Lng: 180.0001}, true},
		{"lng too low", Position{Lat: 0, Lng: -180.0001}, true},
		{"lat NaN", Position{Lat: math.NaN(), Lng: 0}, true},
		{"lng NaN", Position{Lat: 0, Lng: math.NaN()}, true},
		{"lat +Inf", Position{Lat: math.Inf(1), Lng: 0}, true},
		{"lng -Inf", Position{Lat: 0, Lng: math.Inf(-1)}, true},
		{"accuracy NaN", Position{Lat: 0, Lng: 0, Accuracy: math.NaN()}, true},
		{"speed +Inf", Position{Lat: 0, Lng: 0, Speed: math.Inf(1)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(nil)
			err := s.SetPosition("user-a", c.pos)
			if (err != nil) != c.wantErr {
				t.Errorf("SetPosition(%+v) err = %v, wantErr %v", c.pos, err, c.wantErr)
			}
		})
	}
}

func TestSetPosition_RejectsUnauthenticated(t *testing.T) {
	s := New(nil)
	if err := s.SetPosition("", Position{Lat: 1, Lng: 1}); err == nil {
		t.Error("expected error for empty userID, got nil")
	}
}

// ─── per-user isolation ────────────────────────────────────────────────────

func TestPerUserIsolation(t *testing.T) {
	s := New(nil)
	if err := s.SetPosition("alice", Position{Lat: 10, Lng: 20}); err != nil {
		t.Fatalf("alice SetPosition: %v", err)
	}

	bob := s.GetPosition("bob")
	if bob.Found {
		t.Errorf("bob should have no position, got %+v", bob)
	}

	alice := s.GetPosition("alice")
	if !alice.Found || alice.Lat != 10 || alice.Lng != 20 {
		t.Errorf("alice should see her own position, got %+v", alice)
	}

	if err := s.SetPosition("bob", Position{Lat: 50, Lng: 60}); err != nil {
		t.Fatalf("bob SetPosition: %v", err)
	}
	alice2 := s.GetPosition("alice")
	if alice2.Lat != 10 || alice2.Lng != 20 {
		t.Errorf("bob's write leaked into alice's position: %+v", alice2)
	}
}

// ─── staleness ─────────────────────────────────────────────────────────────

func TestGetPosition_Staleness(t *testing.T) {
	s := New(nil)
	// Fresh position (TS defaulted to now in SetPosition).
	if err := s.SetPosition("u1", Position{Lat: 1, Lng: 1}); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	fresh := s.GetPosition("u1")
	if !fresh.Found || fresh.Stale {
		t.Errorf("freshly-set position should not be stale: %+v", fresh)
	}
	if fresh.AgeSeconds > 1 {
		t.Errorf("fresh AgeSeconds too high: %d", fresh.AgeSeconds)
	}

	// Manually inject an old TS (bypassing SetPosition's "now" default) to
	// simulate a position recorded > staleAfter ago.
	s.mu.Lock()
	old := s.byUser["u1"]
	old.TS = time.Now().Add(-90 * time.Second).Unix()
	s.byUser["u1"] = old
	s.mu.Unlock()

	stale := s.GetPosition("u1")
	if !stale.Found {
		t.Fatal("expected stale-but-found position")
	}
	if !stale.Stale {
		t.Errorf("90s-old position should be stale: %+v", stale)
	}
	if stale.AgeSeconds < 89 {
		t.Errorf("AgeSeconds = %d, want >= 89", stale.AgeSeconds)
	}
}

// stubModem is a ModemFallback test double.
type stubModem struct {
	pos Position
	ok  bool
}

func (m stubModem) ModemPosition() (Position, bool) { return m.pos, m.ok }

func TestGetPosition_ModemFallback_WhenStale(t *testing.T) {
	s := New(stubModem{pos: Position{Lat: 5, Lng: 6, Source: "modem", TS: time.Now().Unix()}, ok: true})

	if err := s.SetPosition("u1", Position{Lat: 1, Lng: 1}); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	// Force staleness.
	s.mu.Lock()
	old := s.byUser["u1"]
	old.TS = time.Now().Add(-90 * time.Second).Unix()
	s.byUser["u1"] = old
	s.mu.Unlock()

	got := s.GetPosition("u1")
	if got.Source != "modem" || got.Lat != 5 {
		t.Errorf("expected modem fallback on stale client fix, got %+v", got)
	}
}

func TestGetPosition_NoFallback_WhenClientFresh(t *testing.T) {
	s := New(stubModem{pos: Position{Lat: 5, Lng: 6, Source: "modem", TS: time.Now().Unix()}, ok: true})
	if err := s.SetPosition("u1", Position{Lat: 1, Lng: 1}); err != nil {
		t.Fatalf("SetPosition: %v", err)
	}
	got := s.GetPosition("u1")
	if got.Source != "client" || got.Lat != 1 {
		t.Errorf("fresh client fix should win over modem, got %+v", got)
	}
}

func TestGetPosition_ModemFallback_WhenAbsent(t *testing.T) {
	s := New(stubModem{pos: Position{Lat: 7, Lng: 8, Source: "modem", TS: time.Now().Unix()}, ok: true})
	got := s.GetPosition("u1")
	if !got.Found || got.Source != "modem" {
		t.Errorf("expected modem fallback with no client fix at all, got %+v", got)
	}
}

func TestGetPosition_NotFound_NoClientNoModem(t *testing.T) {
	s := New(nil)
	got := s.GetPosition("nobody")
	if got.Found {
		t.Errorf("expected not-found, got %+v", got)
	}
}

// ─── FormatNMEA ────────────────────────────────────────────────────────────

func TestFormatNMEA_Shape(t *testing.T) {
	pos := Position{Lat: 40.7128, Lng: -74.0060, Altitude: 10, Speed: 5, Heading: 270, TS: 1753257600}
	out := FormatNMEA(pos)
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\r\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NMEA sentences, got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "$GPRMC,") {
		t.Errorf("first sentence should be GPRMC, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "$GPGGA,") {
		t.Errorf("second sentence should be GPGGA, got %q", lines[1])
	}
	// Southern/western hemisphere letters should be present for this
	// (positive lat / negative lng) fix: N and W.
	if !strings.Contains(lines[0], ",N,") || !strings.Contains(lines[0], ",W,") {
		t.Errorf("expected N and W hemisphere letters in GPRMC: %q", lines[0])
	}
}

func TestFormatNMEA_Checksum(t *testing.T) {
	pos := Position{Lat: -33.9249, Lng: 18.4241, TS: 1753257600}
	out := FormatNMEA(pos)
	for _, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\r\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "$") {
			t.Fatalf("sentence missing leading $: %q", line)
		}
		star := strings.LastIndex(line, "*")
		if star < 0 || star != len(line)-3 {
			t.Fatalf("sentence missing 2-hex-digit checksum suffix: %q", line)
		}
		body := line[1:star]
		wantCS := line[star+1:]
		var cs byte
		for i := 0; i < len(body); i++ {
			cs ^= body[i]
		}
		gotCS := strings.ToUpper(wantCS)
		computed := strings.ToUpper(byteToHex(cs))
		if gotCS != computed {
			t.Errorf("checksum mismatch for %q: sentence says %s, computed %s", line, gotCS, computed)
		}
	}
}

func byteToHex(b byte) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[b>>4], hex[b&0xF]})
}

func TestFormatNMEA_SouthEastHemispheres(t *testing.T) {
	pos := Position{Lat: -33.9249, Lng: 18.4241, TS: 1753257600}
	out := FormatNMEA(pos)
	if !strings.Contains(out, ",S,") {
		t.Errorf("negative lat should render S hemisphere: %q", out)
	}
	if !strings.Contains(out, ",E,") {
		t.Errorf("positive lng should render E hemisphere: %q", out)
	}
}

// ─── modem KV parse (pure functions, no hardware) ─────────────────────────

const sampleLocationGetKV = `
location.3gpp.mcc                      : 655
location.3gpp.mnc                      : 01
location.gps.altitude                  : 61.900000
location.gps.latitude                  : 40.417475
location.gps.longitude                 : -3.703583
location.gps.nmea[1]                   : $GPRMC,194909.00,A,4025.0485,N,00342.2150,W,0.0,0.0,230726,,,A*6A
location.gps.utc                       : 194909.00
`

func TestParseModemKV(t *testing.T) {
	kv := parseModemKV(sampleLocationGetKV)
	if kv["location.gps.latitude"] != "40.417475" {
		t.Errorf("latitude: %q", kv["location.gps.latitude"])
	}
	if kv["location.gps.longitude"] != "-3.703583" {
		t.Errorf("longitude: %q", kv["location.gps.longitude"])
	}
}

func TestGPSLatLng_Present(t *testing.T) {
	kv := parseModemKV(sampleLocationGetKV)
	lat, lng, alt, ok := gpsLatLng(kv)
	if !ok {
		t.Fatal("expected ok=true when GPS fields present")
	}
	if lat != 40.417475 || lng != -3.703583 {
		t.Errorf("lat/lng = %v/%v", lat, lng)
	}
	if alt != 61.9 {
		t.Errorf("alt = %v, want 61.9", alt)
	}
}

func TestGPSLatLng_AlternatePrefix(t *testing.T) {
	kv := parseModemKV(`
modem.location.gps.latitude  : 1.5
modem.location.gps.longitude : 2.5
`)
	lat, lng, _, ok := gpsLatLng(kv)
	if !ok || lat != 1.5 || lng != 2.5 {
		t.Errorf("alternate-prefix parse failed: lat=%v lng=%v ok=%v", lat, lng, ok)
	}
}

func TestGPSLatLng_NoFix(t *testing.T) {
	kv := parseModemKV(`
location.3gpp.mcc : 655
location.gps.latitude : --
location.gps.longitude : --
`)
	_, _, _, ok := gpsLatLng(kv)
	if ok {
		t.Error("expected ok=false when no GNSS fix (fields are the '--' unset sentinel)")
	}
}

func TestGPSLatLng_Empty(t *testing.T) {
	_, _, _, ok := gpsLatLng(map[string]string{})
	if ok {
		t.Error("expected ok=false for empty KV map")
	}
}
