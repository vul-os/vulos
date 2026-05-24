import { useState } from 'react'
import { Lock, Eye, EyeOff, AlertCircle } from 'lucide-react'
import { useAuthStore } from '../store/authStore'

/*
 * LoginScreen — first contact. Goes for warm, hand-set typography instead of
 * the previous "indigo grid" tech-startup look.  The serif title sets the
 * editorial tone immediately; one accent button completes the path.
 */
export default function LoginScreen() {
  const { login, error, remainingAttempts } = useAuthStore()
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!password || loading) return
    setLoading(true)
    try { await login(password) } catch { /* error in store */ }
    finally { setLoading(false); setPassword('') }
  }

  return (
    <div className="h-screen flex items-center justify-center bg-bg paper-grain relative overflow-hidden">
      {/* A single, off-centre warm circle — gives the page a centre of gravity
          without resorting to gradients or grids. */}
      <div
        aria-hidden
        className="absolute -top-32 -right-32 w-[420px] h-[420px] rounded-full opacity-40 pointer-events-none"
        style={{ background: 'radial-gradient(closest-side, var(--accent-tint-2), transparent 70%)' }}
      />
      <div className="relative w-full max-w-sm mx-4 animate-rise-in">
        <div className="bg-paper rounded-xl border border-line shadow-e2 p-8">
          <div className="flex flex-col items-start mb-7">
            <span className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-2">
              Vulos Office
            </span>
            <h1 className="font-serif text-3xl text-ink leading-tight">
              Welcome back.
            </h1>
            <p className="text-sm text-ink-muted mt-2 leading-snug">
              Enter your password to continue.
            </p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-xs font-medium text-ink-muted mb-1.5 tracking-tightish">
                Password
              </label>
              <div className="relative">
                <Lock size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-ink-faint" />
                <input
                  type={show ? 'text' : 'password'}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full pl-9 pr-9 h-10 bg-paper border border-line rounded-md text-sm outline-none focus:border-accent focus:shadow-focus transition-colors"
                  placeholder="Enter password"
                  autoFocus
                />
                <button
                  type="button"
                  onClick={() => setShow(!show)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-ink-faint hover:text-ink transition-colors"
                  aria-label={show ? 'Hide password' : 'Show password'}
                >
                  {show ? <EyeOff size={14} /> : <Eye size={14} />}
                </button>
              </div>
            </div>

            {error && (
              <div className="flex items-start gap-2 p-3 bg-danger-bg border border-line rounded-md text-xs text-danger animate-fade-in">
                <AlertCircle size={14} className="flex-shrink-0 mt-0.5" />
                <div>
                  <p className="leading-snug">{error}</p>
                  {remainingAttempts !== null && remainingAttempts > 0 && (
                    <p className="mt-0.5 opacity-80">
                      {remainingAttempts} attempt{remainingAttempts !== 1 ? 's' : ''} remaining
                    </p>
                  )}
                </div>
              </div>
            )}

            <button
              type="submit"
              disabled={!password || loading}
              className="w-full h-10 bg-accent text-white rounded-md text-sm font-semibold hover:bg-accent-hover active:bg-accent-press disabled:opacity-50 disabled:cursor-not-allowed transition-colors shadow-e1 tracking-tightish"
            >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <span className="w-3.5 h-3.5 border-2 border-white border-t-transparent rounded-full animate-spin" />
                  Signing in
                </span>
              ) : 'Sign in'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
