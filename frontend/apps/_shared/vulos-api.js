/* ---------------------------------------------------------------------------
   vulos-api.js — resolve an app's own API URL against the mount point the app
   is actually being served from.

   THE DEFECT THIS EXISTS TO KILL
   -----------------------------
   A bundled app is served from two different places depending on one server
   flag, and only one of them makes an absolute `/api/…` correct:

     per-app origins ON   →  https://{app}--{profile}.{base}[:port]/     ← app at root
     per-app origins OFF  →  https://{base}[:port]/app/{app}/            ← app under a prefix

   `backend/services/gateway/gateway.go` Handler() builds `appPath` from the
   request in both shapes: on an app origin `appPath = r.URL.Path` (the app sees
   the whole path), on the path prefix `appPath` is what is left after
   `/app/{id}` is stripped. So the app's server only ever sees `/api/info` —
   which is why `/app/system-info/api/info` works when you curl it, and why
   `/api/info` from inside the frame does not: it never entered the gateway's
   app route at all.

   OFF is the DEFAULT (`frontend/src/core/AppOrigins.ts`: the fallback config is
   `{enabled:false}`, and `setOriginConfig` additionally requires a non-empty
   base_domain), so the broken shape is the normal shape. An absolute `/api/…`
   from the frame leaves the app's mount point and lands on the SHELL's origin,
   where it is either a 404 or — worse — a real, unrelated shell endpoint.

   WHY A HELPER AND NOT JUST A RELATIVE PATH
   -----------------------------------------
   `fetch('api/info')` does resolve correctly against `document.baseURI` in both
   modes, and that is 90% of the fix. It is not all of it: the gateway also
   serves an app addressed WITHOUT the trailing slash (`/app/gallery`, see
   gateway_test.go TestPathFallback), and against that document URL a bare
   `api/info` resolves to `/app/api/info` — a different app's mount, silently.
   `appUrl()` reads the mount out of the path instead of trusting the base URI,
   so it is right either way. It is also a name: a reviewer who sees
   `vulosApi.appUrl('/api/info')` will not "tidy" it back into `'/api/info'`,
   which is exactly how this bug is written in the first place.

   THIS FILE IS THE SINGLE SOURCE OF TRUTH.
   It is inlined verbatim into every bundled app's index.html inside a script
   element tagged data-vulos-shared="vulos-api.js". Inlined rather than linked
   because each app ships its own hand-rolled `server.py` with its own static
   route table (fifteen different shapes — four of them serve
   `../_shared/vulos-tokens.css`, eleven serve nothing shared at all), so a
   `<script src>` would be a 404 and a dead app in every server.py that was not
   also edited. An inline copy has no such failure mode.

   Regenerate the copies:  node frontend/apps/_shared/sync-api-helper.mjs
   The copies are pinned byte-for-byte by
   backend/internal/docsref/appapibase_test.go, so drift fails the build.
   --------------------------------------------------------------------------- */
(function (root) {
  'use strict';

  // The gateway's path-prefix route. Kept in sync with PATH_PREFIX in
  // frontend/src/core/AppOrigins.ts and the `/app/` literal in gateway.go.
  var PREFIX = '/app/';
  // The same app-id shape AppOrigins.gatewayAppId accepts. Deliberately strict:
  // anything else is not a mount we minted, so we do not treat it as one.
  var MOUNT = /^\/app\/([a-z0-9][a-z0-9-]*)(?:\/|$)/;

  // mountBase returns the path the app is served under, always with a trailing
  // slash: '/app/{id}/' behind the gateway path prefix, '/' on an app origin
  // (and '/' when the app's own server is hit directly in development).
  function mountBase(pathname) {
    var p = typeof pathname === 'string'
      ? pathname
      : (root && root.location && root.location.pathname) || '/';
    var m = MOUNT.exec(p);
    return m ? PREFIX + m[1] + '/' : '/';
  }

  // appUrl maps a path the app's own server implements onto the URL the browser
  // must actually request. A fully-qualified URL is returned untouched.
  function appUrl(path, pathname) {
    var p = path == null ? '' : String(path);
    if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(p) || p.slice(0, 2) === '//') return p;
    return mountBase(pathname) + p.replace(/^\/+/, '');
  }

  // appFetch is appUrl + fetch, so a call site changes in one place.
  function appFetch(path, init) {
    return fetch(appUrl(path), init);
  }

  var api = { mountBase: mountBase, appUrl: appUrl, appFetch: appFetch };
  if (root) root.vulosApi = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : null);
