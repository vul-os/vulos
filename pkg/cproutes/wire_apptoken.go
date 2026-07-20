// wire_apptoken.go — SECURITY-C1: stop handing the user's CP session to the
// app backends we reverse-proxy to.
//
// THE HOLE (historical). VULOS_COOKIE_DOMAIN scopes `vc_session` to the apex
// (vulos.org), so the browser attaches it to EVERY app subdomain — office./
// files.${VULOS_DOMAIN}. The reverse proxies forwarded the request
// verbatim, so each app backend received a live, 30-day, full-privilege CP
// session credential. Office even depended on it: it introspected it to learn
// who the user is, and replayed it to the CP storage gateway to presign
// objects. Any one of those backends — a separately deployed, lower-trust,
// partly open-source product — could therefore act as the user across all
// ~217 session-gated CP routes (billing, account, security settings, storage
// config). (Talk and Meet were withdrawn as products entirely, 2026-07-15:
// comms are third-party now, so they never held this credential in the first
// place.)
//
// THE FIX. The proxy now strips `vc_session` and stamps a CP-minted
// app-identity token in its place (internal/apptoken): audience-bound to exactly
// one app, ~2 minutes long, and NOT a session — auth.RequireSession rejects the
// class outright, so it cannot be replayed against any session-gated route. The
// app's blast radius shrinks from "the whole account" to "the delegated
// capabilities we grant that one audience".
//
// BACK-COMPAT (no app-repo change required). The minted token is stamped in TWO
// places: the `X-Vulos-App-Auth` header (the clean, forward-looking contract an
// app can verify or introspect), AND as the value of the forwarded `vc_session`
// cookie. Office keeps working untouched, because the only two things it ever
// does with that cookie are (a) POST it to /api/session/introspect — which
// now resolves app tokens (auth.IntrospectSession) — and (b) forward it to
// /api/storage/presign|delete — which now accepts app tokens and binds the
// object prefix to the token's audience (routes_storage.go). Once the app moves
// to reading the header, the cookie stamp can be dropped.
//
// KEYING. The signing key is DERIVED from CP_SHARED_SECRET (domain-separated by
// appTokenKeyLabel) rather than being a new deploy secret: every deployment
// where Office works already sets CP_SHARED_SECRET, because introspection
// fail-closes without it. Rotation is honoured — a token minted under the
// previous shared secret still verifies while CP_SHARED_SECRET_PREVIOUS is set.
// CP_APP_TOKEN_SECRET overrides the derivation if an operator wants an
// independent key.
//
// FAIL-CLOSED. With no key configured we mint nothing and still strip the
// cookie: the app sees an unauthenticated request (401) rather than inheriting
// the user's session. Denying access is the safe direction; leaking the session
// is not.
//
// WHERE THIS RUNS. This OSS module operates NO app reverse proxy of its own —
// self-host reaches the Class-P apps directly, and the hosted CP's
// reverse-proxy front door (the code that actually calls stampAppIdentity while
// forwarding a request to office./files./… app backends) is deployed in the
// vulos-cloud control-plane repo, backend/cp/cmd/server, which vendors this
// package and carries its own copy of these mint helpers. There is no
// wire_appproxy.go / wire_mailproxy.go in this repo. What this file provides
// HERE is (1) the REFERENCE mint implementation (stampAppIdentity / mintAppToken)
// that such a proxy calls, and (2) the RECEIVE side that is live in every
// deployment: initAppIdentity teaches auth.Store to introspect these tokens, and
// routes_storage.go binds a presign/delete to the token's audience. Because this
// module never proxies, it never forwards a user session to an app backend —
// the SECURITY-C1 invariant holds here vacuously.
//lint:file-ignore U1000 The mint/proxy half of the app-identity seam is a reference
// implementation here. It is deployed in vulos-cloud, which carries its own copy of this
// file and calls stampAppIdentity from its reverse-proxy front door. This module operates
// no app proxy at all (no httputil.ReverseProxy anywhere), so nothing local calls it —
// but the RECEIVE side IS wired, via initAppIdentity in register_all.go, and routes_storage.go
// binds presign/delete to the token audience. Kept whole so both halves stay readable
// together, and so SECURITY-C1 (a CP proxy must never forward a user session to an app
// backend) can be checked against one file rather than reconstructed from two repos.

package cproutes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/apptoken"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// appTokenKeyLabel domain-separates the derived app-token key from every other
// use of CP_SHARED_SECRET (service-auth compares, device auth, …), so holding an
// app token tells you nothing about the shared secret and vice versa.
const appTokenKeyLabel = "vulos-apptoken-v1"

