// VulosAccountStep.jsx — Reserve your Vulos account username (cloud identity).
//
// This step appears in the first-boot wizard after the account step and before
// the intent/appearance steps.  It is shown only for cloud and create-cloud
// install modes; the parent Setup component filters it out of the step list
// entirely when config.NETB05_choice === 'local', so this component is never
// rendered in local-only mode.
//
// Your Vulos account identity is your registered email / linked OAuth (Google or
// Microsoft) plus a chosen username. This step reserves that unique username.
// Mail is a bring-your-own connector to a mailbox you already own — no Vulos
// mailbox is provisioned here.
//
// UX flow:
//  1. User types a username. It is not an email address, so any "@" is stripped
//     and a hint is shown.
//  2. Availability is checked live, debounced 350 ms, against the local OS
//     proxy at GET /api/identity/check?handle=<handle> which forwards to the
//     cloud control plane.  If the cloud is unreachable the step fails-open
//     with a soft "checking later" warning; final atomicity is guaranteed by
//     Provision on submit.
//  3. On confirm, POST /api/identity/claim to the local OS server.
//  4. On success, advance the wizard.
//
// Dev override: if the environment variable VITE_VULOS_CLOUD_SKIP=1 is set, the step
// renders a lightweight skip button (dev/CI only — never in production builds).
import { useState, useEffect, useRef } from 'react'

// ─── Constants ────────────────────────────────────────────────────────────────

const HANDLE_RE = /^[a-z0-9][a-z0-9_-]{1,30}[a-z0-9]$|^[a-z0-9]{3}$/
const CHECK_DEBOUNCE_MS = 350

// ─── Component ────────────────────────────────────────────────────────────────

/**
 * VulosAccountStep — wizard step for reserving the Vulos account username.
 *
 * Props:
 *   config  {object}   — wizard config bag (read: username, write: cloudAccountAddress)
 *   update  {function} — (key, value) → updates config bag
 *   onNext  {function} — advances the wizard on success
 *   onPrev  {function} — navigates back
 */
