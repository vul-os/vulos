/**
 * console/nav.js — the console sidebar navigation model.
 *
 * App-relative paths (no /console basename — the router prepends it). Icons are
 * referenced by name; Layout.jsx resolves them from its icon table. The billing
 * GROUP (Usage / Invoices / Billing) is advertised ONLY when the commercial seam
 * is enabled — a self-hoster running the OSS management binary never sees a
 * billing entry (those routes render the empty commercial slots if reached).
 *
 * Adapted from vulos-cloud's src/pages/app/Layout.jsx SIDEBAR_GROUPS, retargeted
 * onto management's own cproutes JSON APIs (fleet / account status / audit /
 * developer / support-status / compliance) and the /console basename router.
 */

import { commercial } from '@vulos/commercial-seam'

/**
 * buildNavGroups — the grouped sidebar model. Kept a function (not a constant)
 * so the commercial gating is evaluated at render time against the live seam.
 */
export function buildNavGroups() {
  const groups = [
    {
      label: 'Overview',
      items: [
        { href: '/', label: 'Dashboard', icon: 'dashboard' },
        { href: '/account-status', label: 'Account status', icon: 'status' },
      ],
    },
    {
      label: 'Operate',
      items: [
        { href: '/boxes', label: 'Boxes', icon: 'box' },
        { href: '/devices', label: 'Devices', icon: 'devices' },
        { href: '/enroll', label: 'Enroll', icon: 'enroll' },
        { href: '/status', label: 'Telemetry', icon: 'telemetry' },
      ],
    },
    {
      label: 'Build',
      items: [
        { href: '/developer', label: 'Developer', icon: 'developer' },
      ],
    },
    {
      label: 'Account',
      items: [
        { href: '/audit', label: 'Audit log', icon: 'audit' },
        { href: '/privacy', label: 'Privacy', icon: 'privacy' },
        // Commercial (billing) surfaces — only when a commercial seam is wired.
        ...(commercial.enabled
          ? [
              { href: '/usage', label: 'Usage & billing', icon: 'usage' },
              { href: '/invoices', label: 'Invoices', icon: 'invoices' },
              { href: '/billing', label: 'Billing', icon: 'billing' },
            ]
          : []),
      ],
    },
  ]
  return groups
}

/** isActive — exact match for root; prefix match for sub-routes. */
export function isActive(href, path) {
  if (href === '/') return path === '/' || path === ''
  return path === href || path.startsWith(href + '/')
}

/** findCrumbs — resolve { group, page } for the active path. */
export function findCrumbs(path) {
  for (const group of buildNavGroups()) {
    for (const item of group.items) {
      if (isActive(item.href, path)) return { group: group.label, page: item.label }
    }
  }
  return { group: 'Overview', page: 'Dashboard' }
}
