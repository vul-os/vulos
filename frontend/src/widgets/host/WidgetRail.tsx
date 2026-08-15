// WidgetRail.tsx — the right-hand widget rail.
//
// Owns exactly four things and delegates everything else:
//
//   1. LAYOUT      which widgets, in what order, at what size (layout.ts)
//   2. TIME        one scheduler for every widget in the rail (useTicker.ts)
//   3. CAPABILITY  one telemetry socket and one agenda read, shared (capabilities.tsx)
//   4. EDITING     add / remove / reorder / resize / configure / grant
//
// It does NOT know what any widget does. Every tile is `def.render(ctx)` or an
// iframe, and the rail's only knowledge of a widget is its manifest — which is
// what makes a third-party widget indistinguishable from a builtin here, and is
// the actual test of whether the API is real.

import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { useShell } from '../../providers/ShellProvider'
import { getAppById } from '../../core/AppRegistry'
import { launchApp } from '../../shell/launchApp'
import { notify as shellNotify } from '../../core/notificationStore'
import { getWidget, listWidgets } from '../registry'
import { storageFor } from '../storage'
import { probeProxy, widgetNet } from '../net'
import {
  addWidget, loadLayout, moveWidget, removeWidget, resizeWidget,
  saveLayout, setGrants, setInstanceSetting,
} from '../layout'
import { finestTick, nowFor, useTicker } from './useTicker'
import { CalendarSource, NotificationSource, TelemetrySource } from './capabilities'
import WidgetTile from './WidgetTile'
import WidgetGallery from './WidgetGallery'
import WidgetConfig from './WidgetConfig'
import type { BridgeHost } from './bridge'
import {
  SIZE_SPAN,
  type AnyWidgetDefinition, type WidgetCalendar, type WidgetContext,
  type WidgetInstance, type WidgetLayout, type WidgetNotifications,
  type WidgetSettingValue, type WidgetSize, type WidgetTelemetry,
} from '../types'
import './widgets.css'

type Action =
  | { t: 'set'; layout: WidgetLayout }
  | { t: 'add'; widgetId: string }
  | { t: 'remove'; instanceId: string }
  | { t: 'move'; instanceId: string; delta: number }
  | { t: 'resize'; instanceId: string; size: WidgetSize }
  | { t: 'setting'; instanceId: string; key: string; value: WidgetSettingValue }
  | { t: 'grants'; instanceId: string; granted: WidgetInstance['granted'] }

function reduce(state: WidgetLayout, a: Action): WidgetLayout {
  switch (a.t) {
    case 'set': return a.layout
    // A widget added from the gallery starts with EVERY permission denied. The
    // user grants them afterwards, from the tile's own settings panel, having
    // seen what each one means. There is no "install with permissions" flow,
    // because a permission bundled into an install button is a permission nobody
    // read.
    case 'add': return addWidget(state, a.widgetId, { granted: [] })
    case 'remove': return removeWidget(state, a.instanceId)
    case 'move': return moveWidget(state, a.instanceId, a.delta)
    case 'resize': return resizeWidget(state, a.instanceId, a.size)
    case 'setting': return setInstanceSetting(state, a.instanceId, a.key, a.value)
    case 'grants': return setGrants(state, a.instanceId, a.granted)
  }
}

