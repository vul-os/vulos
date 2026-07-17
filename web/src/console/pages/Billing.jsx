/**
 * console/pages/Billing.jsx, Usage.jsx, Invoices.jsx — commercial seam surfaces.
 *
 * These three routes are COMMERCIAL: billing, metering and invoicing are not part
 * of the OSS control plane. They render ONLY through the commercial seam slots
 * (BillingSlot / UsageSlot / InvoicesSlot), which return null in the OSS
 * management build — so a self-hoster sees an honest "managed by your provider"
 * placeholder, never a storefront. The cloud build's seam replacement injects the
 * real UI. The pages never contain billing logic themselves.
 *
 * This module exports all three (Billing default, plus Usage/Invoices) so the
 * three tiny seam pages live in one place.
 */

import { Section } from '../../ui/index.jsx'
import { BillingSlot, UsageSlot, InvoicesSlot, commercial } from '@vulos/commercial-seam'

const STYLES = `
  .cm-header { margin-bottom: var(--sp-4); }
  .cm-title { font-family: var(--font-mono); font-size: clamp(1.125rem, 2.2vw, 1.375rem); font-weight: 700; letter-spacing: -0.025em; margin: 0; }
  .cm-sub { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); margin-top: var(--sp-0-5); }
  .cm-managed { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-faint); line-height: 1.7; padding: var(--sp-4); border: 1px dashed var(--border-strong); border-radius: var(--radius-lg); background: var(--bg-surface); max-width: 60ch; }
`

/** Shared frame: a heading + the seam slot, with an OSS-managed fallback. */
function SeamPage({ title, sub, Slot, surface }) {
  return (
    <Section slim>
      <style>{STYLES}</style>
      <div className="cm-header">
        <h1 className="cm-title">{title}</h1>
        <p className="cm-sub">{sub}</p>
      </div>
      {/* The slot renders the real commercial UI when a seam is wired; null in OSS. */}
      <Slot context={{ surface }} />
      {!commercial.enabled && (
        <div className="cm-managed">
          Billing and metering are managed by your provider. This open-source control
          plane doesn&apos;t process payments — there&apos;s nothing to configure here.
        </div>
      )}
    </Section>
  )
}

export default function Billing() {
  return <SeamPage title="Billing" sub="Payment method and subscription." Slot={BillingSlot} surface="billing" />
}

export function Usage() {
  return <SeamPage title="Usage & billing" sub="Metered usage for the current period." Slot={UsageSlot} surface="usage" />
}

export function Invoices() {
  return <SeamPage title="Invoices" sub="Your invoice history and receipts." Slot={InvoicesSlot} surface="invoices" />
}
