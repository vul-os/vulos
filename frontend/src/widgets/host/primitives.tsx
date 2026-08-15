// primitives.tsx — the small set of presentational pieces a widget builds from.
//
// These exist so a widget author never has to guess the OS's type scale or
// reach for a colour value. Every colour here is a semantic token from
// index.css; there is not a single hex in this file, which is what keeps a
// third-party widget inside the contrast gate in BOTH themes without its author
// knowing the gate exists.
//
// They are also the reason `WidgetLabel` takes a `tone` instead of a colour: a
// widget that could pass an arbitrary colour could pass one that fails AA, and
// then the gate fails on a string the OS didn't write.
import type { ReactNode } from 'react'
import './widgets.css'

/**
 * The card. Every widget's outermost element.
 *
 * `title` is the accessible name of the region — the rail is a list of unlabelled
 * boxes to a screen reader otherwise.
 */
export function WidgetFrame({
  title, children, footer, onClick, interactive = false,
}: {
  title: string
  children: ReactNode
  footer?: ReactNode
  onClick?: () => void
  interactive?: boolean
}) {
  const body = (
    <>
      <div className="vwidget-body">{children}</div>
      {footer ? <div className="vwidget-foot">{footer}</div> : null}
    </>
  )
  if (interactive && onClick) {
    return (
      <button type="button" onClick={onClick} className="vwidget-card vwidget-card-btn focus-primary" aria-label={title}>
        {body}
      </button>
    )
  }
  return (
    <section className="vwidget-card" aria-label={title}>
      {body}
    </section>
  )
}

/** The small uppercase caption at the top of a tile. */
export function WidgetTitle({ children, right }: { children: ReactNode; right?: ReactNode }) {
  return (
    <div className="vwidget-title-row">
      <span className="vwidget-title">{children}</span>
      {right ? <span className="vwidget-title-right">{right}</span> : null}
    </div>
  )
}

/**
 * The one big number a small tile exists to show.
 *
 * Tabular numerals are not decoration: a clock or a percentage that re-renders
 * every second visibly jitters with proportional digits, and on a glanceable
 * surface that reads as the box being busy.
 */
export function WidgetBigValue({ children, sub }: { children: ReactNode; sub?: ReactNode }) {
  return (
    <div>
      <div className="vwidget-big">{children}</div>
      {sub ? <div className="vwidget-sub">{sub}</div> : null}
    </div>
  )
}

/**
 * A line of secondary text.
 *
 * `tone` picks a token rather than accepting a colour — see the file header.
 * 'faint' is the dimmest tone that still measures above AA in both themes.
 */
export function WidgetLabel({
  children, tone = 'secondary', mono = false,
}: {
  children: ReactNode
  tone?: 'primary' | 'secondary' | 'faint' | 'accent' | 'success' | 'warning' | 'danger'
  mono?: boolean
}) {
  return <span className={`vwidget-label vwidget-tone-${tone}${mono ? ' mono' : ''}`}>{children}</span>
}

/**
 * The honest empty state.
 *
 * A widget with nothing to show must render THIS rather than an empty card or a
 * spinner that never resolves. "Nothing to show and here's why" is information;
 * a blank tile is the user wondering whether the OS is broken.
 */
export function WidgetEmpty({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="vwidget-empty">
      <span className="vwidget-empty-text">{children}</span>
      {action ? <div className="vwidget-empty-action">{action}</div> : null}
    </div>
  )
}

/** The honest error state. Same argument as WidgetEmpty, different cause. */
export function WidgetError({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="vwidget-empty">
      <span className="vwidget-error-dot" aria-hidden="true" />
      <span className="vwidget-empty-text">{children}</span>
      {action ? <div className="vwidget-empty-action">{action}</div> : null}
    </div>
  )
}

/** A tappable row inside a list-shaped widget. */
export function WidgetRowButton({
  children, onClick, label,
}: {
  children: ReactNode
  onClick: () => void
  label: string
}) {
  return (
    <button type="button" onClick={onClick} aria-label={label} className="vwidget-row focus-primary">
      {children}
    </button>
  )
}
