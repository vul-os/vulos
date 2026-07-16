// Package routing — dns.go
//
// Programmatic zone / A-record model for the *.{ulid}.vulos.org namespace.
// This is NOT a real DNS server. It provides:
//
//   - ARecord: the resolved IP for a ULID (either the direct-mode instance IP
//     or the nearest healthy PoP IP for fabric mode).
//   - ResolveRoute: primary route resolution function used by GET /api/route.
//   - VanityTarget: looks up a vanity name and returns the destination ULID.
//
// Only vulos.org scope. Never a general DNS host.
package routing

import (
	"context"
	"fmt"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ARecord represents a synthesised A-record for the *.{ulid}.vulos.org zone.
type ARecord struct {
	// FQDN is the wildcard zone apex, e.g. "*.01J5EXAMPLE.vulos.org".
	FQDN string
	// IP is the resolved IPv4 address for this record.
	IP string
	// Mode is the connection mode that produced the record.
	Mode ConnMode
	// PoPID is the selected PoP identifier (only set for ModeFabric).
	PoPID string
	// Region is the selected PoP region (only set for ModeFabric).
	Region string
}

// RouteResult is the resolved routing target returned by ResolveRoute.
type RouteResult struct {
	Mode   ConnMode `json:"mode"`
	Target string   `json:"target"`
	PoPID  string   `json:"pop_id,omitempty"`
	Region string   `json:"region,omitempty"`
}

// ResolveRoute resolves the nearest routing target for a ULID given a client
// location (lat, lon). For direct-mode bindings it returns the instance IP
// directly. For fabric-mode bindings it calls Store.HealthyPoPs + NearestPoP.
// For own-domain mode the target is empty (the client uses its own domain).
//
// Sentinel errors propagated to callers:
//   - ErrNotEnrolled  → ULID has no binding
//   - ErrNoHealthyPoP → fabric mode with no healthy PoP
func ResolveRoute(ctx context.Context, st Store, ulid string, lat, lon float64) (RouteResult, error) {
	b, err := st.GetBinding(ctx, ulid)
	if err != nil {
		return RouteResult{}, err
	}

	switch b.Mode {
	case ModeDirect:
		return RouteResult{
			Mode:   ModeDirect,
			Target: b.DirectIP,
		}, nil

	case ModeOwnDomain:
		return RouteResult{
			Mode: ModeOwnDomain,
		}, nil

	default: // ModeFabric (the normal cloud path)
		pops, err := st.HealthyPoPs(ctx)
		if err != nil {
			return RouteResult{}, err
		}
		pop, err := NearestPoP(pops, lat, lon)
		if err != nil {
			return RouteResult{}, err // ErrNoHealthyPoP
		}
		return RouteResult{
			Mode:   ModeFabric,
			Target: pop.IP,
			PoPID:  pop.ID,
			Region: pop.Region,
		}, nil
	}
}

// ZoneApex returns the wildcard A-record apex for a ULID, e.g.
// "*.01J5EXAMPLE.vulos.org". Only vulos.org scope.
func ZoneApex(ulid string) string {
	return fmt.Sprintf("*.%s.%s", ulid, env.Domain())
}

// ResolveARecord synthesises the A-record for the given ULID's wildcard zone,
// resolving the target using the same logic as ResolveRoute.
func ResolveARecord(ctx context.Context, st Store, ulid string, lat, lon float64) (ARecord, error) {
	rr, err := ResolveRoute(ctx, st, ulid, lat, lon)
	if err != nil {
		return ARecord{}, err
	}
	return ARecord{
		FQDN:   ZoneApex(ulid),
		IP:     rr.Target,
		Mode:   rr.Mode,
		PoPID:  rr.PoPID,
		Region: rr.Region,
	}, nil
}

// VanityTarget resolves a vanity name to its destination ULID.
// Returns ErrNotFound if the name is unknown.
func VanityTarget(ctx context.Context, st Store, name string) (string, error) {
	v, err := st.ResolveVanity(ctx, name)
	if err != nil {
		return "", err
	}
	return v.ULID, nil
}
