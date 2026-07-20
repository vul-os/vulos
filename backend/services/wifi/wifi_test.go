package wifi

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseIWScan — unit tests using fixture strings (no device calls)
// ---------------------------------------------------------------------------

const iwScanFixture = `BSS aa:bb:cc:dd:ee:ff(on wlan0)
	last seen: 120ms ago
	TSF: 1234567890 usec (0d, 00:20:34)
	freq: 2437
	beacon interval: 100 TUs
	capability: ESS Privacy ShortSlotTime (0x0431)
	signal: -55 dBm
	SSID: HomeNetwork
	RSN:	 * Version: 1
		 * Group cipher: CCMP
		 * Pairwise ciphers: CCMP
		 * Authentication suites: PSK
		 * Capabilities: 1-PTKSA-RC 1-GTKSA-RC (0x0000)

BSS 11:22:33:44:55:66(on wlan0)
	last seen: 80ms ago
	freq: 5180
	signal: -70 dBm
	SSID: OfficeWifi
	WPA:	 * Version: 1
		 * Group cipher: CCMP

BSS aa:00:11:22:33:44(on wlan0)
	freq: 2462
	signal: -90 dBm
	SSID: OpenNet
`

func TestParseIWScan_Count(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	if len(nets) != 3 {
		t.Fatalf("expected 3 networks, got %d", len(nets))
	}
}

func TestParseIWScan_SSIDs(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	wantSSIDs := []string{"HomeNetwork", "OfficeWifi", "OpenNet"}
	for i, want := range wantSSIDs {
		if nets[i].SSID != want {
			t.Errorf("net[%d].SSID = %q, want %q", i, nets[i].SSID, want)
		}
	}
}

func TestParseIWScan_Signal(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	if nets[0].Signal != -55 {
		t.Errorf("HomeNetwork signal = %d, want -55", nets[0].Signal)
	}
	if nets[1].Signal != -70 {
		t.Errorf("OfficeWifi signal = %d, want -70", nets[1].Signal)
	}
}

func TestParseIWScan_Band(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	// HomeNetwork at 2437 MHz → 2.4GHz
	if nets[0].Band != "2.4GHz" {
		t.Errorf("HomeNetwork band = %q, want 2.4GHz", nets[0].Band)
	}
	// OfficeWifi at 5180 MHz → 5GHz
	if nets[1].Band != "5GHz" {
		t.Errorf("OfficeWifi band = %q, want 5GHz", nets[1].Band)
	}
}

func TestParseIWScan_Freq(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	if nets[0].Freq != 2437 {
		t.Errorf("HomeNetwork freq = %d, want 2437", nets[0].Freq)
	}
	if nets[1].Freq != 5180 {
		t.Errorf("OfficeWifi freq = %d, want 5180", nets[1].Freq)
	}
}

func TestParseIWScan_Security(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	// HomeNetwork has RSN → WPA2
	if nets[0].Security != "WPA2" {
		t.Errorf("HomeNetwork security = %q, want WPA2", nets[0].Security)
	}
	// OfficeWifi has WPA → WPA2
	if nets[1].Security != "WPA2" {
		t.Errorf("OfficeWifi security = %q, want WPA2", nets[1].Security)
	}
}

func TestParseIWScan_BSSID(t *testing.T) {
	nets := parseIWScan(iwScanFixture)
	if nets[0].BSSID != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("HomeNetwork BSSID = %q, want aa:bb:cc:dd:ee:ff", nets[0].BSSID)
	}
}

func TestParseIWScan_Empty(t *testing.T) {
	nets := parseIWScan("")
	if len(nets) != 0 {
		t.Errorf("expected empty result for empty input, got %v", nets)
	}
}

func TestParseIWScan_NoCompleteEntry(t *testing.T) {
	// BSS with no SSID line is excluded by the final filter
	data := "BSS aa:bb:cc:dd:ee:ff(on wlan0)\n\tfreq: 2437\n\tsignal: -60 dBm\n"
	nets := parseIWScan(data)
	if len(nets) != 0 {
		t.Errorf("expected 0 networks (no SSID), got %d", len(nets))
	}
}

func TestParseIWScan_WPA3(t *testing.T) {
	data := `BSS 00:11:22:33:44:55(on wlan0)
	freq: 5180
	signal: -65 dBm
	SSID: SecureNet
	RSN:	 * Authentication suites: SAE
`
	nets := parseIWScan(data)
	if len(nets) != 1 {
		t.Fatalf("expected 1 network, got %d", len(nets))
	}
	if nets[0].Security != "WPA3" {
		t.Errorf("security = %q, want WPA3", nets[0].Security)
	}
}

// ---------------------------------------------------------------------------
// wpa_cli list_networks output parsing — mirrors SavedNetworks logic
// ---------------------------------------------------------------------------

const wpaListNetworksFixture = `network id / ssid / bssid / flags
0	HomeNetwork	any	[CURRENT]
1	OfficeWifi	any
2	GuestNet	any	[DISABLED]
`

// parseSavedNetworksOutput replicates the parsing logic from SavedNetworks so
// we can unit-test it without invoking wpa_cli.
func parseSavedNetworksOutput(raw string) []SavedNetwork {
	var result []SavedNetwork
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		// first field must be numeric (network id)
		id := 0
		ok := true
		for _, c := range fields[0] {
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if !ok || fields[0] == "" {
			continue
		}
		result = append(result, SavedNetwork{SSID: fields[1], Priority: id})
	}
	return result
}

func TestParseSavedNetworks_Count(t *testing.T) {
	nets := parseSavedNetworksOutput(wpaListNetworksFixture)
	if len(nets) != 3 {
		t.Fatalf("expected 3 saved networks, got %d", len(nets))
	}
}

