/**
 * MangaVeyra browser entry: HTMX (+ WebSocket extension), theme and
 * preference application, <dialog> modals and drawers, dropdowns, toasts
 * (HX-Trigger and realtime), keyboard shortcuts and the island loader.
 * Pages work without it; it only adds convenience and live updates.
 */
import '@tabler/icons-webfont/dist/tabler-icons.min.css';
import './styles/index.css';
import htmx from 'htmx.org';
import 'htmx-ext-ws';
import { applyPrefs, nextTheme, persistTheme, resolvedTheme, type UIPrefs } from './lib/theme';
import { normalizeToast, showToast } from './lib/toast';
import { shortcutFor } from './lib/keys';
import { Realtime, wsURL } from './lib/realtime';
import { passwordStrength } from './lib/password';
import './lib/studio';
import './lib/export';
import { installForms } from './lib/forms';
import { installCounters } from './lib/counter';
import { installAutoSubmit } from './lib/filters';
import { installPageTurns } from './lib/transitions';
import { installQueueRefresh } from './lib/queue';
import { updateUnread } from './lib/unread';
import './lib/models';
import { mountIslands, unmountIslands } from './islands/registry';

declare global {
  interface Window {
    htmx: typeof htmx;
    mv: {
      toast: typeof showToast;
      realtime: Realtime | null;
      applyPrefs: typeof applyPrefs;
      mountIslands: typeof mountIslands;
    };
  }
}

const root = document.documentElement;
root.classList.add('js');
installForms();
installCounters();
installAutoSubmit();

// ---- HTMX -------------------------------------------------------------------

window.htmx = htmx;
htmx.config.includeIndicatorStyles = false; // the CSP forbids injected <style>
htmx.config.allowEval = false;
htmx.config.allowScriptTags = false;
htmx.config.selfRequestsOnly = true;
htmx.config.historyCacheSize = 0;
htmx.config.responseHandling = [
  { code: '204', swap: false },
  { code: '[23]..', swap: true },
  { code: '422', swap: true },
  { code: '[45]..', swap: false, error: true },
];

document.body.addEventListener('mv:toast', (e) => {
  const d = (e as CustomEvent).detail as { value?: unknown } | undefined;
  const t = normalizeToast(d && typeof d === 'object' && 'value' in d ? d.value : d);
  if (t) showToast(t);
});

document.body.addEventListener('mv:prefs', (e) => {
  applyPrefs((e as CustomEvent).detail as UIPrefs);
});

document.body.addEventListener('htmx:responseError', (e) => {
  const xhr = (e as CustomEvent).detail?.xhr as XMLHttpRequest | undefined;
  if (xhr && xhr.getResponseHeader('HX-Trigger')) return; // the server already explained
  showToast({ kind: 'error', title: root.dataset.errorLabel || 'Something went wrong', body: xhr ? `${xhr.status}` : undefined });
});

document.body.addEventListener('htmx:sendError', () => {
  showToast({ kind: 'error', title: root.dataset.offlineLabel || 'You are offline' });
});

document.body.addEventListener('htmx:beforeSwap', (e) => {
  const target = (e as CustomEvent).detail?.target as HTMLElement | undefined;
  if (target) unmountIslands(target);
});

document.body.addEventListener('htmx:afterSettle', (e) => {
  const target = (e as CustomEvent).detail?.target as HTMLElement | undefined;
  void mountIslands(target ?? document);
  enhance(target ?? document);
});

// ---- dialogs, drawers, dropdowns ---------------------------------------------

document.addEventListener('click', (e) => {
  const t = e.target as HTMLElement;
  const opener = t.closest<HTMLElement>('[data-dialog-open]');
  if (opener) {
    const dlg = document.getElementById(opener.dataset.dialogOpen ?? '');
    if (dlg instanceof HTMLDialogElement) {
      e.preventDefault();
      dlg.showModal();
    }
    return;
  }
  const closer = t.closest<HTMLElement>('[data-dialog-close]');
  if (closer) {
    closer.closest('dialog')?.close();
    return;
  }
  if (t instanceof HTMLDialogElement && t.open) {
    // Click on the backdrop (outside the dialog box).
    const r = t.getBoundingClientRect();
    const ev = e as MouseEvent;
    if (ev.clientX < r.left || ev.clientX > r.right || ev.clientY < r.top || ev.clientY > r.bottom) t.close();
  }
  // Close dropdowns that do not contain the click.
  document.querySelectorAll<HTMLDetailsElement>('details.mv-dropdown[open]').forEach((d) => {
    if (!d.contains(t)) d.open = false;
  });
});

document.addEventListener(
  'toggle',
  (e) => {
    const d = e.target;
    if (d instanceof HTMLDetailsElement && d.classList.contains('mv-dropdown') && d.open) {
      document.querySelectorAll<HTMLDetailsElement>('details.mv-dropdown[open]').forEach((o) => {
        if (o !== d) o.open = false;
      });
    }
  },
  true,
);

