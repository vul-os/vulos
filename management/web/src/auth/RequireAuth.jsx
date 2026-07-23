/**
 * vulos-cloud — RequireAuth
 *
 * Gate for `/app/*` (and any other authed route). Three states:
 *
 *   loading  → quiet centred skeleton (no flash of "redirecting…")
 *   no user  → replaceState to `/login?return=<encoded path>` and emit
 *              a popstate so the App router renders Login without a
 *              page reload (preserves AuthProvider state)
 *   user     → render `children`
 *
 * No external router dependency — matches the existing tiny hand-rolled
 * history-API router in App.jsx.
 */

import { useEffect } from 'react'
import { useAuth } from './AuthProvider.jsx'
import { clientReplace, currentAppPath, toAppPath } from './nav.js'

/* Build the login URL (app-relative) preserving where the user was trying to go. */
function buildLoginHref() {
  const appPath = toAppPath(window.location.pathname || '/')
  // Don't bounce-loop if we're already on /login.
  if (appPath === '/login') return currentAppPath()
  return `/login?return=${encodeURIComponent(currentAppPath())}`
}

/* ─── Quiet loading skeleton ─────────────────────────────
   No spinner-noise. Just a centred breath of the accent dot, in line
   with the rest of the OSS-native UI. Honours prefers-reduced-motion
   via the body-level rule in theme.css.
──────────────────────────────────────────────────────── */
function AuthSkeleton() {
  return (
    <>
      <style>{`
        .vc-auth-skel {
          min-height: 60dvh;
          display: flex;
          align-items: center;
          justify-content: center;
          padding: var(--sp-6, 48px) var(--sp-3, 24px);
        }
        .vc-auth-skel-inner {
          display: inline-flex;
          align-items: center;
          gap: 10px;
          font-family: var(--font-mono);
          font-size: 0.8125rem;
          letter-spacing: 0.02em;
          color: var(--text-faint);
        }
        .vc-auth-skel-dot {
          width: 6px;
          height: 6px;
          border-radius: 50%;
          background: var(--accent);
          animation: vcAuthPulse 1.1s ease-in-out infinite;
        }
        @keyframes vcAuthPulse {
          0%, 100% { opacity: 1;    transform: scale(1); }
          50%      { opacity: 0.35; transform: scale(0.65); }
        }
        @media (prefers-reduced-motion: reduce) {
          .vc-auth-skel-dot { animation: none; opacity: 0.85; }
        }
      `}</style>
      <div
        className="vc-auth-skel"
        role="status"
        aria-live="polite"
        aria-busy="true"
      >
        <span className="vc-auth-skel-inner">
          <span className="vc-auth-skel-dot" aria-hidden="true" />
          Checking session…
        </span>
      </div>
    </>
  )
}

/* ─── Gate ─────────────────────────────────────────────── */
export default function RequireAuth({ children, fallback }) {
  const { user, loading } = useAuth()

  // MANDATORY-PASSWORD gate: a social sign-up that has not yet set a Vulos
  // password (GET /api/auth/me → password_setup_required) is not usable. Force it
  // to /onboarding/set-password before any authed route renders.
  const needsPassword = !!user && user.password_setup_required === true

  // Redirect AFTER paint so we don't mutate history during render.
  useEffect(() => {
    if (loading) return
    if (!user) { clientReplace(buildLoginHref()); return }
    if (needsPassword && toAppPath(window.location.pathname) !== '/onboarding/set-password') {
      clientReplace('/onboarding/set-password')
    }
  }, [loading, user, needsPassword])

  if (loading)  return fallback || <AuthSkeleton />
  if (!user)    return fallback || <AuthSkeleton />
  if (needsPassword) return fallback || <AuthSkeleton />
  return children
}