export default function VulosAccountStep({ config, update, onNext, onPrev }) {
  // ── State ──────────────────────────────────────────────────────────────────
  const [handle, setHandle] = useState(() => {
    // Pre-fill from username if it matches the handle charset.
    const u = (config?.username || '').toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32)
    return HANDLE_RE.test(u) ? u : ''
  })
  const [availability, setAvailability] = useState(null) // null | 'checking' | 'available' | 'taken' | 'error' | 'offline'
  const [claiming, setClaiming] = useState(false)
  const [claimError, setClaimError] = useState('')
  const [claimed, setClaimed] = useState(false)
  // emailInputError: set when the user types an "@" (usernames are not emails)
  const [emailInputError, setEmailInputError] = useState('')

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
          `/api/identity/check?handle=${encodeURIComponent(handle)}`
        )
        if (!res.ok) {
          setAvailability('error')
          return
        }
        const data = await res.json()
        lastCheckedRef.current = handle
        // Soft offline: cloud unreachable but local proxy responded
        if (data.offline) {
          setAvailability('offline')
          return
        }
        setAvailability(data.available ? 'available' : 'taken')
        // Surface suggestions if taken
        if (!data.available && data.suggestions?.length) {
          setClaimError(`Taken — try: ${data.suggestions.join(', ')}`)
        }
      } catch {
        // Network error reaching even the local proxy — treat as offline/soft warn
        setAvailability('offline')
      }
    }, CHECK_DEBOUNCE_MS)

    return () => clearTimeout(debounceRef.current)
  }, [handle, isValidHandle])

  // ── Claim ──────────────────────────────────────────────────────────────────
  const handleClaim = async () => {
    if (!isValidHandle || (availability !== 'available' && availability !== 'offline') || claiming) return

    setClaiming(true)
    setClaimError('')

    try {
      const res = await fetch('/api/identity/claim', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ handle }),
      })

      const data = await res.json().catch(() => ({}))

      if (res.ok || res.status === 201) {
        const address = data.address || handle
        update('cloudAccountAddress', address)
        setClaimed(true)
        // Advance the wizard after a short moment to let the success state render.
        setTimeout(onNext, 600)
        return
      }

      if (res.status === 409) {
        setAvailability('taken')
        setClaimError('That username was just taken — please choose another.')
        return
      }

      setClaimError(data.error || `Unexpected error (${res.status}). Please retry.`)
    } catch {
      setClaimError('Could not reach the server. Check your network and retry.')
    } finally {
      setClaiming(false)
    }
  }

  // ── Dev skip (VITE_VULOS_CLOUD_SKIP=1) ───────────────────────────────────────
  // import.meta.env.VITE_VULOS_CLOUD_SKIP is set in the dev Vite config when
  // the boot environment has VITE_VULOS_CLOUD_SKIP=1.  Never available in production.
  const devSkipEnabled = typeof import.meta !== 'undefined' &&
    import.meta.env?.VITE_VULOS_CLOUD_SKIP === '1'

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
            <p className="text-lg font-medium text-neutral-100">Username reserved</p>
            <p className="text-sm text-blue-400 mt-1 font-mono">
              {handle}
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
        <h2 className="text-2xl font-light text-neutral-100">Create your Vulos account</h2>
        <p className="text-sm text-neutral-500 mt-1">
          Choose a username for your Vulos account — your identity across the suite. You sign in with your email and password or a linked Google/Microsoft account. Mail is a bring-your-own connector; a Vulos-hosted mailbox is an optional add-on, not created here.
          {' '}This step is mandatory.
        </p>
      </div>

      {/* Username input */}
      <div className="mb-4">
        <label className="block text-xs text-neutral-500 mb-2 uppercase tracking-wider">
          Choose a username
        </label>
        <div className="flex items-center gap-0 rounded-xl border-2 overflow-hidden transition-all
          border-neutral-700/60 bg-neutral-900/60 focus-within:border-blue-500/60">
          <input
            type="text"
            value={handle}
            onChange={e => {
              const raw = e.target.value
              // A username is not an email address — strip any "@…" and hint.
              if (raw.includes('@')) {
                setEmailInputError('Enter a username, not an email address.')
                setHandle(raw.split('@')[0].toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32))
              } else {
                setEmailInputError('')
                setHandle(raw.toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 32))
              }
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
        </div>

        {/* Validation / availability feedback */}
        <div className="mt-2 min-h-[1.25rem]">
          {emailInputError && (
            <p className="text-xs text-red-400">{emailInputError}</p>
          )}
          {!emailInputError && handle.length > 0 && !isValidHandle && (
            <p className="text-xs text-amber-400">
              Username must be 3–32 characters: lowercase letters, numbers, hyphens, and underscores only. Must start and end with a letter or number.
            </p>
          )}
          {!emailInputError && isValidHandle && availability === 'checking' && (
            <p className="text-xs text-neutral-500 flex items-center gap-1.5">
              <span className="w-3 h-3 border border-neutral-500 border-t-blue-400 rounded-full animate-spin inline-block" />
              Checking…
            </p>
          )}
          {!emailInputError && isValidHandle && availability === 'available' && (
            <p className="text-xs text-green-400">
              {handle} is available
            </p>
          )}
          {!emailInputError && isValidHandle && availability === 'taken' && (
            <p className="text-xs text-red-400">
              That username is taken — please choose another.
            </p>
          )}
          {!emailInputError && isValidHandle && availability === 'offline' && (
            <p className="text-xs text-amber-400">
              Availability will be confirmed when you submit — checking later.
            </p>
          )}
          {!emailInputError && isValidHandle && availability === 'error' && (
            <p className="text-xs text-amber-400">
              Could not verify availability. You can still try to reserve it.
            </p>
          )}
        </div>
      </div>

      {/* Username rules callout */}
      <div className="mb-5 rounded-xl bg-neutral-900/50 border border-neutral-800/50 px-4 py-3">
        <p className="text-xs text-neutral-500 leading-relaxed">
          <span className="text-neutral-400 font-medium">Your Vulos account username is permanent.</span>
          {' '}Choose something you are happy to share publicly — it will appear on your contact cards and peering invitations.
        </p>
      </div>

      {/* Claim error (taken suggestions etc.) */}
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
          {/* Dev-only skip (VITE_VULOS_CLOUD_SKIP=1) */}
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
            disabled={!isValidHandle || (availability !== 'available' && availability !== 'offline' && availability !== 'error') || claiming}
            className="btn-primary flex items-center gap-2 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {claiming ? (
              <>
                <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                Reserving…
              </>
            ) : (
              'Reserve username →'
            )}
          </button>
        </div>
      </div>
    </div>
  )
}
