// LOGINISO-01: Passkey register + login component.
//
// PasskeyLoginButton — shown on the login form; calls navigator.credentials.get
// after fetching the challenge from POST /api/auth/passkey/login/begin.
//
// PasskeyRegisterButton — shown in Settings / Security after the user is
// already logged in; calls navigator.credentials.create after fetching the
// challenge from POST /api/auth/passkey/register/begin.

import { useState } from 'react'

// ─── PasskeyLoginButton ───────────────────────────────────────────────────────

// Props:
//   username   — the username typed by the user in the parent form.
//   onSuccess  — callback called after a successful login (same as onSuccess in LocalForm).
//   onError    — callback(msg) on failure.
export function PasskeyLoginButton({ username, onSuccess, onError }) {
  const [loading, setLoading] = useState(false)

  const handleClick = async () => {
    if (!username) {
      onError?.('Enter your username first, then click Sign in with Passkey.')
      return
    }
    if (!window.PublicKeyCredential) {
      onError?.('This browser does not support passkeys.')
      return
    }

    setLoading(true)
    try {
      // 1. Begin assertion — get challenge from server.
      const beginRes = await fetch('/api/auth/passkey/login/begin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username }),
      })
      if (!beginRes.ok) {
        const d = await beginRes.json().catch(() => ({}))
        throw new Error(d.error || 'Could not start passkey login')
      }
      const { challenge: credReqOpts, session_data } = await beginRes.json()

      // 2. Call the WebAuthn browser API.
      const options = credReqOpts.publicKey
      options.challenge = base64urlDecode(options.challenge)
      if (options.allowCredentials) {
        options.allowCredentials = options.allowCredentials.map(c => ({
          ...c,
          id: base64urlDecode(c.id),
        }))
      }

      const assertion = await navigator.credentials.get({ publicKey: options })
      if (!assertion) throw new Error('Passkey prompt cancelled')

      // 3. Finish assertion — verify on server and get session.
      const assertionJSON = credentialToJSON(assertion)
      const finishRes = await fetch('/api/auth/passkey/login/finish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username,
          session_data,
          assertion_response: assertionJSON,
        }),
      })
      if (!finishRes.ok) {
        const d = await finishRes.json().catch(() => ({}))
        throw new Error(d.error || 'Passkey verification failed')
      }

      // Session cookie is set by the backend — tell the app to re-check auth.
      await onSuccess?.()
    } catch (err) {
      if (err.name === 'NotAllowedError') {
        onError?.('Passkey prompt was cancelled or timed out.')
      } else {
        onError?.(err.message || 'Passkey login failed')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      disabled={loading}
      className="w-full flex items-center justify-center gap-2 py-3 rounded-xl
        border border-neutral-700/60 bg-neutral-900/60 hover:bg-neutral-800/70
        text-sm text-neutral-300 hover:text-neutral-100
        transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
    >
      {loading
        ? <span className="w-4 h-4 border-2 border-neutral-500 border-t-neutral-200 rounded-full animate-spin" />
        : <PasskeyIcon />
      }
      {loading ? 'Checking passkey…' : 'Sign in with Passkey'}
    </button>
  )
}

// ─── PasskeyRegisterButton ────────────────────────────────────────────────────

// Props:
//   displayName — hint for the passkey authenticator UI.
//   onSuccess   — callback(credentialId) after successful registration.
//   onError     — callback(msg) on failure.
export function PasskeyRegisterButton({ displayName, onSuccess, onError }) {
  const [loading, setLoading] = useState(false)

  const handleClick = async () => {
    if (!window.PublicKeyCredential) {
      onError?.('This browser does not support passkeys.')
      return
    }

    setLoading(true)
    try {
      // 1. Begin registration.
      const beginRes = await fetch('/api/auth/passkey/register/begin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ display_name: displayName }),
      })
      if (!beginRes.ok) {
        const d = await beginRes.json().catch(() => ({}))
        throw new Error(d.error || 'Could not start passkey registration')
      }
      const { challenge: credCreateOpts, session_data } = await beginRes.json()

      // 2. Call the WebAuthn browser API.
      const options = credCreateOpts.publicKey
      options.challenge = base64urlDecode(options.challenge)
      options.user.id = base64urlDecode(options.user.id)
      if (options.excludeCredentials) {
        options.excludeCredentials = options.excludeCredentials.map(c => ({
          ...c,
          id: base64urlDecode(c.id),
        }))
      }

      const attestation = await navigator.credentials.create({ publicKey: options })
      if (!attestation) throw new Error('Passkey prompt cancelled')

      // 3. Finish registration.
      const attestationJSON = credentialToJSON(attestation)
      const finishRes = await fetch('/api/auth/passkey/register/finish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          session_data,
          attestation_response: attestationJSON,
        }),
      })
      if (!finishRes.ok) {
        const d = await finishRes.json().catch(() => ({}))
        throw new Error(d.error || 'Passkey registration failed')
      }
      const { credential_id } = await finishRes.json()
      onSuccess?.(credential_id)
    } catch (err) {
      if (err.name === 'NotAllowedError') {
        onError?.('Passkey prompt was cancelled.')
      } else {
        onError?.(err.message || 'Passkey registration failed')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      disabled={loading}
      className="flex items-center gap-2 px-4 py-2 rounded-lg
        border border-neutral-700/60 bg-neutral-900/60 hover:bg-neutral-800/70
        text-sm text-neutral-300 hover:text-neutral-100
        transition-colors disabled:opacity-50"
    >
      {loading
        ? <span className="w-4 h-4 border-2 border-neutral-500 border-t-neutral-200 rounded-full animate-spin" />
        : <PasskeyIcon />
      }
      {loading ? 'Registering…' : 'Add Passkey'}
    </button>
  )
}

// ─── WebAuthn browser API helpers ─────────────────────────────────────────────

function base64urlDecode(str) {
  // Add padding if needed.
  const padded = str + '==='.slice((str.length + 3) % 4)
  const binary = atob(padded.replace(/-/g, '+').replace(/_/g, '/'))
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

function base64urlEncode(buffer) {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.byteLength; i++) binary += String.fromCharCode(bytes[i])
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '')
}

// credentialToJSON converts a PublicKeyCredential to a plain JSON-serialisable
// object that the Go backend (go-webauthn) expects.
function credentialToJSON(cred) {
  const obj = {
    id: cred.id,
    rawId: base64urlEncode(cred.rawId),
    type: cred.type,
    response: {},
  }

  // AttestationResponse (create)
  if (cred.response.attestationObject) {
    obj.response.clientDataJSON = base64urlEncode(cred.response.clientDataJSON)
    obj.response.attestationObject = base64urlEncode(cred.response.attestationObject)
  }

  // AssertionResponse (get)
  if (cred.response.authenticatorData) {
    obj.response.clientDataJSON = base64urlEncode(cred.response.clientDataJSON)
    obj.response.authenticatorData = base64urlEncode(cred.response.authenticatorData)
    obj.response.signature = base64urlEncode(cred.response.signature)
    obj.response.userHandle = cred.response.userHandle
      ? base64urlEncode(cred.response.userHandle)
      : ''
  }

  return obj
}

// ─── Icon ─────────────────────────────────────────────────────────────────────

function PasskeyIcon() {
  return (
    <svg viewBox="0 0 20 20" fill="none" className="w-4 h-4 shrink-0" aria-hidden="true">
      <circle cx="8" cy="8" r="4.5" stroke="currentColor" strokeWidth="1.4" />
      <path d="M11 11l7 7" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      <path d="M15 16l1.5-1.5M17.5 13.5L16 15" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  )
}
