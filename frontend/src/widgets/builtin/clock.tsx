// clock.tsx — the box's own time and date.
//
// The one widget that must be right on a box with nothing configured, no
// network and no account. It reads the system clock and the viewer's locale and
// nothing else.
import {
  defineWidget, registerWidget,
  WidgetFrame, WidgetBigValue, WidgetLabel,
  type WidgetContext,
} from '../index'

export default function ClockWidget(ctx: WidgetContext) {
  const now = ctx.now
  // Locale-driven, never a hardcoded "h:mm A". A box in Johannesburg and a box
  // in Chicago disagree about 24-hour time, and the platform already knows which
  // the viewer prefers.
  const t = now.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
  const weekday = now.toLocaleDateString(undefined, { weekday: 'long' })
  const date = now.toLocaleDateString(undefined, { month: 'long', day: 'numeric' })

  if (ctx.size === 'small') {
    return (
      <WidgetFrame title={`Clock, ${t}`}>
        <WidgetBigValue sub={<span>{weekday}</span>}>{t}</WidgetBigValue>
      </WidgetFrame>
    )
  }
  return (
    <WidgetFrame title={`Clock, ${t}`}>
      <div className="flex items-end justify-between gap-2">
        <WidgetBigValue sub={<span>{weekday}, {date}</span>}>{t}</WidgetBigValue>
        <WidgetLabel tone="faint" mono>{now.getFullYear()}</WidgetLabel>
      </div>
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.clock',
    name: 'Clock',
    description: 'The time and date on this box.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['small', 'medium'],
    // Minute, not second: nothing rendered here changes faster than that, and a
    // 1 Hz re-render for a display with no seconds is 59 wasted renders a minute
    // on a surface that sits under every window.
    tick: 'minute',
  },
  render: (ctx) => <ClockWidget {...ctx} />,
}))
