// Vulos OS — Service Worker (OFFLINE-03)
//
// Strategy (mirrors vulos-office + vulos-mail for OS-wide consistency):
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
          return Response.error();
        });

      // Cache-first: return cached immediately; revalidate in the background.
      return cached || networkFetch;
    })
  );
});
