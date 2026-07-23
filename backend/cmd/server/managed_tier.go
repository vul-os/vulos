package main

import (
	"os"
	"strings"
)

// managedTierEnabled reports whether this box runs the optional MANAGED-TIER
// surfaces (KMS, compliance, support). These are features of a hosted managed
// offering, not the sovereign self-host box — their Settings UIs were dropped in
// the management fold, so on a self-host box they would otherwise be orphan
// endpoints answering HTTP with no product surface behind them.
//
// They are therefore OFF by default and only mount when VULOS_MANAGED_TIER is
// explicitly set (greenfield decision 2026-07-23: box = authority, no hollow
// managed surfaces on the OSS box). Set VULOS_MANAGED_TIER=1 to opt a box into
// the managed offering.
func managedTierEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_MANAGED_TIER")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
