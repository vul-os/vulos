// pulse.tsx — the box's own vital signs.
//
// The most sovereignty-native widget in the set: every number here is measured
// by the machine the user owns, delivered over a socket to that machine, and
// leaves nothing. It also has the most honest failure mode to get right — a box
// whose telemetry socket is down must say "not reporting", not show 0%.
import {
  defineWidget, registerWidget,
  WidgetFrame, WidgetTitle, WidgetLabel, WidgetEmpty, WidgetBigValue,
  type WidgetContext,
} from '../index'

function pct(n: number | undefined): string {
  return typeof n === 'number' ? `${Math.round(n)}%` : '—'
}

/** Traffic-light tone for a utilisation figure. */
function tone(n: number | undefined): 'accent' | 'warning' | 'danger' {
  if (typeof n !== 'number') return 'accent'
  if (n >= 90) return 'danger'
  if (n >= 75) return 'warning'
  return 'accent'
}

function Meter({ value }: { value: number | undefined }) {
  const t = tone(value)
  return (
    <div className="vwidget-meter">
      <div
        className="vwidget-meter-fill"
        data-tone={t === 'accent' ? undefined : t}
        style={{ width: `${Math.max(0, Math.min(100, value ?? 0))}%` }}
      />
    </div>
  )
}

export default function PulseWidget(ctx: WidgetContext) {
  const t = ctx.telemetry

  if (!t) {
    return (
      <WidgetFrame title="Box health">
        <WidgetTitle>Box health</WidgetTitle>
        <WidgetEmpty>Allow “Read box health” in this widget’s settings.</WidgetEmpty>
      </WidgetFrame>
    )
  }

  // Not connected is DIFFERENT from connected-with-no-numbers, and both are
  // different from 0%. A gauge sitting at zero because nothing answered is the
  // single most misleading thing this widget could draw.
  if (!t.connected) {
    return (
      <WidgetFrame title="Box health">
        <WidgetTitle>Box health</WidgetTitle>
        <WidgetEmpty>This box is not reporting telemetry right now.</WidgetEmpty>
      </WidgetFrame>
    )
  }

  const haveAny = t.cpu !== undefined || t.memPercent !== undefined || t.battery !== undefined
  if (!haveAny) {
    return (
      <WidgetFrame title="Box health">
        <WidgetTitle right={t.hostname ? <span className="mono">{t.hostname}</span> : undefined}>Box health</WidgetTitle>
        <WidgetEmpty>Connected, but this box reports no metrics.</WidgetEmpty>
      </WidgetFrame>
    )
  }

  if (ctx.size === 'small') {
    return (
      <WidgetFrame title={`Box health, CPU ${pct(t.cpu)}`}>
        <WidgetTitle>CPU</WidgetTitle>
        <WidgetBigValue>{pct(t.cpu)}</WidgetBigValue>
        <Meter value={t.cpu} />
      </WidgetFrame>
    )
  }

  return (
    <WidgetFrame title="Box health">
      <WidgetTitle right={t.hostname ? <span className="mono">{t.hostname}</span> : undefined}>Box health</WidgetTitle>
      <div className="flex flex-col gap-2">
        <div>
          <div className="flex items-baseline justify-between">
            <WidgetLabel tone="secondary">CPU</WidgetLabel>
            <WidgetLabel tone={tone(t.cpu)} mono>{pct(t.cpu)}</WidgetLabel>
          </div>
          <Meter value={t.cpu} />
        </div>
        <div>
          <div className="flex items-baseline justify-between">
            <WidgetLabel tone="secondary">Memory</WidgetLabel>
            <WidgetLabel tone={tone(t.memPercent)} mono>{pct(t.memPercent)}</WidgetLabel>
          </div>
          <Meter value={t.memPercent} />
        </div>
        {ctx.size === 'large' && (
          <div className="flex flex-col gap-1">
            {t.battery !== undefined && (
              <div className="flex items-baseline justify-between">
                <WidgetLabel tone="secondary">Battery</WidgetLabel>
                <WidgetLabel tone={t.charging ? 'success' : 'faint'} mono>
                  {pct(t.battery)}{t.charging ? ' charging' : ''}
                </WidgetLabel>
              </div>
            )}
            {t.tempC !== undefined && (
              <div className="flex items-baseline justify-between">
                <WidgetLabel tone="secondary">Temperature</WidgetLabel>
                <WidgetLabel tone="faint" mono>{Math.round(t.tempC)}°C</WidgetLabel>
              </div>
            )}
            {t.uptime && (
              <div className="flex items-baseline justify-between">
                <WidgetLabel tone="secondary">Uptime</WidgetLabel>
                <WidgetLabel tone="faint" mono>{t.uptime}</WidgetLabel>
              </div>
            )}
          </div>
        )}
      </div>
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.pulse',
    name: 'Box health',
    description: 'CPU, memory and uptime of this box. Measured locally, sent nowhere.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['small', 'medium', 'large'],
    // No tick: the telemetry socket pushes, so there is nothing for a timer to
    // do. A widget that polls what is already being pushed at it is a widget
    // that re-renders for no reason.
    tick: 'none',
    permissions: ['telemetry'],
  },
  render: (ctx) => <PulseWidget {...ctx} />,
}))