// appTokenSecretEnv lets an operator configure an INDEPENDENT app-token signing
// key instead of the CP_SHARED_SECRET derivation. Rotation-aware via the usual
// _PREVIOUS suffix.
const appTokenSecretEnv = "CP_APP_TOKEN_SECRET"

// seam is deployed in vulos-cloud, which carries its own copy of this file and calls
// stampAppIdentity from its reverse-proxy front door; this repo operates no app proxy
// (no httputil.ReverseProxy anywhere) and only serves the RECEIVE side, which IS wired
// via initAppIdentity in register_all.go. Kept so the two halves stay readable together.
// appIdentityStore is the auth store used to resolve a session cookie to the
// user it authenticates. Set once at startup by initAppIdentity. Nil means the
// proxies mint nothing (and still strip the session cookie — fail-closed).
//
//lint:ignore U1000 Reference implementation. The proxy/mint side of the app-identity
var appIdentityStore *auth.Store

// initAppIdentity wires the session→user resolver used by the reverse proxies,
// and teaches auth.Store how to introspect the app tokens we mint. Called from
// the composition root once authStore exists.
func initAppIdentity(st *auth.Store) {
	appIdentityStore = st
	st.SetAppTokenIntrospector(func(ctx context.Context, token, expectedAud string) (userID string, expiresAt time.Time, ok bool) {
		keys := appTokenKeys(ctx)
		// expectedAud (F4) is the audience of the app that is CALLING introspect,
		// threaded through from the route layer. When non-empty it binds the
		// verification: a token minted for another app is rejected (ErrAudience)
		// before it can resolve to a user, so app B cannot introspect app A's
		// token. Empty aud preserves the audience-agnostic path used where a
		// requester is unknown; every capability-bearing path (presign/delete)
		// still passes a concrete audience of its own.
		c, err := apptoken.VerifyAny(keys, token, expectedAud, time.Now())
		if err != nil {
			return "", time.Time{}, false
		}
		return c.Sub, time.Unix(c.Exp, 0).UTC(), true
	})
}

// appTokenKeys returns the accepted app-token signing keys, newest first: the
// explicit CP_APP_TOKEN_SECRET candidates when configured, otherwise the keys
// derived from each CP_SHARED_SECRET rotation candidate. Empty when nothing is
// configured — callers fail closed.
func appTokenKeys(ctx context.Context) [][]byte {
	if explicit := secretCandidates(ctx, appTokenSecretEnv); len(explicit) > 0 {
		out := make([][]byte, 0, len(explicit))
		for _, s := range explicit {
			out = append(out, []byte(s))
		}
		return out
	}
	shared := secretCandidates(ctx, "CP_SHARED_SECRET")
	out := make([][]byte, 0, len(shared))
	for _, s := range shared {
		out = append(out, deriveAppTokenKey(s))
	}
	return out
}

// deriveAppTokenKey derives the app-token signing key from a shared secret.
func deriveAppTokenKey(shared string) []byte {
	m := hmac.New(sha256.New, []byte(shared))
	m.Write([]byte(appTokenKeyLabel))
	return m.Sum(nil)
}

// mintAppToken issues an app-identity token for userID bound to audience aud.
// Returns ("", false) when no signing key is configured.
func mintAppToken(ctx context.Context, userID, aud string) (string, bool) {
	keys := appTokenKeys(ctx)
	if len(keys) == 0 {
		return "", false
	}
	tok, err := apptoken.NewMinter(keys[0], apptoken.DefaultTTL).Mint(userID, aud)
	if err != nil {
		return "", false
	}
	return tok, true
}

// --- session→user resolution, with a short positive cache ---
//
// The proxies sit in front of whole SPAs, so EVERY asset request (js/css/fonts)
// passes through here. auth.LookupSession is not free — besides the SELECT it
// writes sessions.last_seen_at — so resolving per asset would put a DB write on
// the CP's hottest path. A small positive cache bounds that to one lookup per
// session per appIdentityCacheTTL.
//
// The cost is revocation lag: for up to appIdentityCacheTTL after a logout we
// may still mint an app token for the dead session. That is bounded and
// acceptable — the tokens themselves live ~2 minutes, and CP logout already does
// not terminate an app's own downstream session (Office mints its own product
// session after introspecting). Keep the TTL short.
const appIdentityCacheTTL = 15 * time.Second

// appIdentityCacheMax caps the cache so a flood of distinct cookies cannot grow
// it without bound. On overflow we drop everything rather than evict cleverly —
// the entries are 15s-lived and trivially recomputed.
const appIdentityCacheMax = 4096

type appIdentityEntry struct {
	userID string
	expiry time.Time
}

var appIdentityCache = struct {
	sync.Mutex
	m map[string]appIdentityEntry
}{m: make(map[string]appIdentityEntry)}

