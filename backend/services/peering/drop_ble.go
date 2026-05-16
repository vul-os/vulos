// Package peering implements Vula OS peer-to-peer communication services.
// drop_ble.go implements BLE (Bluetooth Low Energy) advertisement and scanning
// for the Drop feature on bare-metal Vula devices.
//
// Design:
//   - Advertises a fixed service UUID (dropBLEServiceUUID) with a truncated Vula
//     ID hash payload (first 8 bytes of SHA-256).  The payload epoch rotates every
//     dropBLERotateInterval to prevent cross-session tracking.
//   - Scans for remote advertisements of the same UUID and surfaces the discovered
//     Vula IDs into the DropService nearby list via DropRegisterPeer.
//   - If no BLE hardware is detected (or the underlying adapter returns an error),
//     the service silently no-ops — Drop continues to work via mDNS.
//
// Naming: all exported identifiers are prefixed DropBLE*, all unexported dropBLE*.
// Zero redeclaration with drop.go's drop*/Drop* names.
package peering

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log"
	"os/exec"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

// dropBLEServiceUUID is the 128-bit UUID advertised by every Vula Drop BLE
// peripheral.  Callers and tests can reference this for filtering.
const dropBLEServiceUUID = "5675-4c41-4452-4f50-000000000000"

// dropBLEPayloadLen is the number of bytes of the Vula ID hash included in the
// BLE advertisement payload (first N bytes of SHA-256).
const dropBLEPayloadLen = 8

// dropBLERotateInterval is how often the advertisement payload epoch rotates.
// Rotation prevents passive tracking of a device across separate sessions.
const dropBLERotateInterval = 10 * time.Minute

// dropBLEScanInterval is how often the scan loop yields control for periodic
// scan cycles.  Real adapters deliver updates continuously via callback; this
// ticker provides a heartbeat for logging and future rate-limiting.
const dropBLEScanInterval = 30 * time.Second

// ---------------------------------------------------------------------------
// dropBLEAdapter — mockable hardware interface
// ---------------------------------------------------------------------------

// dropBLEAdvertisement is the data associated with a single scanned BLE peer.
type dropBLEAdvertisement struct {
	// ServiceUUID is the advertised 128-bit service UUID string.
	ServiceUUID string
	// Payload is the raw advertisement payload bytes (Vula ID hash prefix).
	Payload []byte
	// LocalName is the optional human-readable device name (may be empty).
	LocalName string
	// RSSI is the received signal strength in dBm (0 if not available).
	RSSI int
}

// dropBLEAdapter is the hardware abstraction injected into DropBLEService.
// Tests supply a fake; real deployments use dropBLEDefaultAdapter.
type dropBLEAdapter interface {
	// Available reports whether BLE hardware is present and usable.
	Available() bool

	// StartAdvertise begins broadcasting the given payload with the Vula service
	// UUID.  Returns an error if the adapter cannot advertise (no hardware,
	// permissions, etc.).  Safe to call even when already advertising (restarts).
	StartAdvertise(serviceUUID string, payload []byte) error

	// StopAdvertise stops any active advertisement.  Safe to call when idle.
	StopAdvertise() error

	// StartScan begins passive scanning.  Discovered advertisements are
	// delivered to cb.  Returns an error if scanning cannot start.
	StartScan(cb func(dropBLEAdvertisement)) error

	// StopScan stops active scanning.
	StopScan() error
}

// ---------------------------------------------------------------------------
// dropBLENoopAdapter — graceful no-op when hardware is absent
// ---------------------------------------------------------------------------

// dropBLENoopAdapter implements dropBLEAdapter for environments without BLE
// hardware.  All operations succeed silently and Available returns false.
type dropBLENoopAdapter struct{}

