package idp_test

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenDeps are the subsystems the isolated identity service (§1.3) MUST NOT
// depend on, transitively. A login must complete with ZERO synchronous coupling
// to the entitlement/billing seam or the admin/reporting surfaces. If any of
// these appears in the transitive import graph of pkg/idp, the isolation is
// broken and this test fails — a guard against a future refactor silently
// re-coupling login to a subsystem whose outage would then take login down.
//
// Note: the commercial billing implementation lives in a SEPARATE module
// (vulos-cloud) and is unreachable from here by construction. The seam that
// could reintroduce coupling is billingport (the entitlement resolver), so it is
// the primary forbidden dep alongside the admin consoles.
var forbiddenDeps = []string{
	"github.com/vul-os/vulos-management/pkg/billingport",
	"github.com/vul-os/vulos-management/pkg/superadmin",
	"github.com/vul-os/vulos-management/pkg/orgadmin",
}

// packagesUnderTest is the identity boundary. The standalone cmd/idp binary
// lives in the composition layer; when it is (re)introduced into this module it
// should be added here.
var packagesUnderTest = []string{
	"github.com/vul-os/vulos-management/pkg/idp",
}

func TestIDPImportBoundary(t *testing.T) {
	for _, pkg := range packagesUnderTest {
		deps := transitiveDeps(t, pkg)
		for _, forbidden := range forbiddenDeps {
			for _, d := range deps {
				if d == forbidden || strings.HasPrefix(d, forbidden+"/") {
					t.Errorf("ISOLATION BREACH: %s transitively imports forbidden dependency %s\n"+
						"The identity/login service must not share fate with that subsystem "+
						"(IDENTITY-SERVICE §1.3). Break the import.", pkg, forbidden)
				}
			}
		}
	}
}

// TestIDPDependencyAllowlist asserts the boundary stays MINIMAL: the only
// first-party packages idp may import are the identity core and small shared
// helpers. This is stricter than the denylist above — it catches an accidental
// dependency on ANY new subsystem, not just the enumerated ones.
func TestIDPDependencyAllowlist(t *testing.T) {
	const prefix = "github.com/vul-os/vulos-management/pkg/"
	allowed := map[string]bool{
		prefix + "auth":     true,
		prefix + "httpx":    true,
		prefix + "cpdb":     true,
		prefix + "env":      true,
		prefix + "idp":      true,
		prefix + "secrets":  true, // may be pulled in transitively via auth/env
		prefix + "apptoken": true, // identity credential class the session gate rejects
	}
	deps := transitiveDeps(t, "github.com/vul-os/vulos-management/pkg/idp")
	for _, d := range deps {
		if !strings.HasPrefix(d, prefix) {
			continue // third-party / stdlib deps are fine
		}
		if !allowed[d] {
			t.Errorf("unexpected first-party dependency in pkg/idp graph: %s\n"+
				"If this is legitimately part of the identity boundary, add it to the "+
				"allowlist AND confirm it does not pull in billing/provisioning/mail.", d)
		}
	}
}

// transitiveDeps returns the full transitive import set of pkg via `go list`.
func transitiveDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	deps := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			deps = append(deps, l)
		}
	}
	return deps
}
