package cproutes

// routes_storage_apptoken_test.go — SECURITY-C1 at the storage gateway.
//
// A proxied cloud-central app (Files is one of the two Class-P audiences the
// CP still recognises for storage presign — see knownStorageApps) presigns
// objects on the user's behalf by forwarding the credential the proxy handed
// it. Two properties have to hold there:
//
//  1. that credential still WORKS (otherwise stripping the session just breaks
//     the app), and
//  2. it is confined to the app's OWN prefix — app_id is taken from the token's
//     audience, not from the request body, so a compromised app cannot presign
//     or delete another app's objects by simply asking to.
//
// (2) is the part that was broken before this change: app_id was a self-asserted
// body field, and the app held the user's full session anyway.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/apptoken"
)

// appTokenFor mints an app token the way the proxy does, for the configured key.
func appTokenFor(t *testing.T, userID, aud string) string {
	t.Helper()
	tok, ok := mintAppToken(context.Background(), userID, aud)
	if !ok {
		t.Fatal("mintAppToken: no signing key — check CP_SHARED_SECRET is set in the test")
	}
	return tok
}

// doWithAppToken issues a storage request authenticated by an app token in the
// vc_session cookie — exactly how Office forwards it today.
func (e *storageTestEnv) doWithAppToken(t *testing.T, path string, body any, appTok string) *httptest.ResponseRecorder {
	t.Helper()
	w := e.do(t, http.MethodPost, path, body, appTok)
	return w
}

// storageAppTokenEnv is newStorageTestEnv + a signing key + a user.
func storageAppTokenEnv(t *testing.T) (*storageTestEnv, string) {
	t.Helper()
	t.Setenv("CP_SHARED_SECRET", "storage-apptoken-test-secret")
	e := newStorageTestEnv(t)
	uid, _ := sessionFor(t, e.authSt, "apptoken-"+strings.ToLower(t.Name())+"@example.com")
	initAppIdentity(e.authSt)
	return e, uid
}

// Files' own prefix: the app token works, and the presign lands under
// <account>/files/.
func TestStoragePresign_AppTokenPresignsItsOwnPrefix(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	tok := appTokenFor(t, uid, "files")

	w := e.doWithAppToken(t, "/api/storage/presign/put", map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"key":        uid + "/files/doc-1.bin",
		"app_id":     "files",
	}, tok)

	if w.Code != http.StatusOK {
		t.Fatalf("files' own presign must still work: got %d: %s", w.Code, w.Body.String())
	}
}

// THE escalation this closes: a files-audience token asking to presign under
// another app's prefix. Before the fix, app_id was whatever the body said.
func TestStoragePresign_AppTokenCannotClaimAnotherAppsPrefix(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	tok := appTokenFor(t, uid, "files")

	for _, other := range []string{"mail", "os"} {
		w := e.doWithAppToken(t, "/api/storage/presign/put", map[string]any{
			"account_id": uid,
			"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
			"key":        uid + "/" + other + "/stolen.bin",
			"app_id":     other,
		}, tok)
		if w.Code != http.StatusForbidden {
			t.Errorf("files token presigned as %q: got %d, want 403", other, w.Code)
		}
	}
}

// Even with app_id omitted, the audience pins the prefix — a files token
// cannot reach a key outside <account>/files/ by leaving the field blank.
func TestStoragePresign_OmittedAppIDIsBoundToAudience(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	tok := appTokenFor(t, uid, "files")

	w := e.doWithAppToken(t, "/api/storage/presign/put", map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"key":        uid + "/os/stolen.bin",
		// app_id deliberately omitted — pre-fix this meant "no app scoping".
	}, tok)

	if w.Code != http.StatusForbidden {
		t.Fatalf("an app token must never presign outside its own prefix: got %d: %s", w.Code, w.Body.String())
	}
}

// Delete is destructive: same binding.
func TestStorageDelete_AppTokenCannotDeleteAnotherAppsObjects(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	tok := appTokenFor(t, uid, "files")

	w := e.doWithAppToken(t, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"app_id":     "mail",
		"key":        "important.eml",
	}, tok)

	if w.Code != http.StatusForbidden {
		t.Fatalf("files token deleted a mail object: got %d, want 403: %s", w.Code, w.Body.String())
	}
}

// A token for an audience with no storage prefix at all (mail authenticates its
// own way and owns no app-scoped prefix here) is refused outright.
func TestStoragePresign_AudienceWithoutStorageIsRefused(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	tok := appTokenFor(t, uid, "mail")

	w := e.doWithAppToken(t, "/api/storage/presign/get", map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"key":        uid + "/mail/x.bin",
	}, tok)

	if w.Code != http.StatusForbidden {
		t.Fatalf("a mail-audience token must not reach storage: got %d: %s", w.Code, w.Body.String())
	}
}

// An app token still cannot act for a DIFFERENT account: selfOnly holds, and the
// subject comes from the signed token, not the body.
func TestStoragePresign_AppTokenIsStillSelfOnly(t *testing.T) {
	e, uid := storageAppTokenEnv(t)
	victim, _ := sessionFor(t, e.authSt, "victim-storage@example.com")
	tok := appTokenFor(t, uid, "files")

	w := e.doWithAppToken(t, "/api/storage/presign/get", map[string]any{
		"account_id": victim,
		"bucket":     "vulos-" + strings.ToLower(boxULID(victim)),
		"key":        victim + "/files/secret.bin",
		"app_id":     "files",
	}, tok)

	if w.Code != http.StatusForbidden {
		t.Fatalf("an app token acted for another account: got %d, want 403: %s", w.Code, w.Body.String())
	}
}

// A forged app token (signed with a key we don't hold) is not a credential.
func TestStoragePresign_ForgedAppTokenRejected(t *testing.T) {
	e, uid := storageAppTokenEnv(t)

	forged, err := apptoken.NewMinter([]byte("attacker-key-not-the-cps-000000"), apptoken.DefaultTTL).Mint(uid, "files")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	w := e.doWithAppToken(t, "/api/storage/presign/put", map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"key":        uid + "/files/doc.bin",
		"app_id":     "files",
	}, forged)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a forged app token was accepted: got %d, want 401: %s", w.Code, w.Body.String())
	}
}

// And the cockpit's own session still works, unscoped, as before — the new class
// is additive, not a replacement.
func TestStoragePresign_UserSessionUnaffected(t *testing.T) {
	t.Setenv("CP_SHARED_SECRET", "storage-apptoken-test-secret")
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "cockpit-session@example.com")
	initAppIdentity(e.authSt)

	req := map[string]any{
		"account_id": uid,
		"bucket":     "vulos-" + strings.ToLower(boxULID(uid)),
		"key":        uid + "/files/doc.bin",
		"app_id":     "files",
	}
	if w := e.do(t, http.MethodPost, "/api/storage/presign/put", req, tok); w.Code != http.StatusOK {
		t.Fatalf("a real user session must still presign: got %d: %s", w.Code, w.Body.String())
	}
	// Sanity: the session cookie is a session, not an app token.
	if apptoken.Looks(tok) {
		t.Fatal("a session token must not be classed as an app token")
	}
}
