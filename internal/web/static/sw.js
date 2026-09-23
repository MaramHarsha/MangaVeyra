/*
 * MangaVeyra service worker (installable PWA).
 *
 * - Precaches the offline page (G6 "PUTSUN") and the app icons.
 * - Hashed build assets under /static/dist/ are cache-first (they never change).
 * - Page navigations are network-first; when the network is gone the offline page is shown.
 * - API, WebSocket, media and worker traffic is never cached here (the reader's
 *   offline chapter downloads manage their own cache).
 */
const VERSION = 'mv-v1';
const STATIC_CACHE = `${VERSION}-static`;
const OFFLINE_URL = '/offline';
const PRECACHE = [OFFLINE_URL, '/static/brand/favicon-gate-blue.svg', '/static/icons/app-icon-gate-blue-192.png'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => !k.startsWith(VERSION)).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws') || url.pathname.startsWith('/media/')) return;

  if (url.pathname.startsWith('/static/dist/')) {
    event.respondWith(
      caches.match(req).then((hit) => hit || fetch(req).then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(STATIC_CACHE).then((c) => c.put(req, copy));
        }
        return res;
      })),
    );
    return;
  }

  if (req.mode === 'navigate') {
    event.respondWith(fetch(req).catch(() => caches.match(OFFLINE_URL)));
  }
});
