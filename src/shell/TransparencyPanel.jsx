/**
 * TransparencyPanel — the honest, in-shell posture surface (Wave 10, #3).
 *
 * Opened from the TrustBadge. Everything here is REAL (backed by
 * /api/assistant/status + /api/auth/masterkey/envelope via useSovereignty) and
 * HONEST — it mirrors the documented boundaries, including the uncomfortable
 * ones: messaging and file shares (incl. filenames) are content-blind /
 * end-to-end, but MAIL and DOCS are provider-readable so the assistant can
 * actually work over them. No marketing overclaim.
 *
 * It also hosts the two verbs that make sovereignty actionable:
 *   • the tier picker  (POST /api/assistant/tier — where your AI runs)
 *   • "Download my data" (GET /api/export/data — the anti-lock-in proof)
 */
import { useEffect } from 'react'
import { useSovereignty } from '../core/useSovereignty'
import { tierInfo } from '../core/sovereignty'
import { useFocusTrap } from './useFocusTrap'

// AT_REST — the honest at-rest posture. Each row is a claim we have earned or a
// limit we admit. `state`: 'e2e' (green, content-blind), 'readable' (amber,
// provider-readable by design).
const AT_REST = [
  { label: 'Messages (Talk)', state: 'e2e',
    note: 'Content-blind. Encrypted with keys only your devices hold.' },
  { label: 'File shares', state: 'e2e',
    note: 'Content-blind end-to-end — including filenames — for cross-instance shares.' },
  { label: 'Mail', state: 'readable',
    note: 'Provider-readable on your instance, by design, so your assistant can read and act on it.' },
  { label: 'Docs / Ofisi', state: 'readable',
    note: 'Provider-readable on your instance, so it can be indexed, searched and edited.' },
]

const EXPORT_COVERS = [
  'Mail — one .eml per message + a JSON index, per folder',
  'Files — your Drive tree, real bytes, original names & paths',
  'Calendar (.ics) & Contacts (.vcf) — if your mail service exposes them',
]
const EXPORT_NOT = [
  'Talk / Meet history, Ofisi documents (docs, sheets, slides, whiteboards) — not yet exportable here',
  'Content held only on a peer instance behind an end-to-end share — that stays',
  'encrypted server-side, so you export it from the box that holds the keys',
]

function Dot({ color }) {
  return <span className="inline-block w-2 h-2 rounded-full shrink-0" style={{ background: color }} />
}

function SectionTitle({ children }) {
  return <div className="text-[10px] font-mono uppercase tracking-[0.18em] text-neutral-500 mb-2">{children}</div>
}

// EgressBanner — the headline "what leaves this box" state, front and center.
function EgressBanner({ egress }) {
  const external = egress.level === 'external'
  const sovereign = egress.level === 'sovereign'
  // Sovereign IS off-box egress to an operator-declared, Vulos-UNVERIFIED
  // endpoint — so it is treated as caution (amber), NOT painted safe-green like
  // the genuinely-local tier. Only a truly-local box earns the green shield.
  const offBox = external || sovereign
  const color = offBox ? 'var(--status-warning)' : 'var(--status-success)'
  const bg = offBox ? 'bg-amber-500/10 border-amber-500/30' : 'bg-emerald-500/10 border-emerald-500/25'
  const headline = external
    ? 'Egress enabled'
    : sovereign ? 'Off-box to a declared endpoint' : 'Nothing leaves this box'
  const detail = external
    ? `Mail content may be sent to ${egress.dest}. Watch the network tab — this is the only thing that goes off-box.`
    : sovereign
      ? `AI runs at an operator-declared endpoint (${egress.dest}). Vulos does not operate or verify it — this is off-box egress you have chosen to trust.`
      : 'AI runs on this instance. No mail content is permitted to leave. Verifiable in your network tab.'
  return (
    <div className={`rounded-lg border px-3.5 py-3 flex items-start gap-3 ${bg}`}>
      <svg viewBox="0 0 16 16" width="20" height="20" fill="none" className="mt-0.5 shrink-0">
        <rect x="1.5" y="3" width="8.5" height="10.5" rx="1.6" stroke={color} strokeWidth="1.3" />
        {offBox
          ? <path d="M8.5 8.25H15M12 5.5 15 8.25 12 11" stroke={color} strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
          : <circle cx="5.75" cy="8.25" r="1.6" fill={color} />}
      </svg>
      <div className="min-w-0">
        <div className="text-[13px] font-medium" style={{ color }}>{headline}</div>
        <div className="text-[11.5px] text-neutral-400 leading-snug mt-0.5">{detail}</div>
      </div>
    </div>
  )
}

