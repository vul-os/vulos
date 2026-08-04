// ─────────────────────────────────────────────────────────────────────────────
// Settings UI kit — the shared design language for every Settings pane and the
// management dashboard. ONE system: cards, rows, pills, meters, stat tiles and
// empty states, all token-driven so they are automatically correct in light and
// dark and retint with the user's chosen accent.
//
// Design intent (WAVE-SET-REDESIGN):
//   • Structure over stacked inputs — related controls live in Cards with a
//     clear header + helper text, and a strong type hierarchy.
//   • State as form — status reads as Pills / Meters with semantic colour, not
//     bare sentences. "Some colour, not colourful": a neutral ground, the Iris
//     accent, and sparing success/warning/danger.
//   • Polish — considered empty states, hover feedback, subtle elevation and
//     dividers, motion that honours prefers-reduced-motion (via --motion-* tokens).
//
// These primitives are intentionally presentational; behaviour, data flow and
// gating stay in the panels that compose them.
// ─────────────────────────────────────────────────────────────────────────────
import { SettingsIcon } from '../AppIcons.jsx'

// ── Section — the top-of-pane header. An optional accent icon chip anchors the
// title; `actions` slot into the top-right (e.g. a Refresh button). ──────────
export function Section({ title, desc, icon, actions, children }) {
  return (
    <div className="animate-[fadeIn_0.18s_ease-out]">
      <header className="mb-6 flex items-start gap-3.5">
        {icon && (
          <span
            aria-hidden="true"
            className="mt-px shrink-0 grid place-items-center w-11 h-11 rounded-2xl accent-bg-soft accent-text ring-1 ring-inset accent-border-soft"
          >
            <SettingsIcon name={icon} size={24} />
          </span>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex items-center justify-between gap-3">
            <h2 className="text-[1.35rem] leading-tight font-semibold tracking-[-0.01em] text-[var(--text-primary)]">{title}</h2>
            {actions && <div className="shrink-0 flex items-center gap-2">{actions}</div>}
          </div>
          {desc && <p className="mt-1.5 text-sm text-[var(--text-tertiary)] leading-relaxed max-w-prose">{desc}</p>}
        </div>
      </header>
      <div className="space-y-5">{children}</div>
    </div>
  )
}

// ── Card — a lifted surface grouping related controls. Optional title/desc/icon
// header and a `footer` slot (e.g. a save bar) that sits on a tinted base. ────
export function Card({ title, desc, icon, aside, footer, className = '', bodyClassName = '', children, ...rest }) {
  return (
    <section
      className={`rounded-2xl border border-[var(--border-default)] bg-[var(--bg-surface)] shadow-sm overflow-hidden ${className}`}
      {...rest}
    >
      {(title || desc || aside) && (
        <div className="flex items-start justify-between gap-3 px-5 pt-4 pb-3">
          <div className="flex items-start gap-3 min-w-0">
            {icon && (
              <span aria-hidden="true" className="mt-px shrink-0 grid place-items-center w-8 h-8 rounded-xl bg-[var(--bg-elevated)] text-[var(--text-secondary)]">
                <SettingsIcon name={icon} size={19} />
              </span>
            )}
            <div className="min-w-0">
              {title && <h3 className="text-sm font-semibold text-[var(--text-primary)] tracking-tight">{title}</h3>}
              {desc && <p className="mt-0.5 text-xs text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
            </div>
          </div>
          {aside && <div className="shrink-0">{aside}</div>}
        </div>
      )}
      <div className={`px-5 ${(title || desc || aside) ? '' : 'pt-4'} pb-4 ${bodyClassName}`}>{children}</div>
      {footer && (
        <div className="px-5 py-3.5 border-t border-[var(--border-subtle)] bg-[var(--bg-elevated)]/40">{footer}</div>
      )}
    </section>
  )
}

// ── Field — a labelled control with optional helper hint. ────────────────────
export function Field({ label, hint, htmlFor, children }) {
  return (
    <div className="mb-4 last:mb-0">
      {label && <label htmlFor={htmlFor} className="block text-xs font-medium text-[var(--text-secondary)] mb-1.5">{label}</label>}
      {children}
      {hint && <p className="text-[12px] text-[var(--text-faint)] mt-1.5 leading-relaxed">{hint}</p>}
    </div>
  )
}

// ── SettingRow — a label/description on the left, a control on the right. The
// workhorse for grouped lists inside a Card; use `<Divider/>` between rows. ───
export function SettingRow({ label, desc, icon, control, children, className = '' }) {
  return (
    <div className={`flex items-center justify-between gap-4 py-3 first:pt-0 last:pb-0 ${className}`}>
      <div className="flex items-start gap-3 min-w-0">
        {icon && (
          <span aria-hidden="true" className="mt-px shrink-0 grid place-items-center w-8 h-8 rounded-xl bg-[var(--bg-elevated)] text-[var(--text-secondary)]">
            <SettingsIcon name={icon} size={19} />
          </span>
        )}
        <div className="min-w-0">
          <div className="text-sm text-[var(--text-primary)] font-medium">{label}</div>
          {desc && <div className="text-xs text-[var(--text-tertiary)] mt-0.5 leading-relaxed">{desc}</div>}
        </div>
      </div>
      <div className="shrink-0 flex items-center gap-2">{control ?? children}</div>
    </div>
  )
}

export function Divider() {
  return <div className="border-t border-[var(--border-subtle)]" />
}

// ── Toggle — accent-driven switch. Standalone or inside a SettingRow. ────────
export function Toggle({ label, checked, onChange, disabled }) {
  const btn = (
    <button
      type="button"
      role="switch"
      aria-checked={!!checked}
      aria-label={typeof label === 'string' ? label : undefined}
      disabled={disabled}
      onClick={() => !disabled && onChange(!checked)}
      style={checked ? { background: 'var(--accent)' } : undefined}
      className={`shrink-0 w-[42px] h-[24px] rounded-full relative transition-colors duration-[var(--motion-base)] disabled:opacity-40
        ${checked ? '' : 'bg-[var(--border-emphasis)]'}`}
    >
      <span
        aria-hidden="true"
        className={`absolute top-[3px] w-[18px] h-[18px] rounded-full bg-white shadow-sm transition-[left] duration-[var(--motion-base)] ${checked ? 'left-[21px]' : 'left-[3px]'}`}
      />
    </button>
  )
  if (label == null) return btn
  return (
    <div className="flex items-center justify-between gap-4 py-2.5">
      <span className="text-sm text-[var(--text-primary)]">{label}</span>
      {btn}
    </div>
  )
}

// ── Pill — a small status chip with a leading dot. tone: neutral | accent |
// success | warning | danger. `pulse` animates the dot (live indicator). ─────
const PILL_TONES = {
  neutral: 'bg-[var(--bg-elevated)] text-[var(--text-tertiary)] ring-[var(--border-strong)]',
  accent: 'accent-bg-soft accent-text ring-transparent',
  success: 'text-[var(--status-success)] ring-transparent',
  warning: 'text-[var(--status-warning)] ring-transparent',
  danger: 'text-[var(--status-danger)] ring-transparent',
}
const PILL_DOT = {
  neutral: 'bg-[var(--text-muted)]',
  accent: 'accent-bg',
  success: 'bg-[var(--status-success)]',
  warning: 'bg-[var(--status-warning)]',
  danger: 'bg-[var(--status-danger)]',
}
const PILL_SOFT = {
  success: 'var(--status-success-soft)',
  warning: 'var(--status-warning-soft)',
  danger: 'var(--status-danger-soft)',
}
export function Pill({ tone = 'neutral', dot = true, pulse = false, children }) {
  const soft = PILL_SOFT[tone]
  return (
    <span
      style={soft ? { background: soft } : undefined}
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-medium ring-1 ring-inset whitespace-nowrap ${PILL_TONES[tone] || PILL_TONES.neutral}`}
    >
      {dot && <span aria-hidden="true" className={`w-1.5 h-1.5 rounded-full ${PILL_DOT[tone] || PILL_DOT.neutral} ${pulse ? 'animate-pulse' : ''}`} />}
      {children}
    </span>
  )
}

// ── Meter — a labelled utilisation bar. Tone shifts amber→red as it fills;
// pass an explicit `tone` to override (e.g. keep accent for a neutral quantity). ─
export function Meter({ label, pct, right, tone, className = '' }) {
  const p = Math.max(0, Math.min(100, pct || 0))
  const resolved = tone || (p > 85 ? 'danger' : p > 65 ? 'warning' : 'accent')
  const fill = resolved === 'danger' ? 'bg-[var(--status-danger)]'
    : resolved === 'warning' ? 'bg-[var(--status-warning)]'
    : resolved === 'success' ? 'bg-[var(--status-success)]'
    : 'accent-bg'
  const readout = right ?? `${Math.round(p)}%`
  return (
    <div className={className}>
      {(label || readout != null) && (
        <div className="flex items-center justify-between mb-1.5">
          {label && <span className="text-xs text-[var(--text-secondary)] font-medium">{label}</span>}
          {readout != null && <span className="text-xs text-[var(--text-tertiary)] tabular-nums mono">{readout}</span>}
        </div>
      )}
      <div
        className="h-2 rounded-full bg-[var(--bg-elevated)] overflow-hidden"
        role="progressbar"
        aria-label={typeof label === 'string' ? label : undefined}
        aria-valuenow={Math.round(p)} aria-valuemin={0} aria-valuemax={100}
        aria-valuetext={typeof readout === 'string' ? readout : undefined}
      >
        <div className={`h-full rounded-full transition-[width] duration-[var(--motion-slow)] ${fill}`} style={{ width: `${p}%` }} />
      </div>
    </div>
  )
}

// ── StatTile — a compact metric cell: big value + label + optional sub/pill.
// Group several in a grid for an at-a-glance readout row. ────────────────────
export function StatTile({ label, value, sub, tone, icon }) {
  const valueTone = tone === 'success' ? 'text-[var(--status-success)]'
    : tone === 'warning' ? 'text-[var(--status-warning)]'
    : tone === 'danger' ? 'text-[var(--status-danger)]'
    : tone === 'accent' ? 'accent-text'
    : 'text-[var(--text-primary)]'
  return (
    <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3.5">
      <div className="flex items-center gap-1.5 text-[12px] uppercase tracking-[0.08em] font-medium text-[var(--text-tertiary)]">
        {icon && <span aria-hidden="true" className="opacity-70"><SettingsIcon name={icon} size={13} /></span>}
        {label}
      </div>
      <div className={`mt-1.5 text-2xl font-semibold tracking-tight tabular-nums ${valueTone}`}>{value}</div>
      {sub && <div className="mt-0.5 text-xs text-[var(--text-muted)]">{sub}</div>}
    </div>
  )
}

// ── InfoList / InfoRow — a bordered card of key/value rows (read-only detail). ─
export function InfoList({ children, className = '' }) {
  return (
    <div className={`rounded-xl border border-[var(--border-default)] overflow-hidden divide-y divide-[var(--border-subtle)] ${className}`}>
      {children}
    </div>
  )
}
export function InfoRow({ label, value, mono = false, ok }) {
  const tone = ok == null ? 'text-[var(--text-secondary)]'
    : ok ? 'text-[var(--status-success)]' : 'text-[var(--status-danger)]'
  return (
    <div className="flex items-center justify-between gap-4 px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <span className={`text-sm text-right truncate ${tone} ${mono ? 'mono text-[13px]' : ''}`}>{value ?? '—'}</span>
    </div>
  )
}

// ── EmptyState — a considered "nothing here yet" with an icon medallion. ─────
export function EmptyState({ icon = 'about', title, hint, action }) {
  return (
    <div className="py-14 px-6 text-center animate-[fadeIn_0.2s_ease-out]">
      <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl border border-[var(--border-default)] bg-[var(--bg-surface)] text-[var(--text-muted)] shadow-sm">
        <SettingsIcon name={icon} size={26} />
      </div>
      <p className="text-sm font-medium text-[var(--text-secondary)]">{title}</p>
      {hint && <p className="text-xs text-[var(--text-muted)] mt-1.5 max-w-xs mx-auto leading-relaxed">{hint}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

// ── Banner — an inline status message (info / success / warning / danger). ───
export function Banner({ tone = 'info', title, children, icon = true }) {
  const map = {
    info: { wrap: 'border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)]', dot: 'bg-[var(--text-muted)]' },
    success: { wrap: 'border-success-soft bg-[var(--status-success-soft)] text-[var(--status-success)]', dot: 'bg-[var(--status-success)]' },
    warning: { wrap: 'border-warning-soft bg-[var(--status-warning-soft)] text-[var(--status-warning)]', dot: 'bg-[var(--status-warning)]' },
    danger: { wrap: 'border-danger-soft bg-[var(--status-danger-soft)] text-[var(--status-danger)]', dot: 'bg-[var(--status-danger)]' },
  }
  const t = map[tone] || map.info
  return (
    <div role={tone === 'danger' || tone === 'warning' ? 'alert' : 'status'} className={`flex items-start gap-2.5 rounded-xl border px-4 py-3 text-sm ${t.wrap}`}>
      {icon && <span aria-hidden="true" className={`mt-1.5 shrink-0 w-2 h-2 rounded-full ${t.dot}`} />}
      <div className="min-w-0 leading-relaxed">
        {title && <div className="font-medium">{title}</div>}
        {children && <div className={title ? 'mt-0.5 opacity-90' : ''}>{children}</div>}
      </div>
    </div>
  )
}

// NOTE: a `humanBytes` formatter was exported from here as the "shared" byte
// formatter, but nothing ever imported it — BoxHealthPanel.jsx and
// ModelsPanel.jsx each define their own. It was dead code, and being a
// non-component export it also broke the react-refresh lint rule for this
// module, so it was removed. The two panel copies are deliberately NOT merged:
// ModelsPanel's variant renders 0 as "—" and stops at GB, which is different
// observable behaviour, not a duplicate.
