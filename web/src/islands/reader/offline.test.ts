import { describe, expect, it, vi } from 'vitest';
import { builtAssets, cacheName, clearAll, dropForeignChapters, expired, installLogoutClearing, isOfflineCache, metaKey, offlineChapters, readMeta, removeChapter, saveChapter, sizeLabel, urlsFor, type Manifest } from './offline';

/** A minimal in-memory CacheStorage. */
class FakeCache {
  store = new Map<string, Response>();
  async put(req: RequestInfo | URL, res: Response) {
    this.store.set(String(req instanceof Request ? req.url : req), res);
  }
  async match(req: RequestInfo | URL) {
    const r = this.store.get(String(req instanceof Request ? req.url : req));
    return r ? r.clone() : undefined;
  }
}
class FakeCaches {
  caches = new Map<string, FakeCache>();
  async open(name: string) {
    if (!this.caches.has(name)) this.caches.set(name, new FakeCache());
    return this.caches.get(name)! as unknown as Cache;
  }
  async has(name: string) {
    return this.caches.has(name);
  }
  async delete(name: string) {
    return this.caches.delete(name);
  }
  async keys() {
    return [...this.caches.keys()];
  }
}

const manifest = (id = '01hzpub'): Manifest => ({
  download: { id: '01hzdl', status: 'pending', expires_at: 2_000_000_000 },
  cache: cacheName(id),
  reader_url: '/s/tidebreak-courier/8',
  data_url: `/api/v1/reader/series/01hzs/chapters/8?lang=en`,
  pages: [{ number: 1, src: '/media/p/1.png?exp=1&sig=a' }, { number: 2, src: '/media/p/2.png?exp=1&sig=b' }],
});

const okFetch = async (url: string) => new Response(`body of ${url}`, { status: 200 });

describe('offline cache naming', () => {
  it('names one cache per publication under the cleared prefix', () => {
    expect(cacheName('01hzpub')).toBe('mv-offline-01hzpub');
    expect(isOfflineCache('mv-offline-01hzpub')).toBe(true);
    expect(isOfflineCache('mv-v1-static')).toBe(false);
    // The settings action deletes every cache starting with "mv-offline".
    expect(cacheName('x').startsWith('mv-offline')).toBe(true);
    expect(metaKey('01hzpub')).toBe('/__mv-offline/01hzpub');
  });
  it('lists the reader page, guided data, page images and assets once', () => {
    expect(urlsFor(manifest(), ['/static/dist/main.js', '/s/tidebreak-courier/8'])).toEqual([
      '/s/tidebreak-courier/8', '/api/v1/reader/series/01hzs/chapters/8?lang=en',
      '/media/p/1.png?exp=1&sig=a', '/media/p/2.png?exp=1&sig=b', '/static/dist/main.js',
    ]);
  });
});

describe('saveChapter', () => {
  it('stores every URL and writes the meta entry with the expiry', async () => {
    const caches = new FakeCaches();
    const progress: number[] = [];
    const bytes = await saveChapter(manifest(), caches as unknown as CacheStorage, okFetch, { onProgress: (d) => progress.push(d), now: () => 1_700_000_000_000 });
    expect(bytes).toBeGreaterThan(0);
    expect(progress).toEqual([1, 2, 3, 4]);
    const cache = caches.caches.get('mv-offline-01hzpub')!;
    expect([...cache.store.keys()]).toContain('/media/p/2.png?exp=1&sig=b');
    const meta = await readMeta(caches as unknown as CacheStorage, '01hzpub');
    expect(meta).toMatchObject({ publication_id: '01hzpub', download_id: '01hzdl', expires_at: 2_000_000_000, saved_at: 1_700_000_000, bytes });
    expect(expired(meta, 1_900_000_000)).toBe(false);
    expect(expired(meta, 2_000_000_000)).toBe(true);
    expect(expired(null, 0)).toBe(true);
    // A chapter without an expiry is never kept (ADR 0012 §7: downloads expire).
    expect(expired({ ...meta!, expires_at: 0 }, 1)).toBe(true);
    expect(expired({ ...meta!, expires_at: Number.NaN }, 1)).toBe(true);
  });
  it('removes a partial cache when a page fails', async () => {
    const caches = new FakeCaches();
    const failing = async (url: string) => (url.includes('2.png') ? new Response('gone', { status: 404 }) : new Response('ok'));
    await expect(saveChapter(manifest(), caches as unknown as CacheStorage, failing)).rejects.toThrow();
    expect(await caches.has('mv-offline-01hzpub')).toBe(false);
    expect(await readMeta(caches as unknown as CacheStorage, '01hzpub')).toBeNull();
  });
});

describe('removing and clearing', () => {
  it('removes one chapter and clears all offline chapters but nothing else', async () => {
    const caches = new FakeCaches();
    await saveChapter(manifest('01a'), caches as unknown as CacheStorage, okFetch);
    await saveChapter(manifest('01b'), caches as unknown as CacheStorage, okFetch);
    await caches.open('mv-v1-static');
    expect((await offlineChapters(caches as unknown as CacheStorage)).sort()).toEqual(['01a', '01b']);
    expect(await removeChapter(caches as unknown as CacheStorage, '01a')).toBe(true);
    expect(await offlineChapters(caches as unknown as CacheStorage)).toEqual(['01b']);
    expect(await clearAll(caches as unknown as CacheStorage)).toBe(1);
    expect(await caches.keys()).toEqual(['mv-v1-static']);
  });
});

