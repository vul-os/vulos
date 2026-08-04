// notificationBridge.js — folds the BACKEND notification service into the
// client notificationStore, so the shell has one unified feed.
//
// The Go backend (backend/services/notify) exposes:
//   GET  /api/notifications           → last N notifications (history)
//   GET  /api/notifications/unread    → { unread: N }
//   WS   /api/notifications/stream    → live notifications as they fire
//   POST /api/notifications/read      → { id } (or empty body = mark all read)
//   POST /api/notifications/clear     → clear all
//   POST /api/notifications/dnd       → { mode: 'off'|'total' } (Do Not Disturb)
//
// This module hydrates history once, subscribes to the live stream, and proxies
// remote-item mutations (mark-read / clear) back to the server via setRemoteSink.
// Everything degrades gracefully: if the endpoints 404 or the box is offline the
// store simply stays client-only. NO NEW EGRESS — same-origin box API only.

import { ingest, setRemoteSink } from './notificationStore'

let started = false

// Map a backend notification (notify.Notification shape) to a store notif.
// Backend levels are 'info' | 'warning' | 'urgent'; priority 'critical' maps to
// our 'critical' level for the can't-miss overlay.
function fromBackend(n) {
  let level = ['info', 'warning', 'urgent'].includes(n.level) ? n.level : 'info'
  if (n.priority === 'critical') level = 'critical'
  const actions = []
  if (n.action) {
    // Legacy single-action: a URL or an in-shell route hint.
    actions.push({ id: 'open', label: 'Open', url: typeof n.action === 'string' ? n.action : undefined })
  }
  return {
    id: n.id,
    title: n.title,
    body: n.body,
    source: n.source || 'system',
    level,
    actions,
    ttl: typeof n.ttl === 'number' ? n.ttl : undefined,
    read: !!n.read,
    ts: n.created_at ? new Date(n.created_at).getTime() : Date.now(),
    // 'sync' notifications carry a conflict deep-link the toaster handles.
    action: n.action,
  }
}

async function hydrateHistory() {
  try {
    const res = await fetch('/api/notifications', { credentials: 'include' })
    if (!res.ok) return
    const list = await res.json()
    if (!Array.isArray(list)) return
    // Ingest oldest→newest so timestamps sort correctly; skip xdg-open (handled
    // by the desktop browser-focus path, not a user-facing notification).
    for (const n of list.slice().reverse()) {
      if (n.source === 'xdg-open') continue
      ingest(fromBackend(n))
    }
  } catch { /* offline / no backend → client-only feed */ }
}

function connectStream() {
  const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/notifications/stream`
  let alive = true
  let ws = null
  function open() {
    if (!alive) return
    try { ws = new WebSocket(url) } catch { return }
    ws.onmessage = (e) => {
      try {
        const n = JSON.parse(e.data)
        if (n.source === 'xdg-open') return // desktop browser focus, not a toast
        ingest(fromBackend(n))
      } catch { /* noop */ }
    }
    ws.onclose = () => { if (alive) setTimeout(open, 3000) }
    ws.onerror = () => { try { ws.close() } catch { /* noop */ } }
  }
  open()
  return () => { alive = false; try { ws?.close() } catch { /* noop */ } }
}

// Proxy remote-item mutations to the backend. Best-effort — a failed sync only
// means the server keeps its own copy; the local view is already updated.
const backendSink = {
  markRead(id) {
    fetch('/api/notifications/read', {
      method: 'POST', credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id }),
    }).catch(() => {})
  },
  markAllRead() {
    fetch('/api/notifications/read', {
      method: 'POST', credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    }).catch(() => {})
  },
  dismiss() { /* backend has no per-id delete; leave server copy intact */ },
  clear() {
    fetch('/api/notifications/clear', { method: 'POST', credentials: 'include' }).catch(() => {})
  },
}

/**
 * Start the bridge exactly once. Returns a stop() function. Safe to call from a
 * React effect; repeated calls are no-ops.
 */
export function startNotificationBridge() {
  if (started) return () => {}
  started = true
  setRemoteSink(backendSink)
  hydrateHistory()
  const stopStream = connectStream()
  return () => { started = false; stopStream(); setRemoteSink(null) }
}
