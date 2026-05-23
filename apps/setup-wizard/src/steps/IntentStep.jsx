// IntentStep.jsx — "How will you use Vulos?" — shown after device pairing,
// before the "All done" screen.
//
// Two primary cards: Business / Personal.
// After choosing, a brief plan pitch is shown, framed as Vulos Cloud add-ons
// (since bare-metal users already own the OS — not OS tiers).
//
// "Skip — continue free" is always prominent; the install is NEVER gated
// behind a paid choice.
import { useState } from 'react'

// ─── Add-on catalogue (bare-metal framing) ────────────────────────────────────

const PERSONAL_ADDONS = [
  {
    icon: (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d="M17.5 19H9a7 7 0 110-14 7.5 7.5 0 017.5 7.5" />
        <path d="M17 12h5M19 10l3 2-3 2" />
      </svg>
    ),
    title: 'Relay',
    desc: 'Access your Vulos device from anywhere — no port forwarding needed.',
    tag: 'Cloud add-on',
  },
  {
    icon: (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 2a10 10 0 100 20A10 10 0 0012 2z" />
        <path d="M12 6v6l4 2" />
      </svg>
    ),
    title: 'AI tokens',
    desc: 'Top up the on-device AI assistant with cloud-burst compute when you need it.',
    tag: 'Cloud add-on',
  },
  {
    icon: (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4M17 8l-5-5-5 5M12 3v12" />
      </svg>
    ),
    title: 'Backup quota',
    desc: 'Encrypted off-device backups stored in the Vulos Cloud.',
    tag: 'Cloud add-on',
  },
]

const BUSINESS_ADDONS = [
  ...PERSONAL_ADDONS,
  {
    icon: (
      <svg viewBox="0 0 24 24" className="w-5 h-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="7" width="20" height="14" rx="2" />
        <path d="M16 7V5a2 2 0 00-2-2h-4a2 2 0 00-2 2v2" />
        <path d="M12 12v4M10 14h4" />
      </svg>
    ),
    title: 'Dedicated IP',
    desc: 'A static egress IP for your relay — useful for allowlisting in corporate firewalls.',
    tag: 'Enterprise add-on',
  },
]

// ─── Component ────────────────────────────────────────────────────────────────

/**
 * IntentStep — Business / Personal intent picker + add-on pitch.
 *
 * Props:
 *   config   {object}   — wizard config bag (write: intent)
 *   update   {function} — (key, value) → updates config bag
 *   onNext   {function} — advances the wizard
 *   onPrev   {function} — navigates back
 */
