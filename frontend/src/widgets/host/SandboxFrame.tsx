// SandboxFrame.tsx — runs an untrusted widget inside an opaque-origin iframe.
//
// `sandbox="allow-scripts"` and nothing else. Not allow-same-origin, ever — that
// single omission is the entire containment story, and it is the same invariant
// core/AppOrigins.ts exists to protect for app frames. Without it the frame is an
// OPAQUE origin: it cannot read the shell's DOM, cookies, localStorage,
// IndexedDB, service worker or session, it cannot navigate the top window, and
// `fetch` from inside it is same-origin-less and CSP-bound. Everything it can
// do, it does by asking over the MessagePort in bridge.ts.
//
// The document is delivered by `srcdoc`, so nothing is fetched: the widget's code
// is data the box already holds, it cannot change under the user between renders,
// and loading it costs no request.

import { useEffect, useMemo, useRef } from 'react'
import { BRIDGE_VERSION, handleWidgetMessage, type BridgeHost } from './bridge'
import { buildSandboxDocument, resolveTokens } from './sandboxDoc'
import type { WidgetContext } from '../types'
import './widgets.css'

export default function SandboxFrame({
  instanceId, source, host, ctx, title,
}: {
  instanceId: string
  source: string
  host: BridgeHost
  ctx: WidgetContext
  title: string
}) {
  const ref = useRef<HTMLIFrameElement | null>(null)
  const portRef = useRef<MessagePort | null>(null)
  // `host` and `ctx` change identity on every render (they close over callbacks
  // and a ticking clock). The message listener must see the LATEST of each
  // without being torn down and rebuilt — a rebuild drops the port and restarts
  // the widget. The refs are therefore written in an EFFECT, never during render:
  // a render-phase mutation is one React may discard.
  const hostRef = useRef(host)
  const ctxRef = useRef(ctx)
  useEffect(() => { hostRef.current = host })
  useEffect(() => { ctxRef.current = ctx })

  // The document is rebuilt only when the source or the theme changes — never on
  // a tick. Rebuilding srcdoc RELOADS the frame, so a per-tick rebuild would
  // restart the widget once a minute and it would never finish painting.
  const theme = typeof document !== 'undefined'
    ? (document.documentElement.getAttribute('data-theme') ?? 'default')
    : 'default'
  const doc = useMemo(
    () => buildSandboxDocument(source, window.location.origin, resolveTokens(theme)),
    [source, theme],
  )

  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      const frame = ref.current
      // CHECK 1 — frame identity. Object comparison against the window handle we
      // hold. Unforgeable, and the only meaningful check when the frame is
      // opaque (every opaque frame's origin is the string 'null').
      if (!frame || !frame.contentWindow || e.source !== frame.contentWindow) return
      // CHECK 2 — origin. An opaque frame reports 'null'. Anything else is not
      // our sandboxed widget.
      if (e.origin !== 'null') return
      const msg: unknown = e.data
      if (typeof msg !== 'object' || msg === null) return
      const m = msg as Record<string, unknown>
      if (m.type !== 'vulos.widget.hello' || m.v !== BRIDGE_VERSION) return
      const port = e.ports && e.ports[0]
      if (!port) return
      // One channel per frame. A second hello (the frame reloaded) replaces the
      // old port and the old one is dropped.
      if (portRef.current) { try { portRef.current.close() } catch { /* gone */ } }
      portRef.current = port
      port.onmessage = (ev: MessageEvent) => {
        handleWidgetMessage(hostRef.current, ev.data, (obj) => {
          // Replies go on the PORT, which is point-to-point and unforgeable.
          // postMessage(…, '*') appears nowhere in this half of the protocol.
          try { port.postMessage(obj) } catch { /* frame went away */ }
        })
      }
      pushContext(port, ctxRef.current)
    }
    window.addEventListener('message', onMessage)
    return () => {
      window.removeEventListener('message', onMessage)
      if (portRef.current) { try { portRef.current.close() } catch { /* gone */ } }
      portRef.current = null
    }
  }, [instanceId])

  // Push the context on every change. Only serialisable values cross: no
  // function, no DOM node, no Date — `now` goes as an ISO string, which is also
  // what stops a structured-clone failure from silently killing the channel.
  useEffect(() => {
    if (portRef.current) pushContext(portRef.current, ctx)
  }, [ctx])

  return (
    <iframe
      ref={ref}
      title={title}
      className="vwidget-frame"
      // THE security boundary. Do not add allow-same-origin. See the file header.
      sandbox="allow-scripts"
      srcDoc={doc}
      loading="lazy"
    />
  )
}

function pushContext(port: MessagePort, ctx: WidgetContext): void {
  try {
    port.postMessage({
      v: BRIDGE_VERSION,
      type: 'widget.context',
      size: ctx.size,
      now: ctx.now.toISOString(),
      settings: ctx.settings,
      reducedMotion: ctx.reducedMotion,
      telemetry: ctx.telemetry,
      notifications: ctx.notifications,
      calendar: ctx.calendar
        ? {
            error: ctx.calendar.error,
            events: ctx.calendar.events?.map((e) => ({
              id: e.id, title: e.title, allDay: e.allDay, location: e.location,
              start: e.start ? e.start.toISOString() : null,
              end: e.end ? e.end.toISOString() : null,
            })) ?? null,
          }
        : null,
    })
  } catch { /* frame went away */ }
}
