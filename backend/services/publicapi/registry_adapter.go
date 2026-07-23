package publicapi

// registry_adapter.go — DeviceLister implementation over the box's shared
// multiinstance.Registry (MINST-01), wiring GET /api/v1/devices and
// /api/v1/account/usage's device_count to real data instead of the
// always-empty nil-devices fallback.
//
// A single box belongs to exactly one account, so RegistryDeviceLister
// ignores the accountID argument — the registry has no account column to
// filter on (see multiinstance.Instance) and every row it holds already
// belongs to this box's one account.

import (
	"context"

	"vulos/backend/internal/multiinstance"
)

// registrySource is the subset of *multiinstance.Registry that
// RegistryDeviceLister needs. Defined as an interface so tests can stub it
// without a real SQLite-backed Registry.
type registrySource interface {
	List() ([]multiinstance.Instance, error)
}

// RegistryDeviceLister adapts a *multiinstance.Registry to DeviceLister. It
// must be constructed from the box's ONE shared Registry handle (opened once
// in cmd/server) — never from a second Registry opened on the same SQLite
// file, which modernc.org/sqlite's single-writer connection does not
// tolerate safely.
type RegistryDeviceLister struct {
	reg registrySource
}

// NewRegistryDeviceLister wraps reg. Returns nil if reg is nil, so callers
// can pass a possibly-unavailable registry straight through to NewHandler and
// keep the "devices may be nil" graceful-degradation behaviour.
func NewRegistryDeviceLister(reg *multiinstance.Registry) *RegistryDeviceLister {
	if reg == nil {
		return nil
	}
	return &RegistryDeviceLister{reg: reg}
}

// ListDevices implements DeviceLister. accountID is unused (see doc above).
func (l *RegistryDeviceLister) ListDevices(ctx context.Context, accountID string) ([]Device, error) {
	if l == nil || l.reg == nil {
		return nil, nil
	}
	insts, err := l.reg.List()
	if err != nil {
		return nil, err
	}
	devs := make([]Device, 0, len(insts))
	for _, inst := range insts {
		d := Device{
			ID:     inst.ULID,
			Name:   inst.DisplayName,
			Kind:   string(inst.Kind),
			Status: string(inst.Status),
		}
		if !inst.LastSeenAt.IsZero() {
			t := inst.LastSeenAt
			d.LastSeenAt = &t
		}
		devs = append(devs, d)
	}
	return devs, nil
}
