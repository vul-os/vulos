// PhoneChrome.tsx — the shared furniture of the Phone app: size awareness,
// avatars, the line indicator, tab bars and the honest empty/error states.

import { type ReactNode, type CSSProperties } from 'react'
import { initials, avatarHue } from './phoneUtils'
import { TABS, type TabId } from './phoneLayout'
import type { Line, LineId } from './usePhoneData'

// ─── avatar ────────────────────────────────────────────────────────────────

export function Avatar({ name, number, size = 40, photo }: { name: string; number?: string; size?: number; photo?: string }) {
  const hue = avatarHue(name || number || '?')
  const style: CSSProperties = {
    width: size, height: size,
    background: `oklch(0.62 0.13 ${hue} / 0.22)`,
    color: `oklch(0.72 0.15 ${hue})`,
    fontSize: Math.round(size * 0.36),
  }
  if (photo) {
    return <img src={photo} alt="" width={size} height={size} className="shrink-0 rounded-full object-cover" style={{ width: size, height: size }} />
  }
  return (
    <span aria-hidden="true" className="shrink-0 grid place-items-center rounded-full font-semibold select-none" style={style}>
      {initials(name, number)}
    </span>
  )
}

// ─── states ────────────────────────────────────────────────────────────────

/**
 * A real failure, shown as a failure. The whole reason this component exists is
 * that a service error rendered as an empty list is indistinguishable from a
 * working app with nothing in it — on a phone, that means a dialler that
 * silently does nothing and an address book that looks wiped.
 */
export function ErrorNotice({ message, onRetry }: { message: string; onRetry?: () => void }) {
  if (!message) return null
  return (
    <div role="alert" className="shrink-0 flex items-start gap-2.5 px-3.5 py-2.5 text-[13px]"
      style={{
        background: 'color-mix(in srgb, var(--status-danger) 12%, var(--bg-surface))',
        borderBottom: '1px solid color-mix(in srgb, var(--status-danger) 30%, transparent)',
        color: 'var(--text-primary)',
      }}>
      <span aria-hidden="true" className="mt-px text-danger">⚠</span>
      <span className="flex-1 min-w-0 leading-snug">{message}</span>
      {onRetry && (
        <button type="button" onClick={onRetry}
          className="shrink-0 px-2.5 py-1 rounded-md text-[12.5px] font-medium focus-primary transition-colors"
          style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
          Retry
        </button>
      )}
    </div>
  )
}

export function EmptyNote({ glyph, title, body, action }: { glyph: string; title: string; body?: string; action?: ReactNode }) {
  return (
    <div className="h-full grid place-items-center px-6 py-10 text-center">
      <div className="max-w-[22rem]">
        <div aria-hidden="true" className="w-12 h-12 mx-auto mb-3.5 grid place-items-center rounded-2xl text-[22px]"
          style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}>
          {glyph}
        </div>
        <div className="text-[14px] font-medium" style={{ color: 'var(--text-primary)' }}>{title}</div>
        {body && <p className="text-[13px] mt-1.5 leading-relaxed" style={{ color: 'var(--text-tertiary)' }}>{body}</p>}
        {action && <div className="mt-4">{action}</div>}
      </div>
    </div>
  )
}

/**
 * What a box with NO telephony hardware shows.
 *
 * It names the actual thing to plug in, and it does not mention Android unless
 * Android is relevant — a box with a USB LTE stick is the first-class case here,
 * and the previous version of this app told every such user that Phone "only
 * works inside the Vulos Android app", which was simply false.
 */
export function NoLineState({ serviceError, onRetry, children }: { serviceError: string; onRetry: () => void; children?: ReactNode }) {
  if (serviceError) {
    return (
      <EmptyNote
        glyph="⚠"
        title="Telephony isn’t answering"
        body={`${serviceError} This is a problem with the box, not a missing modem — your calls and messages are not lost.`}
        action={
          <button type="button" onClick={onRetry}
            className="px-3.5 py-2 rounded-lg text-[13px] font-medium focus-primary transition-colors"
            style={{ background: 'var(--accent)', color: 'var(--accent-contrast, #fff)' }}>
            Try again
          </button>
        }
      />
    )
  }
  return (
    <EmptyNote
      glyph="📡"
      title="No SIM or modem on this box"
      body="Plug a GSM modem into the box — a USB LTE stick, an M.2 or PCIe card — and insert a SIM. Vulos talks to whatever ModemManager detects, so any make works; no phone required. Your contacts are still here in the meantime."
      action={children}
    />
  )
}

// ─── line indicator ────────────────────────────────────────────────────────

function SignalBars({ signal }: { signal: number }) {
  if (signal < 0) return null
  const lit = signal >= 75 ? 4 : signal >= 50 ? 3 : signal >= 25 ? 2 : signal > 0 ? 1 : 0
  return (
    <span className="inline-flex items-end gap-[2px] h-3" title={`Signal ${signal}%`} aria-label={`Signal ${signal} percent`}>
      {[3, 6, 9, 12].map((h, i) => (
        <span key={h} className="w-[3px] rounded-[1px]"
          style={{ height: h, background: i < lit ? 'var(--accent)' : 'var(--border-strong)' }} />
      ))}
    </span>
  )
}

