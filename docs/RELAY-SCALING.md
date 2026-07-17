# Relay scaling: choose your provisioner

**The relay pool scales through a pluggable seam.** The control plane decides
*desired state* — how many relays each region should run, from observed load and
region demand — and a **provisioner** you choose actuates it on your own
substrate. Self-hosters use whatever they already operate (Kubernetes,
Firecracker, Proxmox, their own scaler, or nothing). Vulos Cloud injects a managed
multi-provider. Same policy, same pool model, same demand API; only the actuation
differs.

This mirrors the billing (`BillingProvider`) and storage (`StorageProvisioner`)
seams: the OSS control plane ships a safe default that **provisions nothing**, and
the commercial layer injects a real implementation.

---

## The pieces

| Piece | Where | What it does |
|-------|-------|--------------|
| **Policy** (`relayscale.Policy`) | OSS, `pkg/relayscale` | Pure function: per-region desired relay count from load + demand, against configurable thresholds. Provisions nothing. |
| **Seam** (`relayscale.RelayProvisioner`) | OSS interface | `Provision(ctx, region, spec)` / `Destroy(ctx, inst)` / `List(ctx)` + `Name()`/`Enabled()`. |
| **Actuator** (`relayscale.Actuator`) | OSS | Reconciles desired vs. actual by driving the injected provisioner. |
| **Demand API** | OSS, mounted by the CP | Publishes desired state for an external scaler; ingests load observations. |
| **Providers** | OSS: manual / external / kubernetes / firecracker / proxmox. Commercial: managed multi-provider. | Actuate on a specific substrate. |

Select the OSS provider with **`RELAY_PROVISIONER`** (default `manual`). The
managed provider is not selected by env — Vulos Cloud injects it directly through
`cpserver.Deps.RelayProvisioner`, bypassing the registry, exactly like the
commercial billing/storage impls.

---

## The demand API (for external scalers)

Regardless of which provisioner is active, the control plane publishes desired
state so an operator's own scaler — or a dashboard — can read it:

```
GET  /api/relay/scale/demand                 # published desired state (no auth; aggregate only)
POST /api/relay/scale/observe                 # push per-region load (X-Relay-Auth: $CP_SHARED_SECRET)
```

`GET /demand` returns, per region: current instances, mean saturation, the
policy's **desired** count, and a reason:

```json
{
  "generated_at": "2026-07-17T10:00:00Z",
  "provisioner": "external",
  "actuated": false,
  "regions": [
    {"region": "eu-central", "current": 2, "desired": 3, "saturation": 0.82,
     "reason": "saturation 0.82 >= scale_up_at 0.75"}
  ]
}
```

`actuated: false` means the CP is **not** scaling the fleet itself (manual/external
mode) — the desired counts are for **your** scaler to act on. `POST /observe`
(fail-closed: 503 with no `CP_SHARED_SECRET`) is how relay PoPs or an aggregator
feed load in:

```json
{"regions": [{"region": "eu-central", "instances": 2, "saturation": 0.82}]}
```

### Policy thresholds (`RELAY_SCALE_*`)

| Env | Meaning | Default |
|-----|---------|---------|
| `RELAY_SCALE_UP_AT` | region-mean saturation to add a relay | `0.75` |
| `RELAY_SCALE_DOWN_AT` | saturation to shed a relay | `0.25` |
| `RELAY_SCALE_MIN_PER_REGION` | floor per active region | `1` |
| `RELAY_SCALE_MAX_PER_REGION` | ceiling per region (`0` = unbounded) | `8` |
| `RELAY_SCALE_STEP` | relays changed per reconcile | `1` |

---

## Choosing a provisioner

### `manual` (default) — bring your own relay

The control plane **provisions nothing**. The pool is whatever relays you have
registered / booted yourself (a static `vulos-relayd` box, a hand-run fleet). The
demand API still publishes desired state so you can watch load, but nothing is
actuated. This is the relay analogue of bring-your-own-bucket.

```
RELAY_PROVISIONER=manual        # or leave unset
```

### `external` — your own scaler actuates

Same as manual (the CP actuates nothing), but it **declares** that an external
scaler drives scaling by reading `GET /api/relay/scale/demand`: a Kubernetes HPA
on a custom/external metric, a cloud autoscaling group, a cron reconcile, or an
IaC run. Use this when you run your own scaling automation and just want the CP's
desired-state signal.

```
RELAY_PROVISIONER=external
```

**Kubernetes HPA pattern:** point a metrics adapter (e.g.
`prometheus-adapter` / KEDA) at `GET /api/relay/scale/demand`, expose the
per-region `desired` field as an external metric, and set the relay Deployment's
HPA target to it. The cluster then pulls desired state and scales itself — no
CP→cluster credential needed.

### `kubernetes` — CP scales a relay workload

