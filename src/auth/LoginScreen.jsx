import { useState, useEffect } from 'react'
import FullscreenHint from './FullscreenHint'
import ThemeToggle from '../core/ThemeToggle'

export default function LoginScreen() {
  const [authStatus, setAuthStatus] = useState(null)
  const [mode, setMode] = useState('login') // 'login' or 'register'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/api/auth/status')
      .then(r => r.json())
      .then(data => {
        setAuthStatus(data)
        // No users yet — show register form
        if (!data.has_users) setMode('register')
        setLoading(false)
      })
      .catch(() => setLoading(false))
  }, [])

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError('')

    const endpoint = mode === 'register' ? '/api/auth/register' : '/api/auth/login'
    const body = mode === 'register'
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
        window.location.reload() // reload to pick up the session cookie
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
          {mode === 'register' ? 'Create your account' : 'Sign in'}
        </h2>

        {mode === 'register' && (
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
          {mode === 'register' ? 'Create Account' : 'Sign In'}
        </button>

        {authStatus?.has_users && mode === 'register' && (
          <button type="button" onClick={() => { setMode('login'); setError('') }} className="w-full text-sm text-neutral-600 hover:text-neutral-400 text-center">
            Already have an account? Sign in
          </button>
        )}

        {mode === 'login' && (
          <button type="button" onClick={() => { setMode('register'); setError('') }} className="w-full text-sm text-neutral-600 hover:text-neutral-400 text-center">
            Create new account
          </button>
        )}
      </form>

      <div className="absolute top-4 right-4">
        <ThemeToggle />
      </div>

      <div className="absolute bottom-8 text-center space-y-2">
        <FullscreenHint />
        <p className="text-[10px] text-neutral-800">Vula OS</p>
      </div>
    </div>
  )
}
