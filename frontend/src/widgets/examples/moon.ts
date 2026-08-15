// moon.ts — A WIDGET BUILT THE WAY A THIRD PARTY WOULD BUILD ONE.
//
// This is the proof that the public API is usable, and it is deliberately the
// UNTRUSTED lane: the code below never touches React, never imports a host
// module, and runs inside `<iframe sandbox="allow-scripts">` on an opaque
// origin. It cannot see the shell's DOM, cookies, localStorage or session. It
// gets `window.vulosWidget` — injected by the host — and nothing else, exactly
// as a widget shipped by a stranger would.
//
// It is also a genuinely useful widget that needs no third party at all: the
// moon's phase is a function of the date, so this computes it rather than asking
// anyone. That is the shape a good widget on this platform has.
//
// A real third-party widget would ship this string in its package instead of
// living in the OS tree; nothing else about it would change.

import { defineSandboxedWidget, registerWidget } from '../index'

// The widget's whole program. Plain ES5-ish JS because it runs as-is inside the
// frame — there is no build step on the far side of a srcdoc boundary.
const SOURCE = `
<div id="root" style="padding:11px 13px;height:100%;display:flex;flex-direction:column;gap:6px">
  <div style="font-size:10.5px;font-weight:600;letter-spacing:.1em;text-transform:uppercase;color:var(--text-tertiary)">Moon</div>
  <div style="display:flex;align-items:center;gap:10px;flex:1;min-height:0">
    <canvas id="disc" width="46" height="46" style="width:46px;height:46px;flex:0 0 auto"></canvas>
    <div style="min-width:0">
      <div id="phase" style="font-size:13px;font-weight:600;color:var(--text-primary)">—</div>
      <div id="illum" style="font-size:11.5px;color:var(--text-tertiary);font-family:var(--font-mono)">—</div>
    </div>
  </div>
</div>
<script>
(function () {
  // Reference new moon: 2000-01-06 18:14 UTC. Synodic month in days.
  var NEW_MOON = Date.UTC(2000, 0, 6, 18, 14, 0);
  var SYNODIC = 29.530588853;

  function phaseOf(date) {
    var days = (date.getTime() - NEW_MOON) / 86400000;
    var age = days % SYNODIC;
    if (age < 0) age += SYNODIC;
    return age / SYNODIC; // 0 = new, .5 = full
  }

  function nameOf(p) {
    if (p < 0.02 || p > 0.98) return 'New moon';
    if (p < 0.23) return 'Waxing crescent';
    if (p < 0.27) return 'First quarter';
    if (p < 0.48) return 'Waxing gibbous';
    if (p < 0.52) return 'Full moon';
    if (p < 0.73) return 'Waning gibbous';
    if (p < 0.77) return 'Last quarter';
    return 'Waning crescent';
  }

  function draw(p) {
    var cv = document.getElementById('disc');
    var ctx = cv.getContext('2d');
    var cs = getComputedStyle(document.documentElement);
    var lit = cs.getPropertyValue('--text-primary').trim() || '#fff';
    var dark = cs.getPropertyValue('--bg-hover').trim() || '#222';
    var r = 22, cx = 23, cy = 23;
    ctx.clearRect(0, 0, 46, 46);
    ctx.fillStyle = dark;
    ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.fill();
    // Terminator: the lit fraction drawn as a half-disc plus an ellipse whose
    // x-radius tracks the phase. Waxing lights the right limb, waning the left.
    var illum = (1 - Math.cos(2 * Math.PI * p)) / 2;
    var waxing = p < 0.5;
    ctx.fillStyle = lit;
    ctx.beginPath();
    ctx.arc(cx, cy, r, -Math.PI / 2, Math.PI / 2, !waxing);
    ctx.ellipse(cx, cy, r * Math.abs(1 - 2 * illum), r, 0, Math.PI / 2, -Math.PI / 2, illum > 0.5 ? !waxing : waxing);
    ctx.fill();
    return illum;
  }

  function paint(ctx) {
    var p = phaseOf(ctx.now);
    var illum = draw(p);
    document.getElementById('phase').textContent = nameOf(p);
    document.getElementById('illum').textContent = Math.round(illum * 100) + '% lit';
  }

  // The ONLY interface this widget has with the OS.
  window.vulosWidget.onContext(paint);
})();
</script>
`.trim()

registerWidget(defineSandboxedWidget({
  manifest: {
    id: 'com.example.moon',
    name: 'Moon phase',
    description: 'Tonight’s moon, computed from the date. Asks for nothing.',
    version: '1.0.0',
    author: 'Example Widgets',
    sizes: ['small', 'medium'],
    tick: 'minute',
    // No permissions. A widget that needs nothing should request nothing, and
    // the gallery says "no permissions" for exactly this reason.
  },
  source: SOURCE,
}))
