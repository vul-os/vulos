import { useState, useEffect } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'

export default function LoginScreen() {
  const [hasUsers, setHasUsers] = useState(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [showHelp, setShowHelp] = useState(false)

  // Check if this is first-time setup (no users yet)
  useEffect(() => {
    fetch('/api/auth/status')
      .then(r => r.json())
      .then(data => {
        setHasUsers(data.has_users)
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [])

  const isSetup = hasUsers === false

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError('')

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
      const data = await res.json()
      if (res.ok) {
        window.location.reload()
      } else {
        setError(data.error || 'Login failed')
      }
    } catch {
      setError('Could not reach server')
    }
  }

  if (loading) {
    return (
      <div className="fixed inset-0 bg-neutral-950 flex items-center justify-center">
        <span className="text-neutral-600 text-sm">Loading...</span>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center px-6">
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[30%] left-[40%] w-[400px] h-[400px] rounded-full bg-blue-600 opacity-[0.03] blur-[150px]" />
        <div className="absolute bottom-[30%] right-[30%] w-[300px] h-[300px] rounded-full bg-violet-600 opacity-[0.03] blur-[150px]" />
      </div>

      {/* Logo */}
      <div className="relative mb-8 flex flex-col items-center">
        <img src="/icon-96.png" alt="Vula OS" className="w-16 h-16 mb-3" />
        <h1 className="text-3xl font-light text-neutral-200 tracking-wider">vula</h1>
        <p className="text-sm text-neutral-600 mt-1">open</p>
      </div>

      {/* Auth form */}
      <form onSubmit={handleSubmit} className="relative w-full max-w-sm space-y-4">
        <h2 className="text-lg text-neutral-300 text-center mb-2">
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
          <p className="text-sm text-red-400 text-center">{error}</p>
        )}

        <button type="submit" className="btn-primary w-full py-3">
          {isSetup ? 'Create Account' : 'Sign In'}
        </button>
      </form>

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
                ? 'Create an account to get started with Vula OS.'
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
              'Chromium runs inside Vula OS as a remote display.',
              'New tabs and popups open within the same browser window.',
              'Use the controls in the top-right corner to manage the window.',
            ]} />
          </div>
        </div>
      )}

      {/* Bottom branding */}
      <div className="absolute bottom-6 text-center">
        <p className="text-[10px] text-neutral-800">Vula OS</p>
      </div>
    </div>
  )
}

function HelpSection({ title, items }) {
  return (
    <div>
      <h3 className="text-xs font-medium text-neutral-400 mb-1.5">{title}</h3>
      <ul className="space-y-1">
        {items.map((item, i) => (
          <li key={i} className="text-[11px] text-neutral-500 leading-relaxed flex gap-2">
            <span className="text-neutral-700 mt-0.5 shrink-0">{'\u2022'}</span>
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
