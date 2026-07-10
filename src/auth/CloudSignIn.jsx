// CloudSignIn.jsx — UNIFIED-SIGNIN shared frontend flow.
//
// One Vulos account signs you in to the cloud AND to this OS. The OS backend
// (POST /api/auth/cloud/login) drives the whole broker dance (CP login → PoW →
// broker-pubkey pinning → device-bound token mint → local verification) and
// answers with either a full OS session or a structured step:
//
//   {"step":"totp_required"}                → show a TOTP input, resubmit
//   {"step":"email_verification_required"}  → friendly "verify your email"
//   {"step":"passkey_required"}             → account is passkey-first; explain
//   {"step":"enrollment_required",
//    "enroll_available":bool}               → RFC 8628 user-code screen via
//                                             /api/auth/cloud/enroll/{start,status}
//
// useCloudSignIn owns the state machine; CloudSignInExtras renders the
// step-specific UI (TOTP field / enrollment code / notices) so the Setup
// wizard and the LoginScreen embed the same behaviour under their own forms.

import { useEffect, useRef, useState } from 'react'

// ─── API helpers (exported for tests) ─────────────────────────────────────────

export async function cloudLoginRequest({ email, password, totpCode, profileName }) {
  const res = await fetch('/api/auth/cloud/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      email,
      password,
      ...(totpCode ? { totp_code: totpCode } : {}),
      ...(profileName ? { profile_name: profileName } : {}),
    }),
  })
  const data = await res.json().catch(() => ({}))
  return { status: res.status, ok: res.ok, data }
}