export function LineBar({ lines, active, onPick }: { lines: Line[]; active: Line | null; onPick: (id: LineId) => void }) {
  if (!active) return null
  return (
    <div className="shrink-0 flex items-center gap-2.5 px-3.5 py-2 text-[12.5px]"
      style={{ background: 'var(--bg-elevated)', borderBottom: '1px solid var(--border-default)' }}>
      <SignalBars signal={active.signal} />
      <span className="min-w-0 flex items-baseline gap-1.5">
        <span className="font-medium truncate" style={{ color: 'var(--text-primary)' }}>{active.label}</span>
        <span className="truncate" style={{ color: 'var(--text-tertiary)' }}>{active.sub}</span>
      </span>
      {!active.voice && (
        <span className="shrink-0 px-1.5 py-0.5 rounded text-[11.5px] font-medium"
          style={{ background: 'color-mix(in srgb, var(--status-warning) 18%, transparent)', color: 'var(--status-warning)' }}
          title="This modem reports no voice support — SMS only">
          SMS only
        </span>
      )}
      {lines.length > 1 && (
        <select
          aria-label="Active line"
          value={active.id}
          onChange={(e) => onPick(e.target.value as LineId)}
          className="ml-auto shrink-0 rounded-md px-1.5 py-1 text-[12.5px] focus-primary"
          style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
          {lines.map((l) => <option key={l.id} value={l.id}>{l.label}</option>)}
        </select>
      )}
    </div>
  )
}

// ─── tabs ──────────────────────────────────────────────────────────────────

/** Top tabs (medium/wide) — an underlined row, as on a tablet or desktop. */
export function TopTabs({ tab, onPick }: { tab: TabId; onPick: (t: TabId) => void }) {
  return (
    <div role="tablist" aria-label="Phone sections" className="shrink-0 flex items-stretch gap-0.5 px-2"
      style={{ borderBottom: '1px solid var(--border-default)' }}>
      {TABS.map((t) => {
        const on = t.id === tab
        return (
          <button key={t.id} type="button" role="tab" aria-selected={on} onClick={() => onPick(t.id)}
            className="px-3.5 py-2.5 text-[13px] font-medium transition-colors focus-primary"
            style={{
              color: on ? 'var(--text-primary)' : 'var(--text-tertiary)',
              borderBottom: on ? '2px solid var(--accent)' : '2px solid transparent',
              marginBottom: -1,
            }}>
            {t.label}
          </button>
        )
      })}
    </div>
  )
}

/** Bottom tabs (narrow) — the phone-shaped nav, thumb-reachable. */
export function BottomTabs({ tab, onPick }: { tab: TabId; onPick: (t: TabId) => void }) {
  return (
    <div role="tablist" aria-label="Phone sections" className="shrink-0 grid grid-cols-4"
      style={{ background: 'var(--bg-elevated)', borderTop: '1px solid var(--border-default)' }}>
      {TABS.map((t) => {
        const on = t.id === tab
        return (
          <button key={t.id} type="button" role="tab" aria-selected={on} onClick={() => onPick(t.id)}
            className="flex flex-col items-center gap-1 py-2 text-[11.5px] font-medium transition-colors focus-primary"
            style={{ color: on ? 'var(--accent)' : 'var(--text-tertiary)' }}>
            <span aria-hidden="true" className="text-[15px] leading-none">{t.glyph}</span>
            {t.label}
          </button>
        )
      })}
    </div>
  )
}

// ─── call button ───────────────────────────────────────────────────────────

export function CallButton({ onClick, disabled, title, size = 40 }: { onClick: () => void; disabled?: boolean; title: string; size?: number }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled} title={title} aria-label={title}
      className="shrink-0 grid place-items-center rounded-full transition-all hover:brightness-110 active:scale-95 disabled:opacity-40 disabled:cursor-not-allowed focus-primary"
      style={{
        width: size, height: size,
        background: disabled ? 'var(--bg-active)' : 'var(--status-success)',
        color: disabled ? 'var(--text-tertiary)' : '#fff',
      }}>
      <svg viewBox="0 0 24 24" width={size * 0.45} height={size * 0.45} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M22 16.92v3a2 2 0 01-2.18 2 19.79 19.79 0 01-8.63-3.07 19.5 19.5 0 01-6-6A19.79 19.79 0 012.12 4.18 2 2 0 014.11 2h3a2 2 0 012 1.72 12.84 12.84 0 00.7 2.81 2 2 0 01-.45 2.11L8.09 9.91a16 16 0 006 6l1.27-1.27a2 2 0 012.11-.45 12.84 12.84 0 002.81.7A2 2 0 0122 16.92z" />
      </svg>
    </button>
  )
}
