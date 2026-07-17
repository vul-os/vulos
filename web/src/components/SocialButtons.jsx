/**
 * SocialButtons — the "or continue with" convenience row for Login/Signup.
 *
 * Account model (LOCKED, 2026-07): email+password is the mandatory CENTRE of every
 * Vulos account. Social login (Sign in with Google / Microsoft / GitHub / Discord)
 * is a convenience path layered on top — never a replacement. This component is
 * deliberately
 * SECONDARY: it renders below the primary password form as a clearly-secondary row.
 *
 * CONFIG SEAM: the available providers come from the backend
 * (GET /api/auth/oauth/providers), which lists ONLY providers the deployment has
 * configured (founder-supplied client id/secret). If none are configured — or the
 * probe fails — the component renders nothing, so the auth pages degrade cleanly to
 * password + passkey with no empty chrome and no crash.
 *
 * Each button is a plain link to the backend authorize endpoint
 * (/api/auth/oauth/<id>/start), which performs the PKCE+state redirect. A social
 * sign-up is still walked through /onboarding/set-password before the account is
 * usable; a collision with an existing account is routed to /onboarding/link-account.
 */

import { useEffect, useState } from 'react'
import { toFullPath } from '../router.jsx'

const PROVIDERS_URL = '/api/auth/oauth/providers'

// A tiny glyph per known provider. Falls back to a generic key glyph for any
// provider added later (the backend can enable new providers without a frontend
// change). Exported for unit tests.
export function ProviderGlyph({ id }) {
  if (id === 'google') {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" strokeWidth="1.4" />
        <path d="M8 8h4a4 4 0 1 1-1.2-2.9" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      </svg>
    )
  }
  if (id === 'microsoft') {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
        <rect x="2.5" y="2.5" width="4.5" height="4.5" fill="currentColor" />
        <rect x="9" y="2.5" width="4.5" height="4.5" fill="currentColor" opacity="0.6" />
        <rect x="2.5" y="9" width="4.5" height="4.5" fill="currentColor" opacity="0.6" />
        <rect x="9" y="9" width="4.5" height="4.5" fill="currentColor" />
      </svg>
    )
  }
  if (id === 'github') {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" fill="currentColor">
        <path
          fillRule="evenodd"
          d="M8 0.2a8 8 0 0 0-2.5 15.6c.4.07.55-.17.55-.38v-1.34c-2.05.44-2.5-.99-2.5-.99-.34-.86-.83-1.09-.83-1.09-.68-.46.05-.45.05-.45.75.05 1.14.77 1.14.77.67 1.14 1.75.81 2.18.62.07-.48.26-.81.47-1-1.64-.19-3.36-.82-3.36-3.64 0-.8.29-1.46.76-1.98-.08-.19-.33-.94.07-1.96 0 0 .62-.2 2.02.76a7 7 0 0 1 3.68 0c1.4-.96 2.02-.76 2.02-.76.4 1.02.15 1.77.07 1.96.48.52.76 1.18.76 1.98 0 2.83-1.72 3.45-3.36 3.63.27.23.5.68.5 1.37v2.03c0 .21.15.46.55.38A8 8 0 0 0 8 0.2Z"
        />
      </svg>
    )
  }
  if (id === 'discord') {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" fill="currentColor">
        <path d="M13.2 3.1A11 11 0 0 0 10.5 2.3l-.16.33a10 10 0 0 1 2.4.77 8.6 8.6 0 0 0-6.5 0c.74-.34 1.55-.6 2.4-.77L8.9 2.3a11 11 0 0 0-2.7.8C2.9 6.1 2.35 9.1 2.6 12.05a11 11 0 0 0 3.36 1.7l.4-.66c-.44-.16-.86-.36-1.26-.6.1-.08.2-.16.3-.24a7.9 7.9 0 0 0 6.8 0l.3.24c-.4.24-.83.44-1.27.6l.4.66a11 11 0 0 0 3.37-1.7c.3-3.4-.55-6.37-2.9-8.95ZM6.3 10.2c-.65 0-1.18-.6-1.18-1.33 0-.74.52-1.34 1.18-1.34s1.2.6 1.18 1.34c0 .73-.53 1.33-1.18 1.33Zm3.4 0c-.65 0-1.18-.6-1.18-1.33 0-.74.52-1.34 1.18-1.34s1.2.6 1.18 1.34c0 .73-.52 1.33-1.18 1.33Z" />
      </svg>
    )
  }
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="6" cy="8" r="3" fill="none" stroke="currentColor" strokeWidth="1.4" />
      <path d="M8.5 8H14M12 6l2 2-2 2" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

