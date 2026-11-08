/**
 * Copy links: an <a href data-copy-url> opens the text (correct without
 * JavaScript); with script it copies the text at data-copy-url to the
 * clipboard instead and shows data-copied as a toast (G14 "Copy log").
 */
import { showToast } from './toast';

export type Fetcher = (url: string) => Promise<string>;
export type Writer = (text: string) => Promise<void>;

const defaultFetch: Fetcher = async (url) => {
  const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/plain' } });
  if (!res.ok) throw new Error(`${res.status}`);
  return res.text();
};

const defaultWrite = (): Writer | null =>
  typeof navigator !== 'undefined' && navigator.clipboard?.writeText ? (t) => navigator.clipboard.writeText(t) : null;

/** copyFrom copies the text at url; it resolves false when copying is not possible. */
export async function copyFrom(url: string, fetcher: Fetcher = defaultFetch, writer: Writer | null = defaultWrite()): Promise<boolean> {
  if (!writer || !url.startsWith('/') || url.startsWith('//')) return false;
  await writer(await fetcher(url));
  return true;
}

/** installCopyLinks wires one delegated click listener and returns a remover. */
export function installCopyLinks(doc: Document = document, fetcher: Fetcher = defaultFetch, writer: Writer | null = defaultWrite()): () => void {
  const onClick = (e: Event) => {
    const el = (e.target as Element | null)?.closest?.<HTMLElement>('[data-copy-url]');
    if (!el || !writer) return;
    const url = el.dataset.copyUrl || '';
    if (!url.startsWith('/') || url.startsWith('//')) return;
    e.preventDefault();
    copyFrom(url, fetcher, writer).then(
      () => showToast({ kind: 'success', title: el.dataset.copied || 'Copied' }),
      () => {
        window.location.href = url; // fall back to opening the text
      },
    );
  };
  doc.addEventListener('click', onClick);
  return () => doc.removeEventListener('click', onClick);
}
