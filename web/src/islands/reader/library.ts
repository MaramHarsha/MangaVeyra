/**
 * The reader island's library mode (E7 Downloads and Bookmarks tabs): marks
 * downloads that are not on this device, deletes the device copy of
 * downloads the server no longer lets the member read, and removes
 * downloads and bookmarks without a reload. The page works without it
 * (ordinary form posts).
 */
import type { LibraryProps } from './types';
import { translator } from './types';
import { cacheName, readMeta, expired, ownedBy, removeChapter, sizeLabel } from './offline';
import { call } from './api';
import { showToast } from '../../lib/toast';

export interface Row { el: HTMLElement; id: string; publicationId: string }

export function rows(root: ParentNode): Row[] {
  return Array.from(root.querySelectorAll<HTMLElement>('.mv-lib-row[data-publication]')).map((el) => ({
    el, id: el.dataset.id || '', publicationId: el.dataset.publication || '',
  }));
}

/** reconcile updates each download row with whether this device holds it, and drops revoked copies. */
export async function reconcile(doc: Document, island: HTMLElement, store: CacheStorage, t: (k: string, a?: Record<string, string | number>) => string): Promise<{ local: number; bytes: number }> {
  const revoked = (island.dataset.revoked || '').split(/\s+/).filter(Boolean);
  await Promise.all(revoked.map((id) => removeChapter(store, id)));
  let local = 0;
  let bytes = 0;
  const now = Date.now() / 1000;
  const user = doc.body.dataset.user || '';
  for (const r of rows(doc)) {
    const meta = await readMeta(store, r.publicationId);
    const status = r.el.querySelector<HTMLElement>('[data-status]');
    const ready = status?.dataset.status === 'ready';
    // Another member's copy on a shared device is not on this device for this member.
    if (meta && (expired(meta, now) || status?.dataset.status === 'expired' || !ownedBy(meta, user))) {
      await store.delete(cacheName(r.publicationId));
    } else if (meta && ready) {
      local += 1;
      bytes += meta.bytes || 0;
      continue;
    }
    if (ready) {
      r.el.classList.add('is-elsewhere');
      const meta = r.el.querySelector<HTMLElement>('[data-meta]');
      if (meta && !meta.dataset.elsewhere) {
        meta.dataset.elsewhere = 'true';
        meta.textContent = `${meta.textContent} · ${t('library.not_on_device')}`;
      }
    }
  }
  const summary = doc.querySelector<HTMLElement>('[data-download-summary]');
  if (summary) summary.textContent = t(local === 1 ? 'library.on_device_one' : 'library.on_device', { count: local, size: sizeLabel(bytes) });
  return { local, bytes };
}

export function mountLibrary(el: HTMLElement, props: LibraryProps): () => void {
  const t = translator(props.messages || {});
  const doc = el.ownerDocument;
  const store = typeof caches === 'undefined' ? null : caches;
  if (props.tab === 'downloads' && store) void reconcile(doc, el, store, t).catch(() => undefined);

  const onSubmit = async (e: Event) => {
    const form = e.target as HTMLFormElement;
    const dl = form.dataset.removeDownload;
    const bm = form.dataset.removeBookmark;
    if (!dl && !bm) return;
    e.preventDefault();
    const row = form.closest<HTMLElement>('.mv-lib-row');
    if (dl) {
      const pub = row?.dataset.publication || '';
      if (store && pub) await removeChapter(store, pub);
      const r = await call('DELETE', `${props.api.downloads}/${encodeURIComponent(dl)}`);
      if (r.ok || r.status === 404) {
        row?.closest('li')?.remove();
        showToast({ kind: 'success', title: t('download.removed') });
        if (store) void reconcile(doc, el, store, t).catch(() => undefined);
      } else showToast({ kind: 'error', title: r.offline ? t('error.network') : t('download.failed') });
      return;
    }
    const r = await call('DELETE', `${props.api.bookmarks}/${encodeURIComponent(bm!)}`);
    if (r.ok || r.status === 404) {
      row?.closest('li')?.remove();
      showToast({ kind: 'success', title: t('bookmark.removed') });
    } else showToast({ kind: 'error', title: r.offline ? t('error.network') : t('refresh.failed') });
  };
  doc.addEventListener('submit', onSubmit);
  return () => doc.removeEventListener('submit', onSubmit);
}