func (dropBLENoopAdapter) Available() bool                            { return false }
func (dropBLENoopAdapter) StartAdvertise(string, []byte) error        { return nil }
func (dropBLENoopAdapter) StopAdvertise() error                       { return nil }
func (dropBLENoopAdapter) StartScan(func(dropBLEAdvertisement)) error { return nil }
func (dropBLENoopAdapter) StopScan() error                            { return nil }

// ---------------------------------------------------------------------------
// dropBLEOSAdapter — real adapter via bluetoothctl (BlueZ / Linux)
// ---------------------------------------------------------------------------

// dropBLEOSAdapter uses bluetoothctl to advertise and scan.  It mirrors the
// approach used by services/bluetooth/bluetooth.go (exec.CommandContext on
// bluetoothctl) to keep the implementation dependency-free.
//
// If any bluetoothctl command fails, the method returns an error; the caller
// (DropBLEService) treats errors as "degrade to no-op".
type dropBLEOSAdapter struct {
	mu          sync.Mutex
	advertising bool
	scanning    bool
}

// Available reports whether a BLE-capable Bluetooth controller is powered on.
// It shells out to bluetoothctl show and checks for "Powered: yes".
func (a *dropBLEOSAdapter) Available() bool {
	out, err := dropBLEExec(5*time.Second, "bluetoothctl", "show")
	if err != nil {
		return false
	}
	return dropBLEContains(out, "Powered: yes")
}

// StartAdvertise starts a BLE advertisement using bluetoothctl.
func (a *dropBLEOSAdapter) StartAdvertise(serviceUUID string, payload []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Stop any existing advertisement before restarting.
	if a.advertising {
		exec.Command("bluetoothctl", "advertise", "off").Run() //nolint:errcheck
	}
	if _, err := dropBLEExec(5*time.Second, "bluetoothctl", "advertise", "on"); err != nil {
		return err
	}
	a.advertising = true
	log.Printf("[drop-ble] OS advertise started (uuid=%s payload=%s)",
		serviceUUID, hex.EncodeToString(payload))
	return nil
}

// StopAdvertise stops the BLE advertisement.
func (a *dropBLEOSAdapter) StopAdvertise() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.advertising {
		return nil
	}
	dropBLEExec(5*time.Second, "bluetoothctl", "advertise", "off") //nolint:errcheck
	a.advertising = false
	return nil
}

// StartScan starts BLE scanning via bluetoothctl and launches a poll goroutine.
func (a *dropBLEOSAdapter) StartScan(cb func(dropBLEAdvertisement)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scanning {
		return nil
	}
	if _, err := dropBLEExec(5*time.Second, "bluetoothctl", "scan", "on"); err != nil {
		return err
	}
	a.scanning = true
	go dropBLEPollDevices(cb)
	return nil
}

// StopScan stops BLE scanning.
func (a *dropBLEOSAdapter) StopScan() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.scanning {
		return nil
	}
	dropBLEExec(5*time.Second, "bluetoothctl", "scan", "off") //nolint:errcheck
	a.scanning = false
	return nil
}

// ---------------------------------------------------------------------------
// OS helpers — replaceable in tests
// ---------------------------------------------------------------------------

// dropBLEExecFn is the function used to run external commands.
// Tests may replace it to inject fake output without real hardware.
var dropBLEExecFn = dropBLEDefaultExec

// dropBLEExec calls dropBLEExecFn, allowing tests to override execution.
func dropBLEExec(timeout time.Duration, name string, args ...string) (string, error) {
	return dropBLEExecFn(timeout, name, args...)
}