/**
 * @param {{ returnTo?: string|null }} props
 *   returnTo — a same-origin path to resume after login (passed to the backend
 *   as ?return=; the backend validates it).
 */
export default function SocialButtons({ returnTo }) {
  const [providers, setProviders] = useState([])

  useEffect(() => {
    let live = true
    ;(async () => {
      try {
        const res = await fetch(PROVIDERS_URL, {
          method: 'GET',
          credentials: 'include',
          headers: { Accept: 'application/json' },
          cache: 'no-store',
        })
        if (!res.ok) return
        const body = await res.json().catch(() => null)
        const list = body && Array.isArray(body.providers) ? body.providers : []
        if (live) setProviders(list)
      } catch {
        // No providers configured / network blocked → render nothing. The
        // password + passkey paths are always the baseline.
      }
    })()
    return () => { live = false }
  }, [])

  if (!providers.length) return null

  // The backend consumes ?return= as a SERVER-SIDE redirect target after a
  // successful OAuth sign-in, so it must be a full pathname the browser can land
  // on — i.e. under the /console basename, not an app-relative path. Default to
  // the console home so a bare social sign-in lands in the dashboard shell.
  const app = returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//') ? returnTo : '/'
  const ret = toFullPath(app)
  const startHref = (id) =>
    `/api/auth/oauth/${encodeURIComponent(id)}/start?return=${encodeURIComponent(ret)}`

  return (
    <div className="vc-social" data-testid="social-login">
      <style>{`
        .vc-social {
          display: flex;
          flex-direction: column;
          gap: 10px;
        }
        .vc-social-or {
          display: flex;
          align-items: center;
          gap: 12px;
          margin: 2px 0;
          color: var(--text-dim, #8a8f98);
          font-family: var(--font-mono, monospace);
          font-size: 0.75rem;
          text-transform: uppercase;
          letter-spacing: 0.08em;
        }
        .vc-social-or::before,
        .vc-social-or::after {
          content: '';
          flex: 1;
          height: 1px;
          background: var(--border-strong, #2a2a2e);
        }
        .vc-social-btn {
          display: inline-flex;
          align-items: center;
          justify-content: center;
          gap: 10px;
          width: 100%;
          min-height: 46px;
          padding: 12px 18px;
          background: transparent;
          color: var(--text, #e8e8ea);
          border: 1px solid var(--border-strong, #2a2a2e);
          border-radius: var(--radius, 10px);
          font-family: var(--font-mono, monospace);
          font-size: 0.9375rem;
          font-weight: 600;
          letter-spacing: 0.01em;
          text-decoration: none;
          cursor: pointer;
          transition: border-color 160ms, background 160ms, transform 160ms;
        }
        .vc-social-btn:hover {
          border-color: var(--accent, #6366f1);
          background: color-mix(in srgb, var(--accent, #6366f1) 8%, transparent);
          transform: translateY(-1px);
        }
        .vc-social-btn:focus-visible {
          outline: none;
          box-shadow: var(--focus-ring, 0 0 0 3px rgba(99,102,241,0.5));
        }
      `}</style>
      <div className="vc-social-or" role="separator" aria-label="or continue with">
        <span>or continue with</span>
      </div>
      {providers.map((p) => (
        <a
          key={p.id}
          className="vc-social-btn"
          href={startHref(p.id)}
          data-provider={p.id}
        >
          <ProviderGlyph id={p.id} />
          <span>Continue with {p.name}</span>
        </a>
      ))}
    </div>
  )
}
