/**
 * PresenterView.jsx — opens a new window with the presenter dashboard.
 *
 * Layout (new window):
 *   ┌──────────────────────────┬──────────────────┐
 *   │  Current slide (large)   │  Next slide (sm) │
 *   │                          │  Timer           │
 *   │                          │  Progress        │
 *   ├──────────────────────────┴──────────────────┤
 *   │  Speaker notes (scrollable)                 │
 *   └─────────────────────────────────────────────┘
 *
 * Cross-window state sync uses BroadcastChannel (primary) with
 * localStorage fallback (for older Safari).
 *
 * Keyboard (inside new window): ← → Space advance; b blank; f fullscreen.
 *
 * This component renders into the EDITOR window and opens a child window.
 * It uses window.open() + postMessage / BroadcastChannel to keep both in sync.
 */

import { useEffect, useRef, useCallback } from 'react'
import DOMPurify from 'dompurify'

const PURIFY_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'input', 'button'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover'],
}
const sanitize = (html) => DOMPurify.sanitize(html ?? '', PURIFY_CONFIG)

// The presenter window HTML is injected as a blob URL so we stay same-origin.
function buildPresenterHTML(slides, activeIdx, themeId) {
  const slidesJson = JSON.stringify(slides.map((s) => ({
    id: s.id,
    title: s.title || '',
    content: sanitize(s.content || ''),
    notes: s.notes || '',
    background: s.background || '',
  })))

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Presenter View — Vulos Slides</title>
  <link rel="stylesheet"
    href="https://cdnjs.cloudflare.com/ajax/libs/reveal.js/5.1.0/reveal.min.css" />
  <link rel="stylesheet"
    href="https://cdnjs.cloudflare.com/ajax/libs/reveal.js/5.1.0/theme/${themeId || 'black'}.min.css" />
  <style>
    *,*::before,*::after{box-sizing:border-box}
    :root{
      --bg:#0f0f11;--surface:#1a1a22;--text:#f1f0ff;--muted:#9191a8;
      --accent:#7c6af7;--warn:#f0be4b;--danger:#f87171;
      --border:rgba(255,255,255,0.08);
    }
    html,body{margin:0;padding:0;background:var(--bg);color:var(--text);
      font-family:"Inter",system-ui,sans-serif;height:100%;overflow:hidden}
    #app{display:grid;grid-template-columns:1fr 280px;grid-template-rows:1fr auto;
      height:100vh;gap:0}
    /* Current slide */
    #current-wrap{grid-column:1;grid-row:1;position:relative;background:#000;
      display:flex;align-items:center;justify-content:center;overflow:hidden}
    #current-wrap.blanked{background:#000}
    #current-wrap.blanked #current-inner{opacity:0}
    #current-inner{width:100%;height:100%}
    .reveal,.reveal .slides{width:100%;height:100%}
    /* Right panel */
    #right{grid-column:2;grid-row:1;display:flex;flex-direction:column;
      border-left:1px solid var(--border);background:var(--surface);padding:12px;gap:10px;
      overflow:hidden}
    #next-label{font-size:10px;font-weight:600;letter-spacing:.08em;
      text-transform:uppercase;color:var(--muted);margin-bottom:4px}
    #next-slide{flex:0 0 auto;background:#111;border-radius:8px;overflow:hidden;
      aspect-ratio:16/9;border:1px solid var(--border)}
    #next-slide-inner{width:100%;height:100%;padding:8px;
      font-size:10px;color:var(--muted);overflow:hidden}
    #next-title{font-weight:700;color:var(--text);font-size:11px;margin-bottom:3px}
    #timer-box{display:flex;flex-direction:column;gap:4px}
    #timer{font-size:28px;font-weight:700;font-variant-numeric:tabular-nums;
      color:var(--accent);letter-spacing:-.01em}
    #slide-timer{font-size:12px;color:var(--muted)}
    #progress-box{display:flex;flex-direction:column;gap:3px}
    #progress-text{font-size:11px;color:var(--muted)}
    #progress-bar-wrap{height:4px;background:rgba(255,255,255,0.08);border-radius:2px}
    #progress-bar{height:100%;background:var(--accent);border-radius:2px;transition:width .3s}
    #audience-link{font-size:10px;color:var(--muted);word-break:break-all}
    #audience-link a{color:var(--accent)}
    /* Notes */
    #notes{grid-column:1/-1;grid-row:2;border-top:1px solid var(--border);
      background:rgba(240,190,75,0.06);padding:12px 16px;
      min-height:80px;max-height:180px;overflow-y:auto}
    #notes-label{font-size:10px;font-weight:600;letter-spacing:.08em;
      text-transform:uppercase;color:var(--warn);margin-bottom:4px}
    #notes-body{font-size:13px;color:var(--text);line-height:1.6;white-space:pre-wrap}
    /* Controls */
    #controls{display:flex;align-items:center;gap:6px;flex-wrap:wrap}
    .ctrl-btn{display:inline-flex;align-items:center;justify-content:center;
      background:rgba(255,255,255,0.06);border:1px solid var(--border);
      border-radius:6px;color:var(--text);font-size:11px;cursor:pointer;
      padding:4px 10px;transition:background .15s}
    .ctrl-btn:hover{background:rgba(255,255,255,0.12)}
    .ctrl-btn.active{background:var(--accent);border-color:var(--accent)}
    #keyboard-hint{font-size:9px;color:var(--muted);margin-top:2px}
  </style>