The control plane **pushes** replica counts to a relay `Deployment` (or
`StatefulSet`) through the Kubernetes REST API using an in-cluster service-account
token — no heavy `client-go` dependency. The workload's replica count *is* the
desired relay count; the scheduler owns placement.

```
RELAY_PROVISIONER=kubernetes
RELAY_K8S_WORKLOAD=vulos-relayd
RELAY_K8S_WORKLOAD_KIND=deployments      # or statefulsets
RELAY_K8S_NAMESPACE=relay                # default: in-cluster namespace
RELAY_K8S_SELECTOR=app=vulos-relayd
RELAY_K8S_REGION=eu-central              # one cluster = one region
# apiserver + token + CA default to the in-cluster service-account files.
# Override for out-of-cluster: RELAY_K8S_APISERVER, RELAY_K8S_TOKEN, RELAY_K8S_INSECURE=1
```

The service account needs RBAC to `get`/`patch` the workload's `scale`
subresource and `list` pods:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: vulos-relay-scaler, namespace: relay}
rules:
  - apiGroups: ["apps"]
    resources: ["deployments/scale"]
    verbs: ["get", "patch"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
```

Single-region by design (a cluster is a region). For multi-region Kubernetes, run
one relay workload per regional cluster and drive them with `external` mode + a
per-cluster HPA, or use the managed provider.

### `firecracker` — CP boots relay microVMs

The control plane boots `vulos-relayd` inside Firecracker microVMs, talking to the
Firecracker API over each VM's unix socket. The API sequence (machine-config,
boot-source, rootfs drive, `InstanceStart`) is wired; the **host-side plumbing**
you supply is: launching the `firecracker`/`jailer` process with a fresh API
socket per VM, a per-VM rootfs overlay carrying relayd, and a tap device. These
are marked `TODO(deploy)` in `pkg/relayscale/firecracker.go` because they are
host-specific (usually owned by a unit manager).

```
RELAY_PROVISIONER=firecracker
RELAY_FC_KERNEL=/var/lib/vulos/vmlinux
RELAY_FC_ROOTFS=/var/lib/vulos/relayd.ext4
RELAY_FC_RUNDIR=/run/vulos-relay        # per-VM sockets + instance registry
RELAY_FC_REGION=eu-central
RELAY_FC_VCPUS=1
RELAY_FC_MEM_MIB=256
```

### `proxmox` — CP clones relay VMs/CTs

The control plane clones a prepared relay **template** (a VM or LXC container that
already contains `vulos-relayd`) to a fresh VMID and starts it, via the Proxmox VE
REST API with an API token. Clone/start/stop/delete/list are wired; the one
`TODO(deploy)` is rendering per-guest cloud-init from the spec (your image,
network, storage).

```
RELAY_PROVISIONER=proxmox
RELAY_PVE_ENDPOINT=https://pve.example.com:8006
RELAY_PVE_TOKEN=user@pam!scaler=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
RELAY_PVE_NODE=pve1
RELAY_PVE_KIND=qemu                     # or lxc
RELAY_PVE_TEMPLATE=9000                 # VMID of the relayd template
RELAY_PVE_REGION=eu-central
RELAY_PVE_INSECURE=1                    # self-signed PVE cert
```

### `managed` — Vulos Cloud (commercial)

Vulos Cloud runs relays across **multiple providers** — Hetzner (primary, flat
€1/TB bandwidth), Vultr (edge / regions Hetzner does not cover, e.g. Johannesburg),
and Fly (elastic gap-fill) — routing each region to the cheapest capable provider
and attributing spend for billing. This impl lives in the private `vulos-cloud`
module (`internal/relayfleet`) and is injected via
`seamadapter.NewRelayProvisioner`. It is **not** selectable through
`RELAY_PROVISIONER`; it enables itself when provider tokens are present
(`RELAYFLEET_HETZNER_TOKEN`, `RELAYFLEET_VULTR_TOKEN`, `RELAYFLEET_FLY_TOKEN`) and
otherwise falls back to `manual`. Self-hosters never need it.

---

## How the choice maps to your deployment

| You deployed the relay as… | Use |
|----------------------------|-----|
| A static `vulos-relayd` box (or a fleet you manage by hand) | `manual` |
| A workload behind your own autoscaler / HPA / ASG / IaC | `external` |
| A Deployment/StatefulSet in one Kubernetes cluster | `kubernetes` |
| microVMs on a bare-metal host | `firecracker` |
| VMs/containers on a Proxmox cluster | `proxmox` |
| Nothing yet — you want Vulos to run it | Vulos Cloud (`managed`) |

The **OSS/commercial boundary**: `vulos-management` ships the policy, the pool
model, the seam, the demand API, and the five OSS providers. `vulos-cloud` ships
**only** the managed multi-provider and injects it through the same seam. The
management module never imports the commercial provider (enforced by
`internal/archtest`), so the OSS control plane stays fully functional — and fully
capable of scaling relays on your own infrastructure — with no cloud.
