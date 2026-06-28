// Package multiinstance — Phase-0 multi-region seam.
//
// PlaceFor maps a region slug to the CP base URL for that cell.
// For Phase-0 (single cell) every region resolves to DefaultCloudBaseURL.
// To add a second cell later: add its entry to regionCPMap and set VULOS_REGION
// on boxes in that cell — no other code changes are required.
package multiinstance

import "os"

// regionCPMap is the authoritative region → CP base URL directory.
// Phase-0: a single "eu" cell backed by DefaultCloudBaseURL.
var regionCPMap = map[string]string{
	"eu": DefaultCloudBaseURL,
}

// boxRegion returns this box's home region from the VULOS_REGION environment
// variable, defaulting to "eu".
func boxRegion() string {
	if v := os.Getenv("VULOS_REGION"); v != "" {
		return v
	}
	return "eu"
}

// PlaceFor returns the CP base URL for the given region slug.
//
// For Phase-0 (single cell) all regions resolve to DefaultCloudBaseURL.
// VULOS_CLOUD_URL always takes precedence so dev / self-hosted deployments
// can override the resolved URL without touching the region map.
func PlaceFor(region string) string {
	// Explicit dev/self-hosted override always wins.
	if v := os.Getenv("VULOS_CLOUD_URL"); v != "" {
		return v
	}
	if base, ok := regionCPMap[region]; ok {
		return base
	}
	// Unknown region: fall back to the eu cell (single-cell safety net).
	return DefaultCloudBaseURL
}