// TierRow — where your AI runs, with the inline picker. Real POST /api/assistant/tier.
function AiTierSection() {
  const { tier, sov, tierOptions, busy, setTier, externalArmed } = useSovereignty()
  const opts = (tierOptions && tierOptions.length ? tierOptions : [
    { tier: 'local', label: tierInfo('local').label },
    { tier: 'sovereign', label: tierInfo('sovereign').label },
    { tier: 'brokered', label: tierInfo('brokered').label },
  ])
  return (
    <div>
      <SectionTitle>Where your AI runs</SectionTitle>
      {sov?.reason && (
        <div className="text-[11.5px] text-neutral-400 leading-snug mb-2.5">{sov.reason}</div>
      )}
      <div className="flex flex-col gap-1.5">
        {opts.map(o => {
          const info = tierInfo(o.tier)
          const active = tier === o.tier
          return (
            <button
              key={o.tier}
              type="button"
              disabled={busy}
              onClick={() => !active && setTier(o.tier)}
              className={`text-left rounded-lg px-3 py-2 border transition-colors disabled:opacity-50 ${
                active ? 'bg-neutral-800/80 border-neutral-600' : 'bg-neutral-900/50 border-neutral-800 hover:border-neutral-700'
              }`}
            >
              <div className="flex items-center gap-2">
                <Dot color={info.dot} />
                <span className={`text-[12px] ${info.tone}`}>{o.label || info.label}</span>
                {active && <span className="ml-auto text-[10px] font-mono text-neutral-500">current</span>}
              </div>
              <div className="text-[10.5px] text-neutral-500 mt-0.5 leading-snug pl-4">{info.blurb}</div>
            </button>
          )
        })}
      </div>
      {externalArmed && (
        <div className="text-[10.5px] text-amber-400/80 mt-2 leading-snug">
          The external egress opt-in is armed on this instance — brokered/external tiers are permitted to send mail off-box.
        </div>
      )}
    </div>
  )
}

function AtRestSection({ hasMasterKey }) {
  return (
    <div>
      <SectionTitle>Encrypted at rest vs provider-readable</SectionTitle>
      <div className="rounded-lg border border-neutral-800 divide-y divide-neutral-800/70 overflow-hidden">
        {AT_REST.map(r => (
          <div key={r.label} className="flex items-start gap-2.5 px-3 py-2">
            <Dot color={r.state === 'e2e' ? 'var(--status-success)' : 'var(--status-warning)'} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="text-[12px] text-neutral-200">{r.label}</span>
                <span className={`text-[9.5px] font-mono uppercase tracking-wider ${
                  r.state === 'e2e' ? 'text-emerald-400' : 'text-amber-400'
                }`}>{r.state === 'e2e' ? 'End-to-end' : 'Provider-readable'}</span>
              </div>
              <div className="text-[10.5px] text-neutral-500 leading-snug mt-0.5">{r.note}</div>
            </div>
          </div>
        ))}
      </div>
      <div className="text-[10.5px] text-neutral-500 leading-snug mt-2">
        Content-blind data is encrypted with keys your client holds
        {hasMasterKey === true ? ' — this account has a client-side master key.'
          : hasMasterKey === false ? '. This is a legacy account with no client-side master key.'
          : '.'} Mail and docs are deliberately readable on your own instance so the assistant can work over them; that readability stops at your box unless you enable egress above.
      </div>
    </div>
  )
}

function KeysSection({ hasMasterKey }) {
  return (
    <div>
      <SectionTitle>Your keys</SectionTitle>
      <div className="flex items-center gap-2.5 mb-2">
        <Dot color={hasMasterKey === false ? 'var(--text-faint)' : 'var(--status-success)'} />
        <span className="text-[12px] text-neutral-200">
          {hasMasterKey === true ? 'Master key held on your device (zero-access)'
            : hasMasterKey === false ? 'Legacy account — no client-side master key'
            : 'Checking master-key state…'}
        </span>
      </div>
      <div className="text-[10.5px] text-neutral-500 leading-snug">
        Keys are client-held and wrapped by your password; the server stores only an
        envelope it cannot open. Keep a recovery kit so a lost password never means lost data.
      </div>
      <a
        href="/api/recovery/kit"
        className="inline-flex items-center gap-1.5 mt-2 text-[11px] text-blue-400 hover:text-blue-300"
      >
        <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.3">
          <path d="M8 2v8M5 7l3 3 3-3M3 13h10" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
        Download recovery kit (admin)
      </a>
    </div>
  )
}