// resolveAppIdentity maps a raw session token to the user id it authenticates,
// or ("", false) for any absent/expired/revoked/partial/suspended session — the
// same fail-closed verdict auth.LookupSession reaches, since that is what backs
// it on a cache miss.
func resolveAppIdentity(ctx context.Context, sessionToken string) (string, bool) {
	if appIdentityStore == nil || sessionToken == "" {
		return "", false
	}
	now := time.Now()

	appIdentityCache.Lock()
	if e, ok := appIdentityCache.m[sessionToken]; ok && now.Before(e.expiry) {
		appIdentityCache.Unlock()
		return e.userID, true
	}
	appIdentityCache.Unlock()

	u, err := appIdentityStore.LookupSession(ctx, sessionToken)
	if err != nil || u == nil || u.ID == "" {
		// Deliberately NOT cached: a negative cache would let a flood of junk
		// cookies pin memory, and the miss path is already a single failed
		// lookup.
		return "", false
	}

	appIdentityCache.Lock()
	if len(appIdentityCache.m) >= appIdentityCacheMax {
		appIdentityCache.m = make(map[string]appIdentityEntry, appIdentityCacheMax)
	}
	appIdentityCache.m[sessionToken] = appIdentityEntry{userID: u.ID, expiry: now.Add(appIdentityCacheTTL)}
	appIdentityCache.Unlock()
	return u.ID, true
}

// --- the proxy-side header surgery ---

// stampAppIdentity rewrites r IN PLACE so a lower-trust app backend behind the
// reverse proxy never receives the user's CP session credential:
//
//  1. any client-supplied X-Vulos-App-Auth is DROPPED first — the header is
//     trusted downstream, so it must only ever be one we minted (the same
//     spoof-proofing productLandingMux applies to the custom-domain tenant
//     header);
//  2. the CP's own cookies (vc_session, vc_device) are removed from the
//     forwarded Cookie header, leaving the app's own cookies untouched;
//  3. if the stripped session resolved to a user, a fresh audience-bound app
//     token is stamped — in the header, and (for back-compat) as the value of
//     the forwarded vc_session cookie.
//
// Authorization is deliberately left ALONE: `vk_` API keys are the app's own
// credential, deliberately presented to it by the user. Stripping it would
// break that flow without closing this hole.
//
// aud is the app slug the token is minted for ("office", "mail", …).
func stampAppIdentity(r *http.Request, aud string) {
	r.Header.Del(apptoken.HeaderName)

	raw := r.Header.Get("Cookie")
	if raw == "" {
		return
	}
	session := rawCookieValue(raw, auth.SessionCookieName)
	stripped := stripCookies(raw, auth.SessionCookieName, auth.DeviceCookieName)

	userID := ""
	if session != "" {
		if id, ok := resolveAppIdentity(r.Context(), session); ok {
			userID = id
		}
	}
	if userID == "" {
		// No (or no longer valid) session: forward without any CP credential.
		// The app sees an anonymous request and does its own 401/redirect.
		setCookieHeader(r, stripped)
		return
	}

	tok, ok := mintAppToken(r.Context(), userID, aud)
	if !ok {
		// No signing key configured. Fail CLOSED: the app gets an
		// unauthenticated request rather than the user's session.
		log.Printf("[apptoken] no signing key (set CP_SHARED_SECRET or %s) — %s gets an anonymous request", appTokenSecretEnv, aud)
		setCookieHeader(r, stripped)
		return
	}

	r.Header.Set(apptoken.HeaderName, tok)
	if stripped == "" {
		stripped = auth.SessionCookieName + "=" + tok
	} else {
		stripped += "; " + auth.SessionCookieName + "=" + tok
	}
	setCookieHeader(r, stripped)
}

// setCookieHeader writes (or removes) the forwarded Cookie header.
func setCookieHeader(r *http.Request, v string) {
	if v == "" {
		r.Header.Del("Cookie")
		return
	}
	r.Header.Set("Cookie", v)
}

// rawCookieValue returns the value of cookie name in a raw Cookie header, or "".
func rawCookieValue(raw, name string) string {
	for _, pair := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}

// stripCookies removes the named cookies from a raw Cookie header, preserving
// every other pair BYTE-FOR-BYTE (we re-emit the original text rather than
// re-encoding parsed cookies, so an app's own cookie values survive intact
// whatever quoting or padding they use).
func stripCookies(raw string, names ...string) string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := make([]string, 0, 8)
	for _, pair := range strings.Split(raw, ";") {
		p := strings.TrimSpace(pair)
		if p == "" {
			continue
		}
		k, _, _ := strings.Cut(p, "=")
		if drop[k] {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "; ")
}