</head>
<body>
<div id="app">
  <div id="current-wrap">
    <div id="current-inner">
      <div class="reveal" id="reveal-deck">
        <div class="slides" id="reveal-slides"></div>
      </div>
    </div>
  </div>
  <div id="right">
    <div>
      <div id="next-label">Next slide</div>
      <div id="next-slide">
        <div id="next-slide-inner">
          <div id="next-title"></div>
          <div id="next-content"></div>
        </div>
      </div>
    </div>
    <div id="timer-box">
      <div id="timer">0:00</div>
      <div id="slide-timer">Slide: 0:00</div>
    </div>
    <div id="progress-box">
      <div id="progress-text">1 of 1</div>
      <div id="progress-bar-wrap">
        <div id="progress-bar" style="width:0%"></div>
      </div>
    </div>
    <div id="controls">
      <button class="ctrl-btn" id="btn-blank" title="b — blank screen">Blank</button>
      <button class="ctrl-btn" id="btn-fs" title="f — fullscreen">Fullscreen</button>
    </div>
    <div id="keyboard-hint">← → Space — advance &nbsp;|&nbsp; b blank &nbsp;|&nbsp; f fullscreen</div>
    <div id="audience-link"></div>
  </div>
  <div id="notes">
    <div id="notes-label">Speaker notes</div>
    <div id="notes-body"></div>
  </div>
</div>

