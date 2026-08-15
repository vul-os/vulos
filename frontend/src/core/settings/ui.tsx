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
import type { ReactNode, ComponentPropsWithoutRef } from 'react'
import { SettingsIcon } from '../AppIcons'

interface SectionProps {
  title: ReactNode
  desc?: ReactNode
  icon?: string
  actions?: ReactNode
  children?: ReactNode
}

// ── Section — the top-of-pane header. An optional accent icon chip anchors the
// title; `actions` slot into the top-right (e.g. a Refresh button). ──────────
export function Section({ title, desc, icon, actions, children }: SectionProps) {
  return (
    <div className="animate-[fadeIn_0.18s_ease-out]">
      {/* The masthead sits above the hairline, the cards below it. Without the
          rule the title floated on the same blank ground as the first card and
          nothing marked where the page began. */}
      <header className="mb-6 pb-5 border-b border-[var(--border-subtle)] flex items-start gap-4">
        {icon && (
          <span
            aria-hidden="true"
            className="mt-0.5 shrink-0 grid place-items-center w-11 h-11 rounded-2xl accent-bg-soft accent-text ring-1 ring-inset accent-border-soft"
          >
            <SettingsIcon name={icon} size={24} />
          </span>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex items-start justify-between gap-4">
            <h2 className="text-[1.5rem] leading-[1.2] font-semibold tracking-[-0.015em] text-[var(--text-primary)]">{title}</h2>
            {actions && <div className="shrink-0 flex items-center gap-2 pt-1">{actions}</div>}
          </div>
          {/* max-w-prose, not the full measure: a description that ran the whole
              width of a maximised window was a 100+ character line nobody can
              track back to the start of. */}
          {desc && <p className="mt-2 text-sm text-[var(--text-tertiary)] leading-relaxed max-w-prose">{desc}</p>}
        </div>
      </header>
      <div className="space-y-5">{children}</div>
    </div>
  )
}

interface ActionsProps {
  hint?: ReactNode
  children?: ReactNode
  className?: string
}

// ── Actions — the standard action bar for a pane or card: an optional hint on
// the left, buttons on the right. Replaces the bare `<button className="btn">`
// that several panes dropped straight onto the page background, where a
// neutral-grey button next to a text link read as a disabled control rather
// than as the panel's primary action. ──────────────────────────────────────
export function Actions({ hint, children, className = '' }: ActionsProps) {
  return (
    <div className={`flex flex-wrap items-center justify-between gap-x-4 gap-y-3 ${className}`}>
      {hint ? <p className="text-xs text-[var(--text-tertiary)] leading-relaxed min-w-0 flex-1">{hint}</p> : <span className="flex-1" />}
      {/* `flex-wrap` and no `shrink-0`: the button group was unshrinkable, so
          two side-by-side buttons overran a 320px screen — About's "View
          licences" + "View written offer" reached x=339 and the second was
          cut off by the screen edge. The outer row already wraps; the inner
          group has to be allowed to as well, or it wraps as one rigid block
          that is itself too wide. */}
      <div className="flex flex-wrap items-center justify-end gap-2.5">{children}</div>
    </div>
  )
}

interface NarrowProps {
  size?: 'xs' | 'sm' | 'md' | 'lg'
  children?: ReactNode
  className?: string
}

const NARROW_SIZES: Record<NonNullable<NarrowProps['size']>, string> = {
  xs: 'max-w-[7rem]',
  sm: 'max-w-[11rem]',
  md: 'max-w-[16rem]',
  lg: 'max-w-[24rem]',
}

// ── Narrow — caps the width of a short control (a PIN, a port, a hex colour).
//
// This exists because of a trap, not a preference: `.input` is a plain
// (unlayered) rule in index.css that hard-sets `width: 100%`, and unlayered CSS
// beats Tailwind's layered utilities. So `<input className="input w-40">` — six
// of which shipped in Settings — silently rendered at the FULL column width,
// and got wider still every time the measure grew. A four-digit PIN box was
// 512px across. Wrapping constrains the input without changing `.input` for the
// rest of the OS, where the same combination is used by other surfaces.
export function Narrow({ size = 'md', children, className = '' }: NarrowProps) {
  return <div className={`w-full ${NARROW_SIZES[size]} ${className}`}>{children}</div>
}

interface CardProps extends Omit<ComponentPropsWithoutRef<'section'>, 'title'> {
  title?: ReactNode
  desc?: ReactNode
  icon?: string
  aside?: ReactNode
  footer?: ReactNode
  className?: string
  bodyClassName?: string
  children?: ReactNode
}

// ── Card — a lifted surface grouping related controls. Optional title/desc/icon
// header and a `footer` slot (e.g. a save bar) that sits on a tinted base. ────
export function Card({ title, desc, icon, aside, footer, className = '', bodyClassName = '', children, ...rest }: CardProps) {
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

interface FieldProps {
  label?: ReactNode
  hint?: ReactNode
  htmlFor?: string
  children?: ReactNode
}

// ── Field — a labelled control with optional helper hint. ────────────────────
export function Field({ label, hint, htmlFor, children }: FieldProps) {
  return (
    <div className="mb-4 last:mb-0">
      {label && <label htmlFor={htmlFor} className="block text-xs font-medium text-[var(--text-secondary)] mb-1.5">{label}</label>}
      {children}
      {hint && <p className="text-[12px] text-[var(--text-faint)] mt-1.5 leading-relaxed">{hint}</p>}
    </div>
  )
}

interface SettingRowProps {
  label?: ReactNode
  desc?: ReactNode
  icon?: string
  control?: ReactNode
  children?: ReactNode
  className?: string
}

// ── SettingRow — a label/description on the left, a control on the right. The
// workhorse for grouped lists inside a Card; use `<Divider/>` between rows. ───
export function SettingRow({ label, desc, icon, control, children, className = '' }: SettingRowProps) {
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

interface ToggleProps {
  label?: ReactNode
  // ariaLabel names the control when it is rendered bare (no `label`, e.g.
  // passed as a SettingRow's `control` — the row's own label text is visible
  // but not programmatically associated with this button).
  ariaLabel?: string
  checked?: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
}

// ── Toggle — accent-driven switch. Standalone or inside a SettingRow. ────────
export function Toggle({ label, ariaLabel, checked, onChange, disabled }: ToggleProps) {
  const btn = (
    <button
      type="button"
      role="switch"
      aria-checked={!!checked}
      aria-label={ariaLabel ?? (typeof label === 'string' ? label : undefined)}
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
export type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

const PILL_TONES: Record<Tone, string> = {
  neutral: 'bg-[var(--bg-elevated)] text-[var(--text-tertiary)] ring-[var(--border-strong)]',
  accent: 'accent-bg-soft accent-text ring-transparent',
  success: 'text-success ring-transparent',
  warning: 'text-warning ring-transparent',
  danger: 'text-danger ring-transparent',
}
const PILL_DOT: Record<Tone, string> = {
  neutral: 'bg-[var(--text-muted)]',
  accent: 'accent-bg',
  success: 'bg-[var(--status-success)]',
  warning: 'bg-[var(--status-warning)]',
  danger: 'bg-[var(--status-danger)]',
}
const PILL_SOFT: Partial<Record<Tone, string>> = {
  success: 'var(--status-success-soft)',
  warning: 'var(--status-warning-soft)',
  danger: 'var(--status-danger-soft)',
}

interface PillProps {
  tone?: Tone
  dot?: boolean
  pulse?: boolean
  children?: ReactNode
}

export function Pill({ tone = 'neutral', dot = true, pulse = false, children }: PillProps) {
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

interface MeterProps {
  label?: ReactNode
  pct?: number
  right?: ReactNode
  tone?: Tone
  className?: string
}

// ── Meter — a labelled utilisation bar. Tone shifts amber→red as it fills;
// pass an explicit `tone` to override (e.g. keep accent for a neutral quantity). ─
export function Meter({ label, pct, right, tone, className = '' }: MeterProps) {
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

interface StatTileProps {
  label?: ReactNode
  value?: ReactNode
  sub?: ReactNode
  tone?: Tone
  icon?: string
}

// ── StatTile — a compact metric cell: big value + label + optional sub/pill.
// Group several in a grid for an at-a-glance readout row. ────────────────────
export function StatTile({ label, value, sub, tone, icon }: StatTileProps) {
  const valueTone = tone === 'success' ? 'text-success'
    : tone === 'warning' ? 'text-warning'
    : tone === 'danger' ? 'text-danger'
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

interface InfoListProps {
  children?: ReactNode
  className?: string
}

// ── InfoList / InfoRow — a bordered card of key/value rows (read-only detail). ─
export function InfoList({ children, className = '' }: InfoListProps) {
  return (
    <div className={`rounded-xl border border-[var(--border-default)] overflow-hidden divide-y divide-[var(--border-subtle)] ${className}`}>
      {children}
    </div>
  )
}

interface InfoRowProps {
  label?: ReactNode
  value?: ReactNode
  mono?: boolean
  ok?: boolean | null
}

export function InfoRow({ label, value, mono = false, ok }: InfoRowProps) {
  const tone = ok == null ? 'text-[var(--text-secondary)]'
    : ok ? 'text-success' : 'text-danger'
  return (
    // A definition grid, not a justify-between row. Pushing the value hard
    // right worked at a 42rem measure and fell apart as the pane grew: on a
    // maximised window "Device" and its value sat ~770px apart with nothing
    // between them, so no row could be read across. A bounded label column
    // keeps every pair adjacent and every value on one left edge at any width.
    <div className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)] items-baseline gap-x-5 px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <span className={`text-sm min-w-0 truncate ${tone} ${mono ? 'mono text-[13px]' : ''}`}>{value ?? '—'}</span>
    </div>
  )
}

interface EmptyStateProps {
  icon?: string
  title?: ReactNode
  hint?: ReactNode
  action?: ReactNode
}

// ── EmptyState — a considered "nothing here yet" with an icon medallion. ─────
export function EmptyState({ icon = 'about', title, hint, action }: EmptyStateProps) {
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

type BannerTone = 'info' | 'success' | 'warning' | 'danger'

interface BannerProps {
  tone?: BannerTone
  title?: ReactNode
  children?: ReactNode
  icon?: boolean
}

// ── Banner — an inline status message (info / success / warning / danger). ───
export function Banner({ tone = 'info', title, children, icon = true }: BannerProps) {
  const map: Record<BannerTone, { wrap: string; dot: string }> = {
    info: { wrap: 'border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)]', dot: 'bg-[var(--text-muted)]' },
    success: { wrap: 'border-success-soft bg-[var(--status-success-soft)] text-success', dot: 'bg-[var(--status-success)]' },
    warning: { wrap: 'border-warning-soft bg-[var(--status-warning-soft)] text-warning', dot: 'bg-[var(--status-warning)]' },
    danger: { wrap: 'border-danger-soft bg-[var(--status-danger-soft)] text-danger', dot: 'bg-[var(--status-danger)]' },
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
