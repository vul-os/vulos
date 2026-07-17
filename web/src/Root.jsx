/**
 * Root.jsx — top-level route split for the management console SPA.
 *
 * Two worlds hang off the hand-rolled /console router (router.jsx):
 *
 *   AUTH / ONBOARDING  (this file, ported from vulos-cloud) — a focused,
 *     distraction-free full-page surface with NO app sidebar. These routes are
 *     public (the pages self-manage any session they need); a couple are authed
 *     but still render bare (/account/social, /oauth/consent).
 *
 *   THE CONSOLE  (App.jsx) — the authed dashboard shell (sidebar + top bar).
 *     Gated by <RequireAuth>: an unauthenticated visitor is bounced to /login
 *     with a ?return= back to where they were headed. A successful sign-in lands
 *     here at "/" (the Dashboard).
 *
 * Everything is code-split (lazy) so the auth bundle and the console bundle load
 * independently.
 */

import { lazy, Suspense } from 'react'
import { useRouter } from './router.jsx'
import RequireAuth from './auth/RequireAuth.jsx'
import App from './App.jsx'

/* ─── Auth / onboarding pages (lazy) ─────────────────────────────────────── */
const Login             = lazy(() => import('./pages/Login.jsx'))
const Signup            = lazy(() => import('./pages/Signup.jsx'))
const Forgot            = lazy(() => import('./pages/Forgot.jsx'))
const Reset             = lazy(() => import('./pages/Reset.jsx'))
const Activate          = lazy(() => import('./pages/Activate.jsx'))
const TwoFactor         = lazy(() => import('./pages/TwoFactor.jsx'))
const SetPassword       = lazy(() => import('./pages/SetPassword.jsx'))
const LinkAccount       = lazy(() => import('./pages/LinkAccount.jsx'))
const OAuthEmail        = lazy(() => import('./pages/OAuthEmail.jsx'))
const PostSignupWizard  = lazy(() => import('./pages/PostSignupWizard.jsx'))
const AccountRecovery   = lazy(() => import('./pages/AccountRecovery.jsx'))
const ConnectedAccounts = lazy(() => import('./pages/ConnectedAccounts.jsx'))
const Consent           = lazy(() => import('./pages/Consent.jsx'))

/* Public bare routes — the page owns its own auth needs. */
const PUBLIC_AUTH_ROUTES = {
  '/login': Login,
  '/signup': Signup,
  '/forgot': Forgot,
  '/reset': Reset,
  '/activate': Activate,
  '/2fa': TwoFactor,
  '/onboarding/set-password': SetPassword,
  '/onboarding/link-account': LinkAccount,
  '/onboarding/oauth-email': OAuthEmail,
  '/post-signup': PostSignupWizard,
  '/account-recovery': AccountRecovery,
}

/* ─── Loading fallback ───────────────────────────────────────────────────── */
function PageLoader() {
  return (
    <div
      style={{
        minHeight: '60dvh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        color: 'var(--text-faint)',
        fontSize: '0.875rem',
        fontFamily: 'var(--font, system-ui)',
      }}
    >
      <span style={{ display: 'inline-flex', gap: '6px', alignItems: 'center' }}>
        <span
          style={{
            display: 'inline-block',
            width: '6px',
            height: '6px',
            borderRadius: '50%',
            background: 'var(--accent)',
            animation: 'vmRootPulse 1s ease infinite',
          }}
        />
        Loading…
      </span>
    </div>
  )
}

/* ─── Bare auth surface (no app chrome) ──────────────────────────────────────
   A focused, centered full-viewport <main>. The ported pages render their own
   <Section slim> card inside; this just supplies the vertical centering + the
   slim-padding trim (matches vulos.org's bare auth main). */
function AuthLayout({ children }) {
  return (
    <main
      className="vm-auth-main"
      style={{
        minHeight: '100dvh',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        boxSizing: 'border-box',
      }}
    >
      <style>{`
        @keyframes vmRootPulse {
          0%, 100% { opacity: 1; transform: scale(1); }
          50%      { opacity: 0.35; transform: scale(0.72); }
        }
        /* Trim the Section's marketing-scale vertical padding on the auth surface
           so short forms center cleanly without overflow. */
        .vm-auth-main .vk-section.slim {
          padding-top: var(--sp-4, 32px);
          padding-bottom: var(--sp-4, 32px);
        }
      `}</style>
      <Suspense fallback={<PageLoader />}>{children}</Suspense>
    </main>
  )
}

/* ─── Root ───────────────────────────────────────────────────────────────── */
export default function Root() {
  const { path } = useRouter()
  const norm = path.length > 1 ? path.replace(/\/$/, '') : path

  // Public bare auth/onboarding pages.
  const PublicPage = PUBLIC_AUTH_ROUTES[norm]
  if (PublicPage) {
    return (
      <AuthLayout>
        <PublicPage />
      </AuthLayout>
    )
  }

  // Authed-but-bare: connected accounts settings (gated), OAuth consent (self-gated).
  if (norm === '/account/social') {
    return (
      <AuthLayout>
        <RequireAuth>
          <ConnectedAccounts />
        </RequireAuth>
      </AuthLayout>
    )
  }
  if (norm === '/oauth/consent') {
    return (
      <AuthLayout>
        <Consent />
      </AuthLayout>
    )
  }

  // Everything else is the gated console shell (App owns its own sub-routing).
  return (
    <RequireAuth>
      <App />
    </RequireAuth>
  )
}
