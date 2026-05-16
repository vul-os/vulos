package bluetooth

import (
	"os/exec"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const fixtureShow = `Controller AA:BB:CC:DD:EE:FF (public)
	Name: VulaOS-BT
	Alias: VulaOS-BT
	Class: 0x001c010c
	Powered: yes
	Discoverable: yes
	Discovering: no
	Pairable: yes
`

const fixtureShowOff = `Controller 11:22:33:44:55:66 (public)
	Name: dev2
	Powered: no
	Discoverable: no
	Discovering: no
`

const fixtureDevices = `Device AA:BB:CC:DD:EE:FF Sony WH-1000XM4
Device 11:22:33:44:55:66 Apple Magic Keyboard
Device ZZ:ZZ:ZZ:ZZ:ZZ:ZZ BadLine
`

// A stripped-down "bluetoothctl info" output for the headphones device.
const fixtureInfoAudio = `Device AA:BB:CC:DD:EE:FF (public)
	Name: Sony WH-1000XM4
	Alias: Sony WH-1000XM4
	Class: 0x00240404
	Icon: audio-headset
	Paired: yes
	Trusted: yes
	Blocked: no
	Connected: yes
	LegacyPairing: no
`

const fixtureInfoKeyboard = `Device 11:22:33:44:55:66 (public)
	Name: Apple Magic Keyboard
	Icon: input-keyboard
	Paired: yes
	Trusted: no
	Connected: no
`

const fixtureInfoUnknown = `Device 00:00:00:00:00:01 (public)
	Name: Generic Gadget
	Paired: no
	Trusted: no
	Connected: no
`

// ---------------------------------------------------------------------------
// parseStatus
// ---------------------------------------------------------------------------

func TestParseStatus_Powered(t *testing.T) {
	st := parseStatus(fixtureShow)
	if !st.Powered {
		t.Error("expected Powered=true")
	}
	if !st.Discoverable {
		t.Error("expected Discoverable=true")
	}
	if st.Discovering {
		t.Error("expected Discovering=false")
	}
}

func TestParseStatus_Address(t *testing.T) {
	st := parseStatus(fixtureShow)
	want := "AA:BB:CC:DD:EE:FF"
	if st.Address != want {
		t.Errorf("Address: got %q want %q", st.Address, want)
	}
}

func TestParseStatus_Name(t *testing.T) {
	st := parseStatus(fixtureShow)
	if st.Name != "VulaOS-BT" {
		t.Errorf("Name: got %q", st.Name)
	}
}

func TestParseStatus_PoweredOff(t *testing.T) {
	st := parseStatus(fixtureShowOff)
	if st.Powered {
		t.Error("expected Powered=false for off fixture")
	}
	if st.Discoverable {
		t.Error("expected Discoverable=false for off fixture")
	}
}

func TestParseStatus_Empty(t *testing.T) {
	st := parseStatus("")
	if st.Powered || st.Discoverable || st.Discovering {
		t.Error("empty input should yield zero-value Status")
	}
}

// ---------------------------------------------------------------------------
// parseDeviceList
// ---------------------------------------------------------------------------

func TestParseDeviceList_Count(t *testing.T) {
	// "ZZ:ZZ..." line has bad prefix style but happens to start with "Device ",
	// so it will be parsed. We only test the well-formed lines from the fixture.
	devs := parseDeviceList(fixtureDevices)
	// All three lines start with "Device ", so we expect 3.
	if len(devs) != 3 {
		t.Errorf("expected 3 devices, got %d", len(devs))
	}
}

func TestParseDeviceList_Fields(t *testing.T) {
	devs := parseDeviceList(fixtureDevices)
	if devs[0].Address != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("wrong address: %q", devs[0].Address)
	}
	if devs[0].Name != "Sony WH-1000XM4" {
		t.Errorf("wrong name: %q", devs[0].Name)
	}
	if devs[1].Address != "11:22:33:44:55:66" {
		t.Errorf("wrong address: %q", devs[1].Address)
	}
}

func TestParseDeviceList_Empty(t *testing.T) {
	devs := parseDeviceList("")
	if len(devs) != 0 {
		t.Errorf("expected 0 devices for empty input, got %d", len(devs))
	}
}

func TestParseDeviceList_NoDeviceLines(t *testing.T) {
	devs := parseDeviceList("nothing here\njust text\n")
	if len(devs) != 0 {
		t.Errorf("expected 0 devices, got %d", len(devs))
	}
}

// ---------------------------------------------------------------------------
// parseDeviceInfo
// ---------------------------------------------------------------------------

func TestParseDeviceInfo_Audio(t *testing.T) {
	dev := Device{}
	parseDeviceInfo(fixtureInfoAudio, &dev)
	if !dev.Paired {
		t.Error("expected Paired=true")
	}
	if !dev.Connected {
		t.Error("expected Connected=true")
	}
	if !dev.Trusted {
		t.Error("expected Trusted=true")
	}
	if dev.Type != "audio" {
		t.Errorf("expected type=audio, got %q", dev.Type)
	}
}

func TestParseDeviceInfo_Keyboard(t *testing.T) {
	dev := Device{}
	parseDeviceInfo(fixtureInfoKeyboard, &dev)
	if !dev.Paired {
		t.Error("expected Paired=true")
	}
	if dev.Connected {
		t.Error("expected Connected=false")
	}
	if dev.Trusted {
		t.Error("expected Trusted=false")
	}
	if dev.Type != "input" {
		t.Errorf("expected type=input, got %q", dev.Type)
	}
}

func TestParseDeviceInfo_UnknownType(t *testing.T) {
	dev := Device{}
	parseDeviceInfo(fixtureInfoUnknown, &dev)
	if dev.Type != "unknown" {
		t.Errorf("expected type=unknown, got %q", dev.Type)
	}
}

func TestParseDeviceInfo_Empty(t *testing.T) {
	dev := Device{}
	parseDeviceInfo("", &dev)
	if dev.Paired || dev.Connected || dev.Trusted {
		t.Error("empty info should leave device in zero state")
	}
	if dev.Type != "unknown" {
		t.Errorf("expected type=unknown for empty info, got %q", dev.Type)
	}
}

// ---------------------------------------------------------------------------
// ValidMAC
// ---------------------------------------------------------------------------

func TestValidMAC_Good(t *testing.T) {
	cases := []string{
		"AA:BB:CC:DD:EE:FF",
		"aa:bb:cc:dd:ee:ff",
		"00:00:00:00:00:00",
		"1A:2B:3C:4D:5E:6F",
	}
	for _, c := range cases {
		if !ValidMAC(c) {
			t.Errorf("expected ValidMAC(%q)=true", c)
		}
	}
}

func TestValidMAC_Bad(t *testing.T) {
	cases := []string{
		"",
		"AABBCCDDEEFF",
		"AA:BB:CC:DD:EE",
		"AA:BB:CC:DD:EE:GG",
		"AA:BB:CC:DD:EE:F",
		"ZZ:ZZ:ZZ:ZZ:ZZ:ZZ",
		"AA-BB-CC-DD-EE-FF",
	}
	for _, c := range cases {
		if ValidMAC(c) {
			t.Errorf("expected ValidMAC(%q)=false", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Live-hardware tests (skipped without bluetoothctl)
// ---------------------------------------------------------------------------

func TestAvailable_LiveSkip(t *testing.T) {
	if _, err := exec.LookPath("bluetoothctl"); err != nil {
		t.Skip("bluetoothctl not in PATH")
	}
	svc := New()
	// Just ensure Available() doesn't panic; result depends on hardware.
	_ = svc.Available()
}
