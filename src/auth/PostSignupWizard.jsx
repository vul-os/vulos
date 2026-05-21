/**
 * CLOGIN-05 — PostSignupWizard
 *
 * Two-step wizard rendered inside the Setup shell after CLOGIN-04 signup:
 *
 *   Step 1 — 2FA setup (skippable for non-fleet_admin, default enabled)
 *     • POST /api/auth/totp/enroll → provisioning_uri + 10 recovery_codes
 *     • QR code rendered via <canvas> + qrcode library (same approach as IK06_QRCanvas)
 *     • Copy-all + Download-as-.txt for recovery codes (shown ONCE)
 *     • 6-digit TOTP confirm → POST /api/auth/totp/confirm
 *     • fleet_admin=1 → skip button hidden + warning message
 *
 *   Step 2 — Email verification nudge
 *     • Shows email address, Resend → POST /api/auth/verify-email/resend
 *     • 30s resend cooldown
 *     • User can continue without verifying; desktop will show banner
 *
 * Styled with Tailwind v4 utility classes to match Setup.jsx aesthetic.
 * No .tsx, no heavy external deps beyond `qrcode` (already in package.json).
 */

import { useState, useCallback, useEffect, useRef } from 'react'
import QRCode from 'qrcode'

/* ─── helpers ───────────────────────────────────────────────────────────── */

async function copyToClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

