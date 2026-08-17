package bluetooth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const fixtureShow = `Controller AA:BB:CC:DD:EE:FF (public)
	Name: VulosOS-BT
	Alias: VulosOS-BT
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
	if st.Name != "VulosOS-BT" {
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

// ---------------------------------------------------------------------------
// "I could not ask" must not be served as an answer
//
// Every test below drives a FAILURE path. They exist because this package used
// to have none: `GetStatus` swallowed a bluetoothctl error and returned a zero
// Status, which cmd/server wrote as HTTP 200 `{"powered":false,"devices":null}`
// — byte-identical to a healthy adapter that is switched off with nothing
// paired. The Settings panel disables its toggle when `powered` is absent, on
// the principle that a radio whose state is unknown is not a radio you can be
// asked to toggle; because the box never said "I don't know", that state could
// not be reached from the real backend at all.
//
// Each failure test is paired with a SUCCESS test asserting the same code path
// still produces a real answer, so none of this can be satisfied by making
// GetStatus always fail.
// ---------------------------------------------------------------------------

// fakeRunner is a scripted bluetoothctl. `fail` names the subcommands that
// error; everything else is answered from `out`. It records every invocation
// so a test can assert the call actually happened rather than passing because
// nothing ran.
type fakeRunner struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (f *fakeRunner) run(args ...string) (string, error) {
	key := args[0]
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.fail[key] {
		return "", fmt.Errorf("fake: %s refused", key)
	}
	return f.out[key], nil
}

func (f *fakeRunner) called(sub string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, sub) {
			return true
		}
	}
	return false
}

// healthyRunner answers every call the way a live box would.
func healthyRunner() *fakeRunner {
	return &fakeRunner{
		out: map[string]string{
			"show":    fixtureShow,
			"devices": fixtureDevices,
			"info":    fixtureInfoAudio,
		},
		fail: map[string]bool{},
	}
}

func TestGetStatus_ShowFails_ReportsUnavailableNotPoweredOff(t *testing.T) {
	f := healthyRunner()
	f.fail["show"] = true
	svc := newWithRunner(f.run)

	st, err := svc.GetStatus(context.Background())
	if err == nil {
		t.Fatalf("GetStatus returned nil error when `bluetoothctl show` failed; it reported "+
			"powered=%v devices=%d as if measured. A dead bluetoothd must not be served as a "+
			"powered-off radio — the caller cannot tell the two apart and one of them is a lie.",
			st.Powered, len(st.Devices))
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error must wrap ErrUnavailable so the HTTP layer can answer 503 rather than "+
			"500 or 200; got %v", err)
	}
	if !f.called("show") {
		t.Error("test is vacuous: bluetoothctl show was never invoked")
	}
}

func TestGetStatus_DevicesFails_ReportsUnavailableNotEmptyList(t *testing.T) {
	f := healthyRunner()
	f.fail["devices"] = true
	svc := newWithRunner(f.run)

	st, err := svc.GetStatus(context.Background())
	if err == nil {
		t.Fatalf("GetStatus returned nil error when `bluetoothctl devices` failed, with %d devices. "+
			"An empty device list because the daemon is dead and an empty device list because "+
			"nothing is nearby are different facts.", len(st.Devices))
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error must wrap ErrUnavailable; got %v", err)
	}
	if !f.called("devices") {
		t.Error("test is vacuous: bluetoothctl devices was never invoked")
	}
}

// The non-vacuity control for both tests above: a box that answers must still
// produce a measured Status, including the genuinely powered-off case, so the
// fix cannot be "return an error whatever happens".
func TestGetStatus_PoweredOffIsStillAMeasuredAnswer(t *testing.T) {
	f := &fakeRunner{
		out:  map[string]string{"show": fixtureShowOff, "devices": ""},
		fail: map[string]bool{},
	}
	svc := newWithRunner(f.run)

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("a box that answered must not report unavailable: %v", err)
	}
	if st.Powered {
		t.Error("Powered=true for the powered-off fixture")
	}
	if len(st.Devices) != 0 {
		t.Errorf("expected 0 devices for an empty listing, got %d", len(st.Devices))
	}
	if st.Address != "11:22:33:44:55:66" {
		t.Errorf("Address=%q — the measured fields must survive the change", st.Address)
	}
}

func TestGetStatus_HealthyBoxReturnsEveryDevice(t *testing.T) {
	f := healthyRunner()
	svc := newWithRunner(f.run)

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("healthy box reported unavailable: %v", err)
	}
	if !st.Powered {
		t.Error("Powered=false for the powered-on fixture")
	}
	// fixtureDevices has three "Device " lines; asserting the count keeps a
	// listing loop that ran zero times from passing as success.
	if len(st.Devices) != 3 {
		t.Fatalf("expected 3 devices from fixtureDevices, got %d", len(st.Devices))
	}
	for _, d := range st.Devices {
		if d.Unmeasured {
			t.Errorf("device %s marked Unmeasured though every info call succeeded", d.Address)
		}
	}
}

func TestListDevices_InfoFails_MarksDeviceUnmeasured(t *testing.T) {
	f := healthyRunner()
	f.fail["info"] = true
	svc := newWithRunner(f.run)

	devs, err := svc.listDevices(context.Background())
	if err != nil {
		t.Fatalf("a failed per-device info must not blank the whole listing: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devs))
	}
	for _, d := range devs {
		if !d.Unmeasured {
			t.Errorf("device %s: info failed but Unmeasured=false, so paired=%v connected=%v "+
				"trusted=%v type=%q are reported as observations nobody made",
				d.Address, d.Paired, d.Connected, d.Trusted, d.Type)
		}
		if d.Paired || d.Connected || d.Trusted {
			t.Errorf("device %s: flags set from an info call that failed", d.Address)
		}
		if d.Address == "" || d.Name == "" {
			t.Errorf("device %s: address/name come from the listing that SUCCEEDED and must survive", d.Address)
		}
	}
	if !f.called("info") {
		t.Error("test is vacuous: bluetoothctl info was never invoked")
	}
}

func TestListDevices_InfoSucceeds_DeviceIsMeasured(t *testing.T) {
	f := healthyRunner()
	svc := newWithRunner(f.run)

	devs, err := svc.listDevices(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devs))
	}
	if devs[0].Unmeasured {
		t.Error("info succeeded but the device is flagged Unmeasured")
	}
	if !devs[0].Paired || !devs[0].Connected || devs[0].Type != "audio" {
		t.Errorf("measured flags lost: %+v", devs[0])
	}
}

// The wire shape is the contract the Settings panel reads, so assert it
// directly rather than trusting the struct.
func TestDeviceJSON_UnmeasuredIsAbsentWhenMeasured(t *testing.T) {
	b, err := json.Marshal(Device{Address: "AA", Name: "n", Paired: true, Type: "audio"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "unmeasured") {
		t.Errorf("a measured device must serialise exactly as before this change, so existing "+
			"callers are unaffected; got %s", b)
	}
}

func TestDeviceJSON_UnmeasuredIsPresentWhenUnmeasured(t *testing.T) {
	b, err := json.Marshal(Device{Address: "AA", Name: "n", Unmeasured: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"unmeasured":true`) {
		t.Errorf("an unmeasured device must say so on the wire; got %s", b)
	}
}

