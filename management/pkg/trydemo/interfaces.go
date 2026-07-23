package trydemo

import (
	"context"
	"time"
)

// Store: persistent per-IP counters + monthly accumulator. Pure-Go SQLite.
type Store interface {
	LastDriverClaim(ctx context.Context, ip string) (time.Time, error)
	RecordDriverClaim(ctx context.Context, ip string, at time.Time) error

	AccumulateUsage(ctx context.Context, yyyymm string, computeMinDelta float64, egressBytesDelta int64, estCostDelta float64) error
	MonthlyCostUSD(ctx context.Context, yyyymm string) (float64, error)

	LogEvent(ctx context.Context, eventType, ip, sessionID, reason string, at time.Time) error
	Close() error
}

// DemoMachines: minimal demo-machine control client (Start/Stop/Restart by
// app+machine id). Real impl uses the Fly Machines REST API + FLY_API_TOKEN
// env. A NoopDemoMachines is supplied for tests (mark machine up/down
// in-memory).
type DemoMachines interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error // for reset-between-drivers
	Status(ctx context.Context) (MachineStatus, error)
}

// MachineStatus is the current state of the demo instance.
type MachineStatus struct {
	Started   bool
	StartedAt time.Time
	Region    string
}

// Queue: in-memory authoritative queue + viewer set. ONE instance per
// process (the demo is a singleton). All methods are safe for concurrent use.
type Queue interface {
	// Join the queue or become a viewer immediately if a spectator seat is
	// open. Returns the session token, the assigned role
	// (RoleSpectator | RoleQueued | RoleDriver), and the queue position
	// (1-based; 0 if not queued).
	Join(ctx context.Context, ip string) (token string, role Role, pos int, err error)

	// Heartbeat keeps a session alive (called by the SPA on a 10s ticker).
	// For the driver, also counts as "non-idle" input only if `input=true`
	// (input events post separately; heartbeat alone does NOT reset idle).
	Heartbeat(ctx context.Context, token string) error

	// Leave releases the seat / queue slot. Idempotent.
	Leave(ctx context.Context, token string) error

	// Promote moves the head of the queue into the driver seat IFF the
	// seat is empty AND the monthly $ ceiling is not engaged. Returns
	// the promoted token, or ("", nil) if no-op.
	Promote(ctx context.Context) (string, error)

	// RecordInput resets the driver's idle timer. No-op for non-driver tokens.
	RecordInput(ctx context.Context, token string) error

	// Snapshot returns the current viewer/queue state.
	Snapshot() Snapshot

	// RoleOf returns the current role for a token ("" if unknown).
	RoleOf(token string) Role

	// SecsLeft returns the driver's remaining session seconds (0 if no driver).
	SecsLeft() int
}

// Role represents a session's position in the demo.
type Role int

const (
	RoleNone      Role = iota
	RoleDriver         // has keyboard + mouse
	RoleSpectator      // watching, spectator seat
	RoleQueued         // waiting in queue
)

// Snapshot is the JSON shape returned by GET /api/try/status.
type Snapshot struct {
	DriverActive   bool `json:"driver_active"`
	SpectatorCount int  `json:"spectator_count"`
	QueueLength    int  `json:"queue_length"`
	DriverSecsLeft int  `json:"driver_secs_left"`
	MachineWarm    bool `json:"machine_warm"`
	CostCapEngaged bool `json:"cost_cap_engaged"`
	YourPosition   int  `json:"your_position"` // populated per-request from token
	YourRole       Role `json:"your_role"`
}

// CostGuard: read monthly accumulator + decide if cap engaged.
type CostGuard interface {
	Engaged(ctx context.Context) (bool, error)
	Tick(ctx context.Context, computeMin float64, egressBytes int64) error
}
