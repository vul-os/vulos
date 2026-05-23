// VumailStep.jsx — VUMAIL-01: mandatory vumail identity creation step.
//
// This step appears in the first-boot wizard after the user account step and
// before the cluster-join / recovery steps.  It is MANDATORY — there is no
// skip option (frozen invariant: every Vulos instance has a vumail identity).
//
// UX flow:
//  1. User types a handle (the part before "@").
//  2. Availability is checked live against the cloud control plane
//     (GET /api/vumail/check?handle=<handle>&domain=<domain>).
//  3. On confirm, POST /api/vumail/claim to the local OS server.
//  4. On success, advance the wizard.
//
// The step also supports custom domains for paid tiers: if the parent wizard
// passes customDomain="example.com", that domain is used instead of
// "vumail.org".  Custom domains require the user to have completed domain
// verification before reaching this step (enforced by the cloud dashboard).
//
// Dev override: if the environment variable VUMAIL_SKIP=1 is set, the step
// renders a lightweight skip button (dev/CI only — never in production builds).
import { useState, useEffect, useRef } from 'react'

// ─── Constants ────────────────────────────────────────────────────────────────

const FREE_DOMAIN = 'vumail.org'
const HANDLE_RE = /^[a-z0-9][a-z0-9_-]{1,30}[a-z0-9]$|^[a-z0-9]{3}$/
const CHECK_DEBOUNCE_MS = 400

// ─── Component ────────────────────────────────────────────────────────────────

/**
 * VumailStep — mandatory wizard step for vumail identity creation.
 *
 * Props:
 *   config        {object}   — wizard config bag (read: username, write: vumailAddress)
 *   update        {function} — (key, value) → updates config bag
 *   onNext        {function} — advances the wizard on success
 *   onPrev        {function} — navigates back
 *   customDomain  {string?}  — override domain for paid-tier custom addresses
 */
