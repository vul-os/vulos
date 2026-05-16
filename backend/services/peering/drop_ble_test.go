package peering

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// dropBLEFakeAdapter implements dropBLEAdapter for tests.
// Available() returns the value of the available field; other operations
// record calls and deliver injected advertisements.
type dropBLEFakeAdapter struct {
	mu        sync.Mutex
	available bool

	advertiseStarted bool
	advertiseStopped int
	lastServiceUUID  string
	lastPayload      []byte

	scanStarted bool
	scanStopped int
	scanCB      func(dropBLEAdvertisement)
}

func (f *dropBLEFakeAdapter) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *dropBLEFakeAdapter) StartAdvertise(serviceUUID string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advertiseStarted = true
	f.lastServiceUUID = serviceUUID
	f.lastPayload = append([]byte(nil), payload...)
	return nil
}

func (f *dropBLEFakeAdapter) StopAdvertise() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advertiseStopped++
	return nil
}

func (f *dropBLEFakeAdapter) StartScan(cb func(dropBLEAdvertisement)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanStarted = true
	f.scanCB = cb
	return nil
}

func (f *dropBLEFakeAdapter) StopScan() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanStopped++
	return nil
}

// injectAdvertisement invokes the scan callback with adv (simulates a remote
// device being discovered).
func (f *dropBLEFakeAdapter) injectAdvertisement(adv dropBLEAdvertisement) {
	f.mu.Lock()
	cb := f.scanCB
	f.mu.Unlock()
	if cb != nil {
		cb(adv)
	}
}

// dropBLENewTestBLEService creates a DropBLEService with a fake adapter and a
// real DropService (also using no-op mDNS) for integration testing.
func dropBLENewTestBLEService(t *testing.T, adapterAvailable bool) (*DropBLEService, *DropService, *dropBLEFakeAdapter) {
	t.Helper()
	ds := NewDropService("vula:ed25519:testkey", "Tester", nil, nil)
	ds.mdnsNew = dropNoopMDNSFactory

	fa := &dropBLEFakeAdapter{available: adapterAvailable}
	bs := &DropBLEService{
		selfVulaID:  "vula:ed25519:testkey",
		selfDisplay: "Tester",
		drop:        ds,
		adapter:     fa,
	}
	return bs, ds, fa
}

// ---------------------------------------------------------------------------
// Tests: no-op adapter
// ---------------------------------------------------------------------------

// TestDropBLENoopAdapterAvailable verifies that dropBLENoopAdapter reports
// Available() == false and all other methods succeed without error.
func TestDropBLENoopAdapterAvailable(t *testing.T) {
	var a dropBLENoopAdapter
	if a.Available() {
		t.Fatal("dropBLENoopAdapter.Available() should return false")
	}
	if err := a.StartAdvertise("uuid", []byte{1, 2, 3}); err != nil {
		t.Fatalf("StartAdvertise: %v", err)
	}
	if err := a.StopAdvertise(); err != nil {
		t.Fatalf("StopAdvertise: %v", err)
	}
	if err := a.StartScan(func(dropBLEAdvertisement) {}); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	if err := a.StopScan(); err != nil {
		t.Fatalf("StopScan: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Start / Stop lifecycle
// ---------------------------------------------------------------------------

// TestDropBLEStartNoHardware verifies that Start is a clean no-op when the
// adapter reports no BLE hardware.
func TestDropBLEStartNoHardware(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, false /* unavailable */)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)

	if bs.DropBLEIsRunning() {
		t.Fatal("service should not be running without BLE hardware")
	}
	fa.mu.Lock()
	advertised := fa.advertiseStarted
	scanned := fa.scanStarted
	fa.mu.Unlock()
	if advertised || scanned {
		t.Fatal("adapter should not be touched when hardware is absent")
	}
}

// TestDropBLEStartWithHardware verifies that Start begins advertising and
// scanning when BLE hardware is available.
func TestDropBLEStartWithHardware(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, true /* available */)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	// Give background goroutines a moment to begin.
	time.Sleep(50 * time.Millisecond)

	if !bs.DropBLEIsRunning() {
		t.Fatal("service should be running with BLE hardware")
	}
	fa.mu.Lock()
	advertised := fa.advertiseStarted
	scanned := fa.scanStarted
	fa.mu.Unlock()
	if !advertised {
		t.Fatal("adapter.StartAdvertise should have been called")
	}
	if !scanned {
		t.Fatal("adapter.StartScan should have been called")
	}
}

