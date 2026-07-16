// wire_orgs.go — ORG-MULTI-01: builds the multi-org service and the signup hook
// that creates an org for every new account.
//
// provisionOrgOnSignup is called from BOTH signup paths (native in
// routes_auth.go, OAuth-linked in routes_auth_oauth.go). Like
// provisionAnchorInbox it runs at REQUEST time via the shared package var, so
// the wiring order in main() does not matter — the var only has to be set before
// the first request is served.
//
// Vulos runs no hosted mail (mail is a bring-your-own connector), so the org
// plane no longer provisions a hosted root mailbox: NewOrgService is wired with a
// nil provisioner and an empty mail domain, which makes the org-mailbox path
// inert (no mailbox is ever stamped or brokered).
package cproutes

import (
	"context"
	"log"

	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

// orgService is the process-wide multi-org service. Set by wireOrgs during
// startup; nil until then (provisionOrgOnSignup is nil-safe).
var orgService *orgadmin.OrgService

// wireOrgs builds the OrgService over the given org store. The paidPlan checker
// (may be nil) drives the anti-farming org caps (CLOUD-BILLING-EDGES edge #1).
// Returns the service (also stored in the package var). The mailer is nil and the
// mail domain is empty — hosted mailboxes are not provisioned (no hosted mail).
func wireOrgs(store orgadmin.OrgStore, paidPlan orgadmin.PaidPlanFunc) *orgadmin.OrgService {
	svc := orgadmin.NewOrgService(store, nil, "")
	svc.PaidPlanChecker = paidPlan
	orgService = svc
	return svc
}

// provisionOrgOnSignup creates the org owned by the new account. Errors are
// logged but NEVER fail the signup — the org can be back-filled. Mirrors
// provisionAnchorInbox.
func provisionOrgOnSignup(ctx context.Context, accountID, slugHint, name string) {
	if orgService == nil {
		log.Printf("[orgs] service not initialised — skipping org provision for account=%s", accountID)
		return
	}
	item, err := orgService.ProvisionOrgForSignup(ctx, accountID, slugHint, name)
	if err != nil {
		log.Printf("[orgs] provision org failed for account=%s: %v", accountID, err)
		return
	}
	log.Printf("[orgs] provisioned org id=%s slug=%s owner=%s", item.ID, item.Slug, accountID)
}
