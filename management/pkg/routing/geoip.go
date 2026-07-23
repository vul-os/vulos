// geoip.go — static geo-IP stub for v1 (DNS-05).
//
// A real MaxMind or similar database lookup would go here in v2. For v1 we map
// well-known prefixes to approximate continental centroids so that the
// nearest-PoP selection is at least regionally correct in production and
// deterministic in tests. Unknown or private addresses fall back to (0, 0)
// (equatorial/Greenwich reference point), which is a safe no-op for selection.
package routing

import (
	"net"
)

// ClientGeo maps a client IP string to an approximate (lat, lon) coordinate
// using a static table. It is a best-effort stub; accuracy is continental.
// Returns (0, 0) for private, loopback, unspecified, or unrecognised addresses.
func ClientGeo(ip string) (lat, lon float64) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0, 0
	}
	// Private / non-routable ranges → default.
	if parsed.IsLoopback() || parsed.IsUnspecified() ||
		parsed.IsLinkLocalUnicast() || parsed.IsMulticast() {
		return 0, 0
	}

	// IPv4 only for the static table (v6 falls through to default).
	v4 := parsed.To4()
	if v4 == nil {
		return 0, 0
	}

	// Table: match on the first one or two octets.
	// Regions are broad continental centroids — good enough for PoP selection.
	type entry struct {
		prefix [2]byte
		mask   int // number of significant octets (1 or 2)
		lat    float64
		lon    float64
	}
	table := []entry{
		// Africa / Sub-Saharan
		{[2]byte{41, 0}, 1, -1.3, 17.0},
		{[2]byte{196, 0}, 1, 1.0, 20.0},
		{[2]byte{197, 0}, 1, 1.0, 20.0},

		// Europe
		{[2]byte{2, 0}, 1, 50.0, 10.0},
		{[2]byte{5, 0}, 1, 50.0, 10.0},
		{[2]byte{31, 0}, 1, 50.0, 10.0},
		{[2]byte{46, 0}, 1, 50.0, 10.0},
		{[2]byte{77, 0}, 1, 50.0, 10.0},
		{[2]byte{78, 0}, 1, 50.0, 10.0},
		{[2]byte{79, 0}, 1, 50.0, 10.0},
		{[2]byte{80, 0}, 1, 50.0, 10.0},
		{[2]byte{81, 0}, 1, 50.0, 10.0},
		{[2]byte{82, 0}, 1, 50.0, 10.0},
		{[2]byte{83, 0}, 1, 50.0, 10.0},
		{[2]byte{84, 0}, 1, 50.0, 10.0},
		{[2]byte{85, 0}, 1, 50.0, 10.0},
		{[2]byte{86, 0}, 1, 50.0, 10.0},
		{[2]byte{87, 0}, 1, 50.0, 10.0},
		{[2]byte{88, 0}, 1, 50.0, 10.0},
		{[2]byte{89, 0}, 1, 50.0, 10.0},
		{[2]byte{90, 0}, 1, 50.0, 10.0},
		{[2]byte{91, 0}, 1, 50.0, 10.0},
		{[2]byte{93, 0}, 1, 50.0, 10.0},
		{[2]byte{94, 0}, 1, 50.0, 10.0},
		{[2]byte{95, 0}, 1, 50.0, 10.0},

		// North America
		{[2]byte{3, 0}, 1, 38.0, -95.0},
		{[2]byte{4, 0}, 1, 38.0, -95.0},
		{[2]byte{8, 0}, 1, 38.0, -95.0},
		{[2]byte{12, 0}, 1, 38.0, -95.0},
		{[2]byte{24, 0}, 1, 38.0, -95.0},
		{[2]byte{32, 0}, 1, 38.0, -95.0},
		{[2]byte{50, 0}, 1, 38.0, -95.0},
		{[2]byte{52, 0}, 1, 38.0, -95.0},
		{[2]byte{54, 0}, 1, 38.0, -95.0},
		{[2]byte{63, 0}, 1, 38.0, -95.0},
		{[2]byte{64, 0}, 1, 38.0, -95.0},
		{[2]byte{65, 0}, 1, 38.0, -95.0},
		{[2]byte{66, 0}, 1, 38.0, -95.0},
		{[2]byte{67, 0}, 1, 38.0, -95.0},
		{[2]byte{68, 0}, 1, 38.0, -95.0},
		{[2]byte{69, 0}, 1, 38.0, -95.0},
		{[2]byte{70, 0}, 1, 38.0, -95.0},
		{[2]byte{71, 0}, 1, 38.0, -95.0},
		{[2]byte{72, 0}, 1, 38.0, -95.0},
		{[2]byte{73, 0}, 1, 38.0, -95.0},
		{[2]byte{74, 0}, 1, 38.0, -95.0},
		{[2]byte{75, 0}, 1, 38.0, -95.0},
		{[2]byte{76, 0}, 1, 38.0, -95.0},
		{[2]byte{98, 0}, 1, 38.0, -95.0},
		{[2]byte{99, 0}, 1, 38.0, -95.0},
		{[2]byte{100, 0}, 1, 38.0, -95.0},
		{[2]byte{104, 0}, 1, 38.0, -95.0},

		// Asia-Pacific
		{[2]byte{1, 0}, 1, 35.0, 105.0},
		{[2]byte{14, 0}, 1, 35.0, 105.0},
		{[2]byte{27, 0}, 1, 35.0, 105.0},
		{[2]byte{36, 0}, 1, 35.0, 105.0},
		{[2]byte{39, 0}, 1, 35.0, 105.0},
		{[2]byte{42, 0}, 1, 35.0, 105.0},
		{[2]byte{43, 0}, 1, 35.0, 105.0},
		{[2]byte{49, 0}, 1, 35.0, 105.0},
		{[2]byte{58, 0}, 1, 35.0, 105.0},
		{[2]byte{59, 0}, 1, 35.0, 105.0},
		{[2]byte{60, 0}, 1, 35.0, 105.0},
		{[2]byte{61, 0}, 1, 35.0, 105.0},
		{[2]byte{103, 0}, 1, 1.3, 103.8}, // Singapore area
		{[2]byte{110, 0}, 1, 35.0, 105.0},
		{[2]byte{111, 0}, 1, 35.0, 105.0},
		{[2]byte{112, 0}, 1, 35.0, 105.0},
		{[2]byte{113, 0}, 1, 35.0, 105.0},
		{[2]byte{114, 0}, 1, 35.0, 105.0},
		{[2]byte{115, 0}, 1, 35.0, 105.0},
		{[2]byte{116, 0}, 1, 35.0, 105.0},
		{[2]byte{117, 0}, 1, 35.0, 105.0},
		{[2]byte{118, 0}, 1, 35.0, 105.0},
		{[2]byte{119, 0}, 1, 35.0, 105.0},
		{[2]byte{120, 0}, 1, 35.0, 105.0},
		{[2]byte{121, 0}, 1, 35.0, 105.0},
		{[2]byte{122, 0}, 1, 35.0, 105.0},
		{[2]byte{123, 0}, 1, 35.0, 105.0},
		{[2]byte{124, 0}, 1, 35.0, 105.0},
		{[2]byte{125, 0}, 1, 35.0, 105.0},
	}

	for _, e := range table {
		if e.mask >= 1 && v4[0] == e.prefix[0] {
			if e.mask == 1 {
				return e.lat, e.lon
			}
			if v4[1] == e.prefix[1] {
				return e.lat, e.lon
			}
		}
	}
	return 0, 0 // default: equatorial reference
}