<script>
(function() {
  var SLIDES = ${slidesJson};
  var idx = ${activeIdx};
  var blanked = false;
  var startTime = Date.now();
  var slideStartTime = Date.now();

  // BroadcastChannel for cross-window sync
  var bc = null;
  try { bc = new BroadcastChannel('vulos-presenter'); } catch(e) {}

  function postState() {
    var state = { type: 'slide-change', idx: idx };
    if (bc) bc.postMessage(state);
    try { localStorage.setItem('vulos-presenter-idx', String(idx)); } catch(e) {}
  }

  function render() {
    var slide = SLIDES[idx] || {};
    var next = SLIDES[idx + 1] || null;

    // notes
    document.getElementById('notes-body').textContent = slide.notes || '';

    // next slide
    document.getElementById('next-title').textContent = next ? next.title : '(end)';
    document.getElementById('next-content').textContent = next
      ? next.content.replace(/<[^>]+>/g, ' ').slice(0, 80)
      : '';

    // progress
    var total = SLIDES.length;
    document.getElementById('progress-text').textContent = (idx+1) + ' of ' + total;
    document.getElementById('progress-bar').style.width = (total > 1 ? (idx / (total-1)) * 100 : 100) + '%';

    // reveal advance
    if (window.revealDeck) {
      window.revealDeck.slide(idx, 0, 0);
    }

    postState();
  }

  // Timer
  function pad(n) { return n < 10 ? '0'+n : ''+n; }
  function formatTime(ms) {
    var s = Math.floor(ms/1000);
    var m = Math.floor(s/60);
    return m + ':' + pad(s % 60);
  }
  setInterval(function() {
    var now = Date.now();
    document.getElementById('timer').textContent = formatTime(now - startTime);
    document.getElementById('slide-timer').textContent = 'Slide: ' + formatTime(now - slideStartTime);
  }, 1000);

  function goNext() {
    if (idx < SLIDES.length - 1) { idx++; slideStartTime = Date.now(); render(); }
  }
  function goPrev() {
    if (idx > 0) { idx--; slideStartTime = Date.now(); render(); }
  }

  // Keyboard
  document.addEventListener('keydown', function(e) {
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown' || e.key === ' ') { e.preventDefault(); goNext(); }
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') { e.preventDefault(); goPrev(); }
    else if (e.key === 'b' || e.key === 'B') {
      blanked = !blanked;
      document.getElementById('current-wrap').classList.toggle('blanked', blanked);
      document.getElementById('btn-blank').classList.toggle('active', blanked);
    }
    else if (e.key === 'f' || e.key === 'F') {
      if (!document.fullscreenElement) document.documentElement.requestFullscreen().catch(function(){});
      else document.exitFullscreen().catch(function(){});
    }
  });

  document.getElementById('btn-blank').addEventListener('click', function() {
    blanked = !blanked;
    document.getElementById('current-wrap').classList.toggle('blanked', blanked);
    this.classList.toggle('active', blanked);
  });
  document.getElementById('btn-fs').addEventListener('click', function() {
    if (!document.fullscreenElement) document.documentElement.requestFullscreen().catch(function(){});
    else document.exitFullscreen().catch(function(){});
  });

  // BroadcastChannel — receive slide changes from editor
  if (bc) {
    bc.onmessage = function(ev) {
      if (ev.data && ev.data.type === 'slide-change' && typeof ev.data.idx === 'number') {
        if (ev.data.source === 'editor') {
          idx = ev.data.idx;
          slideStartTime = Date.now();
          render();
        }
      }
    };
  }

  // Audience view link
  var link = document.getElementById('audience-link');
  link.innerHTML = 'Audience: <a href="' + window.opener?.location?.href + '" target="_blank">' + (window.opener?.location?.href || location.href) + '</a>';

  // Boot reveal.js
  var script = document.createElement('script');
  script.src = 'https://cdnjs.cloudflare.com/ajax/libs/reveal.js/5.1.0/reveal.js';
  script.onload = function() {
    // Build slides HTML
    var slidesEl = document.getElementById('reveal-slides');
    SLIDES.forEach(function(slide, i) {
      var section = document.createElement('section');
      if (slide.background) section.setAttribute('data-background', slide.background);
      if (slide.title) {
        var h2 = document.createElement('h2');
        h2.style.fontSize = '1.4em';
        h2.textContent = slide.title;
        section.appendChild(h2);
      }
      var div = document.createElement('div');
      div.innerHTML = slide.content;
      section.appendChild(div);
      slidesEl.appendChild(section);
    });

    window.revealDeck = new Reveal(document.getElementById('reveal-deck'), {
      embedded: true,
      controls: false,
      progress: false,
      slideNumber: false,
      keyboard: false,
      hash: false,
      center: true,
      transition: 'slide',
    });
    window.revealDeck.initialize().then(function() {
      window.revealDeck.slide(idx, 0, 0);
    });
  };
  document.head.appendChild(script);

  // Initial render
  render();
})();
</script>
</body>
</html>`
}

/**
 * Hook: usePresenterView
 * Call openPresenter() to open the presenter window.
 * Returns { openPresenter, syncSlide }.
 */
export function usePresenterView(slidesData) {
  const presenterWindowRef = useRef(null)
  const channelRef = useRef(null)

  useEffect(() => {
    try {
      channelRef.current = new BroadcastChannel('vulos-presenter')
    } catch {
      channelRef.current = null
    }
    return () => {
      try { channelRef.current?.close() } catch { /* ignore */ }
    }
  }, [])

  const syncSlide = useCallback((idx) => {
    try {
      channelRef.current?.postMessage({ type: 'slide-change', idx, source: 'editor' })
      localStorage.setItem('vulos-presenter-idx', String(idx))
    } catch { /* ignore */ }
  }, [])

  const openPresenter = useCallback((activeIdx) => {
    const slides = slidesData?.slides || []
    const themeId = slidesData?.customTheme?.revealTheme || slidesData?.themeId || 'black'
    const html = buildPresenterHTML(slides, activeIdx || 0, themeId)
    const blob = new Blob([html], { type: 'text/html' })
    const url = URL.createObjectURL(blob)

    if (presenterWindowRef.current && !presenterWindowRef.current.closed) {
      presenterWindowRef.current.focus()
      URL.revokeObjectURL(url)
      return
    }

    const w = window.open(url, 'vulos-presenter',
      'width=1280,height=800,menubar=no,toolbar=no,location=no')
    presenterWindowRef.current = w

    // Revoke blob URL after a short delay (window has loaded it by then).
    setTimeout(() => URL.revokeObjectURL(url), 10000)
  }, [slidesData])

  return { openPresenter, syncSlide }
}
