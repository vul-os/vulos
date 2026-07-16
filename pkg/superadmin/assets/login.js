// login.js — WebAuthn assertion ceremony for the super-admin login page.
//
// Served from GET /superadmin/login.js (go:embed) under a strict CSP
// (script-src 'self'). It is the ONLY script on the login page; there is no
// inline JS. The per-request WebAuthn options are passed via a data-* attribute
// on the form (a JSON string), NOT via an inline <script> block.
(function () {
  "use strict";
  var form = document.getElementById("waf");
  var btn = document.getElementById("wa-activate");
  if (!form || !btn) return;

  function b64urlToBytes(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s);
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }

  function activate() {
    var raw = form.getAttribute("data-options");
    if (!raw) return;
    var parsed;
    try { parsed = JSON.parse(raw); } catch { showErr("bad options"); return; }
    var pk = parsed.publicKey || parsed;

    // Decode the base64url challenge + credential ids into ArrayBuffers, as
    // required by the WebAuthn API.
    try {
      pk.challenge = b64urlToBytes(pk.challenge);
      if (pk.allowCredentials) {
        pk.allowCredentials = pk.allowCredentials.map(function (c) {
          return { type: c.type, id: b64urlToBytes(c.id), transports: c.transports };
        });
      }
    } catch { showErr("decode error"); return; }

    navigator.credentials.get({ publicKey: pk }).then(function (cred) {
      document.getElementById("assertion_input").value = JSON.stringify({
        id: cred.id,
        rawId: Array.from(new Uint8Array(cred.rawId)),
        response: {
          authenticatorData: Array.from(new Uint8Array(cred.response.authenticatorData)),
          clientDataJSON: Array.from(new Uint8Array(cred.response.clientDataJSON)),
          signature: Array.from(new Uint8Array(cred.response.signature)),
        },
        type: cred.type,
      });
      form.submit();
    }).catch(function (e) { showErr(String(e)); });
  }

  function showErr(msg) {
    var host = document.getElementById("wa-error");
    if (host) { host.textContent = "WebAuthn error: " + msg; host.hidden = false; }
  }

  btn.addEventListener("click", activate);
})();
