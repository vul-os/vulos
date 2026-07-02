/**
 * TrustBadge — the always-on sovereignty indicator woven into the shell chrome
 * (Wave 10, "trust you can verify is the product").
 *
 * Compact by design so it lives in the top menu bar, but it carries three REAL,
 * live signals, each backed by backend state via useSovereignty():
 *
 *   1. AI tier      — a colored dot + short label ("On device" / "Sovereign" /
 *                     "Brokered" / "External"), from /api/assistant/status.
 *   2. Egress       — "what leaves this box": a leak/shield glyph, green when
 *                     nothing (or only the in-region no-train endpoint) leaves,
 *                     amber when a brokered/external destination is enabled.
 *   3. At-rest lock — a padlock when the account holds a client-side master key.
 *
 * Clicking it opens the transparency panel (#3) with the full, honest posture.
 */
import { useSovereignty } from '../core/useSovereignty'
import { tierInfo } from '../core/sovereignty'

const SHORT = { local: 'On device', sovereign: 'Sovereign', brokered: 'Brokered', external: 'External' }

// Egress glyph: an outlined box (your instance) with an arrow. Amber + a leaking
// arrow when something is permitted to leave; green + a contained dot when not.
function EgressGlyph({ level }) {
  const leaves = level === 'external'
  const color = leaves ? '#f59e0b' : '#22c55e'
  return (
    <svg viewBox="0 0 16 16" width="12" height="12" fill="none" aria-hidden="true">
      <rect x="1.5" y="3" width="8" height="10" rx="1.4" stroke={color} strokeWidth="1.2" />
      {leaves ? (
        <path d="M8 8h6M11.5 5.5 14 8l-2.5 2.5" stroke={color} strokeWidth="1.2"
          strokeLinecap="round" strokeLinejoin="round" />
      ) : (
        <circle cx="5.5" cy="8" r="1.4" fill={color} />
      )}
    </svg>
  )
}

function LockGlyph({ held }) {
  const color = held ? '#a3a3a3' : '#737373'
  return (
    <svg viewBox="0 0 16 16" width="11" height="11" fill="none" aria-hidden="true"
      title={held ? 'Keys held on your device' : 'No client-side master key'}>
      <rect x="3.5" y="7" width="9" height="6" rx="1.2" stroke={color} strokeWidth="1.2" />
      <path d={held ? 'M5.5 7V5.2a2.5 2.5 0 0 1 5 0V7' : 'M5.5 7V5.2a2.5 2.5 0 0 1 4.6-1.3'}
        stroke={color} strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  )
}

export default function TrustBadge({ compact = false }) {
  const { tier, label, egress, hasMasterKey, togglePanel } = useSovereignty()
  if (!tier) return null // no status yet — stay quiet rather than guess
  const info = tierInfo(tier)
  const leaves = egress.level === 'external'

  const title = [
    `AI tier: ${label || info.label}`,
    `Egress: ${egress.text}`,
    hasMasterKey === true ? 'Keys held on your device' :
      hasMasterKey === false ? 'Legacy account — no client-side master key' : '',
    'Click for the full transparency panel',
  ].filter(Boolean).join('\n')

  return (
    <button
      type="button"
      onClick={togglePanel}
      title={title}
      aria-label="Sovereignty and privacy status"
      className={`group flex items-center gap-1.5 h-6 rounded-md border transition-colors
        ${compact ? 'px-1.5' : 'px-2'}
        ${leaves
          ? 'border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/15'
          : 'border-emerald-500/25 bg-emerald-500/[0.07] hover:bg-emerald-500/[0.12]'}`}
    >
      <span className="inline-block w-2 h-2 rounded-full shrink-0" style={{ background: info.dot }} />
      {!compact && (
        <span className={`text-[10px] font-mono uppercase tracking-wider ${info.tone} hidden md:inline`}>
          {SHORT[tier] || 'External'}
        </span>
      )}
      <span className="w-px h-3 bg-neutral-600/40 hidden md:block" />
      <EgressGlyph level={egress.level} />
      {hasMasterKey && <LockGlyph held />}
    </button>
  )
}