// ---- keyboard --------------------------------------------------------------

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    const open = document.querySelector<HTMLDetailsElement>('details.mv-dropdown[open]');
    if (open) {
      open.open = false;
      open.querySelector('summary')?.focus();
    }
    return;
  }
  const action = shortcutFor(e);
  if (action === 'search') {
    const input = document.getElementById('mv-search') as HTMLInputElement | null;
    if (input && input.offsetParent !== null) {
      e.preventDefault();
      input.focus();
      input.select();
    }
  } else if (action === 'help') {
    const sheet = document.getElementById('mv-shortcuts');
    if (sheet instanceof HTMLDialogElement) {
      e.preventDefault();
      sheet.showModal();
    }
  }
});

// ---- theme toggle -------------------------------------------------------------

document.addEventListener('click', (e) => {
  const btn = (e.target as HTMLElement).closest<HTMLElement>('[data-theme-toggle]');
  if (!btn) return;
  e.preventDefault();
  const theme = nextTheme(resolvedTheme(root));
  applyPrefs({ theme });
  btn.setAttribute('aria-pressed', theme === 'light' ? 'true' : 'false');
  void persistTheme(theme, btn.dataset.themeToggle || '/settings/theme');
});

// Clear offline chapter downloads kept by the service worker / reader on this
// device, then the member's download records on the server (ADR 0012 §7). A
// failed server call is not fatal: the library marks rows missing on this device.
document.addEventListener('click', (e) => {
  const btn = (e.target as HTMLElement).closest<HTMLElement>('[data-clear-downloads]');
  if (!btn || !('caches' in window)) return;
  e.preventDefault();
  void caches.keys().then(async (keys) => {
    await Promise.all(keys.filter((k) => k.startsWith('mv-offline')).map((k) => caches.delete(k)));
    await fetch('/api/v1/reader/downloads', { method: 'DELETE', credentials: 'same-origin', headers: { Accept: 'application/json' } }).catch(() => undefined);
    showToast({ kind: 'success', title: btn.dataset.done || root.dataset.doneLabel || 'Done' });
  });
});

// ---- form enhancements ---------------------------------------------------------

/** enhance wires sliders and password strength meters under root. */
function enhance(root: ParentNode): void {
  root.querySelectorAll<HTMLInputElement>('input.mv-range').forEach((input) => {
    const sync = () => {
      const min = Number(input.min || 0);
      const max = Number(input.max || 100);
      const pct = max > min ? ((Number(input.value) - min) / (max - min)) * 100 : 0;
      input.style.setProperty('--mv-p', `${pct}%`);
      const out = input.dataset.output ? document.getElementById(input.dataset.output) : null;
      if (out) out.textContent = `${input.value}${input.dataset.suffix ?? ''}`;
    };
    if (!input.dataset.enhanced) {
      input.addEventListener('input', sync);
      input.dataset.enhanced = 'true';
    }
    sync();
  });
  root.querySelectorAll<HTMLInputElement>('input[data-strength]').forEach((input) => {
    const out = document.getElementById(input.dataset.strength ?? '');
    if (!out || input.dataset.enhanced) return;
    input.dataset.enhanced = 'true';
    const labels = (out.dataset.labels ?? '').split('|');
    const min = Number(input.getAttribute('minlength') || 10);
    const sync = () => {
      const s = passwordStrength(input.value, min);
      out.dataset.level = String(s.level);
      out.textContent = s.level === 0 ? '' : labels[s.level - 1] ?? s.key;
    };
    input.addEventListener('input', sync);
    sync();
  });
}

// ---- realtime ------------------------------------------------------------------

let realtime: Realtime | null = null;
const userID = document.body.dataset.user;
if (userID && 'WebSocket' in window) {
  realtime = new Realtime(wsURL());
  realtime.subscribe(`user:${userID}`);
  realtime.on('notification.new', (m) => {
    const data = m.data as { notification?: { title?: string; body?: string; link?: string; kind?: string }; unread?: number };
    const n = data?.notification;
    if (n?.title) {
      showToast({
        kind: n.kind === 'job_failed' ? 'error' : n.kind === 'job_done' ? 'success' : 'info',
        title: n.title,
        body: n.body,
        href: n.link,
        action: root.dataset.viewLabel,
      });
    }
    updateUnread(data?.unread ?? 1);
  });
  realtime.on('notification.read', (m) => updateUnread((m.data as { unread?: number })?.unread ?? 0));
  realtime.connect();
}

// ---- page turns ---------------------------------------------------------------

installPageTurns(window, root);

// ---- start -------------------------------------------------------------------

window.mv = { toast: showToast, realtime, applyPrefs, mountIslands };
installQueueRefresh(document, window, fetch, realtime);
enhance(document);
void mountIslands(document);

// Toasts queued by the server for a full page load (after a redirect).
document.querySelectorAll<HTMLTemplateElement>('template[data-toast]').forEach((tpl) => {
  try {
    const t = normalizeToast(JSON.parse(tpl.dataset.toast ?? ''));
    if (t) showToast(t);
  } catch {
    /* ignore malformed flash */
  }
  tpl.remove();
});

// Offline-capable PWA.
if ('serviceWorker' in navigator && root.dataset.sw === 'on') {
  navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => undefined);
}
