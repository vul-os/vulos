import { useState, useEffect, type FormEvent } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'
import { useAuth } from './AuthProvider'

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
        <h1 className="text-3xl font-light tracking-[0.18em]" style={{ color: 'var(--text-primary)' }}>vula</h1>
        <p className="text-sm mt-1 tracking-wide" style={{ color: 'var(--text-faint)' }}>open</p>
      </div>

      {/* Auth card */}
      <div className="relative w-full max-w-sm rounded-2xl glass elevate-xl p-6 sm:p-7 animate-[fadeIn_0.5s_ease-out]" style={{ border: '1px solid var(--glass-border)' }}>

      <form onSubmit={handleSubmit} className="space-y-4">
        <h2 className="text-lg font-medium text-center mb-2" style={{ color: 'var(--text-secondary)' }}>
          {isSetup ? 'Create your account' : 'Sign in'}
        </h2>

        {isSetup && (
          <div>
            <label className="block text-xs text-neutral-500 mb-1">Display Name</label>
            <input
              type="text"
              value={displayName}
              onChange={e => setDisplayName(e.target.value)}
              placeholder="Your name"
              className="input"
            />
          </div>
        )}

        <div>
          <label className="block text-xs text-neutral-500 mb-1">Username</label>
          <input
            type="text"
            value={username}
            onChange={e => setUsername(e.target.value)}
            placeholder="Username"
            required
            autoFocus
            className="input"
          />
        </div>

        <div>
          <label className="block text-xs text-neutral-500 mb-1">Password</label>
          <input
            type="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            placeholder="Password"
            required
            className="input"
          />
        </div>

        {error && (
          <p role="alert" className="text-sm text-center" style={{ color: 'var(--status-danger)' }}>{error}</p>
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
      </form>
      </div>

      {/* Top-right controls */}
      <div className="absolute top-4 right-4 flex items-center gap-2">
        <button
          onClick={() => setShowHelp(!showHelp)}
          className="w-8 h-8 flex items-center justify-center rounded-lg text-neutral-600 hover:text-neutral-300 hover:bg-neutral-800/60 transition-colors"
          title="Help"
        >
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" strokeWidth="1.2"/>
            <path d="M6 6.5a2 2 0 013.5 1.3c0 1.2-1.5 1.2-1.5 2.2" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"/>
            <circle cx="8" cy="12" r="0.5" fill="currentColor"/>
          </svg>
        </button>
        <ThemeToggle />
      </div>

      {/* Help panel */}
      {showHelp && (
        <div className="absolute top-14 right-4 w-80 bg-neutral-900/95 backdrop-blur-xl border border-neutral-700/50 rounded-xl shadow-2xl shadow-black/50 overflow-hidden animate-[fadeIn_0.12s_ease-out] z-50">
          <div className="px-4 pt-3 pb-2 border-b border-neutral-800/60">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium text-neutral-200">Help</span>
              <button onClick={() => setShowHelp(false)} className="text-neutral-600 hover:text-neutral-300 text-xs">Close</button>
            </div>
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

      {/* Bottom branding */}
      <div className="absolute bottom-6 text-center">
        <p className="text-[12px] text-neutral-800">Vulos OS</p>
      </div>
    </div>
  )
}

function HelpSection({ title, items }: { title: string; items: string[] }) {
  return (
    <div>
      <h3 className="text-xs font-medium text-neutral-400 mb-1.5">{title}</h3>
      <ul className="space-y-1">
        {items.map((item, i) => (
          <li key={i} className="text-[12px] text-neutral-500 leading-relaxed flex gap-2">
            <span className="text-neutral-700 mt-0.5 shrink-0">{'\u2022'}</span>
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
