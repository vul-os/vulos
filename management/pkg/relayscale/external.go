package relayscale

import "context"

// external.go — ExternalProvisioner. Like manual it actuates nothing IN-PROCESS,
// but it declares that an operator's OWN scaler drives scaling by reading the
// demand API (see demand.go): a Kubernetes HPA on a custom metric, a cloud
// autoscaling group, a cron job, or an IaC reconcile. The control plane's job in
// this mode is to PUBLISH the desired state, not to act on it.
//
// The distinction from manual is purely intent + observability: the demand API
// reports provisioner="external" so an operator knows the published desired
// counts are meant to be consumed by their scaler, whereas "manual" means no
// autoscaling is expected at all.

// ExternalProvisioner publishes demand for an external scaler. Provision/Destroy
// return ErrExternalMode; List returns nothing (the external system owns the
// fleet inventory).
type ExternalProvisioner struct{}

// NewExternalProvisioner returns the external-scaler provisioner.
func NewExternalProvisioner() *ExternalProvisioner { return &ExternalProvisioner{} }

func (*ExternalProvisioner) Name() string  { return "external" }
func (*ExternalProvisioner) Enabled() bool { return false }

func (*ExternalProvisioner) Provision(context.Context, string, RelaySpec) (Instance, error) {
	return Instance{}, ErrExternalMode
}

func (*ExternalProvisioner) Destroy(context.Context, Instance) error { return ErrExternalMode }

func (*ExternalProvisioner) List(context.Context) ([]Instance, error) { return nil, nil }

var _ RelayProvisioner = (*ExternalProvisioner)(nil)
