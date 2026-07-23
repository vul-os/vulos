# The Commercial Seam (`@vulos/commercial-seam`)

The vulos-management SPA (`web/`) is **open source and commercial-agnostic**.
It contains no billing, pricing, invoicing, or provisioning UI. Everything
commercial is imported through **one swappable module** — the JavaScript analog
of the Go `pkg/billingport` / `pkg/storageport` seams (and the `replace`
directive that injects their real implementations).

```js
import { BillingSlot, UsageSlot, commercial } from '@vulos/commercial-seam'
```

- **OSS build** (this repo): `@vulos/commercial-seam` resolves to
  `web/src/seam/commercial.jsx`. Every slot is a **NoOp** (`() => null`) and
  `commercial.enabled === false`. A self-hoster never sees a Pay button, a
  price, an invoice, or a managed-provisioning flow.
- **Cloud build** (vulos-cloud, private): overrides the **one** Vite alias to
  point at its own closed-source implementation that honours the contract below.
  The app code is byte-for-byte identical in both builds.

---

## The alias (build-time swap)

`web/vite.config.js` defines:

```js
resolve: {
  alias: {
    '@vulos/commercial-seam':
      process.env.VULOS_COMMERCIAL_SEAM ||
      fileURLToPath(new URL('./src/seam/commercial.jsx', import.meta.url)),
  },
}
```

The cloud team overrides the resolution in **either** of two ways:

1. **Env var (no source change):** set `VULOS_COMMERCIAL_SEAM` to the absolute
   path of the real implementation module before `vite build`, e.g.

   ```sh
   VULOS_COMMERCIAL_SEAM=/abs/path/to/cloud/commercial.jsx npm --prefix web run build
   ```

2. **Own vite config:** point the `@vulos/commercial-seam` alias at a package
   the cloud repo installs (e.g. its own `@vulos-cloud/commercial`).

Either way the alias key is **`@vulos/commercial-seam`** — do not change it.

---

## The contract — what a replacement MUST export

A drop-in replacement is an ES module exporting **all** of the following. Every
slot is a React component; each receives the common prop bag and may ignore any
of it. Slots MAY be async / Suspense-driven and MAY render real UI.

| Export             | Kind                | Purpose                                            |
| ------------------ | ------------------- | -------------------------------------------------- |
| `commercial`       | object              | `{ enabled: boolean, provider?: string }` flag     |
| `BillingSlot`      | React component     | Payment method / subscription management           |
| `InvoicesSlot`     | React component     | Invoice history + downloads                        |
| `UsageSlot`        | React component     | Metered-usage meters / current-period consumption  |
| `PricingSlot`      | React component     | Plan/tier selection + upgrade CTAs                 |
| `ProvisioningSlot` | React component     | Managed-box / managed-bucket provisioning flow     |

A default export mirroring the named exports is provided by the OSS module and
SHOULD be provided by a replacement (either import style must work).

### `commercial` flag

```ts
export const commercial: { enabled: boolean; provider?: string }
```

- OSS: `{ enabled: false, provider: 'none' }`.
- Cloud: `{ enabled: true, provider: 'paystack' /* etc */ }`.

The app reads `commercial.enabled` to decide whether to advertise commercial nav
items and fallbacks (e.g. the sidebar "Billing" link, the Dashboard "Not
applicable — open-source build" note). Slots must still render safely if the app
mounts them regardless of the flag.

### Common props (all optional — a slot may ignore any)

```ts
type CommercialSlotProps = {
  accountId?: string          // signed-in account id, when known
  orgId?: string              // active org/tenant id, when known
  context?: Record<string, unknown> // free-form page context (route, entity ids)
  onChange?: () => void       // call after a state-mutating action so the host can refresh
  children?: React.ReactNode  // a slot MAY wrap host-provided fallback content
}
```

### Rules for a replacement

- **Pure seam, no app logic.** Keep account/session plumbing in the host app;
  the seam renders commercial UI only.
- **Fail safe.** A slot must never throw during render in a way that breaks the
  console; guard your own network/state.
- **Same names, same alias key.** Renaming any export or the alias key breaks
  the swap.
- **Self-hostable output stays clean.** Nothing here should leak into the OSS
  bundle — the swap happens only in the cloud build.

---

## Where the slots are used (phase 1)

- `web/src/App.jsx` — sidebar advertises the "Billing" nav item only when
  `commercial.enabled`.
- `web/src/pages/Dashboard.jsx` — renders `<BillingSlot/>` and `<UsageSlot/>`
  (empty in OSS) to prove the injection point end-to-end.

More slots wire in as the real console pages migrate (phase 2).
