/* ---------------------------------------------------------------------------
   vulos-theme.js — let a bundled app render in the theme the SHELL is in.

   THE PROBLEM
   -----------
   A bundled app is a separate document in an iframe. With per-app origins off
   (the default) it runs in an OPAQUE origin — src/core/AppOrigins.ts withholds
   allow-same-origin on the shell's own origin, deliberately — and with them on
   it runs on its own origin. Either way it cannot read the shell's
   <html data-theme>, and it cannot read the shell's localStorage where
   ThemeProvider persists the user's choice.

   So prefers-color-scheme is all an app can see by itself, and that is only
   ever right for ONE of the shell's four theme modes. ThemeProvider supports
   'auto' (follow the system), 'light', 'dark' and 'schedule'; three of those
   can disagree with the system preference, and when they do the app renders
   light furniture inside a dark shell. An app that is consistently wrong is
   better than one that is wrong only sometimes — so guessing is not an option
   either.

   THE CHANNEL
   -----------
   The shell already puts data into the app's frame URL. AppBridge.appFrameSrc()
   appends `#__vulos_s=<seed>`, the app's own storage snapshot, to every frame
   src. A fragment is the right carrier and it is already proven here:

     - it never reaches the server, so it cannot leak into gateway logs or be
       cached by the proxy;
     - it survives the `?_r=<attempt>` query-string cache-bust IframeApp applies
       when it retries a frame;
     - it is readable on the frame's first line, before first paint, so there is
       no flash of the wrong theme;
     - changing ONLY the fragment fires hashchange without reloading the frame,
       so a live theme switch in the shell costs the app nothing.

   This file reads `__vulos_t` off that same fragment and stamps data-theme on
   <html>, where apps/_shared/vulos-tokens.css layers 2 and 3 pick it up. When
   the parameter is ABSENT it stamps nothing, on purpose: the attribute's
   absence is what lets the prefers-color-scheme fallback in layer 4 govern. A
   stamped guess would silently disable that fallback.

   WHAT IS STILL MISSING
   ---------------------
   The sender. appFrameSrc must add `__vulos_t=<resolved>` alongside
   `__vulos_s`, and that file is shell-owned. Until it does, every app follows
   the system preference — correct for the DEFAULT mode ('auto') and wrong only
   for an explicit Light/Dark/Schedule choice. This file is the receiving half
   and is inert until the sender lands: no parameter, no attribute, no change.

   THIS FILE IS A SINGLE SOURCE OF TRUTH.
   Inlined verbatim into every bundled app by sync-shared-assets.mjs and pinned
   byte-for-byte by backend/internal/docsref/appthemetokens_test.go.
   --------------------------------------------------------------------------- */
(function (root) {
  'use strict';

  // The fragment key. Kept alongside __vulos_s, which AppBridge already sends.
  var PARAM = '__vulos_t';
  // The only two values that mean anything. ThemeProvider resolves 'auto' and
  // 'schedule' down to one of these before it stamps its own attribute, so the
  // app never has to know about modes — only about the resolved result.
  var VALID = { light: 1, dark: 1 };

  // themeFromHash extracts the resolved theme, or '' when the shell did not
  // send one (or sent something that is not a theme).
  function themeFromHash(hash) {
    var h = typeof hash === 'string'
      ? hash
      : (root && root.location && root.location.hash) || '';
    if (h.charAt(0) === '#') h = h.slice(1);
    if (!h) return '';
    for (var i = 0, parts = h.split('&'); i < parts.length; i++) {
      var eq = parts[i].indexOf('=');
      if (eq < 0) continue;
      if (parts[i].slice(0, eq) !== PARAM) continue;
      var v = decodeURIComponent(parts[i].slice(eq + 1)).toLowerCase();
      return VALID[v] ? v : '';
    }
    return '';
  }

  // applyTheme stamps <html data-theme>, or REMOVES it when the shell has not
  // told us — absence is what hands control to the prefers-color-scheme
  // fallback, so it is a meaningful state and not merely "unset".
  function applyTheme(doc, hash) {
    var d = doc || (root && root.document);
    if (!d || !d.documentElement) return '';
    var t = themeFromHash(hash);
    if (t) d.documentElement.setAttribute('data-theme', t);
    else d.documentElement.removeAttribute('data-theme');
    return t;
  }

  var api = { themeFromHash: themeFromHash, applyTheme: applyTheme };
  if (root) {
    root.vulosTheme = api;
    applyTheme();
    // A shell-side theme switch rewrites only the fragment, so the frame is not
    // reloaded and this is the only thing that would notice.
    if (root.addEventListener) {
      root.addEventListener('hashchange', function () { applyTheme(); });
    }
  }
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : null);
