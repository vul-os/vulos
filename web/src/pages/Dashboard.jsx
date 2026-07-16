/**
 * pages/Dashboard.jsx — placeholder Dashboard (phase 1 proof).
 *
 * Its job in phase 1 is to PROVE the commercial seam works end-to-end: it
 * renders <BillingSlot/> and <UsageSlot/> from '@vulos/commercial-seam'. In this
 * OSS build those are NoOp components that render nothing — so the "Billing"
 * card below shows only its OSS fallback. When the cloud build aliases the seam
 * to its real implementation, the SAME slots inject real billing/usage UI here
 * with no change to this file.
 */

import { BillingSlot, UsageSlot, commercial } from '@vulos/commercial-seam'

export default function Dashboard() {
  return (
    <div className="vm-page">
      <div className="vm-page-head">
        <h1>Dashboard</h1>
        <p className="vm-page-sub">
          Self-hosted control plane. Free, no-op billing rail — nothing charges,
          nothing phones home.
        </p>
      </div>

      <div className="vm-cards">
        <section className="vm-card">
          <h3>Control plane</h3>
          <p>Operational. Serving the API and this console from one origin.</p>
        </section>

        <section className="vm-card">
          <h3>Billing</h3>
          {/*
            SEAM PROOF: BillingSlot is empty in OSS (renders null), so only the
            fallback below shows. The cloud build injects real billing UI here.
          */}
          <BillingSlot context={{ page: 'dashboard' }} />
          {!commercial.enabled && (
            <p className="vm-muted">
              Not applicable — this is the open-source build.
            </p>
          )}
        </section>

        <section className="vm-card">
          <h3>Usage</h3>
          <UsageSlot context={{ page: 'dashboard' }} />
          {!commercial.enabled && (
            <p className="vm-muted">Unmetered on self-host.</p>
          )}
        </section>
      </div>
    </div>
  )
}
