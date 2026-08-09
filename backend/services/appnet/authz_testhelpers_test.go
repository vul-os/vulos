package appnet

import "net/http"

// allowAllAppOwners is a test-only authorizer that permits everything.
//
// It exists because a nil authorizer now DENIES, and the handler tests above
// are not testing authorization — they cover 400/404/409 request handling and
// would otherwise all collapse to 403. Passing this explicitly keeps those
// tests focused while making the fail-closed default the one a forgetful
// caller gets. Authorization itself is covered by the dedicated tests that
// supply a real authorizer and assert the cross-user denial.
var allowAllAppOwners AppOwnerAuthorizer = func(*http.Request, string) bool { return true }
