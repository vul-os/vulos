// WidgetTile.tsx — one placed widget, and the blast radius around it.
//
// The error boundary is the reason this file exists as its own component. The
// rail is mounted on the desktop under every window, so a widget that throws
// during render throws inside the shell's tree — and React unmounts the whole
// subtree from the nearest boundary. Without one HERE, a single bad widget takes
// out the rail, and if the boundary were any higher it would take out the
// desktop. A boundary per tile means a broken widget is a broken TILE, showing
// its own name and what went wrong, with every other widget still ticking.
//
// This is not hypothetical for widgets specifically: they render on every clock
// tick, against data that can be absent, from code the box did not necessarily
// write.

import { Component, useMemo, type ErrorInfo, type ReactNode } from 'react'
import SandboxFrame from './SandboxFrame'
import { isSandboxed, type AnyWidgetDefinition, type WidgetContext, type WidgetInstance } from '../types'
import type { BridgeHost } from './bridge'
import './widgets.css'

class TileBoundary extends Component<
  { name: string; children: ReactNode },
  { failed: boolean; message: string }
> {
  constructor(props: { name: string; children: ReactNode }) {
    super(props)
    this.state = { failed: false, message: '' }
  }

  static getDerivedStateFromError(err: unknown) {
    return { failed: true, message: err instanceof Error ? err.message : String(err) }
  }

  componentDidCatch(err: unknown, info: ErrorInfo) {
    // Logged, not swallowed. A widget that fails silently is a widget nobody
    // fixes; the tile below is what the USER sees, and this is what a developer
    // sees.
    console.error(`[widget] ${this.props.name} failed to render`, err, info)
  }

  render() {
    if (!this.state.failed) return this.props.children
    return (
      <section className="vwidget-card" aria-label={`${this.props.name} (failed)`}>
        <div className="vwidget-body">
          <div className="vwidget-title-row">
            <span className="vwidget-title">{this.props.name}</span>
          </div>
          <div className="vwidget-empty">
            <span className="vwidget-error-dot" aria-hidden="true" />
            <span className="vwidget-empty-text">This widget stopped working.</span>
            <span className="vwidget-empty-text mono">{this.state.message.slice(0, 80)}</span>
          </div>
        </div>
      </section>
    )
  }
}

export default function WidgetTile({
  def, instance, ctx, bridgeHost,
}: {
  def: AnyWidgetDefinition
  instance: WidgetInstance
  ctx: WidgetContext
  bridgeHost: BridgeHost
}) {
  const name = def.manifest.name
  const body = useMemo(() => {
    if (isSandboxed(def)) {
      return (
        <SandboxFrame
          instanceId={instance.instanceId}
          source={def.source}
          host={bridgeHost}
          ctx={ctx}
          title={name}
        />
      )
    }
    return def.render(ctx)
  }, [def, instance.instanceId, ctx, bridgeHost, name])

  return <TileBoundary name={name}>{body}</TileBoundary>
}
