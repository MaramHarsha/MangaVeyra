/*
 * MangaVeyra service worker (installable PWA).
 *
 * - Precaches the offline page (G6 "PUTSUN") and the app icons.
 * - Hashed build assets under /static/dist/ are cache-first (they never change).
 * - Page navigations are network-first; when the network is gone the offline page is shown.
 * - API, WebSocket, media and worker traffic is never cached here, except the
 *   reader's offline chapters (ADR 0012 §7): the reader island stores a
 *   downloaded chapter in its own cache "mv-offline-<publication id>" (the
 *   reader page, its guided data and the frozen page images, plus a meta entry
 *   with the expiry and the member who downloaded it). While the network works
 *   every request still goes to the server, so revoked access is respected;
 *   only when the network fails are those requests answered from a chapter
 *   cache that has not expired (one without a valid expiry never answers).
 *   Pages served from a cache are marked data-offline-copy. "Clear offline
 *   downloads" and signing out delete every "mv-offline" cache, and a page
 *   fresh from the server deletes the chapters of anyone but the member signed
 *   in (all of them when signed out); see web/src/islands/reader/offline.ts.
 */
const VERSION = 'mv-v1';
const STATIC_CACHE = `${VERSION}-static`;
const OFFLINE_URL = '/offline';
const PRECACHE = [OFFLINE_URL, '/static/brand/favicon-gate-blue.svg', '/static/icons/app-icon-gate-blue-192.png'];
const OFFLINE_PREFIX = 'mv-offline-';
const OFFLINE_META = '/__mv-offline/';

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      // Offline chapters belong to the reader, not to a worker version: keep them.
      .then((keys) => Promise.all(keys.filter((k) => !k.startsWith(VERSION) && !k.startsWith('mv-offline')).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

/**
 * liveChapterCache opens an offline chapter cache that has not expired. Downloads
 * always expire (ADR 0012 §7): a chapter whose meta entry has no valid expiry, or
 * whose expiry has passed, is deleted and never answers. A cache without a meta
 * entry is a download still being saved and does not answer either.
 */
async function liveChapterCache(name) {
  const cache = await caches.open(name);
  const meta = await cache.match(OFFLINE_META + name.slice(OFFLINE_PREFIX.length));
  if (!meta) return null;
  let expires = NaN;
  try {
    const info = await meta.json();
    expires = info && typeof info === 'object' && typeof info.expires_at === 'number' ? info.expires_at : NaN;
  } catch {
    /* unreadable meta: treated as expired */
  }
  if (!(expires > 0) || expires * 1000 <= Date.now()) {
    await caches.delete(name);
    return null;
  }
  return cache;
}

/** fromChapters answers a request from the offline chapter caches. */
async function fromChapters(req, ignoreSearch) {
  const names = (await caches.keys()).filter((k) => k.startsWith(OFFLINE_PREFIX));
  for (const name of names) {
    const cache = await liveChapterCache(name);
    if (!cache) continue;
    const hit = (await cache.match(req, { ignoreSearch })) || (ignoreSearch ? undefined : await cache.match(new URL(req.url).pathname));
    if (hit) return hit;
  }
  return undefined;
}

/**
 * offlineCopy marks a page served from a cache with data-offline-copy on <body>.
 * Its data-user is whoever was signed in when it was saved, so the page script
 * never takes it for the current user (it deletes other members' offline
 * chapters only on pages fresh from the server).
 */
async function offlineCopy(res) {
  if (!res || !(res.headers.get('Content-Type') || '').includes('text/html')) return res;
  const html = await res.text();
  return new Response(html.replace(/<body\b/i, '<body data-offline-copy'), { status: res.status, statusText: res.statusText, headers: res.headers });
}

/** networkThenChapters goes to the network and falls back to an offline chapter only when it fails. */
function networkThenChapters(req, ignoreSearch, fallback) {
  return fetch(req).catch(async () => {
    const hit = (await fromChapters(req, ignoreSearch)) || (fallback ? await fallback() : Response.error());
    return req.mode === 'navigate' ? offlineCopy(hit) : hit;
  });
}

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Offline chapters: frozen page images (signed URLs, matched without their signature)
  // and the reader's chapter data.
  if (url.pathname.startsWith('/media/')) {
    event.respondWith(networkThenChapters(req, true));
    return;
  }
  if (url.pathname.startsWith('/api/v1/reader/series/')) {
    event.respondWith(networkThenChapters(req, false));
    return;
  }
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) return;

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
    if (url.pathname.startsWith('/s/')) {
      event.respondWith(networkThenChapters(req, false, () => caches.match(OFFLINE_URL)));
      return;
    }
    event.respondWith(fetch(req).catch(async () => offlineCopy(await caches.match(OFFLINE_URL))));
  }
});
