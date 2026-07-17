// Package relayscale is the provider-agnostic SEAM for RELAY POOL scaling.
//
// The relay is the one bandwidth-bound service the fleet runs, deployed as a
// geo-distributed pool of relayd nodes. HOW those nodes come and go is a
// deployment choice, and this package refuses to bake one in:
//
//   - A self-hoster runs the relay on whatever they already operate — a static
//     box, Kubernetes, Firecracker microVMs, a Proxmox cluster, their own
//     autoscaler, or nothing at all — and this package must not assume a cloud
//     API.
//   - Vulos Cloud runs a managed multi-provider (Fly / Hetzner / Vultr,
//     demand-driven and billed). That is a COMMERCIAL concern and lives in the
//     private vulos-cloud module, injected through this seam exactly like the
//     billing (pkg/billingport) and storage (pkg/storageport) seams.
//
// So the split mirrors those seams:
//
//   - POLICY (Policy) decides DESIRED STATE — the desired relay count per region
//     from observed load + region demand, against configurable thresholds. It is
//     pure: it provisions nothing and names no provider.
//   - The SEAM (RelayProvisioner) is the narrow interface an actuator drives to
//     bring relay nodes up / down on whatever substrate the operator uses.
//   - The ACTUATOR (Actuator) reconciles desired state through the injected
//     RelayProvisioner. In manual / external mode it actuates nothing and simply
//     surfaces the demand for an operator's own scaler to read.
//
// INVARIANTS (mirroring pkg/servingpool):
//   - Pure-Go, no cloud SDKs, no CGO. The OSS providers here (manual / external /
//     kubernetes / firecracker / proxmox) speak their targets over net/http only.
//   - No Go-module dependency on vulos-mail / vulos-relay / vulos-office, and none
//     on the commercial vulos.cloud/cp module. The Vulos-specific managed
//     multi-provider is NOT here — it is injected from vulos-cloud.
package relayscale

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors.
var (
	// ErrManualMode is returned by ManualProvisioner.Provision/Destroy: this
	// deployment never provisions relays itself. The actuator treats it as
	// "operator registers their own relay", NOT as a failure.
	ErrManualMode = errors.New("relayscale: manual mode — register your relay (no automatic provisioning)")

	// ErrExternalMode is returned by ExternalProvisioner.Provision/Destroy: an
	// operator's own scaler reads the demand API and actuates scaling out of band.
	ErrExternalMode = errors.New("relayscale: external mode — desired state is published; an external scaler actuates it")

	// ErrUnknownProvisioner is returned by the registry for an unrecognised
	// RELAY_PROVISIONER value.
	ErrUnknownProvisioner = errors.New("relayscale: unknown provisioner (want manual|external|kubernetes|firecracker|proxmox)")

	// ErrNotConfigured is returned by a provisioner missing required config
	// (e.g. a Kubernetes token / a Proxmox endpoint). Fail closed: a
	// misconfigured provider provisions nothing rather than half-acting.
	ErrNotConfigured = errors.New("relayscale: provisioner not configured")

	// ErrInstanceNotFound is returned by Destroy when the instance is unknown.
	ErrInstanceNotFound = errors.New("relayscale: instance not found")
)

// RelaySpec is the desired shape of ONE relay node the actuator asks a
// provisioner to bring up. A provisioner maps these fields onto its substrate —
// a container image + env for Kubernetes, a VM/CT template for Proxmox, a
// kernel+rootfs for Firecracker, a machine size for a cloud API. Zero fields take
// the provisioner's own defaults.
type RelaySpec struct {
	// Image is the relayd artifact reference: a container image
	// (e.g. "ghcr.io/vul-os/vulos-relayd:v0.3.0") for Kubernetes, or a VM/CT
	// template identifier for Proxmox/Firecracker. Provider-interpreted.
	Image string
	// Domain is the base relay domain the node serves (VULOS_RELAY_DOMAIN).
	Domain string
	// PublicEndpointTmpl, if set, is a template for the node's agent-facing base
	// URL with "{id}"/"{region}" placeholders (e.g. "wss://{id}.relay.vulos.org").
	PublicEndpointTmpl string
	// SoftMaxAgents / SoftMaxStreams / SoftMaxBytesPerSec are the node's soft
	// capacity dimensions (VULOS_RELAY_SOFT_MAX_*), used for its saturation gauge.
	SoftMaxAgents      int
	SoftMaxStreams     int
	SoftMaxBytesPerSec int64
	// CPURL / CPSharedSecret wire the node back to the control plane for
	// entitlement + usage reporting (VULOS_CP_URL / CP_SHARED_SECRET).
	CPURL          string
	CPSharedSecret string
	// Env carries extra VULOS_RELAY_* overrides merged over the derived set.
	Env map[string]string
}

// Instance is one live (or provisioning) relay node as the provisioner sees it.
// It is the provisioner's record of truth returned by Provision + List and passed
// back to Destroy.
type Instance struct {
	// ID is the provisioner-scoped unique id (a pod name, a Proxmox VMID, a
	// microVM id, a cloud machine id). Stable for the instance's lifetime.
	ID string
	// Region is the coarse geo tag the instance serves.
	Region string
	// Provider is the substrate tag ("kubernetes", "proxmox", "firecracker",
	// "hetzner", …) — informational, for observability + billing attribution.
	Provider string
	// Addr is the node's reachable base URL, once known (health-probe target +
	// router hint). May be empty while the node is still coming up.
	Addr string
	// Ready reports whether the provisioner considers the node live and serving.
	Ready bool
	// CreatedAt is when this instance was provisioned (best-effort).
	CreatedAt time.Time
	// Meta carries provider-specific handles the provisioner needs for Destroy
	// (e.g. the Proxmox node name, the jailer pid file). Opaque to the actuator.
	Meta map[string]string
}

// RelayProvisioner is the seam an actuator drives to grow/shrink the relay pool
// on whatever substrate an operator runs. Implementations MUST be safe for
// concurrent use. The OSS defaults live in this package; the commercial managed
// multi-provider is injected from vulos-cloud.
//
// Provision/Destroy are called OFF any hot path (the actuator runs them from its
// own reconcile loop) and receive a context the caller cancels on shutdown, so a
// slow provider API never stalls the control plane.
type RelayProvisioner interface {
	// Name is the stable provisioner id ("manual", "external", "kubernetes",
	// "firecracker", "proxmox", or a commercial name).
	Name() string

	// Enabled reports whether this provisioner actually provisions. Manual and
	// external modes return false: the composition root then knows to publish the
	// demand for an operator/external scaler rather than expecting actuation.
	Enabled() bool

	// Provision brings up ONE additional relay node in region with spec and
	// returns it once the provisioner has accepted it (it may still be coming up —
	// callers gate on Ready / a health probe). A manual/external provisioner
	// returns ErrManualMode / ErrExternalMode and provisions nothing.
	Provision(ctx context.Context, region string, spec RelaySpec) (Instance, error)

	// Destroy drains and removes the given instance. It returns once the instance
	// is gone (or ctx is done). A manual/external provisioner returns
	// ErrManualMode / ErrExternalMode.
	Destroy(ctx context.Context, inst Instance) error

	// List returns the relay instances this provisioner currently manages, across
	// all regions. The actuator diffs this against desired state. A provisioner
	// that cannot enumerate (pure manual) returns an empty list.
	List(ctx context.Context) ([]Instance, error)
}