// TestDropBLEStartIdempotent verifies that calling Start twice does not
// launch duplicate goroutines or call the adapter twice.
func TestDropBLEStartIdempotent(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	bs.Start(ctx) // second call must be a no-op
	time.Sleep(50 * time.Millisecond)

	fa.mu.Lock()
	// StopAdvertise is called once by the first refresh before re-starting.
	// StartScan should have been called only once.
	scanStarts := 0
	if fa.scanStarted {
		scanStarts = 1
	}
	fa.mu.Unlock()
	if scanStarts != 1 {
		t.Fatalf("StartScan should be called exactly once, got %d", scanStarts)
	}
}

// TestDropBLEStop verifies that Stop halts the service and calls StopAdvertise
// and StopScan.
func TestDropBLEStop(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	bs.Stop()

	if bs.DropBLEIsRunning() {
		t.Fatal("service should not be running after Stop()")
	}
	fa.mu.Lock()
	stopped := fa.advertiseStopped > 0 && fa.scanStopped > 0
	fa.mu.Unlock()
	if !stopped {
		t.Fatal("Stop() must call StopAdvertise and StopScan on the adapter")
	}
}

// TestDropBLEStopBeforeStart verifies that Stop is safe to call even if Start
// was never called (e.g., no hardware path).
func TestDropBLEStopBeforeStart(t *testing.T) {
	bs, _, _ := dropBLENewTestBLEService(t, false)
	bs.Stop() // must not panic
}

// ---------------------------------------------------------------------------
// Tests: advertisement payload
// ---------------------------------------------------------------------------

// TestDropBLEPayloadServiceUUID verifies that StartAdvertise is called with the
// canonical Vula Drop service UUID.
func TestDropBLEPayloadServiceUUID(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	fa.mu.Lock()
	uuid := fa.lastServiceUUID
	fa.mu.Unlock()

	if uuid != dropBLEServiceUUID {
		t.Fatalf("advertised UUID = %q, want %q", uuid, dropBLEServiceUUID)
	}
}

