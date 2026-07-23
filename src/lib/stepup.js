/**
 * stepup.js — client side of the generic "extra security step" gate.
 *
 * Pattern: privileged UI actions (destructive/sensitive admin operations)
 * call `await requireStepUp()` immediately before performing the mutation.
 * It opens a confirm-password modal, POSTs the password to
 * `/api/auth/stepup/verify`, and resolves once the backend has minted a
 * short-lived (~5 min) elevated token (see backend/services/stepup) and set
 * it as the vulos_stepup cookie. The backend handler for the actual
 * mutation is expected to call `stepup.Require(r)` server-side — this
 * modal is a UX nicety, not the security boundary; the server never trusts
 * "the user clicked confirm" without the cookie it itself validates.
 *
 * Usage:
 *
 *   import { requireStepUp } from '../../lib/stepup'
 *
 *   async function onDeleteThing() {
 *     try {
 *       await requireStepUp()
 *     } catch {
 *       return // cancelled — do nothing
 *     }
 *     await request('/things/123', { method: 'DELETE' })
 *   }
 *
 * requireStepUp() throws on cancel (err.code === 'CANCELLED') or on repeated
 * failure (the modal itself retries password entry; it only rejects when the
 * user gives up and cancels).
 */

import { createRoot } from 'react-dom/client'
import { createElement } from 'react'
import StepUpModal from './StepUpModal.jsx'

// SAFETY_MARGIN_MS keeps the client from treating a token as fresh right up
// against the server's own expiry — a network round-trip could land just
// past it, which would surface as a confusing 401 on the very next request
// instead of a clean re-prompt.
const SAFETY_MARGIN_MS = 15_000

// elevatedUntil is a purely client-side cache of the last successful
// elevation's expiry, so a UI flow that calls requireStepUp() more than once
// in quick succession (e.g. several gated actions in the same panel) doesn't
// nag the user for a password on every click. It is never the source of
// truth — the backend re-checks the cookie on every gated request
// regardless, so a stale/wrong value here only ever costs an extra prompt,
// never a bypass.
let elevatedUntil = 0

// pendingPromise de-dupes concurrent callers: if requireStepUp() is invoked
// again while a modal is already open, the second caller waits on the same
// modal instead of stacking a second one.
let pendingPromise = null

/**
 * Resolve once the current session is freshly elevated. Opens a
 * confirm-password modal if needed; resolves immediately (no prompt) if a
 * recent elevation is still within its safety window.
 *
 * @returns {Promise<void>}
 */
export function requireStepUp() {
  if (Date.now() < elevatedUntil - SAFETY_MARGIN_MS) {
    return Promise.resolve()
  }
  if (pendingPromise) return pendingPromise

  pendingPromise = new Promise((resolve, reject) => {
    const container = document.createElement('div')
    container.setAttribute('data-stepup-modal-root', '')
    document.body.appendChild(container)
    const root = createRoot(container)

    const cleanup = () => {
      root.unmount()
      container.remove()
    }

    const onResolve = (data) => {
      const expiresAt = data?.expires_at ? Date.parse(data.expires_at) : NaN
      elevatedUntil = Number.isFinite(expiresAt) ? expiresAt : Date.now() + 5 * 60_000
      cleanup()
      resolve()
    }
    const onReject = (err) => {
      cleanup()
      reject(err)
    }

    root.render(createElement(StepUpModal, { onResolve, onReject }))
  }).finally(() => {
    pendingPromise = null
  })

  return pendingPromise
}