describe('owned offline chapters', () => {
  const saveFor = async (caches: FakeCaches, id: string, owner: string, expires = 2_000_000_000) => {
    const m = { ...manifest(id), owner };
    m.download = { ...m.download, expires_at: expires };
    await saveChapter(m, caches as unknown as CacheStorage, okFetch);
  };
  it('records the member who downloaded the chapter', async () => {
    const caches = new FakeCaches();
    await saveFor(caches, '01a', '01hzrin');
    expect(await readMeta(caches as unknown as CacheStorage, '01a')).toMatchObject({ owner: '01hzrin' });
  });
  it("deletes other members' and unattributed chapters, and every chapter when signed out", async () => {
    const caches = new FakeCaches();
    await saveFor(caches, '01mine', '01hzrin');
    await saveFor(caches, '01theirs', '01hzkai');
    await saveFor(caches, '01legacy', '');
    await saveFor(caches, '01old', '01hzrin', 1_000);
    await caches.open('mv-offline-01partial'); // a download still in progress: no meta yet
    await caches.open('mv-v1-static');
    const store = caches as unknown as CacheStorage;
    expect(await dropForeignChapters(store, '01hzrin', 1_700_000_000)).toBe(3);
    expect((await caches.keys()).sort()).toEqual(['mv-offline-01mine', 'mv-offline-01partial', 'mv-v1-static']);
    expect(await dropForeignChapters(store, '', 1_700_000_000)).toBe(2);
    expect(await caches.keys()).toEqual(['mv-v1-static']);
  });
});

describe('signing out', () => {
  const logoutForm = () => {
    document.body.innerHTML = `<form method="post" action="/logout"><button type="submit">Log out</button></form>
      <form method="post" action="/settings/theme"><button type="submit">Theme</button></form>`;
    const [logout, other] = Array.from(document.querySelectorAll('form'));
    logout.submit = vi.fn();
    other.submit = vi.fn();
    return { logout, other };
  };
  it('deletes every offline chapter before the logout form is sent', async () => {
    const caches = new FakeCaches();
    await saveChapter({ ...manifest('01a'), owner: '01hzrin' }, caches as unknown as CacheStorage, okFetch);
    await caches.open('mv-v1-static');
    const { logout, other } = logoutForm();
    const stop = installLogoutClearing(document, () => caches as unknown as CacheStorage);
    const ev = new Event('submit', { bubbles: true, cancelable: true });
    logout.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    await vi.waitFor(() => expect(logout.submit).toHaveBeenCalledTimes(1));
    expect(await caches.keys()).toEqual(['mv-v1-static']);
    // Other forms are untouched.
    const ev2 = new Event('submit', { bubbles: true, cancelable: true });
    other.dispatchEvent(ev2);
    expect(ev2.defaultPrevented).toBe(false);
    stop();
  });
  it('still signs out when the caches cannot be cleared', async () => {
    const { logout } = logoutForm();
    const broken = { keys: () => new Promise<string[]>(() => undefined) } as unknown as CacheStorage;
    const stop = installLogoutClearing(document, () => broken, 20);
    logout.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(logout.submit).toHaveBeenCalledTimes(1));
    stop();
    const failing = { keys: () => Promise.reject(new Error('denied')) } as unknown as CacheStorage;
    const { logout: again } = logoutForm();
    const stop2 = installLogoutClearing(document, () => failing, 20);
    again.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(again.submit).toHaveBeenCalledTimes(1));
    stop2();
  });
  it('leaves the form alone without Cache Storage', () => {
    const { logout } = logoutForm();
    const stop = installLogoutClearing(document, () => null);
    const ev = new Event('submit', { bubbles: true, cancelable: true });
    logout.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    stop();
  });
});

describe('built assets', () => {
  it('collects same-origin static files of the page', () => {
    document.head.innerHTML = `<link rel="stylesheet" href="/static/dist/assets/main-1.css"><script type="module" src="/static/dist/assets/main-2.js"></script>
      <link rel="preload" as="font" href="/static/dist/assets/font.woff2"><link rel="stylesheet" href="https://cdn.example/x.css"><link rel="canonical" href="/static/no.html">`;
    const got = builtAssets(document, { getEntriesByType: () => [{ name: `${location.origin}/static/dist/assets/island-reader-3.js` }] as unknown as PerformanceEntryList });
    expect(got).toEqual(expect.arrayContaining(['/static/dist/assets/main-1.css', '/static/dist/assets/main-2.js', '/static/dist/assets/font.woff2', '/static/dist/assets/island-reader-3.js']));
    expect(got.some((u) => u.includes('cdn.example') || u.includes('no.html'))).toBe(false);
  });
  it('formats sizes', () => {
    expect(sizeLabel(412 * 1024 * 1024)).toBe('412 MB');
    expect(sizeLabel(4096)).toBe('4 KB');
    expect(sizeLabel(0)).toBe('0 MB');
  });
});
