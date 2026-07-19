/**
 * web/src/seam/commercial.jsx — THE COMMERCIAL SEAM (OSS / NoOp implementation)
 * ═══════════════════════════════════════════════════════════════════════════
 *
 * This is the JS analog of the Go `pkg/billingport` / `pkg/storageport` seams
 * (and the `replace` directive that swaps them): the ONE module the management
 * SPA imports for every piece of commercial UI. In the open-source management
 * build this file is what `@vulos/commercial-seam` resolves to, and EVERY slot
 * renders nothing — a self-hoster who runs the management binary never sees a
 * Pay button, a price, an invoice, or a "provision managed box" flow.
 *
 * The commercial (vulos-cloud) build overrides ONE Vite alias
 * (`@vulos/commercial-seam` → its own impl; see web/vite.config.js + web/SEAM.md)
 * to inject the real, closed-source billing/usage/invoices/pricing/provisioning
 * UI. The app code (App.jsx, pages/*) imports ONLY from '@vulos/commercial-seam'
 * and is byte-for-byte identical in both builds — nothing about money lives in
 * this repo.
 *
 * ─── THE CONTRACT (must be honoured by any replacement) ──────────────────────
 * This module MUST export, at minimum:
 *   • `commercial`       — a flag object; `{ enabled: boolean, ...meta }`.
 *   • `BillingSlot`      — payment method / subscription management UI.
 *   • `InvoicesSlot`     — invoice history + downloads.
 *   • `UsageSlot`        — metered-usage meters / current-period consumption.
 *   • `PricingSlot`      — plan/tier selection + upgrade CTAs.
 *   • `ProvisioningSlot` — managed-box / managed-bucket provisioning flow.
 *
 * Every slot is a React component. It receives a common prop bag (below) and may
 * ignore any of it. In OSS each returns `null` (renders nothing). A replacement
 * MAY render real UI, and MAY be async/suspenseful.
 *
 * Common props passed to every slot (all optional — a slot may ignore them):
 *   • `accountId?: string`     — the signed-in account id, when known.
 *   • `orgId?: string`         — the active org/tenant id, when known.
 *   • `context?: object`       — free-form page context (route, entity ids…).
 *   • `onChange?: () => void`  — invoke after a state-mutating action so the
 *                                host page can refresh (NoOp never calls it).
 *   • `children?: ReactNode`   — a slot MAY wrap host-provided fallback content.
 *
 * DO NOT add app logic here. Keep this a pure seam: flag + NoOp components.
 * ═══════════════════════════════════════════════════════════════════════════
 */

/**
 * Commercial capability flag. `enabled: false` in the OSS build — the app uses
 * this to hide commercial affordances entirely (a self-hoster sees an
 * operational console, never a storefront). A replacement sets `enabled: true`.
 *
 * @type {{ enabled: boolean, provider?: string }}
 */
export const commercial = Object.freeze({
  enabled: false,
  provider: 'none', // OSS: no commercial provider wired.
})

/**
 * NoOp slot factory. Returns a component that renders nothing regardless of
 * props, but is a real, named React component so it composes and tree-shakes
 * cleanly and shows a useful name in React DevTools.
 *
 * @param {string} name display name for DevTools
 */
function noopSlot(name) {
  function NoOpSlot(_props) {
    return null
  }
  NoOpSlot.displayName = name
  return NoOpSlot
}

/** Payment method / subscription management. OSS: renders nothing. */
export const BillingSlot = noopSlot('BillingSlot(NoOp)')

/** Invoice history + downloads. OSS: renders nothing. */
export const InvoicesSlot = noopSlot('InvoicesSlot(NoOp)')

/** Metered usage meters / current-period consumption. OSS: renders nothing. */
export const UsageSlot = noopSlot('UsageSlot(NoOp)')

/** Plan/tier selection + upgrade CTAs. OSS: renders nothing. */
export const PricingSlot = noopSlot('PricingSlot(NoOp)')

/** Managed-box / managed-bucket provisioning flow. OSS: renders nothing. */
export const ProvisioningSlot = noopSlot('ProvisioningSlot(NoOp)')

// Default export mirrors the named exports so a replacement can be consumed
// either way (`import commercial from …` or `import { commercial } from …`).
export default {
  commercial,
  BillingSlot,
  InvoicesSlot,
  UsageSlot,
  PricingSlot,
  ProvisioningSlot,
}
