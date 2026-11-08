/**
 * Offline chapters (ADR 0012 §7). A downloaded publication lives in its own
 * Cache Storage cache "mv-offline-<publication id>" holding the reader page,
 * its guided data, the frozen page images and the built assets, plus a
 * small meta entry with the expiry the service worker checks before serving
 * anything from it. The settings action "Clear offline downloads" deletes
 * every cache whose name starts with "mv-offline".
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
  cache: string;
  reader_url: string;
  data_url: string;
  pages: { number: number; src: string }[];
}

export interface OfflineMeta { publication_id: string; download_id: string; reader_url: string; expires_at: number; saved_at: number; bytes: number }

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
      publication_id: publicationFromManifest(m), download_id: m.download.id, reader_url: m.reader_url,
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

/** expired reports whether meta is past its expiry at nowSeconds. */
export function expired(meta: OfflineMeta | null, nowSeconds: number): boolean {
  return !meta || (meta.expires_at > 0 && meta.expires_at <= nowSeconds);
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

/** sizeLabel formats bytes as "412 MB". */
export function sizeLabel(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`;
  if (n > 0) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return '0 MB';
}
