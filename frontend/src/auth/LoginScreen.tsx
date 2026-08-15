import { useState, useEffect, type FormEvent } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useAuth } from './AuthProvider'
import './setup.css'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// logArg mirrors the exact `err?.message || err` shape used throughout this
// file's best-effort catch blocks: log the message when the caught value has
// a real string one, otherwise log the raw (unknown-shaped) value itself so
// the console can still expand it.
function logArg(err: unknown): unknown {
  return isRecord(err) && typeof err.message === 'string' && err.message ? err.message : err
}

export default function LoginScreen() {
  const { checkAuth, checkConcurrentSessions } = useAuth()
  const [hasUsers, setHasUsers] = useState<boolean | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [showHelp, setShowHelp] = useState(false)
  const [showPassword, setShowPassword] = useState(false)
  const [capsLock, setCapsLock] = useState(false)
  // WAVE2-RECOVERY: the phrase-based reset flow. See RecoverPanel below for why
  // this had to be built rather than merely wired up.
  const [recovering, setRecovering] = useState(false)

  // Check if this is first-time setup (no users yet)
  useEffect(() => {
    fetch('/api/auth/status')
      .then(r => r.json())
      .then((data: unknown) => {
        // Mirrors the original `data.has_users` pass-through exactly: setup
        // mode triggers only when the server explicitly says `false` — any
        // other value (missing field, malformed response) keeps the safe
        // sign-in default rather than falling into account creation.
        setHasUsers(isRecord(data) && data.has_users === false ? false : true)
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [])

  const isSetup = hasUsers === false

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (submitting) return // double-submit guard
    setError('')
    setSubmitting(true)

    const endpoint = isSetup ? '/api/auth/register' : '/api/auth/login'
    const body = isSetup
      ? { username, password, display_name: displayName || username }
      : { username, password }

    try {
      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      // Untrusted network JSON — narrowed to a record before any field
      // access, never cast to a nicer-looking response shape.
      const data: unknown = await res.json()
      if (res.ok) {
        // WAVE2-RECOVERY: unwrap the per-user master key client-side and hold it
        // in memory for the session (best-effort; fail-closed unwrap, never
        // persisted). Legacy accounts without a master key are a no-op.
        try {
          const { unlockMasterKeyForSession, getMasterKey } = await import('../lib/masterKey.js')
          // OFFLINE-AUTH-01: cache the wrapped envelope (only after the password
          // proves correct) so the OS can offer a fail-closed offline unlock when
          // the box is later unreachable. Best-effort — never blocks login.
          const cacheOfflineEnvelope = async (slot: unknown) => {
            try {
              const oa = await import('../lib/offlineAuth.js')
              await oa.cacheEnvelope(slot)
              const u = isRecord(data) && isRecord(data.user) ? data.user : null
              await oa.cacheIdentity({
                id: (u && u.id) || '',
                name: (u && (u.display_name || u.username)) || displayName || username || '',
              })
            } catch (e: unknown) {
              console.warn('[offlineAuth] envelope cache skipped:', logArg(e))
            }
          }
          await unlockMasterKeyForSession(password, fetch, cacheOfflineEnvelope)
          // WAVE-3 content-blind sharing: publish this user's PUBLIC X25519 content
          // key to their profile so peers can wrap file content to it. Idempotent
          // (same master key -> same key); best-effort, never blocks login.
          const mk = getMasterKey()
          if (mk) {
            const { publishContentPublicKey } = await import('../lib/contentSeal.js')
            await publishContentPublicKey(mk).catch((e: unknown) =>
              console.warn('[contentseal] publish content key skipped:', logArg(e)),
            )
          }
        } catch (err: unknown) {
          console.warn('[masterkey] client-side unlock skipped:', logArg(err))
        }
        // Do NOT window.location.reload() — under cage + software-GL Chromium
        // a full page reload re-spawns the GPU process and can stall the
        // compositor commit, leaving a uniform-black framebuffer with no
        // recovery (the post-create-profile black screen). Re-fetch the
        // user via AuthProvider instead — React unmounts LoginScreen and
        // mounts the Shell with no Chromium-side navigation.
        await checkAuth()
        // SESSION-TAKEOVER: only after a real sign-in (not on boot) — if the
        // account is already active elsewhere, the shell will prompt to take
        // over or keep the other session. Best-effort; never blocks entry.
        if (!isSetup) { await checkConcurrentSessions() }
      } else {
        const serverError = isRecord(data) && typeof data.error === 'string' ? data.error : undefined
        setError(serverError || (isSetup
          ? 'Could not create your account. Check your details and try again.'
          : 'Incorrect username or password.'))
      }
    } catch {
      setError('Could not reach the server. Check your connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  if (loading) {
    return (
      <div
        className="fixed inset-0 flex flex-col items-center justify-center gap-4"
        style={{ background: 'var(--bg-base)' }}
      >
        <span className="spinner w-7 h-7" aria-hidden="true" />
        <span className="text-sm" style={{ color: 'var(--text-muted)' }}>Loading…</span>
      </div>
    )
  }

  return (
    <div
      className="fixed inset-0 flex flex-col items-center justify-center px-6 py-10 overflow-y-auto safe-px safe-pt safe-pb"
      style={{ background: 'var(--bg-base)' }}
    >
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[28%] left-[38%] w-[440px] h-[440px] rounded-full opacity-[0.05] blur-[160px]" style={{ background: 'var(--accent)' }} />
        <div className="absolute bottom-[26%] right-[28%] w-[320px] h-[320px] rounded-full opacity-[0.04] blur-[160px]" style={{ background: 'color-mix(in srgb, var(--accent) 60%, #a855f7)' }} />
      </div>

      {/* Logo */}
      <div className="relative mb-8 flex flex-col items-center animate-[fadeIn_0.4s_ease-out]">
        <div className="relative mb-4">
          <div
            className="absolute inset-0 -m-4 rounded-full blur-xl opacity-60"
            style={{ background: 'radial-gradient(circle, color-mix(in srgb, var(--accent) 40%, transparent), transparent 70%)' }}
            aria-hidden="true"
          />
          <img src="/icon-96.png" alt="Vulos OS" className="relative w-16 h-16 drop-shadow-lg" />
        </div>
        <h1 className="text-3xl font-light tracking-[0.18em]" style={{ color: 'var(--text-primary)' }}>vulos</h1>
      </div>

      {/* Auth card */}
      <div className="relative w-full max-w-sm rounded-2xl glass elevate-xl p-6 sm:p-7 animate-[fadeIn_0.5s_ease-out]" style={{ border: '1px solid var(--glass-border)' }}>

      {recovering ? (
        <RecoverPanel initialUsername={username} onCancel={() => setRecovering(false)} />
      ) : (
      <form onSubmit={handleSubmit} className="space-y-4">
        <h2 className="text-lg font-medium text-center mb-2" style={{ color: 'var(--text-secondary)' }}>
          {isSetup ? 'Create your account' : 'Sign in'}
        </h2>

        {isSetup && (
          <div>
            {/* htmlFor/id throughout. Every field on this screen had a <label>
                associated with nothing, so the first screen of the product
                announced three unlabelled boxes. */}
            <label className="wz-label" htmlFor="login-display-name">Your name</label>
            <input
              id="login-display-name"
              type="text"
              value={displayName}
              onChange={e => setDisplayName(e.target.value)}
              placeholder="What should we call you?"
              autoComplete="name"
              className="input"
            />
          </div>
        )}

        <div>
          <label className="wz-label" htmlFor="login-username">Username</label>
          <input
            id="login-username"
            type="text"
            value={username}
            onChange={e => setUsername(e.target.value)}
            // Was placeholder="Username" under a label reading "Username" —
            // the placeholder restated the label and told the user nothing.
            placeholder={isSetup ? 'Pick a short name to sign in with' : ''}
            required
            autoFocus
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            className="input"
          />
        </div>

        <div>
          <label className="wz-label" htmlFor="login-password">Password</label>
          <div className="login-pw">
            <input
              id="login-password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={e => setPassword(e.target.value)}
              // Caps Lock is the single most common cause of "my password
              // stopped working", and a password field shows dots either way,
              // so there is nothing for the user to notice on their own.
              onKeyUp={e => setCapsLock(e.getModifierState?.('CapsLock') ?? false)}
              onKeyDown={e => setCapsLock(e.getModifierState?.('CapsLock') ?? false)}
              required
              autoComplete={isSetup ? 'new-password' : 'current-password'}
              className="input"
            />
            <button
              type="button"
              onClick={() => setShowPassword(v => !v)}
              aria-pressed={showPassword}
              aria-label={showPassword ? 'Hide password' : 'Show password'}
              className="login-pw-toggle"
            >
              {showPassword ? 'Hide' : 'Show'}
            </button>
          </div>
          {capsLock && (
            <p className="wz-hint wz-warn mt-1.5" role="status">Caps Lock is on.</p>
          )}
        </div>

        {error && (
          <p role="alert" className="wz-note wz-note--danger">
            <span className="wz-note-icon" aria-hidden="true">!</span>
            <span>{error}</span>
          </p>
        )}

        <button
          type="submit"
          disabled={submitting}
          className="btn-primary w-full py-3 flex items-center justify-center gap-2 disabled:opacity-60 disabled:cursor-not-allowed"
        >
          {submitting && (
            <span
              aria-hidden="true"
              className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin"
            />
          )}
          {submitting
            ? (isSetup ? 'Creating account…' : 'Signing in…')
            : (isSetup ? 'Create Account' : 'Sign In')}
        </button>

        {!isSetup && (
          <div className="text-center">
            <button type="button" onClick={() => setRecovering(true)} className="wz-linkish text-sm">
              Forgot your password?
            </button>
          </div>
        )}
      </form>
      )}
      </div>

      {/* Top-right controls */}
      <div className="absolute top-4 right-4 flex items-center gap-2 safe-pt safe-px">
        {/* Was a 32px button in text-neutral-600 — 2.2:1 against the page in
            dark, 2.3:1 in light. An icon-only control nobody could see. */}
        <button
          onClick={() => setShowHelp(!showHelp)}
          aria-expanded={showHelp}
          aria-label="Help"
          className="login-iconbtn"
        >
          <svg width="18" height="18" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" strokeWidth="1.3"/>
            <path d="M6 6.5a2 2 0 013.5 1.3c0 1.2-1.5 1.2-1.5 2.2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round"/>
            <circle cx="8" cy="12" r="0.6" fill="currentColor"/>
          </svg>
        </button>
        <ThemeToggle />
      </div>

      {/* Help panel */}
      {showHelp && (
        <div className="login-help glass elevate-xl">
          <div className="login-help-head">
            <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Help</span>
            <button onClick={() => setShowHelp(false)} className="wz-quiet text-xs">Close</button>
          </div>
          <div className="p-4 space-y-4 max-h-[70vh] overflow-y-auto">
            <HelpSection title="Getting Started" items={[
              isSetup
                ? 'Create an account to get started with Vulos OS.'
                : 'Sign in with your credentials. Ask your admin for an account.',
              'Your username and password are also used for the terminal (sudo).',
            ]} />
            <HelpSection title="Fullscreen Mode" items={[
              'Click the fullscreen button below or press F11 for the best experience.',
              'On Firefox: set about:config > browser.fullscreen.exit_on_escape to false to prevent Esc from exiting.',
              'On Chrome/Edge: fullscreen works automatically.',
            ]} />
            <HelpSection title="Keyboard Shortcuts" items={[
              'Ctrl+1-9 — Switch between desktops',
              'Ctrl+N — New desktop',
              'Ctrl+L — Lock screen',
              'F11 — Toggle fullscreen',
            ]} />
            <HelpSection title="Terminal" items={[
              'Open Terminal from the Launchpad to access the command line.',
              'Your sudo password matches your login password.',
              'Sessions persist when you close the terminal window — reattach anytime.',
            ]} />
            <HelpSection title="Browser" items={[
              'Chromium runs inside Vulos OS as a remote display.',
              'New tabs and popups open within the same browser window.',
              'Use the controls in the top-right corner to manage the window.',
            ]} />
          </div>
        </div>
      )}

      {/* The fullscreen affordance was IMPORTED and never rendered — dead since
          whenever the "bottom branding" block it sat beside was removed. It is
          genuinely useful on this screen (the OS wants to be fullscreen and
          this is the first screen anyone meets), so it is rendered rather than
          having its import deleted.

          The founder removed the "an open operating system" tagline and the
          "Vulos OS" foot line on 2026-08-15 (27d607d7). Not reinstated. */}
      <div className="relative mt-8">
        <FullscreenHint />
      </div>
    </div>
  )
}

/**
 * Password reset using the 24-word master recovery phrase.
 *
 * POST /api/auth/masterkey/recover has existed, unauthenticated (it is in the
 * backend's publicPaths — "user is locked out"), rate-limited, and with a
 * deliberately uniform failure message so it cannot be used to probe usernames.
 * NOTHING IN THE FRONTEND CALLED IT. Grepped: this was the only reference in
 * src/ to that path, and it was absent.
 *
 * So the master recovery phrase was a credential with no lock. Setup minted it,
 * MasterKeyReveal made the user attest in writing that they had stored it
 * safely, the recovery kit now ships it — and a user who actually forgot their
 * password had no way to spend it. Building this is what makes the rest of that
 * ceremony honest.
 *
 * Contract (services/auth/handlers.go): { username, mnemonic, new_password }.
 * The server's error text is shown verbatim precisely BECAUSE it is uniform;
 * substituting a friendlier message here that distinguished "no such user" from
 * "wrong phrase" would reintroduce the enumeration oracle the backend avoids.
 */
function RecoverPanel({ initialUsername, onCancel }: { initialUsername: string; onCancel: () => void }) {
  const [username, setUsername] = useState(initialUsername)
  const [phrase, setPhrase] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  const words = phrase.trim().split(/\s+/).filter(Boolean).length
  const MIN_PW = 8

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setError('')
    if (newPassword.length < MIN_PW) { setError(`Your new password must be at least ${MIN_PW} characters.`); return }
    if (newPassword !== confirm) { setError('The two new passwords do not match.'); return }
    setBusy(true)
    try {
      const res = await fetch('/api/auth/masterkey/recover', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: username.trim(),
          // Normalised, because a phrase pasted out of the recovery kit JSON
          // arrives with whatever whitespace the file had.
          mnemonic: phrase.trim().replace(/\s+/g, ' ').toLowerCase(),
          new_password: newPassword,
        }),
      })
      const raw: unknown = await res.json().catch(() => ({}))
      const data = isRecord(raw) ? raw : {}
      if (!res.ok) {
        setError(
          (typeof data.error === 'string' && data.error)
            || (res.status === 429
              ? 'Too many attempts from this device. Wait a few minutes and try again.'
              : `Recovery failed (${res.status}).`),
        )
        setBusy(false)
        return
      }
      setDone(true)
    } catch {
      setError('Could not reach this box. Check your connection and try again.')
    }
    setBusy(false)
  }

  if (done) {
    return (
      <div className="space-y-4">
        <h2 className="text-lg font-medium text-center" style={{ color: 'var(--text-secondary)' }}>
          Password changed
        </h2>
        <p className="wz-note wz-note--ok">
          <span className="wz-note-icon" aria-hidden="true">✓</span>
          <span>Your password has been reset. Sign in with the new one.</span>
        </p>
        <button onClick={onCancel} className="btn-primary w-full py-3">Back to sign in</button>
      </div>
    )
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <h2 className="text-lg font-medium text-center mb-2" style={{ color: 'var(--text-secondary)' }}>
        Reset with your recovery phrase
      </h2>
      <p className="wz-hint">
        The 24 words from the recovery kit you saved when you set this box up. This resets your
        password on the box itself — nothing is sent anywhere else.
      </p>

      <div>
        <label className="wz-label" htmlFor="rec-username">Username</label>
        <input
          id="rec-username"
          value={username}
          onChange={e => setUsername(e.target.value)}
          required
          autoFocus
          autoCapitalize="none"
          spellCheck={false}
          className="input"
        />
      </div>

      <div>
        <label className="wz-label" htmlFor="rec-phrase">Recovery phrase</label>
        <textarea
          id="rec-phrase"
          value={phrase}
          onChange={e => setPhrase(e.target.value)}
          rows={3}
          required
          autoCapitalize="none"
          spellCheck={false}
          placeholder="ridge acid lumber velvet …"
          className="input login-phrase"
        />
        {/* A live word count, because 24 words typed by hand is the step people
            get wrong, and a uniform server error cannot tell them which half
            was wrong. */}
        <p className={`wz-hint mt-1.5 ${words === 24 ? 'wz-ok' : ''}`}>
          {words === 0 ? '24 words' : `${words} of 24 words`}
        </p>
      </div>

      <div>
        <label className="wz-label" htmlFor="rec-new">New password</label>
        <input
          id="rec-new"
          type="password"
          value={newPassword}
          onChange={e => setNewPassword(e.target.value)}
          required
          autoComplete="new-password"
          className="input"
        />
        <p className="wz-hint mt-1.5">At least {MIN_PW} characters.</p>
      </div>

      <div>
        <label className="wz-label" htmlFor="rec-confirm">Confirm new password</label>
        <input
          id="rec-confirm"
          type="password"
          value={confirm}
          onChange={e => setConfirm(e.target.value)}
          required
          autoComplete="new-password"
          className="input"
        />
      </div>

      {error && (
        <p role="alert" className="wz-note wz-note--danger">
          <span className="wz-note-icon" aria-hidden="true">!</span>
          <span>{error}</span>
        </p>
      )}

      <button type="submit" disabled={busy || words !== 24} className="btn-primary w-full py-3">
        {busy ? 'Checking…' : 'Reset password'}
      </button>
      <div className="text-center">
        <button type="button" onClick={onCancel} className="wz-linkish text-sm">Back to sign in</button>
      </div>
    </form>
  )
}

function HelpSection({ title, items }: { title: string; items: string[] }) {
  return (
    <div>
      <h3 className="wz-eyebrow mb-1.5">{title}</h3>
      <ul className="space-y-1.5">
        {items.map((item, i) => (
          // The bullet was text-neutral-700: 1.6:1 dark, 1.4:1 light. It was
          // not a contrast failure the scanner would report, because a lone
          // "\u2022" in its own <span> reads as text \u2014 it simply could not be seen.
          <li key={i} className="wz-hint flex gap-2">
            <span aria-hidden="true" className="shrink-0" style={{ color: 'var(--text-tertiary)' }}>{'\u2022'}</span>
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
