/**
 * Topbar — restrained editor/page top bar.
 *
 * Slot model (Linear-style):
 *   <Topbar
 *     leading={<BackButton />}
 *     title={<EditableTitle />}
 *     meta={<SaveStatus />}
 *     actions={<>… buttons …</>}
 *   />
 *
 * Visual:
 *   - 44px tall, paper background, hairline bottom border
 *   - title sits left-of-centre with leading; meta is a quiet inline string
 *   - actions cluster on the right
 *
 * Keeps no state — it's purely compositional, so the caller stays in control.
 */
export default function Topbar({ leading, title, meta, actions, className = '' }) {
  return (
    <header
      className={[
        'flex items-center gap-2 h-11 px-3',
        'bg-paper border-b border-line',
        className,
      ].join(' ')}
    >
      {leading && <div className="flex items-center gap-2 flex-shrink-0">{leading}</div>}
      <div className="flex-1 min-w-0 flex items-center gap-2">
        {title}
      </div>
      {meta && <div className="hidden sm:flex items-center gap-2 text-2xs text-ink-faint">{meta}</div>}
      {actions && <div className="flex items-center gap-1 flex-shrink-0">{actions}</div>}
    </header>
  )
}
