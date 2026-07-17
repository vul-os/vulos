package relayscale

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// registry.go — selects the RelayProvisioner impl from configuration, mirroring
// how oauthclient.NewRegistryFromEnv and the storage/billing seams resolve their
// providers. The choice is RELAY_PROVISIONER; each provider reads its own
// RELAY_<PROVIDER>_* env. The commercial managed multi-provider is NOT selectable
// here — vulos-cloud injects it directly through cpserver.Deps, bypassing the env
// registry, exactly like the commercial billing/storage impls.
//
// DEFAULT is "manual": a self-hoster who sets nothing gets the bring-your-own-relay
// posture and the control plane provisions no relays.

// ProvisionerFromEnv builds the RelayProvisioner named by RELAY_PROVISIONER
// (default "manual"). It returns ErrUnknownProvisioner for an unrecognised value
// and a wrapped ErrNotConfigured when a selected provider is missing required
// env — fail closed rather than silently falling back to manual, so a
// misconfiguration is loud.
func ProvisionerFromEnv() (RelayProvisioner, error) {
	return provisionerFromEnv(os.Getenv)
}

// provisionerFromEnv is the testable core (getenv injected).
func provisionerFromEnv(getenv func(string) string) (RelayProvisioner, error) {
	kind := strings.ToLower(strings.TrimSpace(getenv("RELAY_PROVISIONER")))
	switch kind {
	case "", "manual":
		return NewManualProvisioner(), nil
	case "external":
		return NewExternalProvisioner(), nil
	case "kubernetes", "k8s":
		return NewKubernetesProvisioner(KubernetesConfig{
			APIServer:          getenv("RELAY_K8S_APISERVER"),
			Token:              getenv("RELAY_K8S_TOKEN"),
			InsecureSkipVerify: getenv("RELAY_K8S_INSECURE") == "1",
			Namespace:          getenv("RELAY_K8S_NAMESPACE"),
			Workload:           getenv("RELAY_K8S_WORKLOAD"),
			WorkloadKind:       getenv("RELAY_K8S_WORKLOAD_KIND"),
			LabelSelector:      getenv("RELAY_K8S_SELECTOR"),
			Region:             getenv("RELAY_K8S_REGION"),
		})
	case "firecracker", "fc":
		return NewFirecrackerProvisioner(FirecrackerConfig{
			KernelImagePath: getenv("RELAY_FC_KERNEL"),
			RootDrivePath:   getenv("RELAY_FC_ROOTFS"),
			BootArgs:        getenv("RELAY_FC_BOOTARGS"),
			VCPUs:           atoiOr(getenv("RELAY_FC_VCPUS"), 0),
			MemMiB:          atoiOr(getenv("RELAY_FC_MEM_MIB"), 0),
			RunDir:          getenv("RELAY_FC_RUNDIR"),
			Region:          getenv("RELAY_FC_REGION"),
			FirecrackerBin:  getenv("RELAY_FC_BIN"),
		})
	case "proxmox", "pve":
		return NewProxmoxProvisioner(ProxmoxConfig{
			Endpoint:           getenv("RELAY_PVE_ENDPOINT"),
			APIToken:           getenv("RELAY_PVE_TOKEN"),
			InsecureSkipVerify: getenv("RELAY_PVE_INSECURE") == "1",
			Node:               getenv("RELAY_PVE_NODE"),
			GuestKind:          getenv("RELAY_PVE_KIND"),
			TemplateID:         atoiOr(getenv("RELAY_PVE_TEMPLATE"), 0),
			VMIDBase:           atoiOr(getenv("RELAY_PVE_VMID_BASE"), 0),
			Region:             getenv("RELAY_PVE_REGION"),
			Tag:                getenv("RELAY_PVE_TAG"),
		})
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvisioner, kind)
	}
}

// PolicyFromEnv builds a Policy from RELAY_SCALE_* env, applying defaults for any
// unset threshold.
func PolicyFromEnv() Policy {
	return policyFromEnv(os.Getenv)
}

func policyFromEnv(getenv func(string) string) Policy {
	return Policy{
		ScaleUpAt:    atofOr(getenv("RELAY_SCALE_UP_AT"), 0),
		ScaleDownAt:  atofOr(getenv("RELAY_SCALE_DOWN_AT"), 0),
		MinPerRegion: atoiOr(getenv("RELAY_SCALE_MIN_PER_REGION"), 0),
		MaxPerRegion: atoiOr(getenv("RELAY_SCALE_MAX_PER_REGION"), 0),
		Step:         atoiOr(getenv("RELAY_SCALE_STEP"), 0),
	}
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func atofOr(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}