export async function cloudEnrollStart() {
  const res = await fetch('/api/auth/cloud/enroll/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  })
  const data = await res.json().catch(() => ({}))
  return { ok: res.ok, data }
}

export async function cloudEnrollStatus() {
  const res = await fetch('/api/auth/cloud/enroll/status')
  return res.json().catch(() => ({}))
}

// Map backend errors to human guidance.
export function cloudErrorMessage(status, data) {
  const err = data?.error || ''
  if (err === 'invalid_credentials') return 'Incorrect email or password for your Vulos Cloud account.'
  if (err === 'invalid_totp') return 'That 2FA code is not valid. Check your authenticator app and try again.'
  if (err === 'cloud_reauth_required') return 'The cloud needs a fresh sign-in — re-enter your password and try again.'
  if (status === 429) return 'Too many attempts — wait a moment and try again.'
  if (status === 502) return 'Could not reach Vulos Cloud. Check your network connection and try again.'
  if (status === 503) return err || 'Cloud sign-in is not available on this device right now.'
  return err || 'Cloud sign-in failed. Please try again.'
}

// ─── State machine hook ───────────────────────────────────────────────────────

// Phases: 'form' | 'totp' | 'verify-email' | 'passkey' | 'enroll' | 'done'
export function useCloudSignIn({ onSuccess }) {
  const [phase, setPhase] = useState('form')
  const [totp, setTotp] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [enrollInfo, setEnrollInfo] = useState(null) // {user_code, verification_uri}
  const pollRef = useRef(null)
  const credsRef = useRef(null) // retained for auto-retry after enrollment

  // Stop polling on unmount.
  useEffect(() => () => { if (pollRef.current) clearInterval(pollRef.current) }, [])

  const stopPolling = () => {
    if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
  }

  const reset = () => {
    stopPolling()
    setPhase('form'); setTotp(''); setError(''); setEnrollInfo(null); setSubmitting(false)
  }

  // Begin device enrollment and poll until the owner approves, then retry the
  // sign-in automatically with the retained credentials.
  const beginEnrollment = async () => {
    const { ok, data } = await cloudEnrollStart()
    if (!ok || !data.user_code) {
      setError(data?.error || 'Could not start device enrollment.')
      setPhase('form')
      return
    }
    setEnrollInfo({ user_code: data.user_code, verification_uri: data.verification_uri })
    setPhase('enroll')
    stopPolling()
    pollRef.current = setInterval(async () => {
      const st = await cloudEnrollStatus()
      if (st.state === 'approved') {
        stopPolling()
        setEnrollInfo(null)
        // Device is enrolled — retry the sign-in transparently.
        await submit(credsRef.current)
      } else if (st.state === 'error') {
        stopPolling()
        setError(st.error || 'Device enrollment failed.')
        setPhase('form')
      }
    }, 3000)
  }

  // submit drives one sign-in attempt. creds: {email, password, profileName}.
  const submit = async (creds) => {
    if (!creds?.email || !creds?.password) {
      setError('Enter your Vulos Cloud email and password.')
      return
    }
    credsRef.current = creds
    setError('')
    setSubmitting(true)
    try {
      const { status, ok, data } = await cloudLoginRequest({
        email: creds.email.trim(),
        password: creds.password,
        totpCode: totp.trim() || undefined,
        profileName: creds.profileName,
      })

      if (ok && data.session_token) {
        setPhase('done')
        stopPolling()
        await onSuccess?.(data)
        return
      }
      if (ok && data.step) {
        switch (data.step) {
          case 'totp_required':
            setPhase('totp')
            return
          case 'email_verification_required':
            setPhase('verify-email')
            return
          case 'passkey_required':
            setPhase('passkey')
            return
          case 'enrollment_required':
            if (data.enroll_available === false) {
              setError('This device cannot enroll with Vulos Cloud right now (device keystore unavailable).')
              return
            }
            await beginEnrollment()
            return
          default:
            setError(`Unexpected sign-in step: ${data.step}`)
            return
        }
      }
      setError(cloudErrorMessage(status, data))
      if (data?.error === 'invalid_totp') setPhase('totp')
    } catch {
      setError('Could not reach the server. Check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return { phase, totp, setTotp, error, setError, submitting, enrollInfo, submit, reset }
}

// ─── Step-specific UI ─────────────────────────────────────────────────────────

// CloudSignInExtras renders the step UI for a useCloudSignIn flow. Hosts place
// it below their email/password inputs.
export function CloudSignInFlowExtras({ flow }) {
  if (flow.phase === 'totp') {
    return (
      <div className="animate-[fadeIn_0.15s_ease-out]">
        <label className="block text-xs text-neutral-500 mb-1.5">Two-factor code</label>
        <input
          type="text"
          inputMode="numeric"
          autoComplete="one-time-code"
          value={flow.totp}
          onChange={e => { flow.setTotp(e.target.value.replace(/\s/g, '')); flow.setError('') }}
          placeholder="123456"
          autoFocus
          maxLength={10}
          className="input text-base py-3 font-mono tracking-[0.2em]"
          aria-label="Two-factor authentication code"
        />
        <p className="text-[11px] text-neutral-600 mt-1">
          Enter the 6-digit code from your authenticator app (or a recovery code).
        </p>
      </div>
    )
  }

  if (flow.phase === 'verify-email') {
    return (
      <div className="flex items-start gap-2 bg-warning-soft border border-warning-soft rounded-xl px-4 py-3 animate-[fadeIn_0.15s_ease-out]" role="alert">
        <p className="text-sm text-warning">
          Your Vulos Cloud email address has not been verified yet. Open the verification
          link that was sent to your inbox, then sign in again.
        </p>
      </div>
    )
  }

  if (flow.phase === 'passkey') {
    return (
      <div className="flex items-start gap-2 bg-warning-soft border border-warning-soft rounded-xl px-4 py-3 animate-[fadeIn_0.15s_ease-out]" role="alert">
        <p className="text-sm text-warning">
          This account signs in with a passkey, which cannot be used from this screen yet.
          Sign in at your Vulos Cloud console and set a password-based 2FA method, then try again.
        </p>
      </div>
    )
  }

  if (flow.phase === 'enroll' && flow.enrollInfo) {
    return (
      <div className="bg-neutral-900/60 border border-violet-500/30 rounded-xl px-5 py-4 animate-[fadeIn_0.15s_ease-out]" data-testid="enroll-panel">
        <p className="text-sm text-neutral-300 mb-2 font-medium">Approve this device</p>
        <p className="text-xs text-neutral-500 leading-relaxed mb-3">
          This device is not linked to your account yet. Open{' '}
          <span className="text-violet-300 font-mono break-all">{flow.enrollInfo.verification_uri}</span>{' '}
          on another device, sign in, and enter this code:
        </p>
        <div className="text-center text-2xl font-mono tracking-[0.25em] text-violet-300 bg-neutral-950/60 border border-neutral-800/60 rounded-lg py-3 select-all">
          {flow.enrollInfo.user_code}
        </div>
        <p className="text-[11px] text-neutral-600 mt-3 flex items-center gap-2">
          <span className="w-3 h-3 border-2 border-violet-400/30 border-t-violet-400 rounded-full animate-spin" aria-hidden="true" />
          Waiting for approval — you’ll be signed in automatically…
        </p>
      </div>
    )
  }

  return null
}