function ExportSection() {
  return (
    <div>
      <SectionTitle>It&apos;s yours — take it with you</SectionTitle>
      <div className="text-[11.5px] text-neutral-400 leading-snug mb-2.5">
        Export your data in standard, portable formats. Nothing here needs Vulos to read it back.
      </div>
      <a
        href="/api/export/data"
        className="inline-flex items-center gap-2 rounded-lg px-3.5 py-2 bg-blue-600/90 hover:bg-blue-600 text-white text-[12px] font-medium transition-colors"
      >
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4">
          <path d="M8 1.5v8M4.5 6.5 8 10l3.5-3.5M2.5 13.5h11" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
        Download my data (.zip)
      </a>
      <div className="grid grid-cols-1 gap-2 mt-3">
        <div>
          <div className="text-[10px] font-mono uppercase tracking-wider text-emerald-500/80 mb-1">Covers</div>
          <ul className="space-y-0.5">
            {EXPORT_COVERS.map((c, i) => (
              <li key={i} className="text-[11px] text-neutral-400 leading-snug pl-3 relative">
                <span className="absolute left-0 text-emerald-500">+</span>{c}
              </li>
            ))}
          </ul>
        </div>
        <div>
          <div className="text-[10px] font-mono uppercase tracking-wider text-neutral-500 mb-1">Not yet covered</div>
          <ul className="space-y-0.5">
            {EXPORT_NOT.map((c, i) => (
              <li key={i} className="text-[11px] text-neutral-500 leading-snug pl-3 relative">
                <span className="absolute left-0 text-neutral-600">–</span>{c}
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  )
}

export default function TransparencyPanel() {
  const { panelOpen, closePanel, egress, hasMasterKey } = useSovereignty()
  // A11Y: trap focus inside the panel while open + restore to the opener (the
  // TrustBadge) on close. Esc dismisses, matching the shell's overlay contract.
  const trapRef = useFocusTrap(panelOpen)
  useEffect(() => {
    if (!panelOpen) return
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); closePanel() } }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [panelOpen, closePanel])
  if (!panelOpen) return null
  return (
    <div
      className="fixed inset-0 z-[250] flex items-start justify-center pt-14 px-4 pb-6 bg-black/50 backdrop-blur-sm animate-[fadeIn_0.12s_ease-out]"
      onClick={closePanel}
    >
      <div
        ref={trapRef}
        role="dialog"
        aria-modal="true"
        aria-label="Transparency and sovereignty"
        onClick={e => e.stopPropagation()}
        className="w-full max-w-lg max-h-full overflow-y-auto rounded-2xl border border-neutral-700/60
          bg-neutral-900/95 backdrop-blur-xl shadow-2xl shadow-black/60"
      >
        {/* Header */}
        <div className="sticky top-0 z-10 flex items-center justify-between px-5 py-3.5
          bg-neutral-900/95 backdrop-blur-xl border-b border-neutral-800/70">
          <div>
            <div className="text-[13px] font-semibold text-neutral-100">Transparency</div>
            <div className="text-[10.5px] font-mono text-neutral-500">Trust you can verify.</div>
          </div>
          <button
            type="button"
            onClick={closePanel}
            className="w-7 h-7 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-neutral-800/70 transition-colors"
            aria-label="Close"
          >
            {'×'}
          </button>
        </div>

        <div className="p-5 space-y-5">
          <EgressBanner egress={egress} />
          <AiTierSection />
          <AtRestSection hasMasterKey={hasMasterKey} />
          <KeysSection hasMasterKey={hasMasterKey} />
          <ExportSection />
          <div className="text-[10px] text-neutral-600 leading-snug pt-1 border-t border-neutral-800/60">
            This panel reads live state from your instance (assistant sovereignty status and your
            key envelope). It is intentionally honest about what is readable and what is not — the
            boundaries above are the ones we have actually earned.
          </div>
        </div>
      </div>
    </div>
  )
}
