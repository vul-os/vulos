// no-broker-dep:allow-file: doc comment cites Pier as an example self-hosted gateway an operator
// MIGHT point the box at; the field defaults to "" (not configured)
// absent that.

// Package multiinstance — Phase-0 multi-region seam.
//
// PlaceFor resolves the control-plane base URL for a given region slug. Vulos
// operates no control plane for any region, so there is no compiled-in
// per-region directory: PlaceFor simply delegates to the single gwurl
// resolver — a persisted override (Settings) or a canonical CP env var when
// the operator has pointed the box at a self-hosted gateway (e.g. Pier,
// github.com/vul-os/pier); otherwise "" (not configured). The region
// parameter is accepted for API stability — a future self-hosted multi-cell
// deployment could reintroduce per-region routing here without changing the
// signature — but Phase-0 has exactly one resolution path and region does not
// currently affect it.
package multiinstance

import (
	"os"

	"vulos/backend/services/gwurl"
)

// boxRegion returns this box's home region from the VULOS_REGION environment
// variable, defaulting to "eu".
func boxRegion() string {
	if v := os.Getenv("VULOS_REGION"); v != "" {
		return v
	}
	return "eu"
}

// PlaceFor returns the configured gateway base URL, or "" if the box has no
// gateway configured (no persisted override and no canonical CP env var). See
// the package doc for the resolution path.
func PlaceFor(region string) string {
	u, _ := gwurl.Resolved()
	return u
}
