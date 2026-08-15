// net.ts — the ONLY way a widget can reach the network, and the reason it is the
// only way.
//
// ─────────────────────────────────────────────────────────────────────────────
// THE SOVEREIGNTY ARGUMENT, WRITTEN DOWN WHERE THE CODE IS
// ─────────────────────────────────────────────────────────────────────────────
//
// This product's pitch is that the user's box does not phone home. A widget rail
// is the easiest possible place to break that promise, because "just fetch the
// quotes" is one line and nobody sees the packet. So the network path is built
// so that the promise cannot be broken by accident:
//
//  1. A WIDGET NEVER CALLS `fetch`. It is handed `ctx.net`, or it is handed
//     null. Sandboxed widgets have no `fetch` worth having anyway (opaque
//     origin, CSP), and in-process widgets are held to it by review and by the
//     fact that there is nothing else in the context to reach for.
//
//  2. THE REQUEST IS MADE BY THE BOX, NOT THE BROWSER. If the browser called the
//     provider directly, the provider would learn the user's IP, their browser
//     fingerprint, and — via `Referer`/timing — that a Vulos desktop is behind
//     it. Routing through the box means the provider sees one origin: the box.
//     The box can cache, batch and coalesce, so ten rail renders are one
//     upstream call, and the box can log the call so the Transparency panel can
//     show the user exactly what left. None of that is possible from the tab.
//
//  3. IF THE BOX HAS NO PROXY, NOTHING HAPPENS. `widgetNet()` returns null when
//     the box does not expose `/api/widgets/fetch`. It does NOT fall back to a
//     direct browser call. A silent fallback is exactly how a privacy promise
//     erodes: the feature keeps working, so nobody notices the guarantee left.
//     Today no box ships that endpoint, which means THIS CODE CANNOT MAKE A
//     THIRD-PARTY REQUEST AT ALL — a property you can verify by grep: the only
//     `fetch` below targets a same-origin `/api/` path.
//
//  4. THE HOST ALLOWLIST IS CHECKED HERE TOO, not only on the box. Belt and
//     braces: the box must enforce it (a browser check is advisory), but
//     checking locally means a mis-typed URL fails loudly in development instead
//     of quietly reaching somewhere it shouldn't.
//
// See roadmap/WIDGETS.md § "Stocks and the network" for the product decision this
// implements.

import type { WidgetFetchResult, WidgetNet } from './types'

/** Same-origin box endpoint. Not configurable — a configurable proxy is a proxy. */
const PROXY_PATH = '/api/widgets/fetch'

// Probe result, cached for the session. `null` = not probed yet.
let proxyAvailable: boolean | null = null

/** Test seam: reset the memoised probe. */
export function resetProxyProbe(): void {
  proxyAvailable = null
}

/** Test seam: force the probe result without a network round-trip. */
export function setProxyAvailable(v: boolean | null): void {
  proxyAvailable = v
}

export function isProxyKnownAvailable(): boolean {
  return proxyAvailable === true
}

/**
 * Ask the box whether it brokers widget requests.
 *
 * Any answer other than an explicit `{ enabled: true }` means NO. A 404 means
 * no, a 500 means no, a network error means no, and a body we cannot parse means
 * no — because the failure mode of guessing "yes" is a request that leaves the
 * box, and the failure mode of guessing "no" is a widget in its offline state.
 */
export async function probeProxy(fetchImpl: typeof fetch = globalThis.fetch): Promise<boolean> {
  if (proxyAvailable !== null) return proxyAvailable
  try {
    const res = await fetchImpl(`${PROXY_PATH}/status`, { credentials: 'same-origin' })
    if (!res || !res.ok) { proxyAvailable = false; return false }
    const body: unknown = await res.json()
    proxyAvailable = !!(body && typeof body === 'object' && (body as Record<string, unknown>).enabled === true)
  } catch {
    proxyAvailable = false
  }
  return proxyAvailable
}

/** Parse a URL and return its hostname, or null if it is not an http(s) URL. */
export function urlHost(raw: string): string | null {
  try {
    const u = new URL(raw)
    // http(s) only. A widget must not be able to name `file:`, `blob:` or a
    // custom scheme and have the box dereference it.
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return null
    return u.hostname.toLowerCase()
  } catch {
    return null
  }
}

/** Exact-match host check. No wildcards, no suffix matching — see manifest.ts. */
export function hostAllowed(raw: string, hosts: string[]): boolean {
  const h = urlHost(raw)
  if (!h) return false
  return hosts.some((allowed) => allowed.toLowerCase() === h)
}

/**
 * Build the `net` capability for one widget instance, or null.
 *
 * Null when the widget wasn't granted `network`, when it declared no hosts, or
 * when the box has no proxy. The widget sees a plain absent capability in all
 * three cases and must already handle it — which is the point: a widget whose
 * offline state is broken is broken on every box that hasn't enabled the proxy,
 * i.e. all of them today.
 */
export function widgetNet(
  hosts: string[],
  opts: { granted: boolean; fetchImpl?: typeof fetch } = { granted: false },
): WidgetNet | null {
  if (!opts.granted) return null
  if (!hosts || hosts.length === 0) return null
  if (proxyAvailable !== true) return null
  const f = opts.fetchImpl || globalThis.fetch

  return {
    async getJSON(url: string): Promise<WidgetFetchResult> {
      if (!hostAllowed(url, hosts)) {
        return { ok: false, status: 0, data: null, error: 'blocked-host' }
      }
      try {
        const res = await f(PROXY_PATH, {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          // The box re-checks `url` against the manifest's hosts. It must: this
          // body is written by the browser and the browser is not the authority.
          body: JSON.stringify({ url }),
        })
        if (!res) return { ok: false, status: 0, data: null, error: 'offline' }
        if (!res.ok) return { ok: false, status: res.status, data: null, error: 'http' }
        try {
          return { ok: true, status: res.status, data: await res.json() }
        } catch {
          return { ok: false, status: res.status, data: null, error: 'bad-body' }
        }
      } catch {
        return { ok: false, status: 0, data: null, error: 'offline' }
      }
    },
  }
}
