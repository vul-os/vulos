package appnet

import "net/http"

// AppOwnerAuthorizer decides whether the current request may act on an
// already-published app's management surface: deployment info/deprovision
// (subdomain_provision_api.go), edge-cache purge/stats (edgecache.go), and
// custom-domain attach/verify/remove (customdomain_api.go).
//
// SECURITY (Finding, HIGH — matches Finding 2 in visibility_api.go): these
// three route groups are registered on the SAME /api/apps/{id}/* namespace as
// the visibility endpoints, which are gated by PublishAuthorizer, but they
// were NOT — any authenticated caller on a multi-user box could deprovision
// another user's public app, purge/inspect its edge cache, or attach/verify/
// remove a custom domain on it, just by supplying its app ID. There is no
// "visibility" being set here so PublishAuthorizer's signature doesn't fit;
// this is the equivalent gate for the rest of the deployment-management
// surface.
//
// A nil authorizer DENIES. That is deliberately the opposite of
// PublishAuthorizer's older "gating off by default" convention: this gate
// protects deprovisioning another user's app and attaching domains to it, and a
// gate whose default is "allow" is the exact shape of the bugs this audit keeps
// finding. No caller passes nil today — main.go always installs
// appnetOwnerAuthorizer, which requires RoleAdmin OR app ownership and itself
// fails closed when the auth store is missing — so denying on nil costs nothing
// and removes the trap where a future registrar forgets the argument and
// silently opens the surface.
type AppOwnerAuthorizer func(r *http.Request, appID string) bool