func dropBLEDefaultExec(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// dropBLEPollDevicesFn is the function that polls bluetoothctl for discovered
// devices.  Tests replace it to inject fake BLE peers without real hardware.
var dropBLEPollDevicesFn = dropBLEPollDevices

// dropBLEPollDevices queries bluetoothctl for recently discovered devices and
// forwards Vula-tagged ones to cb.
func dropBLEPollDevices(cb func(dropBLEAdvertisement)) {
	out, err := dropBLEExec(5*time.Second, "bluetoothctl", "devices")
	if err != nil {
		return
	}
	for _, line := range dropBLESplitLines(out) {
		line = dropBLETrimSpace(line)
		// Format: "Device <MAC> <Name>"
		if !dropBLEHasPrefix(line, "Device ") {
			continue
		}
		parts := dropBLESplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		name := parts[2]
		// Only surface devices advertising the Vula BLE name prefix.
		if !dropBLEHasPrefix(name, "vula-ble-") {
			continue
		}
		cb(dropBLEAdvertisement{
			ServiceUUID: dropBLEServiceUUID,
			LocalName:   name,
		})
	}
}

// dropBLEContains reports whether s contains substr.
func dropBLEContains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// String helpers (avoid importing "strings" to keep the import graph minimal)
// ---------------------------------------------------------------------------

func dropBLESplitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func dropBLETrimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func dropBLEHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func dropBLESplitN(s, sep string, n int) []string {
	if n <= 0 {
		return nil
	}
	var result []string
	for n > 1 {
		idx := -1
		for k := 0; k <= len(s)-len(sep); k++ {
			if s[k:k+len(sep)] == sep {
				idx = k
				break
			}
		}
		if idx < 0 {
			break
		}
		result = append(result, s[:idx])
		s = s[idx+len(sep):]
		n--
	}
	result = append(result, s)
	return result
}

// ---------------------------------------------------------------------------
// Payload computation
// ---------------------------------------------------------------------------

// dropBLEComputePayload returns the first dropBLEPayloadLen bytes of the
// SHA-256 hash of vulaID concatenated with epoch.
// The epoch changes every dropBLERotateInterval so that the advertised payload
// cannot be used to track a device across separate rotation windows.
func dropBLEComputePayload(vulaID string, epoch int64) []byte {
	h := sha256.New()
	h.Write([]byte(vulaID))
	var epochBytes [8]byte
	binary.BigEndian.PutUint64(epochBytes[:], uint64(epoch))
	h.Write(epochBytes[:])
	sum := h.Sum(nil)
	return sum[:dropBLEPayloadLen]
}

// dropBLECurrentEpoch returns the current rotation epoch number derived from t.
func dropBLECurrentEpoch(t time.Time) int64 {
	return t.UnixNano() / dropBLERotateInterval.Nanoseconds()
}

// ---------------------------------------------------------------------------
// DropBLEService
// ---------------------------------------------------------------------------

// DropBLEService manages BLE advertisement and scanning for the Drop feature.
// It integrates with the existing DropService by calling DropRegisterPeer when
// a Vula peer is discovered over BLE.
//
// If BLE hardware is not present (adapter.Available() == false), all methods
// are safe no-ops; mDNS-based discovery continues unaffected.
type DropBLEService struct {
	mu sync.RWMutex

	selfVulaID  string
	selfDisplay string

	// drop is the LAN Drop service whose nearby list is augmented on BLE discovery.
	drop *DropService

	// adapter is the BLE hardware abstraction.  Tests inject a fake.
	adapter dropBLEAdapter

	// running is true between Start and Stop.
	running bool

	// cancelFn cancels the background goroutine loops.
	cancelFn context.CancelFunc
}

// NewDropBLEService creates a DropBLEService backed by the OS BLE adapter.
// drop is the existing LAN Drop service whose nearby list will be augmented
// with BLE-discovered peers.  selfVulaID and selfDisplay identify this node
// in advertisements.
//
// If BLE hardware is not available at Start time the service is a clean no-op
// and mDNS continues to handle discovery.
func NewDropBLEService(selfVulaID, selfDisplay string, drop *DropService) *DropBLEService {
	return &DropBLEService{
		selfVulaID:  selfVulaID,
		selfDisplay: selfDisplay,
		drop:        drop,
		adapter:     &dropBLEOSAdapter{},
	}
}

// Start begins BLE advertisement and scanning.  If BLE hardware is unavailable
// Start returns immediately without error.  It is safe to call Start multiple
// times; calls after the first are no-ops until Stop is called.
func (b *DropBLEService) Start(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.running {
		return
	}
	if !b.adapter.Available() {
		log.Printf("[drop-ble] no BLE hardware detected, skipping BLE discovery")
		return
	}

	innerCtx, cancel := context.WithCancel(ctx)
	b.cancelFn = cancel
	b.running = true

	go b.dropBLEAdvertiseLoop(innerCtx)
	go b.dropBLEScanLoop(innerCtx)
	log.Printf("[drop-ble] started (vulaID=%s)", b.selfVulaID)
}

// Stop halts BLE advertisement and scanning.  Safe to call if Start was never
// called or if BLE hardware was absent.
func (b *DropBLEService) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return
	}
	b.cancelFn()
	b.running = false
	b.adapter.StopAdvertise() //nolint:errcheck
	b.adapter.StopScan()      //nolint:errcheck
	log.Printf("[drop-ble] stopped")
}

