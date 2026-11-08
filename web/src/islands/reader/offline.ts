/**
 * Offline chapters (ADR 0012 §7). A downloaded publication lives in its own
 * Cache Storage cache "mv-offline-<publication id>" holding the reader page,
 * its guided data, the frozen page images and the built assets, plus a
 * small meta entry with the expiry the service worker checks before serving
 * anything from it and the member who downloaded it. The settings action
 * "Clear offline downloads" and signing out delete every cache whose name
 * starts with "mv-offline"; a page fresh from the server deletes the chapters
 * of anyone but the member signed in (every chapter when signed out), so a
 * shared device never shows one member's chapters, bookmarks or progress to
 * the next person.
 */

export const OFFLINE_PREFIX = 'mv-offline-';
/** META_PREFIX is the same-origin key of a cache's meta entry (never served by the server). */
export const META_PREFIX = '/__mv-offline/';

export function cacheName(publicationId: string): string {
  return OFFLINE_PREFIX + publicationId;
}

export function isOfflineCache(name: string): boolean {
  return name.startsWith(OFFLINE_PREFIX);
}

export function publicationOf(name: string): string {
  return isOfflineCache(name) ? name.slice(OFFLINE_PREFIX.length) : '';
}

export function metaKey(publicationId: string): string {
  return META_PREFIX + publicationId;
}

export interface Manifest {
  download: { id: string; status: string; expires_at: number };
  /** owner is the user ID of the member downloading the chapter. */
  owner?: string;
  cache: string;
  reader_url: string;
  data_url: string;
  pages: { number: number; src: string }[];
}

export interface OfflineMeta { publication_id: string; download_id: string; owner: string; reader_url: string; expires_at: number; saved_at: number; bytes: number }

type Fetch = (input: string, init?: RequestInit) => Promise<Response>;

/** urlsFor lists what a chapter needs offline, de-duplicated, page images included. */
export function urlsFor(m: Manifest, assets: string[] = []): string[] {
  const out: string[] = [];
  const add = (u: string | undefined) => {
    if (u && !out.includes(u)) out.push(u);
  };
  add(m.reader_url);
  add(m.data_url);
  m.pages.forEach((p) => add(p.src));
  assets.forEach(add);
  return out;
}

/** builtAssets lists the same-origin built files this page loaded (scripts, styles, fonts). */
export function builtAssets(doc: Document = document, perf: Pick<Performance, 'getEntriesByType'> | undefined = globalThis.performance): string[] {
  const out = new Set<string>();
  const keep = (raw: string | null) => {
    if (!raw) return;
    try {
      const u = new URL(raw, doc.baseURI);
      if (u.origin === new URL(doc.baseURI).origin && u.pathname.startsWith('/static/')) out.add(u.pathname + u.search);
    } catch {
      /* not a URL */
    }
  };
  doc.querySelectorAll<HTMLScriptElement>('script[src]').forEach((s) => keep(s.getAttribute('src')));
  doc.querySelectorAll<HTMLLinkElement>('link[href]').forEach((l) => {
    if (/stylesheet|modulepreload|preload|icon|manifest/.test(l.rel)) keep(l.getAttribute('href'));
  });
  try {
    perf?.getEntriesByType('resource').forEach((e) => keep(e.name));
  } catch {
    /* no resource timing */
  }
  return [...out];
}

/**
 * saveChapter stores every URL of the manifest in the chapter's cache and
 * writes its meta entry last; any failure removes the partial cache. It
 * returns the stored bytes.
 */
export async function saveChapter(
  m: Manifest,
  caches: CacheStorage,
  fetchFn: Fetch,
  opts: { assets?: string[]; onProgress?: (done: number, total: number) => void; now?: () => number } = {},
): Promise<number> {
  const name = m.cache || cacheName(publicationFromManifest(m));
  const cache = await caches.open(name);
  const urls = urlsFor(m, opts.assets);
  let bytes = 0;
  let done = 0;
  try {
    for (const url of urls) {
      const res = await fetchFn(url, { credentials: 'same-origin', cache: 'no-store' });
      if (!res.ok) throw new Error(`offline: ${res.status} for ${url}`);
      const blob = await res.clone().blob();
      bytes += blob.size;
      await cache.put(url, res);
      done += 1;
      opts.onProgress?.(done, urls.length);
    }
    const meta: OfflineMeta = {
      publication_id: publicationFromManifest(m), download_id: m.download.id, owner: m.owner ?? '', reader_url: m.reader_url,
      expires_at: m.download.expires_at, saved_at: Math.floor((opts.now ?? Date.now)() / 1000), bytes,
    };
    await cache.put(metaKey(meta.publication_id), new Response(JSON.stringify(meta), { headers: { 'Content-Type': 'application/json' } }));
  } catch (err) {
    await caches.delete(name);
    throw err;
  }
  return bytes;
}

