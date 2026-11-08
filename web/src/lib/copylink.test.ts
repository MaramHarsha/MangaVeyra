import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { copyFrom, installCopyLinks } from './copylink';

let cleanup: (() => void) | undefined;
let copied: string[];
const fetched: string[] = [];
const fetcher = async (url: string) => {
  fetched.push(url);
  return `log of ${url}`;
};

beforeEach(() => {
  copied = [];
  fetched.length = 0;
  document.body.innerHTML = `<a id="copy" href="/admin/update/log?id=1" data-copy-url="/admin/update/log?id=1" data-copied="Log copied">Copy log</a>
    <a id="far" href="https://example.org" data-copy-url="https://example.org/x">Away</a>`;
  cleanup = installCopyLinks(document, fetcher, async (t) => {
    copied.push(t);
  });
});
afterEach(() => {
  cleanup?.();
  document.getElementById('mv-toasts')?.remove();
});

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('copy links', () => {
  it('copies the linked text instead of navigating and says so', async () => {
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true });
    document.getElementById('copy')!.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    await flush();
    await flush();
    expect(copied).toEqual(['log of /admin/update/log?id=1']);
    expect(document.getElementById('mv-toasts')?.textContent).toContain('Log copied');
  });

  it('leaves links to other origins alone', async () => {
    let preventedByCopy = true;
    const stop = (e: Event) => {
      preventedByCopy = e.defaultPrevented;
      e.preventDefault(); // jsdom cannot navigate
    };
    document.addEventListener('click', stop);
    document.getElementById('far')!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    document.removeEventListener('click', stop);
    expect(preventedByCopy).toBe(false);
    await flush();
    expect(fetched).toEqual([]);
  });

  it('reports false without a clipboard so the link opens the text', async () => {
    expect(await copyFrom('/admin/update/log', fetcher, null)).toBe(false);
    expect(await copyFrom('//evil.example/x', fetcher, async () => {})).toBe(false);
  });
});