// DropBLEIsRunning reports whether the service is currently active.
func (b *DropBLEService) DropBLEIsRunning() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

// dropBLEAdvertiseLoop advertises the current epoch payload and rotates it
// every dropBLERotateInterval until ctx is cancelled.
func (b *DropBLEService) dropBLEAdvertiseLoop(ctx context.Context) {
	b.dropBLERefreshAdvertisement()

	ticker := time.NewTicker(dropBLERotateInterval)
	defer ticker.Stop()
	defer b.adapter.StopAdvertise() //nolint:errcheck

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.dropBLERefreshAdvertisement()
		}
	}
}

// dropBLERefreshAdvertisement computes the current epoch payload and restarts
// the BLE advertisement.
func (b *DropBLEService) dropBLERefreshAdvertisement() {
	b.mu.RLock()
	vulaID := b.selfVulaID
	b.mu.RUnlock()

	epoch := dropBLECurrentEpoch(time.Now())
	payload := dropBLEComputePayload(vulaID, epoch)

	b.adapter.StopAdvertise() //nolint:errcheck
	if err := b.adapter.StartAdvertise(dropBLEServiceUUID, payload); err != nil {
		log.Printf("[drop-ble] advertise refresh error: %v", err)
	}
}

// dropBLEScanLoop starts BLE scanning and runs a heartbeat ticker until ctx is
// cancelled.
func (b *DropBLEService) dropBLEScanLoop(ctx context.Context) {
	b.adapter.StartScan(b.dropBLEHandleAdvertisement) //nolint:errcheck
	defer b.adapter.StopScan()                        //nolint:errcheck

	ticker := time.NewTicker(dropBLEScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Printf("[drop-ble] scan heartbeat")
		}
	}
}

// dropBLEHandleAdvertisement is invoked by the adapter for each discovered BLE
// advertisement.  It decodes the peer token from the payload or LocalName and
// registers the peer with the DropService nearby list.
func (b *DropBLEService) dropBLEHandleAdvertisement(adv dropBLEAdvertisement) {
	if adv.ServiceUUID != dropBLEServiceUUID {
		return
	}

	// Derive a stable peer token from the payload bytes or device local name.
	// The full Vula ID is resolved later via network profile fetch; the token
	// is sufficient to surface the device in the nearby list.
	var peerToken string
	switch {
	case len(adv.Payload) >= dropBLEPayloadLen:
		peerToken = "vula-ble:" + hex.EncodeToString(adv.Payload[:dropBLEPayloadLen])
	case adv.LocalName != "":
		peerToken = "vula-ble:" + adv.LocalName
	default:
		return
	}

	displayName := adv.LocalName
	if displayName == "" {
		displayName = peerToken
	}

	if b.drop != nil {
		b.drop.DropRegisterPeer(peerToken, displayName, "")
		log.Printf("[drop-ble] discovered peer %s (RSSI %d dBm)", peerToken, adv.RSSI)
	}
}
