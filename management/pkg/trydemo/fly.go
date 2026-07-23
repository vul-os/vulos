package trydemo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// flyClient is the real Fly Machines API implementation of DemoMachines. It
// uses the v1 REST API authenticated via FLY_API_TOKEN.
//
// The demo runs as a single Fly app with one machine. Start/Stop map to
// starting/stopping that machine; Restart restarts it (used to reset state
// between drivers); Status reads the machine's reported state.
//
// Wire reference (Fly Machines API, https://fly.io/docs/machines/api/,
// base https://api.machines.dev/v1; mirrors compute/fly.go +
// managed/provider_fly.go). Auth: bearer FLY_API_TOKEN.
//
//	POST /v1/apps/{app}/machines/{id}/start   — start a stopped machine (Start).
//	POST /v1/apps/{app}/machines/{id}/stop    — stop a running machine (Stop).
//	POST /v1/apps/{app}/machines/{id}/restart — restart the machine (Restart / reset).
//	GET  /v1/apps/{app}/machines/{id}         — read the machine + its state.
type flyClient struct {
	token     string
	app       string // Fly app slug
	machineID string // Fly machine id (the unit of control)
	baseURL   string // API root; overridable in tests
	hc        *http.Client
}

// flyAPIBase is the production Fly Machines API root.
const flyAPIBase = "https://api.machines.dev/v1"

// NewFlyClient returns a DemoMachines backed by the real Fly Machines REST API.
// token = FLY_API_TOKEN, app = Fly app slug, machineID = the demo machine id
// (the unit of control). An empty machineID disables Start/Stop/Restart (they
// return a clear error) but Status still reports not-started.
func NewFlyClient(token, app, machineID string) DemoMachines {
	return &flyClient{
		token:     token,
		app:       app,
		machineID: machineID,
		baseURL:   flyAPIBase,
		hc:        &http.Client{Timeout: 15 * time.Second},
	}
}

// machineURL builds the Fly Machines API URL for an optional action on the
// configured app+machine. action == "" → the bare machine resource (GET state).
func (f *flyClient) machineURL(action string) string {
	base := f.baseURL
	if base == "" {
		base = flyAPIBase
	}
	if action == "" {
		return fmt.Sprintf("%s/apps/%s/machines/%s", base, f.app, f.machineID)
	}
	return fmt.Sprintf("%s/apps/%s/machines/%s/%s", base, f.app, f.machineID, action)
}

func (f *flyClient) doPost(ctx context.Context, url string) error {
	if f.machineID == "" {
		return fmt.Errorf("fly: no demo machine id configured (set TRY_DEMO_FLY_MACHINE_ID)")
	}
	// Fly's machine lifecycle endpoints (start/stop/restart) take no body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("fly: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.hc.Do(req)
	if err != nil {
		return fmt.Errorf("fly: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fly: unexpected status %d for %s", resp.StatusCode, url)
	}
	return nil
}

func (f *flyClient) Start(ctx context.Context) error {
	// Start brings a stopped machine back up.
	return f.doPost(ctx, f.machineURL("start"))
}

func (f *flyClient) Stop(ctx context.Context) error {
	// Stop gracefully stops the machine (the scale-to-zero / idle-stop path).
	return f.doPost(ctx, f.machineURL("stop"))
}

func (f *flyClient) Restart(ctx context.Context) error {
	// Restart cycles the machine, which resets demo state between drivers.
	return f.doPost(ctx, f.machineURL("restart"))
}

func (f *flyClient) Status(ctx context.Context) (MachineStatus, error) {
	if f.machineID == "" {
		// No machine configured → report not started rather than erroring.
		return MachineStatus{Started: false, Region: ""}, nil
	}
	url := f.machineURL("")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return MachineStatus{}, fmt.Errorf("fly: status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)

	resp, err := f.hc.Do(req)
	if err != nil {
		return MachineStatus{}, fmt.Errorf("fly: status do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return MachineStatus{}, fmt.Errorf("fly: status unexpected %d", resp.StatusCode)
	}

	// GET /v1/apps/{app}/machines/{id} returns the machine object. We read the
	// reported state, region, and creation timestamp.
	var body struct {
		State     string `json:"state"`      // e.g. "started", "stopped", "starting"
		Region    string `json:"region"`     //
		CreatedAt string `json:"created_at"` // RFC3339
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return MachineStatus{}, fmt.Errorf("fly: status decode: %w", err)
	}

	var startedAt time.Time
	if body.CreatedAt != "" {
		startedAt, _ = time.Parse(time.RFC3339, body.CreatedAt)
	}
	return MachineStatus{
		Started:   flyStarted(body.State),
		StartedAt: startedAt,
		Region:    body.Region,
	}, nil
}

// flyStarted maps a Fly machine state string to "is the demo machine up".
// These are the live machine states from the Fly Machines API.
func flyStarted(state string) bool {
	switch state {
	case "started", "starting", "running", "replacing":
		return true
	default:
		return false
	}
}
