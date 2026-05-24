// Vulos Office — Service Worker
// Strategy:
//   - App shell (index.html, JS/CSS chunks, static assets) → cache-first with
//     network fallback on miss and background revalidation.
//   - /api/** and /collab/** → network-only (never cache server state).
//   - Offline fallback: if a navigation request fails, serve cached index.html.

const CACHE_NAME = 'vulos-office-v1';

// Assets that are cached on install (app shell).
// In production Vite outputs hashed filenames; we cache the root entry points
// and let the fetch handler cache hashed chunks on first load.
const SHELL_URLS = ['/', '/manifest.webmanifest'];

// Paths that should never be cached.
const NEVER_CACHE = ['/api/', '/collab/'];

function shouldCache(url) {
  const u = new URL(url);
  for (const prefix of NEVER_CACHE) {
    if (u.pathname.startsWith(prefix)) return false;
  }
  return true;
}

// ── Install: pre-cache app shell ─────────────────────────────────────────────
self.addEventListener('install', (event) => {
  self.skipWaiting();
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL_URLS))
  );
});

// ── Activate: evict stale caches ─────────────────────────────────────────────
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys
          .filter((k) => k !== CACHE_NAME)
          .map((k) => caches.delete(k))
      )
    ).then(() => self.clients.claim())
  );
});

// ── Fetch: cache-first for static, network-only for API ──────────────────────
self.addEventListener('fetch', (event) => {
  const { request } = event;

  // Only intercept GET requests.
  if (request.method !== 'GET') return;

  // Never cache API or collab WebSocket upgrade paths.
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
          // Network failed — if it's a navigation, serve index.html fallback.
          if (request.mode === 'navigate') {
            return caches.match('/');
          }
          return Response.error();
        });

      // Return cached version immediately; revalidate in background.
      return cached || networkFetch;
    })
  );
});
