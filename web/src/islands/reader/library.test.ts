import { describe, expect, it } from 'vitest';
import { reconcile } from './library';
import { cacheName, metaKey } from './offline';

class FakeCache {
  store = new Map<string, Response>();
  async put(req: string, res: Response) {
    this.store.set(req, res);
  }
  async match(req: string) {
    return this.store.get(req)?.clone();
  }
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
}

const t = (k: string, a: Record<string, string | number> = {}) => `${k}:${Object.values(a).join(',')}`;

async function stored(caches: FakeCaches, id: string, owner: string) {
  const cache = await caches.open(cacheName(id));
  await cache.put(metaKey(id), new Response(JSON.stringify({ publication_id: id, owner, expires_at: 2_000_000_000, bytes: 1024 })));
}

describe('library downloads on a shared device', () => {
  it("counts only this member's copies and deletes another member's", async () => {
    document.body.dataset.user = '01hzrin';
    document.body.innerHTML = `<div id="island" data-revoked=""></div>
      <article class="mv-lib-row" data-publication="01mine" data-id="d1"><span data-meta>Ch. 1</span><span data-status="ready"></span></article>
      <article class="mv-lib-row" data-publication="01theirs" data-id="d2"><span data-meta>Ch. 2</span><span data-status="ready"></span></article>
      <p data-download-summary></p>`;
    const caches = new FakeCaches();
    await stored(caches, '01mine', '01hzrin');
    await stored(caches, '01theirs', '01hzkai');
    const got = await reconcile(document, document.getElementById('island')!, caches as unknown as CacheStorage, t);
    expect(got).toEqual({ local: 1, bytes: 1024 });
    expect(await caches.keys()).toEqual([cacheName('01mine')]);
    const theirs = document.querySelector<HTMLElement>('[data-publication="01theirs"]')!;
    expect(theirs.classList.contains('is-elsewhere')).toBe(true);
    delete document.body.dataset.user;
  });
});
