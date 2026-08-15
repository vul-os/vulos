// stocks.tsx — a watchlist, and a deliberate decision about the network.
//
// ─────────────────────────────────────────────────────────────────────────────
// THE DECISION, IN THE FILE THAT IMPLEMENTS IT
// ─────────────────────────────────────────────────────────────────────────────
//
// Every stock-quote source is a third party. The set of tickers a person watches
// is not incidental data — it is their portfolio, their employer, the thing they
// are about to buy — and a quote request discloses it, with an IP and a
// timestamp, on a schedule that reveals when the machine is awake. On a product
// whose entire claim is that the user's box does not phone home, putting a
// silent US finance API on every desktop by default would not be an
// implementation detail. It would quietly retract the pitch.
//
// So this widget REQUESTS NO NETWORK PERMISSION AT ALL. It cannot ask for one
// later either: `hosts` is part of a manifest, the manifest is what the
// permission UI reads, and this manifest declares neither. The widget is
// therefore not "configured off by default" — it is structurally incapable of a
// third-party request, which is a property you can check by reading this file
// rather than a setting you have to trust.
//
// What it does instead: it is a watchlist you maintain. You type the symbols and
// the last price you know, and it shows you the move against your own reference
// price, with an honest "as you entered it" and the age of that entry. That is
// less magical than a live ticker and it is completely true offline, which is
// the state a sovereign box is in whenever its owner wants it to be.
//
// THE LIVE-QUOTE PATH IS NOT MISSING, IT IS UNTAKEN. The platform has one
// (widgets/net.ts): a widget declares the exact hosts it will reach, the user
// grants `network` per placement having seen those hosts, and THE BOX makes the
// request — never the browser, so the provider sees the box and not the user's
// tab, and the box can cache, coalesce and log it for the Transparency panel. A
// live-quote widget is a separate widget that declares its own provider, ships
// disabled, and is a thing the user chooses. It is not this one, and no box
// ships the broker today.
//
// See roadmap/WIDGETS.md § "Stocks and the network" for the full reasoning.

import { useEffect, useState } from 'react'
import {
  defineWidget, registerWidget,
  WidgetFrame, WidgetTitle, WidgetLabel, WidgetEmpty,
  type WidgetContext, type WidgetStorage,
} from '../index'
import {
  ageLabel, changePct, parsePosition, readAsOf,
  WATCHLIST_AS_OF_KEY as AS_OF_KEY, WATCHLIST_RAW_KEY as RAW_KEY,
  type Position,
} from './logic'

/**
 * When the user last changed these figures.
 *
 * Two halves, deliberately separated:
 *
 *  - The VALUE is derived. When the symbol list changes, state is adjusted
 *    during render (React's documented "derive state from props" pattern) rather
 *    than in an effect. Setting state inside an effect would render once with
 *    the stale timestamp, then immediately re-render — a cascading render for a
 *    value that was knowable the first time.
 *  - The WRITE is an effect, because localStorage is an external system and
 *    synchronising an external system is exactly what effects are for. A render
 *    must stay free of side effects; React may call it twice in development and
 *    may discard the result.
 */
function useAsOf(storage: WidgetStorage | null, rawList: string[], now: Date): number | null {
  const joined = rawList.join('|')
  const nowMs = now.getTime()
  // `now` comes from the HOST's clock, not Date.now(). Calling Date.now() during
  // render is an impure read — the value changes between two renders React
  // considers equivalent, which is exactly the class of bug the purity rule
  // exists for. The host tick is a prop: stable within a render, advanced by the
  // rail.
  const [snap, setSnap] = useState(() => ({ joined, asOf: readAsOf(storage, joined, nowMs) }))
  if (snap.joined !== joined) setSnap({ joined, asOf: nowMs })

  useEffect(() => {
    if (!storage) return
    if (storage.get(RAW_KEY) === snap.joined) return
    storage.set(RAW_KEY, snap.joined)
    storage.set(AS_OF_KEY, String(snap.asOf ?? 0))
  }, [storage, snap])

  return snap.asOf
}

export default function WatchlistWidget(ctx: WidgetContext) {
  const rawList = Array.isArray(ctx.settings.positions) ? ctx.settings.positions : []
  const positions = rawList.map(parsePosition).filter((p): p is Position => p !== null)
  const rejected = rawList.length - positions.length
  const asOf = useAsOf(ctx.storage, rawList, ctx.now)

  if (positions.length === 0) {
    return (
      <WidgetFrame title="Watchlist">
        <WidgetTitle>Watchlist</WidgetTitle>
        <WidgetEmpty>
          {rawList.length === 0
            ? 'No symbols yet. Open this widget’s settings and add e.g. “AAPL 189.50 175.00”.'
            : 'None of those entries could be read. Use “SYMBOL price” — for example “AAPL 189.50”.'}
        </WidgetEmpty>
      </WidgetFrame>
    )
  }

  const limit = ctx.size === 'large' ? 6 : 3
  return (
    <WidgetFrame title="Watchlist">
      <WidgetTitle right={<span className="mono">manual</span>}>Watchlist</WidgetTitle>
      <div className="vwidget-scroll flex flex-col gap-1">
        {positions.slice(0, limit).map((p) => {
          const ch = changePct(p)
          return (
            <div key={p.symbol} className="flex items-baseline justify-between gap-2">
              <WidgetLabel tone="primary" mono>{p.symbol}</WidgetLabel>
              <span className="flex items-baseline gap-2 shrink-0">
                <WidgetLabel tone="secondary" mono>{p.last.toFixed(2)}</WidgetLabel>
                {ch !== null && (
                  // Sign is printed as well as coloured. Colour alone carries no
                  // meaning for a colour-blind reader, and "up" vs "down" is the
                  // one thing this row exists to say.
                  <WidgetLabel tone={ch >= 0 ? 'success' : 'danger'} mono>
                    {ch >= 0 ? '+' : ''}{ch.toFixed(2)}%
                  </WidgetLabel>
                )}
              </span>
            </div>
          )
        })}
      </div>
      {/* The disclaimer is not fine print — it is the widget's actual claim.
          These are the numbers the user typed, and the widget says so every
          time it is looked at. */}
      <WidgetLabel tone="faint">
        Your own figures{asOf ? `, entered ${ageLabel(asOf, ctx.now.getTime())}` : ''}. No price source on this box.
      </WidgetLabel>
      {rejected > 0 && <WidgetLabel tone="warning">{rejected} entr{rejected === 1 ? 'y' : 'ies'} could not be read</WidgetLabel>}
    </WidgetFrame>
  )
}

registerWidget(defineWidget({
  manifest: {
    id: 'vulos.watchlist',
    name: 'Watchlist',
    description: 'Symbols and prices you enter yourself. Makes no network requests.',
    version: '1.0.0',
    author: 'Vulos',
    sizes: ['medium', 'large'],
    // Minute cadence only so the "entered N hours ago" line stays true. Nothing
    // else here changes on its own — because nothing else here CAN.
    tick: 'minute',
    // `storage` and nothing else. Deliberately, permanently, no `network`.
    permissions: ['storage'],
    settings: [
      {
        key: 'positions',
        type: 'list',
        label: 'Symbols',
        default: [],
        placeholder: 'AAPL 189.50 175.00, MSFT 412.10',
        help: 'One per entry: SYMBOL, the price you last saw, and optionally your reference price for the % change.',
        maxItems: 12,
      },
    ],
  },
  // Rendered as a COMPONENT, never called: it holds hooks, and calling it would
  // splice its hook order into whatever rendered it.
  render: (ctx) => <WatchlistWidget {...ctx} />,
}))
