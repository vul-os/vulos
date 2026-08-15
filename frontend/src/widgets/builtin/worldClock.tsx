// worldClock.tsx — "New York and Sydney time etc".
//
// Built entirely through the public widget API: this file imports from
// '../index' and nothing else, exactly as a third-party widget would. That is
// enforced by src/widgets/__tests__/publicApi.test.ts, and it is the only way to
// know the API is sufficient — if it weren't, this file would have had to reach
// into the host, and the test would have caught it.
//
// The hard parts are all in `time` (lib/tz.ts): DST in both hemispheres,
// half- and quarter-hour offsets, and the date over there differing from the
// date here. This file is presentation.

import {
  defineWidget, registerWidget, time,
  WidgetFrame, WidgetTitle, WidgetLabel, WidgetEmpty,
  type WidgetContext,
} from '../index'

// The dial. SVG, no text inside it — the time is printed in HTML beside it, so
// the numerals are measured by the contrast gate (which skips SVG) and are
// selectable, scalable and readable by a screen reader. The dial is decoration
// that agrees with the digits; it is never the only place the time appears.
function Dial({ hourFraction, phase, size = 30 }: { hourFraction: number; phase: string; size?: number }) {
  const r = size / 2
  const hourAngle = ((hourFraction % 12) / 12) * 360 - 90
  const minuteAngle = ((hourFraction % 1) * 60 / 60) * 360 - 90
  const hand = (angleDeg: number, len: number) => {
    const a = (angleDeg * Math.PI) / 180
    return { x: r + Math.cos(a) * len, y: r + Math.sin(a) * len }
  }
  const h = hand(hourAngle, r * 0.48)
  const m = hand(minuteAngle, r * 0.72)
  return (
    <svg className="vwidget-dial" data-phase={phase} width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
      <circle className="vwidget-dial-face" cx={r} cy={r} r={r - 0.5} />
      {[0, 3, 6, 9].map((t) => {
        const a = ((t / 12) * 360 - 90) * (Math.PI / 180)
        return (
          <line
            key={t} className="vwidget-dial-tick"
            x1={r + Math.cos(a) * (r - 3)} y1={r + Math.sin(a) * (r - 3)}
            x2={r + Math.cos(a) * (r - 5.5)} y2={r + Math.sin(a) * (r - 5.5)}
          />
        )
      })}
      <line className="vwidget-dial-hour" x1={r} y1={r} x2={h.x} y2={h.y} />
      <line className="vwidget-dial-min" x1={r} y1={r} x2={m.x} y2={m.y} />
      <circle className="vwidget-dial-pin" cx={r} cy={r} r={1.4} />
    </svg>
  )
}

export default function WorldClockWidget(ctx: WidgetContext) {
  const zonesSetting = ctx.settings.zones
  const raw = Array.isArray(zonesSetting) ? zonesSetting : []
  const home = time.localTimeZone()

  // A zone id the platform does not know is DROPPED, not rendered as an error
  // row and never allowed to throw. Persisted settings outlive tzdata entries
  // (Europe/Kiev, Asia/Calcutta), and the desktop must survive that.
  const zones = raw.filter((z) => time.isValidTimeZone(z))
  const showDate = ctx.settings.showDate === true
  const limit = ctx.size === 'large' ? 6 : ctx.size === 'medium' ? 3 : 1

  if (zones.length === 0) {
    return (
      <WidgetFrame title="World clock">
        <WidgetTitle>World clock</WidgetTitle>
        <WidgetEmpty>
          {raw.length > 0
            ? 'None of the saved time zones are recognised on this box.'
            : 'No cities yet — open this widget’s settings to add some.'}
        </WidgetEmpty>
      </WidgetFrame>
    )
  }

  return (
    <WidgetFrame title="World clock">
      <WidgetTitle right={<span className="mono">{time.labelForZone(home)}</span>}>World clock</WidgetTitle>
      <div className="vwidget-scroll">
        <div className="vwidget-clocks">
          {zones.slice(0, limit).map((zone) => {
            const delta = time.dayDelta(ctx.now, zone, home)
            const label = time.dayLabel(delta)
            const offset = time.formatOffset(time.zoneOffsetMinutes(ctx.now, zone))
            const frac = time.zoneHourFraction(ctx.now, zone) ?? 0
            const phase = time.dayPhase(ctx.now, zone) ?? 'day'
            return (
              <div className="vwidget-clock-row" key={zone}>
                <Dial hourFraction={frac} phase={phase} size={ctx.size === 'large' ? 30 : 26} />
                <span className="vwidget-clock-meta">
                  <span className="vwidget-clock-city">{time.labelForZone(zone)}</span>
                  {/* The offset and the day-difference are printed TOGETHER
                      because either alone misleads: "+10" does not tell you it
                      is already tomorrow there, and "Tomorrow" does not tell you
                      by how much. */}
                  <span className="vwidget-clock-when">
                    {label ? `${label} · ` : ''}
                    {showDate ? `${time.formatZoneDate(ctx.now, zone)} · ` : ''}
                    {offset}
                  </span>
                </span>
                <span className="vwidget-clock-time">
                  {time.formatZoneTime(ctx.now, zone)}
                </span>
              </div>
            )
          })}
        </div>
      </div>
      {zones.length > limit && (
        <WidgetLabel tone="faint">{zones.length - limit} more — make this widget larger</WidgetLabel>
      )}
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.worldclock',
    name: 'World clock',
    description: 'The time in other places, correct across DST and half-hour offsets.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['medium', 'large'],
    // Minute cadence, not second. A dial with an hour and a minute hand is
    // fully described by the minute; a second hand would cost 60× the renders
    // for a hand nobody reads on a 30px face — and this rail sits under every
    // window on an OS that is meant to idle quietly.
    tick: 'minute',
    // No permissions AT ALL. The platform's tzdata is already on the box; a
    // world clock that asked for the network would be asking for something it
    // does not need, and this one could not use it if it had it.
    settings: [
      {
        key: 'zones',
        type: 'list',
        label: 'Cities',
        // The two the founder named, plus London — enough on a fresh box to show
        // what the widget is for, spanning three continents and two hemispheres.
        default: ['America/New_York', 'Australia/Sydney', 'Europe/London'],
        placeholder: 'America/New_York, Asia/Tokyo',
        help: 'IANA time zone names, comma separated. e.g. Asia/Kolkata, Pacific/Auckland',
        maxItems: 8,
      },
      // NOT a "show seconds" toggle. This widget ticks once a minute, so a
      // seconds display would be frozen for 59 of every 60 seconds — a control
      // that produces a wrong number is worse than no control.
      { key: 'showDate', type: 'boolean', label: 'Show the local date', default: false },
    ],
  },
  render: (ctx) => <WorldClockWidget {...ctx} />,
}))