function downloadTxt(filename, text) {
  const blob = new Blob([text], { type: 'text/plain' })
  const url  = URL.createObjectURL(blob)
  const a    = Object.assign(document.createElement('a'), {
    href: url, download: filename,
  })
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

/* ─── CL05_QRCanvas ──────────────────────────────────────────────────────── */

function CL05_QRCanvas({ uri }) {
  const canvasRef = useRef(null)
  const [qrError, setQrError] = useState('')

  useEffect(() => {
    if (!uri || !canvasRef.current) return
    QRCode.toCanvas(canvasRef.current, uri, {
      width: 176,
      margin: 2,
      color: { dark: '#e2e8f0', light: '#0a0a0a' },
      errorCorrectionLevel: 'M',
    }).catch(err => {
      setQrError(err?.message || 'QR render failed')
    })
  }, [uri])

  if (qrError) {
    return (
      <div className="text-[11px] text-neutral-600 text-center px-2 py-3">
        (QR unavailable — copy the URI below instead)
      </div>
    )
  }

  return (
    <canvas
      ref={canvasRef}
      className="rounded-lg mx-auto block"
      style={{ imageRendering: 'pixelated' }}
    />
  )
}

/* ─── CL05_CopyBtn ───────────────────────────────────────────────────────── */

function CL05_CopyBtn({ text, label = 'Copy' }) {
  const [copied, setCopied] = useState(false)
  const handle = useCallback(async () => {
    const ok = await copyToClipboard(text)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }, [text])
  return (
    <button
      type="button"
      onClick={handle}
      className="px-3 py-1.5 rounded-lg text-xs font-medium border border-neutral-700/60 text-neutral-400 hover:text-neutral-200 hover:border-neutral-600 transition-colors"
    >
      {copied ? 'Copied!' : label}
    </button>
  )
}

/* ─── CL05_RecoveryCodes ─────────────────────────────────────────────────── */

function CL05_RecoveryCodes({ codes }) {
  const flat = codes.join('\n')
  const fileContent = [
    'Vulos Cloud — Recovery Codes',
    'Store these somewhere safe. Each code can be used once if you lose access to your authenticator.',
    '',
    ...codes,
    '',
    `Generated: ${new Date().toISOString()}`,
  ].join('\n')

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-neutral-400 leading-relaxed">
        <span className="text-amber-400 font-medium">Save these 10 recovery codes now.</span>
        {' '}They will not be shown again. Each code can be used once if you lose your authenticator.
      </p>

      <div className="bg-neutral-950 border border-neutral-800/60 rounded-xl p-4">
        <div className="grid grid-cols-2 gap-x-6 gap-y-1.5">
          {codes.map((code, i) => (
            <span
              key={i}
              className="font-mono text-sm text-neutral-200 tracking-[0.08em] py-1 border-b border-neutral-800/50"
            >
              {code}
            </span>
          ))}
        </div>
      </div>

      <div className="flex items-center gap-2 flex-wrap">
        <CL05_CopyBtn text={flat} label="Copy all codes" />
        <button
          type="button"
          onClick={() => downloadTxt('vulos-recovery-codes.txt', fileContent)}
          className="px-3 py-1.5 rounded-lg text-xs font-medium border border-neutral-700/60 text-neutral-400 hover:text-neutral-200 hover:border-neutral-600 transition-colors"
        >
          Download as .txt
        </button>
      </div>
    </div>
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
   STEP 1 — 2FA Setup
═══════════════════════════════════════════════════════════════════════════ */

function CL05_Step2FA({ isFleetAdmin, onDone, onSkip }) {
  /* enroll state: idle | enrolling | enrolled | confirming | confirmed | error */
  const [enrollStep, setEnrollStep] = useState('idle')
  const [enrollData, setEnrollData] = useState(null) // { provisioning_uri, secret, recovery_codes }
  const [confirmCode, setConfirmCode] = useState('')
  const [error, setError] = useState('')

  /* POST /api/auth/totp/enroll */
  const handleEnroll = useCallback(async () => {
    setEnrollStep('enrolling')
    setError('')
    try {
      const res = await fetch('/api/auth/totp/enroll', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setEnrollData(body)
      setEnrollStep('enrolled')
    } catch (e) {
      setError(e.message || 'Failed to start 2FA setup. Please try again.')
      setEnrollStep('error')
    }
  }, [])

  /* POST /api/auth/totp/confirm */
  const handleConfirm = useCallback(async () => {
    const code = confirmCode.replace(/\s/g, '')
    if (code.length < 6) return
    setEnrollStep('confirming')
    setError('')
    try {
      const res = await fetch('/api/auth/totp/confirm', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ code }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) throw new Error(body?.error || body?.message || `Error ${res.status}`)
      setEnrollStep('confirmed')
    } catch (e) {
      setError(e.message || 'Invalid code — check your authenticator app and try again.')
      setEnrollStep('enrolled')
    }
  }, [confirmCode])

  return (
    <div className="flex flex-col gap-5">
      {/* Step header */}
      <div>
        <div className="text-[10px] font-semibold tracking-[0.1em] uppercase text-blue-400 mb-1.5">
          Step 1 of 2
        </div>
        <h2 className="text-xl font-light text-neutral-100">Set up two-factor authentication</h2>
        <p className="text-sm text-neutral-500 mt-1 leading-relaxed">
          2FA adds a one-time code from your authenticator app (Google Authenticator, Authy, 1Password, etc.) each time you sign in.
        </p>
      </div>

      {/* ── idle ── */}
      {enrollStep === 'idle' && (
        <div className="flex flex-col gap-4">
          <div className="flex items-start gap-3 bg-blue-600/8 border border-blue-500/20 rounded-xl px-4 py-3">
            <div className="w-2 h-2 rounded-full bg-blue-400 flex-shrink-0 mt-1.5" />
            <p className="text-sm text-blue-300">Recommended — takes about 60 seconds.</p>
          </div>

          <div className="flex items-center gap-3 flex-wrap">
            <button
              type="button"
              onClick={handleEnroll}
              className="btn-primary"
            >
              Set up 2FA now →
            </button>
            {!isFleetAdmin && (
              <button
                type="button"
                onClick={onSkip}
                className="text-sm text-neutral-500 hover:text-neutral-300 transition-colors"
              >
                Skip for now
              </button>
            )}
          </div>

          {isFleetAdmin && (
            <p className="text-xs text-amber-400 leading-relaxed">
              Fleet administrators must enable 2FA. The skip option is not available for your account type.
            </p>
          )}
        </div>
      )}

      {/* ── enrolling ── */}
      {enrollStep === 'enrolling' && (
        <div className="flex items-center gap-3 text-sm text-neutral-500">
          <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-500 rounded-full animate-spin flex-shrink-0" />
          Preparing your authenticator secret…
        </div>
      )}

      {/* ── error ── */}
      {enrollStep === 'error' && (
        <div className="flex flex-col gap-4">
          <div className="flex items-start gap-3 bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-3">
            <div className="w-2 h-2 rounded-full bg-red-400 flex-shrink-0 mt-1.5" />
            <p className="text-sm text-red-300">{error}</p>
          </div>
          <button
            type="button"
            onClick={() => { setEnrollStep('idle'); setError('') }}
            className="self-start text-sm text-neutral-500 hover:text-neutral-300 transition-colors"
          >
            ← Try again
          </button>
        </div>
      )}

      {/* ── enrolled / confirming ── */}
      {(enrollStep === 'enrolled' || enrollStep === 'confirming') && enrollData && (
        <div className="flex flex-col gap-5">
          {/* QR code */}
          <div className="flex flex-col items-center gap-3">
            <div className="bg-neutral-950 border border-neutral-800/60 rounded-xl p-4">
              <CL05_QRCanvas uri={enrollData.provisioning_uri} />
            </div>
            <p className="text-xs text-neutral-600 text-center max-w-xs leading-relaxed">
              Scan with your authenticator app. Can't scan?{' '}
              <span className="text-neutral-500">Copy the URI below.</span>
            </p>
          </div>

          {/* URI */}
          <div>
            <div className="text-[10px] text-neutral-500 uppercase tracking-wider mb-2">Authenticator URI</div>
            <div className="bg-neutral-950 border border-neutral-800/60 rounded-xl px-3 py-2.5 overflow-x-auto">
              <span className="font-mono text-[11px] text-neutral-400 break-all leading-relaxed block">
                {enrollData.provisioning_uri}
              </span>
            </div>
            <div className="flex items-center gap-2 mt-2 flex-wrap">
              <CL05_CopyBtn text={enrollData.provisioning_uri} label="Copy URI" />
              {enrollData.secret && (
                <CL05_CopyBtn text={enrollData.secret} label="Copy secret" />
              )}
            </div>
          </div>

          {/* Recovery codes */}
          {enrollData.recovery_codes && enrollData.recovery_codes.length > 0 && (
            <CL05_RecoveryCodes codes={enrollData.recovery_codes} />
          )}

          {/* TOTP confirm */}
          <div>
            <label htmlFor="cl05-totp-code" className="block text-xs text-neutral-500 mb-1.5">
              Enter the 6-digit code from your authenticator to activate 2FA
            </label>
            <div className="flex items-stretch gap-3 flex-wrap">
              <input
                id="cl05-totp-code"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="000 000"
                value={confirmCode}
                onChange={e => setConfirmCode(e.target.value.replace(/[^0-9 ]/g, '').slice(0, 7))}
                disabled={enrollStep === 'confirming'}
                className="input py-2.5 font-mono text-base tracking-[0.2em] max-w-[160px] text-center"
              />
              <button
                type="button"
                onClick={handleConfirm}
                disabled={enrollStep === 'confirming' || confirmCode.replace(/\s/g, '').length < 6}
                className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2"
              >
                {enrollStep === 'confirming' ? (
                  <>
                    <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                    Verifying…
                  </>
                ) : (
                  'Confirm & activate →'
                )}
              </button>
            </div>

            {error && enrollStep !== 'confirming' && (
              <div className="flex items-start gap-2 bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-3 mt-3">
                <div className="w-2 h-2 rounded-full bg-red-400 flex-shrink-0 mt-1.5" />
                <p className="text-sm text-red-300">{error}</p>
              </div>
            )}
          </div>

          {!isFleetAdmin && (
            <button
              type="button"
              onClick={onSkip}
              className="self-start text-sm text-neutral-600 hover:text-neutral-400 transition-colors"
            >
              Skip 2FA for now
            </button>
          )}
        </div>
      )}

      {/* ── confirmed ── */}
      {enrollStep === 'confirmed' && (
        <div className="flex flex-col gap-4">
          <div className="flex items-start gap-3 bg-green-600/10 border border-green-500/25 rounded-xl px-4 py-3">
            <div className="w-2 h-2 rounded-full bg-green-400 flex-shrink-0 mt-1.5" />
            <p className="text-sm text-green-300">2FA enabled. Your account is now protected with TOTP.</p>
          </div>
          <div>
            <button type="button" onClick={onDone} className="btn-primary">
              Continue →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
   STEP 2 — Email verification nudge
═══════════════════════════════════════════════════════════════════════════ */

const CL05_RESEND_COOLDOWN = 30 // seconds

function CL05_StepEmailVerify({ email, onDone }) {
  /* resend state: idle | sending | sent | error */
  const [resendState, setResendState] = useState('idle')
  const [resendError, setResendError] = useState('')
  const [cooldown, setCooldown]       = useState(0)
  const timerRef = useRef(null)

  useEffect(() => {
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [])

  const handleResend = useCallback(async () => {
    setResendState('sending')
    setResendError('')
    try {
      const res = await fetch('/api/auth/verify-email/resend', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      })
      if (!res.ok && res.status !== 204) {
        const body = await res.json().catch(() => null)
        throw new Error(body?.error || body?.message || `Error ${res.status}`)
      }
      setResendState('sent')
      let secs = CL05_RESEND_COOLDOWN
      setCooldown(secs)
      timerRef.current = setInterval(() => {
        secs -= 1
        setCooldown(secs)
        if (secs <= 0) {
          clearInterval(timerRef.current)
          setResendState('idle')
          setCooldown(0)
        }
      }, 1000)
    } catch (e) {
      setResendError(e.message || 'Could not resend. Please try again shortly.')
      setResendState('error')
    }
  }, [])

  return (
    <div className="flex flex-col gap-5">
      {/* Step header */}
      <div>
        <div className="text-[10px] font-semibold tracking-[0.1em] uppercase text-blue-400 mb-1.5">
          Step 2 of 2
        </div>
        <h2 className="text-xl font-light text-neutral-100">Verify your email</h2>
        <p className="text-sm text-neutral-500 mt-1 leading-relaxed">
          We sent a verification link to your inbox. Verifying your email unlocks all account features.
        </p>
      </div>

      <div className="flex flex-col gap-4">
        {/* Inbox notice */}
        <div className="flex items-start gap-3 bg-neutral-900/60 border border-neutral-800/50 rounded-xl px-4 py-3">
          <div className="w-2 h-2 rounded-full bg-neutral-500 flex-shrink-0 mt-1.5" />
          <p className="text-sm text-neutral-400 leading-relaxed">
            Check your inbox for{' '}
            <span className="font-mono text-neutral-300">{email || 'your email address'}</span>.
            {' '}If you don't see it, check your spam folder.
          </p>
        </div>

        {resendError && (
          <div className="flex items-start gap-2 bg-red-900/20 border border-red-800/40 rounded-xl px-4 py-3">
            <div className="w-2 h-2 rounded-full bg-red-400 flex-shrink-0 mt-1.5" />
            <p className="text-sm text-red-300">{resendError}</p>
          </div>
        )}

        {resendState === 'sent' && (
          <div className="flex items-start gap-2 bg-green-600/10 border border-green-500/25 rounded-xl px-4 py-3">
            <div className="w-2 h-2 rounded-full bg-green-400 flex-shrink-0 mt-1.5" />
            <p className="text-sm text-green-300">Verification email resent. Check your inbox.</p>
          </div>
        )}

        {/* Resend button */}
        <button
          type="button"
          onClick={handleResend}
          disabled={resendState === 'sending' || cooldown > 0}
          className="self-start px-4 py-2 rounded-xl text-sm font-medium border border-neutral-700/60 text-neutral-400 hover:text-neutral-200 hover:border-neutral-600 transition-colors disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-2"
        >
          {resendState === 'sending' ? (
            <>
              <span className="w-3.5 h-3.5 border-2 border-neutral-600 border-t-neutral-400 rounded-full animate-spin" />
              Sending…
            </>
          ) : cooldown > 0
            ? `Resend in ${cooldown}s`
            : 'Resend verification email'}
        </button>

        <p className="text-xs text-neutral-600 leading-relaxed">
          You can continue to the desktop now — you'll see a reminder banner until your email is verified.
        </p>
      </div>

      {/* Continue */}
      <div className="pt-2 border-t border-neutral-800/30">
        <button type="button" onClick={onDone} className="btn-primary">
          Continue to desktop →
        </button>
      </div>
    </div>
  )
}

/* ═══════════════════════════════════════════════════════════════════════════
   WIZARD SHELL  (default export — mounted by Setup.jsx post-signup)
═══════════════════════════════════════════════════════════════════════════ */

/**
 * PostSignupWizard is rendered inside the Setup overlay after a successful
 * CLOGIN-04 cloud signup. Props:
 *
 *   email        {string}  — pre-filled from the signup form (CL04_createEmail)
 *   isFleetAdmin {boolean} — when true, skip-2FA is blocked
 *   onComplete   {fn}      — called when the wizard finishes (both steps done/skipped)
 */
export default function PostSignupWizard({ email, isFleetAdmin, onComplete }) {
  /* step: '2fa' | 'email' */
  const [currentStep, setCurrentStep] = useState('2fa')
  const [transitioning, setTransitioning] = useState(false)

  const goToEmail = useCallback(() => {
    setTransitioning(true)
    setTimeout(() => {
      setCurrentStep('email')
      setTransitioning(false)
    }, 200)
  }, [])

  return (
    <div className={`w-full transition-all duration-200 ${transitioning ? 'opacity-0 translate-y-4' : 'opacity-100 translate-y-0'}`}>
      {/* Progress dots */}
      <div className="flex items-center justify-center gap-2 mb-8">
        <span
          className={`rounded-full transition-all duration-300 ${
            currentStep === '2fa'
              ? 'w-6 h-2 bg-blue-500'
              : 'w-2 h-2 bg-blue-500/50'
          }`}
        />
        <span
          className={`rounded-full transition-all duration-300 ${
            currentStep === 'email'
              ? 'w-6 h-2 bg-blue-500'
              : 'w-2 h-2 bg-neutral-800'
          }`}
        />
      </div>

      {currentStep === '2fa' && (
        <CL05_Step2FA
          isFleetAdmin={Boolean(isFleetAdmin)}
          onDone={goToEmail}
          onSkip={goToEmail}
        />
      )}

      {currentStep === 'email' && (
        <CL05_StepEmailVerify
          email={email}
          onDone={onComplete}
        />
      )}
    </div>
  )
}
