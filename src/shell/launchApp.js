// launchApp.js — the single, shared app-launch path.
//
// Extracted from Launchpad so the Launchpad, the ⌘K command palette, and any
// other surface open registered apps through EXACTLY the same lane logic
// (builtin React component · WebApp iframe · ComputeWorker · LocalOnly · native
// · CPU/GPU stream · legacy fallback). Keeping one implementation avoids the two
// launchers drifting apart.
//
// It is deliberately not a hook: it reads the native-mode flag via the
// non-hook getNativeMode() getter, so it can be called from event handlers,
// command `run(ctx)` callbacks, etc.
import { createElement, lazy, Suspense } from 'react'
import { builtinComponent, isBuiltinComponent, BUILTIN_SINGLETONS } from './builtinApps'
import { getNativeMode } from '../core/useNativeMode'

const StreamViewer = lazy(() => import('../builtin/stream/StreamViewer'))

// ROUTER-02: Consult /api/router/classify before launching an app.
// Returns the lane string or null on fetch failure (caller falls back to
// legacy dispatch).
async function fetchLane(appId) {
  try {
    const res = await fetch(`/api/router/classify?app=${encodeURIComponent(appId)}`)
    if (!res.ok) return null
    const data = await res.json()
    return data.lane || null
  } catch {
    return null
  }
}

// openInHostBrowser opens an in-shell web-view window for a URL.
// BROWSER-01: this path spawns ZERO stream.Session objects.
function openInHostBrowser(url, title, icon, openWindow) {
  openWindow({ appId: '_webview_' + Date.now(), title: title || url, url, icon })
}

/**
 * Launch a registered app. Mirrors the lane dispatch that used to live in
 * Launchpad.launch.
 *
 * @param {object} app       the registry app entry (from AppRegistry.getApps())
 * @param {object} deps
 * @param {(w:object)=>void} deps.openWindow  the shell's openWindow
 */
