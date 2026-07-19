/**
 * App.jsx — the authenticated console (phase 3a).
 *
 * Root.jsx mounts this under <RequireAuth>. App owns the console's own
 * sub-routing: it resolves the app-relative path (from the basename-aware router)
 * to a lazily-loaded console page and renders it inside the console shell
 * (console/Layout.jsx — sidebar + topbar + mobile drawer).
 *
 * Route map (app-relative; the router prepends /console):
 *   /                 → Dashboard        (overview)
 *   /account-status   → AccountStatus     (GET /api/account/status)
 *   /boxes            → Boxes             (GET /api/fleet/devices + ProvisioningSlot)
 *   /devices          → Devices           (GET /api/fleet/devices, POST /api/fleet/decommission)
 *   /enroll           → Enroll            (POST /enroll/approve|deny)
 *   /status           → Status            (GET /api/support/status)
 *   /developer        → Developer         (/api/developer/keys|webhooks|mcp-servers)
 *   /audit            → AuditLog          (GET /api/org/audit)
 *   /privacy          → PrivacyDashboard  (/api/compliance/*, /api/account/export)
 *   COMMERCIAL (seam-gated, shown in nav only when commercial.enabled):
 *   /usage            → Usage    → UsageSlot
 *   /invoices         → Invoices → InvoicesSlot
 *   /billing          → Billing  → BillingSlot
 *
 * BILLING NEVER SHIPS AS REAL CODE HERE: usage/invoices/billing render only the
 * commercial-seam slots, which are NoOp/empty in this OSS build.
 */

import { lazy, Suspense } from 'react'
import { useRouter } from './router.jsx'
import Layout from './console/Layout.jsx'

/* ─── Lazy console pages ─────────────────────────────────────────────────── */
const Dashboard        = lazy(() => import('./console/pages/Dashboard.jsx'))
const AccountStatus    = lazy(() => import('./console/pages/AccountStatus.jsx'))
const Boxes            = lazy(() => import('./console/pages/Boxes.jsx'))
const Devices          = lazy(() => import('./console/pages/Devices.jsx'))
const Enroll           = lazy(() => import('./console/pages/Enroll.jsx'))
const Status           = lazy(() => import('./console/pages/Status.jsx'))
const Developer        = lazy(() => import('./console/pages/Developer.jsx'))
const AuditLog         = lazy(() => import('./console/pages/AuditLog.jsx'))
const PrivacyDashboard = lazy(() => import('./console/pages/PrivacyDashboard.jsx'))
// Commercial seam pages (Billing default + named Usage/Invoices exports).
const Billing          = lazy(() => import('./console/pages/Billing.jsx'))
const Usage            = lazy(() => import('./console/pages/Billing.jsx').then((m) => ({ default: m.Usage })))
const Invoices         = lazy(() => import('./console/pages/Billing.jsx').then((m) => ({ default: m.Invoices })))
// Operator (admin) console pages — a distinct /admin section, gated for
// operators (each page self-gates on the admin JSON API). Mounted in the binary
// only when the operator opts the admin console in.
const AdminDashboard   = lazy(() => import('./console/admin/AdminDashboard.jsx'))
const AdminAccounts    = lazy(() => import('./console/admin/AdminAccounts.jsx'))
const AdminTeam        = lazy(() => import('./console/admin/AdminTeam.jsx'))
const AdminAudit       = lazy(() => import('./console/admin/AdminAudit.jsx'))
const AdminSecurity    = lazy(() => import('./console/admin/AdminSecurity.jsx'))
const AdminRelay       = lazy(() => import('./console/admin/AdminRelay.jsx'))

const ROUTES = {
  '/': Dashboard,
  '/account-status': AccountStatus,
  '/boxes': Boxes,
  '/devices': Devices,
  '/enroll': Enroll,
  '/status': Status,
  '/developer': Developer,
  '/audit': AuditLog,
  '/privacy': PrivacyDashboard,
  '/usage': Usage,
  '/invoices': Invoices,
  '/billing': Billing,
  // Operator console (distinct admin section).
  '/admin': AdminDashboard,
  '/admin/accounts': AdminAccounts,
  '/admin/admins': AdminTeam,
  '/admin/audit': AdminAudit,
  '/admin/security': AdminSecurity,
  '/admin/relay': AdminRelay,
}

function PageLoader() {
  return (
    <div className="vkl-page-loader" aria-live="polite" aria-label="Loading…">
      <span className="vkl-page-loader-dot" aria-hidden="true" />
      <span>Loading…</span>
    </div>
  )
}

function NotFound({ path }) {
  return (
    <div style={{ padding: 'var(--sp-6, 48px) var(--sp-4, 32px)', maxWidth: 560 }}>
      <h2 style={{ fontFamily: 'var(--font-mono)', fontSize: '1.125rem', margin: 0 }}>Page not found</h2>
      <p style={{ fontFamily: 'var(--font-mono)', fontSize: '0.85rem', color: 'var(--text-faint)', marginTop: 8 }}>
        <code>{path}</code> isn&apos;t a console page.{' '}
        <a href="/console/" data-router style={{ color: 'var(--accent)' }}>Back to the dashboard →</a>
      </p>
    </div>
  )
}

export default function App() {
  const { path } = useRouter()
  const norm = path.length > 1 ? path.replace(/\/$/, '') : path
  const Page = ROUTES[norm]
  return (
    <Layout>
      <Suspense fallback={<PageLoader />}>
        {Page ? <Page /> : <NotFound path={norm} />}
      </Suspense>
    </Layout>
  )
}
