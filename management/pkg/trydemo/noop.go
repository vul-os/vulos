package trydemo

import (
	"context"
	"sync"
	"time"
)

// noopDemoMachines is an in-memory DemoMachines implementation for local dev
// and tests. It tracks machine state without making any network calls.
type noopDemoMachines struct {
	mu        sync.Mutex
	started   bool
	startedAt time.Time
}

// NewNoopDemoMachines returns a DemoMachines that never calls Fly.
// Initial state: machine is stopped.
func NewNoopDemoMachines() DemoMachines {
	return &noopDemoMachines{}
}

func (n *noopDemoMachines) Start(_ context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.started {
		n.started = true
		n.startedAt = time.Now()
	}
	return nil
}

func (n *noopDemoMachines) Stop(_ context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = false
	n.startedAt = time.Time{}
	return nil
}

func (n *noopDemoMachines) Restart(_ context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = true
	n.startedAt = time.Now()
	return nil
}

func (n *noopDemoMachines) Status(_ context.Context) (MachineStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return MachineStatus{
		Started:   n.started,
		StartedAt: n.startedAt,
		Region:    "noop",
	}, nil
}