export default function WidgetRail() {
  const [layout, dispatch] = useReducer(reduce, null, loadLayout)
  const [editing, setEditing] = useState(false)
  const [gallery, setGallery] = useState(false)
  const [configuring, setConfiguring] = useState<string | null>(null)
  const [telemetry, setTelemetry] = useState<WidgetTelemetry>({ connected: false })
  const [calendar, setCalendar] = useState<WidgetCalendar>({ events: null, error: false })
  const [notifications, setNotifications] = useState<WidgetNotifications>({ recent: [], unread: 0 })
  const { openWindow } = useShell()

  useEffect(() => { saveLayout(layout) }, [layout])

  // Ask the box ONCE whether it brokers widget requests. Until it answers (and
  // it answers "no" on every box shipping today), ctx.net is null everywhere and
  // no widget can originate an outbound call. See net.ts.
  const [proxyReady, setProxyReady] = useState(0)
  useEffect(() => { probeProxy().then(() => setProxyReady((n) => n + 1)) }, [])

  const reducedMotion = useReducedMotion()

  // Resolve each placement against the registry once per layout change.
  const mounted = useMemo(() => {
    const out: { instance: WidgetInstance; def: AnyWidgetDefinition }[] = []
    for (const instance of layout.instances) {
      const def = getWidget(instance.widgetId)
      if (def) out.push({ instance, def })
    }
    return out
  }, [layout])

  const needTelemetry = mounted.some((m) => m.instance.granted.includes('telemetry'))
  const needCalendar = mounted.some((m) => m.instance.granted.includes('calendar'))
  const needNotifications = mounted.some((m) => m.instance.granted.includes('notifications'))

  const ticker = useTicker(finestTick(mounted.map((m) => m.def.manifest.tick)))

  const openApp = useCallback((appId: string) => {
    const app = getAppById(appId)
    if (app) launchApp(app, { openWindow })
  }, [openWindow])

  const setSetting = useCallback((instanceId: string, key: string, value: WidgetSettingValue) => {
    dispatch({ t: 'setting', instanceId, key, value })
  }, [])

  const railEmpty = mounted.length === 0

  // Whether the scrollport actually overflows. Measured from the DOM rather than
  // guessed from the widget count, because the same five widgets overflow a
  // 1280x800 desktop and do not overflow a 1680x1050 one — and a fade drawn over
  // a rail that does not scroll is just a tile with its bottom faded off.
  const portRef = useRef<HTMLDivElement | null>(null)
  // Opening a panel scrolls the port back to the top. Without this, a user who
  // scrolled down to a tile's gear would open a panel that is off-screen ABOVE
  // them — the same defect as before, mirrored.
  useEffect(() => {
    if (gallery || configuring) portRef.current?.scrollTo({ top: 0, behavior: 'smooth' })
  }, [gallery, configuring])
  const [hasMore, setHasMore] = useState(false)
  useEffect(() => {
    const el = portRef.current
    if (!el) return
    const measure = () => setHasMore(el.scrollHeight - el.scrollTop > el.clientHeight + 2)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    el.addEventListener('scroll', measure, { passive: true })
    window.addEventListener('resize', measure)
    return () => { ro.disconnect(); el.removeEventListener('scroll', measure); window.removeEventListener('resize', measure) }
  }, [mounted.length, gallery, configuring])

  return (
    <div className="flex flex-col gap-2.5">
      {needTelemetry && <TelemetrySource onChange={setTelemetry} />}
      {needCalendar && <CalendarSource onChange={setCalendar} />}
      {needNotifications && <NotificationSource onChange={setNotifications} />}

      <div className="vwidget-railbar">
        <button
          type="button"
          className="vwidget-railbtn focus-primary"
          data-active={editing ? 'true' : undefined}
          onClick={() => { setEditing((v) => !v); setGallery(false); setConfiguring(null) }}
          aria-pressed={editing}
        >
          {editing ? 'Done' : 'Edit widgets'}
        </button>
        {editing && (
          <button
            type="button"
            className="vwidget-railbtn focus-primary"
            data-active={gallery ? 'true' : undefined}
            onClick={() => { setGallery((v) => !v); setConfiguring(null) }}
            aria-expanded={gallery}
          >
            Add widget
          </button>
        )}
      </div>

      {/* Everything below the bar scrolls together, so a rail taller than the
          screen stays reachable instead of running off the bottom of it. */}
      <div ref={portRef} className="vwidget-scrollport flex flex-col gap-2.5" data-more={hasMore ? 'true' : undefined}>
      {/* Panels sit at the TOP of the scrollport, not beside or beneath the tile
          they belong to. Rendered after the tiles, the settings panel for the
          second widget in a five-widget rail landed below the fold: the gear lit
          up and nothing appeared to happen. A panel must open where the user is
          looking. */}
      {configuring && (() => {
        const found = mounted.find((m) => m.instance.instanceId === configuring)
        if (!found) return null
        return (
          <WidgetConfig
            manifest={found.def.manifest}
            instance={found.instance}
            onSetting={(k, v) => setSetting(configuring, k, v)}
            onGrants={(g) => dispatch({ t: 'grants', instanceId: configuring, granted: g })}
            onClose={() => setConfiguring(null)}
            proxyProbed={proxyReady > 0}
          />
        )
      })()}

      {gallery && (
        <WidgetGallery
          widgets={listWidgets()}
          onAdd={(id) => { dispatch({ t: 'add', widgetId: id }); setGallery(false) }}
          onClose={() => setGallery(false)}
        />
      )}

      {railEmpty && !gallery && (
        <div className="vwidget-card" style={{ height: 'auto' }}>
          <div className="vwidget-body">
            <div className="vwidget-title-row"><span className="vwidget-title">Widgets</span></div>
            <div className="vwidget-empty">
              <span className="vwidget-empty-text">
                No widgets. Choose “Edit widgets”, then “Add widget”.
              </span>
            </div>
          </div>
        </div>
      )}

      <div className="vwidget-rail" data-widget-rail>
        {mounted.map(({ instance, def }, i) => {
          const m = def.manifest
          const granted = instance.granted
          const net = widgetNet(m.hosts ?? [], { granted: granted.includes('network') })
          const ctx: WidgetContext = {
            size: instance.size,
            now: nowFor(ticker, m.tick),
            settings: instance.settings,
            setSetting: (k, v) => setSetting(instance.instanceId, k, v),
            reducedMotion,
            storage: granted.includes('storage') ? storageFor(instance.instanceId) : null,
            net,
            telemetry: granted.includes('telemetry') ? telemetry : null,
            calendar: granted.includes('calendar') ? calendar : null,
            notifications: granted.includes('notifications') ? notifications : null,
            notify: granted.includes('notify')
              ? (title: string, body?: string) => { shellNotify({ title, body, source: m.name, app: m.id, level: 'info' }) }
              : null,
            openApp: granted.includes('launch') ? openApp : null,
          }
          const bridgeHost: BridgeHost = {
            instanceId: instance.instanceId,
            granted,
            hosts: m.hosts ?? [],
            net,
            notify: ctx.notify,
            openApp: ctx.openApp,
            setSetting: (k, v) => setSetting(instance.instanceId, k, v),
          }
          return (
            <div
              key={instance.instanceId}
              className="vwidget-slot"
              data-size={instance.size}
              data-widget-id={m.id}
            >
              <WidgetTile def={def} instance={instance} ctx={ctx} bridgeHost={bridgeHost} />
              {editing && (
                // ONE row along the bottom. Split across the tile's corners the
                // remove chip landed on the world clock's home-zone label and on
                // the agenda's live dot — an edit affordance must not cover the
                // thing you are deciding whether to keep.
                <div className="vwidget-edit-overlay">
                  <div className="vwidget-edit-strip">
                    <button
                      type="button" className="vwidget-chip focus-primary" disabled={i === 0}
                      aria-label={`Move ${m.name} earlier`}
                      onClick={() => dispatch({ t: 'move', instanceId: instance.instanceId, delta: -1 })}
                    >↑</button>
                    <button
                      type="button" className="vwidget-chip focus-primary" disabled={i === mounted.length - 1}
                      aria-label={`Move ${m.name} later`}
                      onClick={() => dispatch({ t: 'move', instanceId: instance.instanceId, delta: 1 })}
                    >↓</button>
                    {m.sizes.length > 1 && (
                      <button
                        type="button" className="vwidget-chip focus-primary"
                        aria-label={`Resize ${m.name}`}
                        onClick={() => dispatch({
                          t: 'resize',
                          instanceId: instance.instanceId,
                          size: nextSize(m.sizes, instance.size),
                        })}
                      >⤢</button>
                    )}
                    <button
                      type="button" className="vwidget-chip focus-primary"
                      data-active={configuring === instance.instanceId ? 'true' : undefined}
                      aria-label={`Configure ${m.name}`}
                      aria-expanded={configuring === instance.instanceId}
                      onClick={() => setConfiguring((c) => (c === instance.instanceId ? null : instance.instanceId))}
                    >⚙</button>
                    <span className="vwidget-edit-spacer" />
                    <button
                      type="button" className="vwidget-chip focus-primary" data-danger="true"
                      aria-label={`Remove ${m.name}`}
                      onClick={() => { dispatch({ t: 'remove', instanceId: instance.instanceId }); setConfiguring(null) }}
                    >×</button>
                  </div>
                </div>
              )}
            </div>
          )
        })}
      </div>

      </div>
    </div>
  )
}

/** Cycle through the sizes the manifest offers, in the order it offers them. */
function nextSize(sizes: WidgetSize[], current: WidgetSize): WidgetSize {
  const ordered = [...sizes].sort((a, b) => SIZE_SPAN[a].cols * SIZE_SPAN[a].rows - SIZE_SPAN[b].cols * SIZE_SPAN[b].rows)
  const i = ordered.indexOf(current)
  return ordered[(i + 1) % ordered.length]
}

/**
 * The viewer's motion preference, live.
 *
 * Read through matchMedia and SUBSCRIBED to, not sampled once: the setting can
 * change while the desktop is up (a user toggling it in Settings, or a system
 * theme change on some platforms), and a widget that animated because the rail
 * sampled the value at boot would be ignoring a preference the user has since
 * expressed.
 */
function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => {
    try { return window.matchMedia('(prefers-reduced-motion: reduce)').matches } catch { return false }
  })
  useEffect(() => {
    let mq: MediaQueryList
    try { mq = window.matchMedia('(prefers-reduced-motion: reduce)') } catch { return }
    const on = () => setReduced(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return reduced
}