// Pair's follow-on `trust` failing must not be reported as a failure to pair —
// the device IS paired — but it must not silently vanish either.
func TestPair_TrustFailureDoesNotFailThePairing(t *testing.T) {
	f := healthyRunner()
	f.fail["trust"] = true
	svc := newWithRunner(f.run)

	if err := svc.Pair(context.Background(), "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatalf("pair succeeded but was reported as failed because trust failed: %v", err)
	}
	if !f.called("trust") {
		t.Error("test is vacuous: bluetoothctl trust was never invoked")
	}
}

func TestPair_PairFailureIsReported(t *testing.T) {
	f := healthyRunner()
	f.fail["pair"] = true
	svc := newWithRunner(f.run)

	if err := svc.Pair(context.Background(), "AA:BB:CC:DD:EE:FF"); err == nil {
		t.Error("a refused pairing reported success")
	}
}

// The remaining mutators already returned their errors; these pin that, since
// cmd/server now propagates them instead of discarding them.
func TestMutatorsReportFailure(t *testing.T) {
	cases := []struct {
		name string
		sub  string
		call func(*Service) error
	}{
		{"StartDiscovery", "scan", func(s *Service) error { return s.StartDiscovery(context.Background()) }},
		{"StopDiscovery", "scan", func(s *Service) error { return s.StopDiscovery(context.Background()) }},
		{"Disconnect", "disconnect", func(s *Service) error { return s.Disconnect(context.Background(), "AA") }},
		{"Remove", "remove", func(s *Service) error { return s.Remove(context.Background(), "AA") }},
		{"SetPower", "power", func(s *Service) error { return s.SetPower(context.Background(), true) }},
		{"Connect", "connect", func(s *Service) error { return s.Connect(context.Background(), "AA") }},
	}
	if len(cases) != 6 {
		t.Fatalf("expected 6 mutators under test, got %d", len(cases))
	}
	for _, tc := range cases {
		f := healthyRunner()
		f.fail[tc.sub] = true
		if err := tc.call(newWithRunner(f.run)); err == nil {
			t.Errorf("%s: bluetoothctl %s failed but the method reported success", tc.name, tc.sub)
		}
		if !f.called(tc.sub) {
			t.Errorf("%s: test is vacuous, bluetoothctl %s was never invoked", tc.name, tc.sub)
		}
	}
}
