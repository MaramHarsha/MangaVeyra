/**
 * Reader island entry. On /s/{series}/{chapter} it replaces the server-
 * rendered stacked pages with the E3/E4 reader; on the E7 library's
 * Downloads and Bookmarks tabs it runs the library helpers.
 */
import { mount as mountSvelte, unmount } from 'svelte';
import Reader from './Reader.svelte';
import { mountLibrary } from './library';
import type { LibraryProps, ReaderProps } from './types';

export function mount(el: HTMLElement, props: Record<string, unknown>): () => void {
  if (props.view === 'library') return mountLibrary(el, props as unknown as LibraryProps);
  if (props.view !== 'reader' || !Array.isArray(props.pages)) return () => undefined;
  el.replaceChildren();
  el.classList.add('mv-rd--live');
  const app = mountSvelte(Reader, { target: el, props: { boot: props as unknown as ReaderProps, host: el } });
  return () => void unmount(app);
}
