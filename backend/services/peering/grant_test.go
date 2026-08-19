package peering

// grant_test.go — every outbound dial in this package rides the operator's
// PEER dial grant, and none of them hardcodes "public only".
//
// This is a source-level pin rather than a behavioural one, deliberately. The
// defect it guards is a one-token regression — `safedial.NewPeer()` written
// back as `safedial.New(false)` — in any of five files, on a path that only
// misbehaves for an operator who has configured a private mesh. No unit test
// on a developer machine with no mesh would ever notice, which is exactly how
// the hardcoded form survived here in the first place: VULOS_PEER_ALLOW_LAN=1
// appeared to enable box-to-box delivery over a mesh and never did, because
// this package ignored it.
//
// The scan asserts its own reach (file count, and that the allowed form is
// actually present) so a broken scan cannot pass by finding nothing — the
// collection-is-not-execution trap.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// publicOnlyForms are the safedial calls that pin a dial to public addresses
// regardless of what the operator granted.
var publicOnlyForms = []*regexp.Regexp{
	regexp.MustCompile(`safedial\.New\(false\)`),
	regexp.MustCompile(`safedial\.ControlFunc\(false\)`),
	regexp.MustCompile(`safedial\.ValidateHost\([^)]*,\s*false\)`),
	regexp.MustCompile(`safedial\.IsDeniedIP\([^)]*,\s*false\)`),
	regexp.MustCompile(`safedial\.ValidateHostWithResolver\([^)]*,\s*false\s*,`),
}

func TestPeeringDialsRideThePeerGrant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var scanned int
	var grantedSites int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		scanned++
		src := string(b)
		grantedSites += strings.Count(src, "safedial.NewPeer()") +
			strings.Count(src, "safedial.ValidateHostPeer(")

		for i, line := range strings.Split(src, "\n") {
			// Comments describe history; only code is load-bearing here.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, re := range publicOnlyForms {
				if re.MatchString(line) {
					t.Errorf("%s:%d hardcodes a public-only dial:\n    %s\n"+
						"  Every outbound call in this package is box-to-box, so it must ride the "+
						"operator's peer grant (safedial.NewPeer / safedial.ValidateHostPeer). With "+
						"nothing configured that is byte-identical to the public-only form; with a "+
						"mesh range granted it is the difference between delivery working and "+
						"silently not.",
						name, i+1, strings.TrimSpace(line))
				}
			}
		}
	}

	// The scan must have actually looked at something, and must have found the
	// form it claims to be enforcing. A clean result from a scan that read no
	// files, or that no longer recognises the allowed spelling, proves nothing.
	if scanned < 5 {
		t.Fatalf("scanned only %d non-test .go files in services/peering; the scan is broken and "+
			"a clean result would be meaningless", scanned)
	}
	if grantedSites < 4 {
		t.Fatalf("found only %d peer-grant dial sites (safedial.NewPeer / ValidateHostPeer) across "+
			"%d files; either the package stopped dialling peers or the spelling changed and this "+
			"check no longer recognises it", grantedSites, scanned)
	}
}

// The pin above is only meaningful if the file it points at exists and this
// package really does import safedial — otherwise a rename could leave the
// scan looking for a string nothing produces.
func TestPeeringGrantPinPointsAtRealCode(t *testing.T) {
	for _, f := range []string{"transport.go", "resolve.go", "wellknown.go", "lobby.go", "bandwidth.go"} {
		b, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("%s: %v — the grant pin names a file that no longer exists", f, err)
		}
		if !strings.Contains(string(b), "vulos/backend/internal/safedial") {
			t.Errorf("%s no longer imports safedial; if its outbound dial moved, move the pin too", f)
		}
	}
}
