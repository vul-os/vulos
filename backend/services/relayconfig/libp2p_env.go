package relayconfig

// libp2p_env.go — the SECOND, box-process-level opt-in gate for actually
// embedding a real go-libp2p host, layered ON TOP OF (never instead of) the
// existing admin-gated Provider selection. See libp2p_manager.go's package
// doc for the full two-gate rationale.
//
// Env surface:
//
//	VULOS_LIBP2P_HOST_ENABLE=1   opt in to a REAL embedded go-libp2p Circuit
//	                             Relay v2 client host (required IN ADDITION
//	                             to selecting the "libp2p" provider in
//	                             Settings/relayconfig.json; unset/anything
//	                             else => the seam stays a report-only stub,
//	                             exactly its pre-existing behaviour).

import (
	"os"
	"strings"
)

const envLibp2pHostEnable = "VULOS_LIBP2P_HOST_ENABLE"

// libp2pHostEnabledByEnv reports whether the box's process environment
// explicitly opts in to a real embedded libp2p host. Absent or any value
// other than "1"/"true"/"yes" (case-insensitive) means NOT enabled — the
// fail-safe default.
func libp2pHostEnabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envLibp2pHostEnable))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