function publicationFromManifest(m: Manifest): string {
  return publicationOf(m.cache) || '';
}

/** readMeta returns a chapter's meta entry, or null when it is not on this device. */
export async function readMeta(caches: CacheStorage, publicationId: string): Promise<OfflineMeta | null> {
  if (!(await caches.has(cacheName(publicationId)))) return null;
  const cache = await caches.open(cacheName(publicationId));
  const hit = await cache.match(metaKey(publicationId));
  if (!hit) return null;
  try {
    return (await hit.json()) as OfflineMeta;
  } catch {
    return null;
  }
}

/** ownedBy reports whether the signed-in member user downloaded the chapter ("" when signed out: never). */
export function ownedBy(meta: OfflineMeta | null, user: string): boolean {
  return !!meta && !!user && meta.owner === user;
}

/** expired reports whether meta is past its expiry at nowSeconds; a chapter without one never stays (downloads expire). */
export function expired(meta: OfflineMeta | null, nowSeconds: number): boolean {
  return !meta || !(meta.expires_at > 0) || meta.expires_at <= nowSeconds;
}

/** removeChapter deletes one chapter from this device. */
export async function removeChapter(caches: CacheStorage, publicationId: string): Promise<boolean> {
  return caches.delete(cacheName(publicationId));
}

/** offlineChapters lists the publication IDs kept on this device. */
export async function offlineChapters(caches: CacheStorage): Promise<string[]> {
  return (await caches.keys()).filter(isOfflineCache).map(publicationOf).filter(Boolean);
}

/** clearAll deletes every offline chapter on this device. */
export async function clearAll(caches: CacheStorage): Promise<number> {
  const names = (await caches.keys()).filter((k) => k.startsWith('mv-offline'));
  await Promise.all(names.map((n) => caches.delete(n)));
  return names.length;
}

/**
 * dropForeignChapters deletes the offline chapters that do not belong to user
 * (the member signed in, "" when signed out): every chapter when signed out,
 * and otherwise chapters another member downloaded, chapters that name no
 * owner or have an unreadable meta entry, and expired ones. A cache without a
 * meta entry yet is a download of this member still being saved and stays.
 * It returns how many chapters were deleted.
 */
export async function dropForeignChapters(store: CacheStorage, user: string, nowSeconds: number): Promise<number> {
  let dropped = 0;
  for (const name of (await store.keys()).filter(isOfflineCache)) {
    let drop = !user;
    if (!drop) {
      const hit = await (await store.open(name)).match(metaKey(publicationOf(name)));
      if (hit) {
        let meta: OfflineMeta | null = null;
        try {
          meta = (await hit.json()) as OfflineMeta;
        } catch {
          meta = null;
        }
        drop = !ownedBy(meta, user) || expired(meta, nowSeconds);
      }
    }
    if (drop && (await store.delete(name))) dropped += 1;
  }
  return dropped;
}

/** isLogoutForm reports whether form signs the member out (POST /logout). */
export function isLogoutForm(form: HTMLFormElement): boolean {
  try {
    return form.method.toLowerCase() === 'post' && new URL(form.action, form.ownerDocument.baseURI).pathname === '/logout';
  } catch {
    return false;
  }
}

/**
 * installLogoutClearing deletes every offline chapter before a sign-out form
 * is sent, then sends it. Signing out never waits longer than timeoutMs and
 * never fails because of the caches; without JavaScript (or Cache Storage)
 * the form posts as usual. It returns a function that removes the handler.
 */
export function installLogoutClearing(doc: Document, store: () => CacheStorage | null, timeoutMs = 1500): () => void {
  const sending = new WeakSet<HTMLFormElement>();
  const onSubmit = (e: Event) => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement) || !isLogoutForm(form)) return;
    const caches = store();
    if (!caches) return;
    e.preventDefault();
    if (sending.has(form)) return;
    sending.add(form);
    const send = () => form.submit(); // submit() fires no submit event, so this runs once
    let cleared: Promise<unknown>;
    try {
      cleared = clearAll(caches).catch(() => 0);
    } catch {
      cleared = Promise.resolve();
    }
    void Promise.race([cleared, new Promise((r) => setTimeout(r, timeoutMs))]).then(send, send);
  };
  doc.addEventListener('submit', onSubmit, true);
  return () => doc.removeEventListener('submit', onSubmit, true);
}

/** sizeLabel formats bytes as "412 MB". */
export function sizeLabel(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`;
  if (n > 0) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return '0 MB';
}
