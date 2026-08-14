// Vulos OS — Service Worker (OFFLINE-03)
//
// Strategy (mirrors Diwan + LilMail for OS-wide consistency):
//   - App shell (index.html, JS/CSS chunks, fonts, icons) → cache-first with
//     background revalidation. Hashed Vite filenames make this safe.
//   - /api/**, /collab/**, /jmap/**, /dav/**, /auth/** → network-only (never
//     cache server state; the failover layer + per-app caches own freshness).
//   - Navigation fallback: when a navigation request fails (offline), serve
//     cached index.html so the SPA shell still boots and renders from local
//     state.
//   - Skip-waiting is opt-in via a postMessage('skipWaiting') from the page,
//     so the UI can prompt the user before swapping in a new version.

const CACHE_NAME = 'vulos-os-shell-v1';

// Assets pre-cached on install (root entry points + manifest + headline icons).
// Hashed Vite chunks get cached on first GET via the fetch handler.
const SHELL_URLS = [
  '/',
  '/index.html',
  '/manifest.json',
  '/icon-32.png',
  '/icon-192.png',
  '/icon-512.png',
];

// Paths that must never be cached — server state owns its own freshness.
const NEVER_CACHE = ['/api/', '/collab/', '/jmap', '/dav/', '/auth/'];

function shouldCache(url) {
  let u;
  try { u = new URL(url); } catch { return false; }
  const p = u.pathname;
  for (const prefix of NEVER_CACHE) {
    if (p === prefix || p.startsWith(prefix)) return false;
  }
  return true;
}

// ── Install: pre-cache the app shell. ───────────────────────────────────────
self.addEventListener('install', (event) => {
  // We do NOT skipWaiting() here — the page opts in via postMessage so the
  // user can be prompted before a new version takes over.
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) =>
      cache.addAll(SHELL_URLS).catch(() => {
        // Some shell URLs may 404 in dev — non-fatal.
      })
    )
  );
});

// ── Activate: evict stale caches and claim clients so the SW controls them. ─
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k))
      )
    ).then(() => self.clients.claim())
  );
});

// ── Message channel: page opts in to a hot-swap of the new SW version. ─────
self.addEventListener('message', (event) => {
  if (event && event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});

// ── Push: render the notification the cell sent (PUSH-CELL-01). ─────────────
//
// The cell POSTs an RFC 8291 (aes128gcm) encrypted payload DIRECTLY to the
// browser-vendor push service; the browser decrypts it with the subscription
// keys and delivers it here. The payload is the minimal PushPayload the cell's
// notify service marshals: { title, body, tag, source, url }. We render it as an
// OS notification. The payload NEVER carries a recipient id — it is E2E-encrypted
// to this device's own subscription, so it is inherently for this user.
self.addEventListener('push', (event) => {
  let data = {};
  if (event.data) {
    try {
      data = event.data.json();
    } catch {
      // Fall back to a plain-text body so a non-JSON payload still shows.
      try { data = { body: event.data.text() }; } catch { data = {}; }
    }
  }
  const title = typeof data.title === 'string' && data.title ? data.title : 'Vulos';
  const options = {
    body: typeof data.body === 'string' ? data.body : '',
    // Coalesce same-category notifications so a burst does not stack endlessly.
    tag: typeof data.tag === 'string' && data.tag ? data.tag : undefined,
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    // Carry the deep-link + source so the click handler can focus/open it. Never
    // include any recipient identifier here.
    data: {
      url: typeof data.url === 'string' ? data.url : '/',
      source: typeof data.source === 'string' ? data.source : '',
    },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

// ── Notification click: focus an existing window or open the deep-link. ─────
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || '/';
  event.waitUntil(
    self.clients
      .matchAll({ type: 'window', includeUncontrolled: true })
      .then((clientList) => {
        // Prefer focusing an already-open Vulos window.
        for (const client of clientList) {
          if ('focus' in client) {
            if (typeof client.navigate === 'function' && target && target !== '/') {
              client.navigate(target).catch(() => {});
            }
            return client.focus();
          }
        }
        // None open → open a new one at the deep-link.
        if (self.clients.openWindow) {
          return self.clients.openWindow(target);
        }
        return undefined;
      })
  );
});

// ── Fetch: cache-first for static, network-only for API/collab/JMAP/DAV. ────
self.addEventListener('fetch', (event) => {
  const { request } = event;

  // Only intercept GET; POST/PUT/DELETE go straight to the network.
  if (request.method !== 'GET') return;

  // Never cache API/collab/JMAP/DAV/auth — let them fail visibly when offline
  // so the API client's failover + the offlineQueue can react.
  if (!shouldCache(request.url)) return;

  event.respondWith(
    caches.match(request).then((cached) => {
      const networkFetch = fetch(request)
        .then((response) => {
          if (response && response.status === 200 && response.type !== 'opaque') {
            const clone = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(request, clone));
          }
          return response;
        })
        .catch(() => {
          // Network failed — navigation requests get the cached app shell so
          // the SPA still mounts and can render from local state.
          if (request.mode === 'navigate') {
            return caches.match('/index.html').then((idx) => idx || caches.match('/'));
          }

          // ONE retry before giving up, for lazy-loaded app chunks.
          //
          // Every app in this shell is a code-split chunk fetched on open. A
          // single failed fetch here used to return Response.error() straight
          // away, which rejects the dynamic import, trips the window's error
          // boundary, and leaves that app DEAD until a full reload — from one
          // transient blip. Observed on a real box: opening Drive and Contacts
          // both died this way while the backend was briefly unresponsive, and
          // the only symptom a user gets is "error loading dynamically
          // imported module".
          //
          // A chunk is immutable and content-hashed, so retrying is safe: the
          // same URL can only ever return the same bytes. This does not paper
          // over a server that is genuinely down — the second failure still
          // errors — it removes the single-packet-loss cliff.
          //
          // Deliberately NOT a longer backoff loop: an app that hangs for
          // seconds is a worse experience than one that reports a failure the
          // user can retry by reopening, and holding the fetch open keeps the
          // window's spinner spinning with no way out.
          return fetch(request)
            .then((response) => {
              if (response && response.status === 200 && response.type !== 'opaque') {
                const clone = response.clone();
                caches.open(CACHE_NAME).then((cache) => cache.put(request, clone));
              }
              return response;
            })
            .catch(() => Response.error());
        });

      // Cache-first: return cached immediately; revalidate in the background.
      return cached || networkFetch;
    })
  );
});