// TestDropBLEPayloadLength verifies that the advertised payload is exactly
// dropBLEPayloadLen bytes.
func TestDropBLEPayloadLength(t *testing.T) {
	bs, _, fa := dropBLENewTestBLEService(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	fa.mu.Lock()
	payload := fa.lastPayload
	fa.mu.Unlock()

	if len(payload) != dropBLEPayloadLen {
		t.Fatalf("payload length = %d, want %d", len(payload), dropBLEPayloadLen)
	}
}

// ---------------------------------------------------------------------------
// Tests: payload rotation
// ---------------------------------------------------------------------------

// TestDropBLEComputePayloadDeterministic verifies that the same vulaID+epoch
// always produces the same payload.
func TestDropBLEComputePayloadDeterministic(t *testing.T) {
	vulaID := "vula:ed25519:abcdef"
	epoch := int64(42)

	p1 := dropBLEComputePayload(vulaID, epoch)
	p2 := dropBLEComputePayload(vulaID, epoch)
	if hex.EncodeToString(p1) != hex.EncodeToString(p2) {
		t.Fatal("same inputs must produce the same payload")
	}
}

// TestDropBLEComputePayloadRotates verifies that different epochs produce
// different payloads for the same vulaID.
func TestDropBLEComputePayloadRotates(t *testing.T) {
	vulaID := "vula:ed25519:abcdef"
	p1 := dropBLEComputePayload(vulaID, 1)
	p2 := dropBLEComputePayload(vulaID, 2)
	if hex.EncodeToString(p1) == hex.EncodeToString(p2) {
		t.Fatal("different epochs must produce different payloads")
	}
}

// TestDropBLEComputePayloadLen verifies the output is always dropBLEPayloadLen bytes.
func TestDropBLEComputePayloadLen(t *testing.T) {
	for _, id := range []string{"", "a", "vula:ed25519:longkey12345678901234567890"} {
		p := dropBLEComputePayload(id, 0)
		if len(p) != dropBLEPayloadLen {
			t.Errorf("payload len for %q = %d, want %d", id, len(p), dropBLEPayloadLen)
		}
	}
}

// TestDropBLECurrentEpoch verifies that two times anchored to the same epoch
// boundary share an epoch, and times in different windows differ.
func TestDropBLECurrentEpoch(t *testing.T) {
	// Anchor to a known epoch boundary: use a fixed time whose nanoseconds are
	// a multiple of dropBLERotateInterval, then add a small delta that keeps
	// both t1 and t2 within that same epoch window.
	intervalNs := dropBLERotateInterval.Nanoseconds()
	// Round down to the start of an epoch window.
	nowNs := time.Now().UnixNano()
	windowStart := (nowNs / intervalNs) * intervalNs
	t1 := time.Unix(0, windowStart+1)            // 1 ns into the window
	t2 := time.Unix(0, windowStart+intervalNs/2) // halfway through the window

	e1 := dropBLECurrentEpoch(t1)
	e2 := dropBLECurrentEpoch(t2)
	if e1 != e2 {
		t.Fatalf("times within the same window should share epoch: e1=%d e2=%d", e1, e2)
	}

	// A time in the next window must have a different epoch.
	t3 := time.Unix(0, windowStart+intervalNs+1)
	e3 := dropBLECurrentEpoch(t3)
	if e1 == e3 {
		t.Fatalf("times in different windows should differ: e1=%d e3=%d", e1, e3)
	}
}

// ---------------------------------------------------------------------------
// Tests: scan — peer surfaces into DropService nearby list
// ---------------------------------------------------------------------------

// TestDropBLEScanSurfacesPeerByPayload verifies that a BLE advertisement with
// a known payload registers a peer in the DropService nearby list.
func TestDropBLEScanSurfacesPeerByPayload(t *testing.T) {
	bs, ds, fa := dropBLENewTestBLEService(t, true)
	ds.settings.Discoverability = dropDiscoverEveryone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// Inject a BLE advertisement with a Vula service UUID and a payload.
	payload := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}
	fa.injectAdvertisement(dropBLEAdvertisement{
		ServiceUUID: dropBLEServiceUUID,
		Payload:     payload,
		LocalName:   "vula-ble-deadbeef",
		RSSI:        -65,
	})

	nearby := ds.DropNearby()
	if len(nearby) == 0 {
		t.Fatal("expected at least one peer in nearby list after BLE scan")
	}
	expectedToken := "vula-ble:" + hex.EncodeToString(payload[:dropBLEPayloadLen])
	found := false
	for _, p := range nearby {
		if p.VulaID == expectedToken {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("peer token %q not found in nearby list; got %+v", expectedToken, nearby)
	}
}

// TestDropBLEScanSurfacesPeerByLocalName verifies that when the payload is
// absent, the LocalName is used as the peer token.
func TestDropBLEScanSurfacesPeerByLocalName(t *testing.T) {
	bs, ds, fa := dropBLENewTestBLEService(t, true)
	ds.settings.Discoverability = dropDiscoverEveryone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	fa.injectAdvertisement(dropBLEAdvertisement{
		ServiceUUID: dropBLEServiceUUID,
		Payload:     nil, // no payload
		LocalName:   "vula-ble-somename",
	})

	nearby := ds.DropNearby()
	expectedToken := "vula-ble:vula-ble-somename"
	found := false
	for _, p := range nearby {
		if p.VulaID == expectedToken {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("peer token %q not found; got %+v", expectedToken, nearby)
	}
}

// TestDropBLEScanIgnoresWrongUUID verifies that advertisements with a
// non-Vula service UUID are not surfaced into the nearby list.
func TestDropBLEScanIgnoresWrongUUID(t *testing.T) {
	bs, ds, fa := dropBLENewTestBLEService(t, true)
	ds.settings.Discoverability = dropDiscoverEveryone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	fa.injectAdvertisement(dropBLEAdvertisement{
		ServiceUUID: "0000-1234-0000-0000-0000-0000-0000-0000",
		LocalName:   "some-other-device",
	})

	nearby := ds.DropNearby()
	if len(nearby) != 0 {
		t.Fatalf("non-Vula advertisement should not surface a peer; got %+v", nearby)
	}
}

// TestDropBLEScanIgnoresEmptyAdvertisement verifies that advertisements with
// neither payload nor LocalName are silently dropped.
func TestDropBLEScanIgnoresEmptyAdvertisement(t *testing.T) {
	bs, ds, fa := dropBLENewTestBLEService(t, true)
	ds.settings.Discoverability = dropDiscoverEveryone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	fa.injectAdvertisement(dropBLEAdvertisement{
		ServiceUUID: dropBLEServiceUUID,
		// No payload, no LocalName.
	})

	nearby := ds.DropNearby()
	if len(nearby) != 0 {
		t.Fatalf("empty advertisement should not surface a peer; got %+v", nearby)
	}
}

// ---------------------------------------------------------------------------
// Tests: scan — no-op when hardware absent
// ---------------------------------------------------------------------------

// TestDropBLEScanNoHardwareNoOp verifies that BLE scanning is skipped entirely
// when the adapter reports no hardware, and the nearby list stays empty.
func TestDropBLEScanNoHardwareNoOp(t *testing.T) {
	bs, ds, _ := dropBLENewTestBLEService(t, false)
	ds.settings.Discoverability = dropDiscoverEveryone

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bs.Start(ctx)

	nearby := ds.DropNearby()
	if len(nearby) != 0 {
		t.Fatalf("no hardware: nearby list should be empty, got %+v", nearby)
	}
}

// ---------------------------------------------------------------------------
// Tests: string helpers
// ---------------------------------------------------------------------------

func TestDropBLESplitLines(t *testing.T) {
	lines := dropBLESplitLines("a\nb\nc")
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Fatalf("unexpected: %v", lines)
	}
	if len(dropBLESplitLines("")) != 0 {
		t.Fatal("empty string should yield no lines")
	}
}

func TestDropBLETrimSpace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  hello  ", "hello"},
		{"\t  \t", ""},
		{"no-spaces", "no-spaces"},
	}
	for _, c := range cases {
		if got := dropBLETrimSpace(c.in); got != c.want {
			t.Errorf("TrimSpace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDropBLEHasPrefix(t *testing.T) {
	if !dropBLEHasPrefix("vula-ble-abc", "vula-ble-") {
		t.Fatal("expected prefix match")
	}
	if dropBLEHasPrefix("other", "vula-ble-") {
		t.Fatal("unexpected prefix match")
	}
}

func TestDropBLESplitN(t *testing.T) {
	parts := dropBLESplitN("Device AA:BB:CC Name", " ", 3)
	if len(parts) != 3 || parts[0] != "Device" || parts[1] != "AA:BB:CC" || parts[2] != "Name" {
		t.Fatalf("unexpected: %v", parts)
	}
}

func TestDropBLEContains(t *testing.T) {
	if !dropBLEContains("Powered: yes", "Powered: yes") {
		t.Fatal("expected match")
	}
	if dropBLEContains("Powered: no", "Powered: yes") {
		t.Fatal("unexpected match")
	}
	if !dropBLEContains("anything", "") {
		t.Fatal("empty substr always matches")
	}
}

// ---------------------------------------------------------------------------
// Tests: OS adapter — skip if bluetoothctl unavailable
// ---------------------------------------------------------------------------

// TestDropBLEOSAdapterAvailableSkip attempts to check BLE hardware presence
// using the real OS adapter.  It skips if bluetoothctl is not installed,
// allowing the test to pass in CI environments without Bluetooth hardware.
func TestDropBLEOSAdapterAvailableSkip(t *testing.T) {
	a := &dropBLEOSAdapter{}
	// Save and restore the exec function to use the real one only in this test.
	orig := dropBLEExecFn
	defer func() { dropBLEExecFn = orig }()

	// If bluetoothctl is unavailable, Available() must return false (no panic).
	dropBLEExecFn = func(timeout time.Duration, name string, args ...string) (string, error) {
		return "", context.DeadlineExceeded // simulate missing binary
	}
	if a.Available() {
		t.Fatal("Available() should return false when command fails")
	}
}

// TestDropBLEOSAdapterStartAdvertiseNoHW verifies that StartAdvertise degrades
// gracefully when bluetoothctl fails.
func TestDropBLEOSAdapterStartAdvertiseNoHW(t *testing.T) {
	orig := dropBLEExecFn
	defer func() { dropBLEExecFn = orig }()
	dropBLEExecFn = func(timeout time.Duration, name string, args ...string) (string, error) {
		return "", context.DeadlineExceeded
	}

	a := &dropBLEOSAdapter{}
	// StartAdvertise returns an error when bluetoothctl fails.
	err := a.StartAdvertise(dropBLEServiceUUID, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	if err == nil {
		t.Fatal("StartAdvertise should return error when bluetoothctl fails")
	}
}

// TestDropBLEOSAdapterPollDevicesFilters verifies that dropBLEPollDevices only
// surfaces devices whose name starts with "vula-ble-".
func TestDropBLEOSAdapterPollDevicesFilters(t *testing.T) {
	orig := dropBLEExecFn
	defer func() { dropBLEExecFn = orig }()

	dropBLEExecFn = func(_ time.Duration, _ string, _ ...string) (string, error) {
		return "Device AA:BB:CC:DD:EE:FF vula-ble-deadbeef\n" +
			"Device 11:22:33:44:55:66 SomeOtherDevice\n", nil
	}

	var received []dropBLEAdvertisement
	dropBLEPollDevices(func(adv dropBLEAdvertisement) {
		received = append(received, adv)
	})

	if len(received) != 1 {
		t.Fatalf("expected 1 Vula device, got %d: %v", len(received), received)
	}
	if received[0].LocalName != "vula-ble-deadbeef" {
		t.Fatalf("unexpected device name: %q", received[0].LocalName)
	}
}
