/**
 * The service worker's offline chapters (internal/web/static/sw.js, ADR 0012
 * §7): it answers from a chapter cache only when the network fails, and
 * never from one that has expired or has no expiry.
 */
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// sw.js is served as-is (it lives outside the Vite root), so it is read as text.
const swSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../../internal/web/static/sw.js'), 'utf8');

const ORIGIN = 'https://mv.test';

type Req = { url: string; method: string; mode: string };
const keyOf = (req: string | Req, ignoreSearch = false) => {
  const u = new URL(typeof req === 'string' ? req : req.url, ORIGIN);
  return u.pathname + (ignoreSearch ? '' : u.search);
};

class FakeCache {
  store = new Map<string, Response>();
  async put(req: string | Req, res: Response) {
    this.store.set(keyOf(req), res);
  }
  async match(req: string | Req, opts: { ignoreSearch?: boolean } = {}) {
    const want = keyOf(req, opts.ignoreSearch);
    for (const [k, v] of this.store) {
      if ((opts.ignoreSearch ? keyOf(k, true) : k) === want) return v.clone();
    }
    return undefined;
  }
  async addAll() {}
}
class FakeCaches {
  caches = new Map<string, FakeCache>();
  async open(name: string) {
    if (!this.caches.has(name)) this.caches.set(name, new FakeCache());
    return this.caches.get(name)!;
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
  async match(req: string | Req) {
    for (const c of this.caches.values()) {
      const hit = await c.match(req);
      if (hit) return hit;
    }
    return undefined;
  }
}

type FetchEvent = { request: Req; respondWith: (p: Promise<Response | undefined>) => void };

/** loadWorker runs sw.js against fake caches and fetch, returning its fetch handler. */
function loadWorker(caches: FakeCaches, online: boolean) {
  const listeners: Record<string, (e: FetchEvent) => void> = {};
  const self = {
    location: { origin: ORIGIN },
    addEventListener: (type: string, fn: (e: FetchEvent) => void) => (listeners[type] = fn),
    skipWaiting: async () => undefined,
    clients: { claim: async () => undefined },
  };
  const fetchFn = async (req: Req) => {
    if (!online) throw new TypeError('Failed to fetch');
    return new Response(`network ${keyOf(req)}`);
  };
  new Function('self', 'caches', 'fetch', swSource)(self, caches, fetchFn);
  return async (url: string, mode = 'no-cors') => {
    let answer: Promise<Response | undefined> | undefined;
    listeners.fetch({ request: { url: ORIGIN + url, method: 'GET', mode }, respondWith: (p) => (answer = p) });
    return answer ? await answer : undefined;
  };
}

const page = '/media/p/1.png?exp=1&sig=a';
const reader = '/s/tidebreak-courier/8';
const nowSeconds = () => Math.floor(Date.now() / 1000);

async function chapter(caches: FakeCaches, id: string, meta: Record<string, unknown> | null) {
  const cache = await caches.open(`mv-offline-${id}`);
  await cache.put(page, new Response('cached page image'));
  await cache.put(reader, new Response('cached reader page'));
  if (meta) await cache.put(`/__mv-offline/${id}`, new Response(JSON.stringify({ publication_id: id, ...meta })));
}

describe('service worker offline chapters', () => {
  it('goes to the network while it works', async () => {
    const caches = new FakeCaches();
    await chapter(caches, '01a', { expires_at: nowSeconds() + 3600 });
    const res = await loadWorker(caches, true)(page);
    expect(await res!.text()).toBe('network /media/p/1.png?exp=1&sig=a');
  });
  it('answers from a chapter that has not expired when the network fails', async () => {
    const caches = new FakeCaches();
    await chapter(caches, '01a', { expires_at: nowSeconds() + 3600 });
    const fetchVia = loadWorker(caches, false);
    expect(await (await fetchVia('/media/p/1.png?exp=2&sig=other'))!.text()).toBe('cached page image');
    expect(await (await fetchVia(reader, 'navigate'))!.text()).toBe('cached reader page');
  });
  it('marks a page it serves from a cache as an offline copy, so its data-user never stands for the current user', async () => {
    const caches = new FakeCaches();
    const html = (body: string) => new Response(`<!doctype html><html><body class="mv-body"${body}><main></main></body></html>`,
      { headers: { 'Content-Type': 'text/html; charset=utf-8' } });
    const cache = await caches.open('mv-offline-01a');
    await cache.put(reader, html(' data-user="01hzrin"'));
    await cache.put('/__mv-offline/01a', new Response(JSON.stringify({ publication_id: '01a', expires_at: nowSeconds() + 3600 })));
    await (await caches.open('mv-v1-static')).put('/offline', html(''));
    const fetchVia = loadWorker(caches, false);
    const page = await fetchVia(reader, 'navigate');
    expect(await page!.text()).toContain('<body data-offline-copy class="mv-body" data-user="01hzrin">');
    expect(page!.headers.get('Content-Type')).toContain('text/html');
    expect(await (await fetchVia('/library/history', 'navigate'))!.text()).toContain('<body data-offline-copy class="mv-body">');
    // Online, the server's page is passed through untouched.
    expect(await (await loadWorker(caches, true)(reader, 'navigate'))!.text()).toBe(`network ${reader}`);
  });
  it('never answers from an expired chapter, or one without an expiry, and deletes it', async () => {
    for (const meta of [{ expires_at: nowSeconds() - 1 }, { expires_at: 0 }, {}, { expires_at: 'soon' }, null]) {
      const caches = new FakeCaches();
      await chapter(caches, '01a', meta);
      await (await caches.open('mv-v1-static')).put('/offline', new Response('offline page'));
      const fetchVia = loadWorker(caches, false);
      const img = await fetchVia(page);
      expect(img!.type, JSON.stringify(meta)).toBe('error');
      expect(await (await fetchVia(reader, 'navigate'))!.text()).toBe('offline page');
      if (meta) expect(await caches.has('mv-offline-01a'), JSON.stringify(meta)).toBe(false);
    }
  });
});
