// account_detail.js — loads the read-only per-account billing view.
//
// Served from GET /superadmin/account_detail.js (go:embed) under a strict CSP
// (script-src 'self'). No inline JS. The account id is read from a data-*
// attribute on the billing table, not from an inline <script> block.
(function () {
  "use strict";
  var tbl = document.getElementById("billing-table");
  var tierEl = document.getElementById("billing-tier");
  if (!tbl || !tierEl) return;
  var id = tbl.getAttribute("data-account-id");
  if (!id) return;

  var LABELS = {
    tier: "Tier", suspended: "Suspended",
    max_send_per_day: "Max send/day", max_bytes: "Mailbox bytes", max_addresses: "Max addresses",
    max_storage_bytes: "Storage bytes", max_seats: "Max seats", features: "Features",
    llm_enabled: "LLM enabled", llm_budget_usd: "LLM budget USD (remaining)",
    relay_enabled: "Relay enabled", relay_bytes_budget: "Relay bytes (remaining)",
    gpu_enabled: "GPU enabled", gpu_session_cap: "GPU session cap",
    meet_enabled: "Meet enabled", meet_minutes_budget: "Meet minutes (remaining)", meet_max_rooms: "Meet max rooms",
    compute_enabled: "Compute enabled", compute_box_cap: "Compute box cap", compute_storage_gb: "Compute storage GB",
    send_count: "Sends", storage_bytes: "Storage bytes", seats_count: "Seats",
    llm_tokens: "LLM tokens", llm_cost_usd_micros: "LLM cost (uUSD)",
    meet_minutes: "Meet minutes", compute_minutes: "Compute minutes", compute_machine: "Compute machines",
    relay_bytes: "Relay bytes", gpu_sessions: "GPU sessions"
  };

  function fmtVal(v) {
    if (v && typeof v === "object") {
      return Object.keys(v).map(function (k) { return k + "=" + v[k]; }).join(", ");
    }
    return String(v);
  }
  function rows(obj) {
    var ul = document.createElement("ul");
    ul.className = "kvlist";
    Object.keys(obj).forEach(function (k) {
      if (k === "tier" || k === "suspended") return;
      var li = document.createElement("li");
      li.textContent = (LABELS[k] || k) + ": " + fmtVal(obj[k]);
      ul.appendChild(li);
    });
    if (!ul.childNodes.length) {
      var li = document.createElement("li"); li.textContent = "—"; ul.appendChild(li);
    }
    return ul;
  }

  fetch("/api/superadmin/accounts/" + encodeURIComponent(id) + "/billing", { credentials: "same-origin" })
    .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
    .then(function (b) {
      tierEl.textContent = "Tier: " + (b.tier || "free") + "  ·  Suspended: " + (b.suspended ? "yes" : "no");
      var order = ["mail", "office", "meet", "llm", "compute", "relay", "gpu"];
      order.forEach(function (p) {
        var row = (b.products && b.products[p]) || {};
        var ent = row.entitlement || {};
        var use = row.usage || {};
        var tr = document.createElement("tr");
        var td0 = document.createElement("td"); td0.textContent = p; tr.appendChild(td0);
        var td1 = document.createElement("td"); td1.className = "wrap"; td1.appendChild(rows(ent)); tr.appendChild(td1);
        var td2 = document.createElement("td"); td2.className = "wrap"; td2.appendChild(rows(use)); tr.appendChild(td2);
        tbl.appendChild(tr);
      });
      tbl.hidden = false;
    })
    .catch(function (e) { tierEl.textContent = "Billing view unavailable: " + e.message; });
})();