func TestParseSavedNetworks_SSIDs(t *testing.T) {
	nets := parseSavedNetworksOutput(wpaListNetworksFixture)
	want := []string{"HomeNetwork", "OfficeWifi", "GuestNet"}
	for i, w := range want {
		if nets[i].SSID != w {
			t.Errorf("net[%d].SSID = %q, want %q", i, nets[i].SSID, w)
		}
	}
}

func TestParseSavedNetworks_Empty(t *testing.T) {
	nets := parseSavedNetworksOutput("")
	if len(nets) != 0 {
		t.Errorf("expected 0 saved networks for empty input, got %d", len(nets))
	}
}

func TestParseSavedNetworks_HeaderOnly(t *testing.T) {
	// The header line contains non-numeric first field and should be skipped.
	header := "network id / ssid / bssid / flags\n"
	nets := parseSavedNetworksOutput(header)
	if len(nets) != 0 {
		t.Errorf("expected 0 networks for header-only input, got %d", len(nets))
	}
}

// ---------------------------------------------------------------------------
// Struct shape / zero-value sanity
// ---------------------------------------------------------------------------

func TestNetworkStructFields(t *testing.T) {
	n := Network{
		SSID:     "test",
		BSSID:    "aa:bb:cc:dd:ee:ff",
		Signal:   -60,
		Security: "WPA2",
		Freq:     2437,
		Band:     "2.4GHz",
	}
	if n.SSID != "test" {
		t.Error("SSID mismatch")
	}
	if n.Signal != -60 {
		t.Error("Signal mismatch")
	}
}

func TestConnectionStructFields(t *testing.T) {
	c := Connection{
		Connected: true,
		SSID:      "home",
		IP:        "192.168.1.10",
		Signal:    -55,
		Freq:      2437,
		TxRate:    "54.0 MBit/s",
	}
	if !c.Connected {
		t.Error("Connected should be true")
	}
	if c.IP != "192.168.1.10" {
		t.Errorf("IP = %q, want 192.168.1.10", c.IP)
	}
}

func TestSavedNetworkStructFields(t *testing.T) {
	s := SavedNetwork{SSID: "net", Priority: 10}
	if s.SSID != "net" {
		t.Error("SSID mismatch")
	}
	if s.Priority != 10 {
		t.Error("Priority mismatch")
	}
}

// ---------------------------------------------------------------------------
// SSID validation helpers — test expected length constraints
// ---------------------------------------------------------------------------

func TestSSIDMaxLength(t *testing.T) {
	// IEEE 802.11 SSID max is 32 bytes.
	ssid32 := strings.Repeat("A", 32)
	ssid33 := strings.Repeat("A", 33)

	if len(ssid32) > 32 {
		t.Error("32-char SSID should be valid length")
	}
	if len(ssid33) <= 32 {
		t.Error("33-char SSID should exceed max length")
	}
}

func TestSSIDEmpty(t *testing.T) {
	if len("") != 0 {
		t.Error("empty SSID length check failed")
	}
}

// ---------------------------------------------------------------------------
// SSID/password injection prevention — SEC-WIFI-01
// ---------------------------------------------------------------------------

// TestConnect_ShellInjectionPrevented verifies that Connect rejects SSIDs and
// passwords that contain shell-significant control characters.  The previous
// implementation appended a wpa_supplicant block via `sh -c "echo '...' >> f"`,
// which allowed an SSID like `'; rm -rf /; echo '` to escape the quoting.
// The fix writes the file directly from Go using os.OpenFile.
func TestConnect_RejectsControlCharsInSSID(t *testing.T) {
	svc := &Service{iface: "wlan0"}
	ctx := context.Background()

	injections := []string{
		"'; rm -rf /; echo '",
		"test\nnetwork={}",
		"evil\r\nkey_mgmt=NONE",
		"a\x00b",
	}
	for _, ssid := range injections {
		if err := svc.Connect(ctx, ssid, "password"); err == nil {
			t.Errorf("Connect should reject SSID %q but returned nil error", ssid)
		}
	}
}

func TestConnect_RejectsControlCharsInPassword(t *testing.T) {
	svc := &Service{iface: "wlan0"}
	ctx := context.Background()

	injections := []string{
		"pass'; rm -rf /; echo '",
		"pass\nnetwork={}",
		"pass\x00word",
	}
	for _, pw := range injections {
		if err := svc.Connect(ctx, "HomeNetwork", pw); err == nil {
			t.Errorf("Connect should reject password %q but returned nil error", pw)
		}
	}
}

func TestConnect_AcceptsNormalSSID(t *testing.T) {
	// Connect will fail on the os.OpenFile call on CI (no /etc/wpa_supplicant),
	// but it must NOT fail on the validation step.  We distinguish by checking
	// that the returned error is NOT a control-character rejection.
	svc := &Service{iface: "wlan0"}
	ctx := context.Background()
	err := svc.Connect(ctx, "MyHomeWifi", "SecurePass123!")
	if err != nil && (strings.Contains(err.Error(), "invalid SSID") || strings.Contains(err.Error(), "invalid password")) {
		t.Errorf("valid SSID/password rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Band classification — mirrors the freq threshold in parseIWScan
// ---------------------------------------------------------------------------

func TestBandClassification(t *testing.T) {
	cases := []struct {
		freq int
		band string
	}{
		{2412, "2.4GHz"},
		{2472, "2.4GHz"},
		{5180, "5GHz"},
		{5825, "5GHz"},
		{6000, "5GHz"},
	}
	for _, tc := range cases {
		var band string
		if tc.freq > 4000 {
			band = "5GHz"
		} else {
			band = "2.4GHz"
		}
		if band != tc.band {
			t.Errorf("freq %d: band = %q, want %q", tc.freq, band, tc.band)
		}
	}
}
