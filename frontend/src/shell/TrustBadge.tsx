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
import { useSovereignty } from './sovereigntyTypes'
import { tierInfo } from '../core/sovereignty'
import '../core/touch-chrome.css'

// TOUCH. Measured at 390×844 on the shipping build, this badge was a 40×24
// target — the smallest control in the phone's status bar and one of the last
// five under the 44px floor.
//
// It is fixed by SPLITTING the button from the pill: the <button> is the hit
// area and the visible chrome moved to a child <span>. On a coarse pointer the
// button grows to 44×44 while the pill stays a pill, so the target clears the
// floor without the status bar turning into a row of blocks. See
// core/touch-chrome.css.
//
// The pill grows too — 28px tall, a 9px tier dot, 14px glyphs. This is a TRUST
// surface: the customization model's security argument is that it is
// unspoofable AND readable, and buying an easier tap by making the one
// always-on honesty indicator smaller or quieter would be a bad trade at any
// target size. Nothing here is hidden at any width that did not already hide it.
//
// On a fine pointer every one of those rules is inert and the menu bar is
// pixel-identical to before.

const SHORT: Record<string, string> = { local: 'On device', sovereign: 'Sovereign', brokered: 'Brokered', external: 'External' }

// Egress glyph: an outlined box (your instance) with an arrow. Amber + a leaking
// arrow when something is permitted to leave; green + a contained dot when not.
function EgressGlyph({ level }: { level: string }) {
  // Sovereign is off-box egress to an operator-declared (Vulos-unverified)
  // endpoint, so it reads as caution — not the safe-green of a truly-local box.
  const offBox = level === 'external' || level === 'sovereign'
  const color = offBox ? 'var(--status-warning)' : 'var(--status-success)'
  return (
    <svg viewBox="0 0 16 16" width="12" height="12" className="vtrust-glyph shrink-0" data-trust="egress" fill="none" aria-hidden="true">
      <rect x="1.5" y="3" width="8" height="10" rx="1.4" stroke={color} strokeWidth="1.2" />
      {offBox ? (
        <path d="M8 8h6M11.5 5.5 14 8l-2.5 2.5" stroke={color} strokeWidth="1.2"
          strokeLinecap="round" strokeLinejoin="round" />
      ) : (
        <circle cx="5.5" cy="8" r="1.4" fill={color} />
      )}
    </svg>
  )
}

function LockGlyph({ held }: { held: boolean }) {
  const color = held ? '#a3a3a3' : '#737373'
  // React's SVGProps type has no `title` attribute (only the SVG <title>
  // child element, which @types/react does model) — browsers render either
  // as the same hover tooltip, so the child element is a type-safe,
  // behaviourally-identical swap for the attribute the .jsx version used.
  return (
    <svg viewBox="0 0 16 16" width="11" height="11" className="vtrust-glyph shrink-0" data-trust="lock" fill="none" aria-hidden="true">
      <title>{held ? 'Keys held on your device' : 'No client-side master key'}</title>
      <rect x="3.5" y="7" width="9" height="6" rx="1.2" stroke={color} strokeWidth="1.2" />
      <path d={held ? 'M5.5 7V5.2a2.5 2.5 0 0 1 5 0V7' : 'M5.5 7V5.2a2.5 2.5 0 0 1 4.6-1.3'}
        stroke={color} strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  )
}

export default function TrustBadge({ compact = false }: { compact?: boolean }) {
  const { tier, label, egress, hasMasterKey, togglePanel } = useSovereignty()
  if (!tier) return null // no status yet — stay quiet rather than guess
  const info = tierInfo(tier)
  // Off-box egress (external OR the operator-declared "sovereign" endpoint) gets
  // the amber chrome; only a truly-local instance stays green.
  const leaves = egress.level === 'external' || egress.level === 'sovereign'

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
      // The NAME stays exactly what it was. It is what a user, a screen reader
      // and three other suites refer to this control by, and shell/Dock.tsx
      // already records what folding live state into a name costs: the tile
      // that was called "Files" or "Files (focused)" or "Files (minimized)"
      // depending on what the window happened to be doing.
      aria-label="Sovereignty and privacy status"
      // The DESCRIPTION is where the live state belongs, and it is what makes
      // this readable rather than colour-only. The pill's colour is the fast
      // signal; it must never be the only one, because a colour-only trust
      // indicator is not a trust indicator for everyone.
      aria-describedby="trust-badge-state"
      className="vtrust-hit group inline-flex items-center justify-center"
    >
      <span id="trust-badge-state" className="sr-only">
        {`${label || info.label}. Egress: ${egress.text}.`}
      </span>
      <span
        className={`vtrust-pill group flex items-center gap-1.5 h-6 rounded-md border transition-colors
          ${compact ? 'px-1.5' : 'px-2'}
          ${leaves
            ? 'border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/15'
            : 'border-emerald-500/25 bg-emerald-500/[0.07] hover:bg-emerald-500/[0.12]'}`}
      >
        <span data-trust="tier-dot" className="vtrust-dot inline-block w-2 h-2 rounded-full shrink-0" style={{ background: info.dot }} />
        {!compact && (
          <span className={`text-[12px] font-mono uppercase tracking-wider ${info.tone} hidden md:inline`}>
            {SHORT[tier] || 'External'}
          </span>
        )}
        <span className="w-px h-3 bg-neutral-600/40 hidden md:block" />
        <EgressGlyph level={egress.level} />
        {hasMasterKey && <LockGlyph held />}
      </span>
    </button>
  )
}
