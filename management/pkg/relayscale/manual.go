package relayscale

import "context"

// manual.go — ManualProvisioner, the DEFAULT. It provisions nothing: the relay
// pool is whatever relays the operator has registered / booted themselves. This
// is the bring-your-own-relay posture, the relay analogue of
// storageport.NoopProvisioner.

// ManualProvisioner never provisions. Provision/Destroy return ErrManualMode so a
// caller can distinguish "this deployment does not autoscale relays" from a real
// backend failure. List returns nothing (the pool membership is tracked elsewhere
// — the CP's relay PoP health aggregation — not by this provisioner).
type ManualProvisioner struct{}

// NewManualProvisioner returns the bring-your-own-relay default.
func NewManualProvisioner() *ManualProvisioner { return &ManualProvisioner{} }

func (*ManualProvisioner) Name() string  { return "manual" }
func (*ManualProvisioner) Enabled() bool { return false }

func (*ManualProvisioner) Provision(context.Context, string, RelaySpec) (Instance, error) {
	return Instance{}, ErrManualMode
}

func (*ManualProvisioner) Destroy(context.Context, Instance) error { return ErrManualMode }

func (*ManualProvisioner) List(context.Context) ([]Instance, error) { return nil, nil }

var _ RelayProvisioner = (*ManualProvisioner)(nil)