export default function IntentStep({ config, update, onNext, onPrev }) {
  // 'none' | 'personal' | 'business'
  const [intent, setIntent] = useState(config?.intent || 'none')
  const [showAddons, setShowAddons] = useState(false)

  const choose = (value) => {
    setIntent(value)
    update('intent', value)
    setShowAddons(true)
  }

  const addons = intent === 'business' ? BUSINESS_ADDONS : PERSONAL_ADDONS

  if (showAddons && intent !== 'none') {
    return (
      <div>
        {/* Header */}
        <div className="mb-6">
          <h2 className="text-2xl font-light text-neutral-100">
            {intent === 'business' ? 'Vulos for Business' : 'Vulos for Personal use'}
          </h2>
          <p className="text-sm text-neutral-500 mt-1">
            You already own the OS. These are optional cloud add-ons — add them any time from Settings.
          </p>
        </div>

        {/* Add-on cards */}
        <div className="space-y-3 mb-6">
          {addons.map(addon => (
            <div
              key={addon.title}
              className="flex items-start gap-3 rounded-xl border border-neutral-800/50 bg-neutral-900/50 px-4 py-3"
            >
              <span className="shrink-0 mt-0.5 text-blue-400">{addon.icon}</span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 mb-0.5">
                  <span className="text-sm font-medium text-neutral-100">{addon.title}</span>
                  <span className="text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-blue-600/10 text-blue-400 border border-blue-500/20">
                    {addon.tag}
                  </span>
                </div>
                <p className="text-xs text-neutral-500 leading-relaxed">{addon.desc}</p>
              </div>
            </div>
          ))}
        </div>

        <div className="flex items-center justify-between pt-4 border-t border-neutral-800/30">
          <button
            onClick={() => setShowAddons(false)}
            className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors"
          >
            ← Back
          </button>

          <div className="flex items-center gap-4">
            <button
              onClick={() => {
                update('intent', intent)
                onNext()
              }}
              className="text-sm text-neutral-500 hover:text-neutral-300 transition-colors"
            >
              Skip — continue free
            </button>
            <button
              onClick={() => {
                update('intent', intent)
                onNext()
              }}
              className="btn-primary flex items-center gap-2"
            >
              Continue →
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div>
      {/* Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-light text-neutral-100">How will you use Vulos?</h2>
        <p className="text-sm text-neutral-500 mt-1">
          Helps us show you the right optional cloud add-ons. Your install is complete either way.
        </p>
      </div>

      {/* Intent cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-6">
        {/* Business card */}
        <button
          onClick={() => choose('business')}
          className="group flex flex-col items-start gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-blue-500/50 hover:bg-blue-600/5 transition-all"
        >
          <div className="w-14 h-14 rounded-2xl bg-blue-600/15 flex items-center justify-center group-hover:bg-blue-600/25 transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 text-blue-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <rect x="2" y="7" width="20" height="14" rx="2" />
              <path d="M16 7V5a2 2 0 00-2-2h-4a2 2 0 00-2 2v2" />
              <path d="M12 12v4M10 14h4" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold text-neutral-100 mb-1">Business</div>
            <div className="text-sm text-neutral-500 leading-relaxed">
              Team use, fleet management, dedicated IPs, enterprise relay.
            </div>
          </div>
          <div className="flex flex-wrap gap-1.5 mt-1">
            {['Fleet', 'Dedicated IP', 'Relay'].map(tag => (
              <span key={tag} className="text-[10px] font-medium px-2 py-0.5 rounded-full bg-blue-600/10 text-blue-400 border border-blue-500/20">
                {tag}
              </span>
            ))}
          </div>
        </button>

        {/* Personal card */}
        <button
          onClick={() => choose('personal')}
          className="group flex flex-col items-start gap-3 px-6 py-7 rounded-2xl border-2 border-neutral-800/60 bg-neutral-900/50 text-left hover:border-violet-500/50 hover:bg-violet-600/5 transition-all"
        >
          <div className="w-14 h-14 rounded-2xl bg-violet-600/15 flex items-center justify-center group-hover:bg-violet-600/25 transition-colors">
            <svg viewBox="0 0 24 24" className="w-7 h-7 text-violet-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="12" cy="8" r="4" />
              <path d="M6 20v-2a6 6 0 0112 0v2" />
            </svg>
          </div>
          <div>
            <div className="text-base font-semibold text-neutral-100 mb-1">Personal</div>
            <div className="text-sm text-neutral-500 leading-relaxed">
              Home use, remote access, AI assistant, personal backup.
            </div>
          </div>
          <div className="flex flex-wrap gap-1.5 mt-1">
            {['Relay', 'AI tokens', 'Backup'].map(tag => (
              <span key={tag} className="text-[10px] font-medium px-2 py-0.5 rounded-full bg-violet-600/10 text-violet-400 border border-violet-500/20">
                {tag}
              </span>
            ))}
          </div>
        </button>
      </div>

      {/* Skip — continue free (always prominent) */}
      <div className="flex items-center justify-between pt-4 border-t border-neutral-800/30">
        <button
          onClick={onPrev}
          className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors"
        >
          ← Back
        </button>
        <button
          onClick={() => {
            update('intent', 'none')
            onNext()
          }}
          className="text-sm text-neutral-500 hover:text-neutral-300 transition-colors"
        >
          Skip — continue free →
        </button>
      </div>
    </div>
  )
}
