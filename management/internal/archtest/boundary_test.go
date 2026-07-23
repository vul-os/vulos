// Package archtest holds module-wide architectural boundary guards.
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoManagementPackageImportsCommercialBilling is the generalised successor
// to the idp import-boundary test. The operational control plane in this module
// must be runnable free/self-hosted with the no-op billing seam. It must NEVER
// import a billing, payments, or pricing IMPLEMENTATION — those live only in the
// commercial wrapper module (vulos-cloud, module path vulos.cloud/cp) and are
// injected through pkg/billingport / pkg/storageport at composition time.
//
// This guard runs `go list -deps` over every package in the module and fails if
// any transitively imports a package under the commercial module's import path.
// Because that module is not even a dependency of this one, a violation would
// require someone to add a require + import it — exactly the regression we want
// to catch loudly.
func TestNoManagementPackageImportsCommercialBilling(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v", err)
	}
	const commercial = "vulos.cloud/cp"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		d := strings.TrimSpace(line)
		if d == commercial || strings.HasPrefix(d, commercial+"/") {
			t.Errorf("BOUNDARY BREACH: a management package imports commercial dependency %q.\n"+
				"The operational control plane must stay billing-agnostic: route through\n"+
				"pkg/billingport / pkg/storageport instead of importing a commercial impl.", d)
		}
	}
}
