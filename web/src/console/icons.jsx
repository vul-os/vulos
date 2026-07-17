/**
 * console/icons.jsx — crisp 16×16 line icons (currentColor) for the console nav.
 * Ported from vulos-cloud's Layout.jsx icon set. Resolved by name via ICONS.
 */

function base(children) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor"
      strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {children}
    </svg>
  )
}

export const ICONS = {
  dashboard: () => base(<>
    <rect x="1.5" y="1.5" width="5" height="5" rx="1" />
    <rect x="9.5" y="1.5" width="5" height="5" rx="1" />
    <rect x="1.5" y="9.5" width="5" height="5" rx="1" />
    <rect x="9.5" y="9.5" width="5" height="5" rx="1" />
  </>),
  status: () => base(<>
    <circle cx="8" cy="8" r="6.5" />
    <path d="M5 8h2l1.5-3L10 11l1.5-3H13" />
  </>),
  box: () => base(<>
    <rect x="1.5" y="2.5" width="13" height="8.5" rx="1.5" />
    <path d="M5 14h6M8 11v3" />
    <circle cx="4" cy="5" r="0.5" fill="currentColor" />
    <path d="M6 5h6" />
  </>),
  devices: () => base(<>
    <rect x="1" y="3" width="10" height="8" rx="1.5" />
    <path d="M11 7h3a1 1 0 0 1 1 1v4a1 1 0 0 1-1 1H9" />
    <path d="M4 11v2M6 13H2" />
  </>),
  enroll: () => base(<>
    <circle cx="8" cy="8" r="6.5" />
    <path d="M8 5v6M5 8h6" />
  </>),
  telemetry: () => base(<>
    <path d="M2 13.5h12" />
    <path d="M2 10l3-3 3 2 5-5" />
  </>),
  developer: () => base(<>
    <path d="M5.5 4.5L2 8l3.5 3.5" />
    <path d="M10.5 4.5L14 8l-3.5 3.5" />
    <path d="M8.5 2.5l-1 11" />
  </>),
  audit: () => base(<>
    <path d="M3 2.5h7l3 3V13a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1Z" />
    <path d="M5.5 7.5l1.5 1.5 3-3" />
    <path d="M5.5 11h5" />
  </>),
  privacy: () => base(<>
    <path d="M8 1.5L2 4v4.5c0 3 2.5 5.5 6 6.5 3.5-1 6-3.5 6-6.5V4L8 1.5Z" />
    <path d="M5.5 8l2 2 3-3" />
  </>),
  usage: () => base(<>
    <path d="M2 13.5h12" />
    <rect x="3" y="8.5" width="2.4" height="4" rx="0.6" />
    <rect x="6.8" y="5.5" width="2.4" height="7" rx="0.6" />
    <rect x="10.6" y="2.5" width="2.4" height="10" rx="0.6" />
  </>),
  invoices: () => base(<>
    <path d="M4 1.5h6l2.5 2.5V14l-1.7-1-1.6 1-1.6-1-1.6 1L4 14V1.5Z" />
    <path d="M6 5.5h4M6 8h4M6 10.5h2.5" />
  </>),
  billing: () => base(<>
    <rect x="1.5" y="3.5" width="13" height="9" rx="1.5" />
    <path d="M1.5 6.5h13" />
    <path d="M4 10h3" />
  </>),
}

export function Icon({ name }) {
  const C = ICONS[name] || ICONS.dashboard
  return <C />
}