export async function launchApp(app, { openWindow }) {
  if (!app) return

  if (isBuiltinComponent(app.id)) {
    openWindow({ appId: app.id, title: app.name, icon: app.icon, component: builtinComponent(app.id), singleton: BUILTIN_SINGLETONS.has(app.id) })
    return
  }

  // ── Streaming Chrome lane — REAL Chromium on the box, streamed over WebRTC ─
  // Distinct from the iframe "Smart Browser" (type:'web', handled below). This
  // launches a per-user stream.Session via POST /api/browser/launch (the box
  // derives a persistent per-user Chrome profile) and connects the StreamViewer
  // to the returned session ID.
  if (app.stream_browser || app.id === 'browser-stream') {
    let sessionId = 'browser'
    try {
      const res = await fetch('/api/browser/launch', { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        if (data && data.id) sessionId = data.id
      }
    } catch { /* non-fatal — viewer connects when the session becomes ready */ }
    const browserLoading = createElement('div', { className: 'flex items-center justify-center h-full bg-neutral-950 text-neutral-500 text-sm' },
      createElement('span', { className: 'flex items-center gap-2' },
        createElement('span', { className: 'w-4 h-4 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin' }),
        'Starting Chrome...'
      )
    )
    openWindow({
      appId: app.id,
      title: app.name,
      icon: app.icon,
      singleton: true,
      component: createElement(Suspense, { fallback: browserLoading }, createElement(StreamViewer, { sessionId })),
    })
    return
  }

  // ROUTER-02: Consult Open Router to get the execution lane.
  const lane = await fetchLane(app.id)

  // ── WebApp lane — open in host browser, ZERO stream.Session ───────────────
  if (lane === 'WebApp' || app.type === 'web' || app.lane_web) {
    if (app.url) {
      openWindow({ appId: app.id, title: app.name, url: app.url, icon: app.icon })
    } else {
      try {
        await fetch('/api/apps/launch', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ app_id: app.id, app_port: app.port || 80, command: app.command || '', work_dir: app.workDir || '' }),
        })
      } catch { /* non-fatal — window still opens */ }
      const proto = location.protocol
      const webAppUrl = `${proto}//${app.id}--default.${location.host}/`
      openInHostBrowser(webAppUrl, app.name, app.icon, openWindow)
    }
    return
  }

  // ── ComputeWorker lane — background job, no window ─────────────────────────
  if (lane === 'ComputeWorker') {
    fetch('/api/apps/launch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ app_id: app.id, app_port: app.port || 80, command: app.command || '' }),
    }).catch(() => {})
    return
  }

  // ── LocalOnly lane — local dispatch only ──────────────────────────────────
  if (lane === 'LocalOnly') {
    fetch('/api/shell/native-launch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ app_id: app.id }),
    }).catch(() => {})
    return
  }

  // v2 native-launch branch (D93): only when VULOS_NATIVE_MODE_V2 is set
  // server-side and a multi-window compositor (labwc/Sway) is running.
  const isNativeV2 = getNativeMode() === 'native'
  const isDesktopApp = app._desktop || app.type === 'desktop'
  if (isNativeV2 && isDesktopApp) {
    const binary = app._exec ? app._exec.split(' ')[0] : app.command?.split(' ')[0] || ''
    const args = app._exec ? app._exec.split(' ').slice(1) : app.command?.split(' ').slice(1) || []
    fetch('/api/shell/native-launch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ app_id: app.id, binary, args }),
    }).catch(() => {})
    return
  }

  // ── CPUStream / GPURoute lanes — streamed window ──────────────────────────
  const isStreamApp = app._desktop || app.type === 'desktop' || lane === 'CPUStream' || lane === 'GPURoute'
  if (isStreamApp) {
    const cmd = app._exec ? app._exec.split(' ')[0] : app.command?.split(' ')[0] || app.id
    const args = app._exec ? app._exec.split(' ').slice(1) : app.command?.split(' ').slice(1) || []
    const sessionId = app.id
    const streamW = window.innerWidth
    const streamH = window.innerHeight - 32 - 64 - 36
    // FPS: no longer hardcoded to 30 — honour an app/session preference and let
    // the pool clamp to its allowed set {30,60,90,120,144} (default 60). The
    // in-viewer toolbar can change FPS live after connect.
    const fps = app.fps || app._fps || 60

    // GAME-07: gaming mode must engage ONLY for real games. The GPURoute lane
    // covers BOTH games (steam/lutris/wine) AND GPU-accelerated non-games
    // (blender/kdenlive/darktable/obs/shotcut), so we do NOT blanket-force
    // gaming. Instead, GPURoute launches go through the auto-detecting
    // POST /api/stream/launch-app, which sets Gaming=true only when the command
    // is a gaming launcher or the manifest declares category=="gaming". We read
    // the resolved `gaming` back from the response and pass it to the viewer so
    // gaming input behaviour (pointer-lock, split unreliable channels) activates
    // for real games only. Plain CPUStream desktop apps keep the standard path.
    let gaming = false
    if (lane === 'GPURoute') {
      try {
        const res = await fetch('/api/stream/launch-app', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ app_id: sessionId, name: app.name, command: cmd, args, width: streamW, height: streamH, fps }),
        })
        if (res.ok) {
          const data = await res.json().catch(() => null)
          if (data && data.gaming) gaming = true
        }
      } catch { /* non-fatal — viewer connects when the session becomes ready */ }
    } else {
      fetch('/api/stream/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: sessionId, name: app.name, command: cmd, args, width: streamW, height: streamH, fps }),
      }).catch(() => {})
    }
    const streamLoading = createElement('div', { className: 'flex items-center justify-center h-full bg-neutral-950 text-neutral-500 text-sm' },
      createElement('span', { className: 'flex items-center gap-2' },
        createElement('span', { className: 'w-4 h-4 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin' }),
        'Starting...'
      )
    )
    openWindow({
      appId: app.id,
      title: app.name,
      icon: app.icon,
      singleton: true,
      component: createElement(Suspense, { fallback: streamLoading }, createElement(StreamViewer, { sessionId, gaming })),
    })
    return
  }

  // ── Legacy fallback — launch backend process, open subdomain web view ─────
  try {
    await fetch('/api/apps/launch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ app_id: app.id, app_port: app.port || 80, command: app.command || '', work_dir: app.workDir || '' }),
    })
    const proto = location.protocol
    const net04AppUrl = `${proto}//${app.id}--default.${location.host}/`
    openWindow({ appId: app.id, title: app.name, url: net04AppUrl, icon: app.icon })
  } catch {
    openWindow({ appId: app.id, title: app.name, url: `/app/${app.id}/`, icon: app.icon })
  }
}
