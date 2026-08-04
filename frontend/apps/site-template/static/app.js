// static/app.js — a real, same-origin asset so the box's asset-serving path
// (and its cache headers) get exercised by a plain <script src>. No imports,
// no external network. It simply confirms to the page that /static/ resolved.
(function () {
  "use strict";

  function markLoaded() {
    var el = document.getElementById("asset-status");
    if (el) {
      el.textContent = "static assets served OK (app.js + style.css)";
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", markLoaded);
  } else {
    markLoaded();
  }
})();