export default function VumailStep({ config, update, onNext, onPrev, customDomain }) {
  const domain = customDomain || FREE_DOMAIN

  // ── State ──────────────────────────────────────────────────────────────────
  const [handle, setHandle] = useState(() => {
    // Pre-fill from username if it matches the handle charset.
    const u = (config?.username || '').toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32)
    return HANDLE_RE.test(u) ? u : ''
  })
  const [availability, setAvailability] = useState(null) // null | 'checking' | 'available' | 'taken' | 'error'
  const [claiming, setClaiming] = useState(false)
  const [claimError, setClaimError] = useState('')
  const [claimed, setClaimed] = useState(false)

  const debounceRef = useRef(null)
  const lastCheckedRef = useRef('')

  // ── Handle validation ──────────────────────────────────────────────────────
  const isValidHandle = HANDLE_RE.test(handle)

  // ── Availability check ─────────────────────────────────────────────────────
  useEffect(() => {
    if (!isValidHandle) {
      setAvailability(null)
      return
    }
    if (handle === lastCheckedRef.current) return

    clearTimeout(debounceRef.current)
    setAvailability('checking')

    debounceRef.current = setTimeout(async () => {
      try {
        const res = await fetch(
          `/api/vumail/check?handle=${encodeURIComponent(handle)}&domain=${encodeURIComponent(domain)}`
        )
        if (!res.ok) {
          setAvailability('error')
          return
        }
        const data = await res.json()
        lastCheckedRef.current = handle
        setAvailability(data.available ? 'available' : 'taken')
      } catch {
        setAvailability('error')
      }
    }, CHECK_DEBOUNCE_MS)

    return () => clearTimeout(debounceRef.current)
  }, [handle, domain, isValidHandle])

  // ── Claim ──────────────────────────────────────────────────────────────────
  const handleClaim = async () => {
    if (!isValidHandle || availability !== 'available' || claiming) return

    setClaiming(true)
    setClaimError('')

    try {
      const res = await fetch('/api/vumail/claim', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ handle, domain }),
      })

      const data = await res.json().catch(() => ({}))

      if (res.ok || res.status === 201) {
        const address = data.address || `${handle}@${domain}`
        update('vumailAddress', address)
        setClaimed(true)
        // Advance the wizard after a short moment to let the success state render.
        setTimeout(onNext, 600)
        return
      }

      if (res.status === 409) {
        setAvailability('taken')
        setClaimError('That handle was just claimed — please choose another.')
        return
      }

      setClaimError(data.error || `Unexpected error (${res.status}). Please retry.`)
    } catch {
      setClaimError('Could not reach the server. Check your network and retry.')
    } finally {
      setClaiming(false)
    }
  }

  // ── Dev skip (VUMAIL_SKIP=1) ───────────────────────────────────────────────
  // import.meta.env.VITE_VUMAIL_SKIP is set in the dev Vite config when
  // the boot environment has VUMAIL_SKIP=1.  Never available in production.
  const devSkipEnabled = typeof import.meta !== 'undefined' &&
    import.meta.env?.VITE_VUMAIL_SKIP === '1'

  // ── Render ─────────────────────────────────────────────────────────────────
  if (claimed) {
    return (
      <div className="text-center">
        <div className="mb-6 flex flex-col items-center gap-4">
          <div className="w-16 h-16 rounded-full bg-green-500/20 flex items-center justify-center">
            <svg viewBox="0 0 24 24" className="w-8 h-8 text-green-400" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M20 6L9 17l-5-5" />
            </svg>
          </div>
          <div>
            <p className="text-lg font-medium text-neutral-100">Identity claimed</p>
            <p className="text-sm text-blue-400 mt-1 font-mono">
              {handle}@{domain}
            </p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div>
      {/* Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-light text-neutral-100">Create your mail identity</h2>
        <p className="text-sm text-neutral-500 mt-1">
          Your vumail address is your Vulos identity — used for mail, account recovery, and peering with other Vulos instances.
          {' '}This step is mandatory.
        </p>
      </div>

      {/* Address input */}
      <div className="mb-4">
        <label className="block text-xs text-neutral-500 mb-2 uppercase tracking-wider">
          Choose a handle
        </label>
        <div className="flex items-center gap-0 rounded-xl border-2 overflow-hidden transition-all
          border-neutral-700/60 bg-neutral-900/60 focus-within:border-blue-500/60">
          {/* Handle field */}
          <input
            type="text"
            value={handle}
            onChange={e => {
              setHandle(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32))
              setClaimError('')
              setClaimed(false)
            }}
            placeholder="yourname"
            autoFocus
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            className="flex-1 bg-transparent px-4 py-3 text-sm text-neutral-100 outline-none placeholder:text-neutral-600 font-mono"
          />
          {/* @ separator */}
          <span className="px-2 text-neutral-600 text-sm select-none">@</span>
          {/* Domain (read-only) */}
          <span className="px-4 py-3 text-sm text-neutral-400 font-mono bg-neutral-800/40">
            {domain}
          </span>
        </div>

        {/* Validation / availability feedback */}
        <div className="mt-2 min-h-[1.25rem]">
          {handle.length > 0 && !isValidHandle && (
            <p className="text-xs text-amber-400">
              Handle must be 3–32 characters: lowercase letters, numbers, hyphens, and underscores only. Must start and end with a letter or number.
            </p>
          )}
          {isValidHandle && availability === 'checking' && (
            <p className="text-xs text-neutral-500 flex items-center gap-1.5">
              <span className="w-3 h-3 border border-neutral-500 border-t-blue-400 rounded-full animate-spin inline-block" />
              Checking availability…
            </p>
          )}
          {isValidHandle && availability === 'available' && (
            <p className="text-xs text-green-400">
              {handle}@{domain} is available
            </p>
          )}
          {isValidHandle && availability === 'taken' && (
            <p className="text-xs text-red-400">
              That handle is already taken — please choose another.
            </p>
          )}
          {isValidHandle && availability === 'error' && (
            <p className="text-xs text-amber-400">
              Could not verify availability. You can still try to claim it.
            </p>
          )}
        </div>
      </div>

      {/* Handle rules callout */}
      <div className="mb-5 rounded-xl bg-neutral-900/50 border border-neutral-800/50 px-4 py-3">
        <p className="text-xs text-neutral-500 leading-relaxed">
          <span className="text-neutral-400 font-medium">Your vumail address is permanent.</span>
          {' '}Choose something you are happy to share publicly — it will appear on your contact cards and peering invitations.
          {domain === FREE_DOMAIN
            ? ' Custom domain addresses (e.g. you@yourdomain.com) are available on paid tiers.'
            : ` You are using a custom domain (${domain}).`}
        </p>
      </div>

      {/* Claim error */}
      {claimError && (
        <div className="mb-4 rounded-xl bg-red-900/20 border border-red-800/40 px-4 py-3">
          <p className="text-sm text-red-400">{claimError}</p>
        </div>
      )}

      {/* Nav */}
      <div className="flex items-center justify-between mt-6 pt-4 border-t border-neutral-800/30">
        <button
          onClick={onPrev}
          className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors"
        >
          ← Back
        </button>

        <div className="flex items-center gap-3">
          {/* Dev-only skip (VUMAIL_SKIP=1) */}
          {devSkipEnabled && (
            <button
              onClick={onNext}
              className="text-xs text-neutral-700 hover:text-neutral-500 transition-colors underline underline-offset-2"
            >
              Skip (dev)
            </button>
          )}

          <button
            onClick={handleClaim}
            disabled={!isValidHandle || (availability !== 'available' && availability !== 'error') || claiming}
            className="btn-primary flex items-center gap-2 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {claiming ? (
              <>
                <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                Claiming…
              </>
            ) : (
              'Claim identity →'
            )}
          </button>
        </div>
      </div>
    </div>
  )
}
