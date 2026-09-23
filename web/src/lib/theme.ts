/**
 * Interface preferences applied to <html> without a reload: theme, motion,
 * text size, textures, focus rings, link underlines and the dyslexia font.
 * The server renders the same attributes, so pages arrive correct without JS.
 */
export type Theme = 'dark' | 'light' | 'system';

export interface UIPrefs {
  theme?: Theme;
  /** "system" | "on" (reduce) | "off" (full motion). */
  motion?: 'system' | 'on' | 'off';
  textSize?: number;
  textures?: boolean;
  strongFocus?: boolean;
  underlineLinks?: boolean;
  dyslexiaFont?: boolean;
}

const TEXT_SIZES = [87, 100, 112, 125, 150];

/** nearestTextSize snaps a percentage to the sizes the stylesheet supports. */
export function nearestTextSize(n: number): number {
  let best = 100;
  for (const s of TEXT_SIZES) if (Math.abs(s - n) < Math.abs(best - n)) best = s;
  return best;
}

function setAttr(el: HTMLElement, name: string, value: string | null): void {
  if (value === null) el.removeAttribute(name);
  else el.setAttribute(name, value);
}

/** applyPrefs writes the data-* attributes the CSS reads. Unset fields are left alone. */
export function applyPrefs(p: UIPrefs, root: HTMLElement = document.documentElement): void {
  if (p.theme) root.dataset.theme = p.theme;
  if (p.motion) setAttr(root, 'data-motion', p.motion === 'on' ? 'reduce' : p.motion === 'off' ? 'full' : null);
  if (typeof p.textSize === 'number') {
    const s = nearestTextSize(p.textSize);
    setAttr(root, 'data-text-size', s === 100 ? null : String(s));
  }
  if (typeof p.textures === 'boolean') setAttr(root, 'data-textures', p.textures ? null : 'off');
  if (typeof p.strongFocus === 'boolean') setAttr(root, 'data-focus', p.strongFocus ? 'strong' : null);
  if (typeof p.underlineLinks === 'boolean') setAttr(root, 'data-links', p.underlineLinks ? 'underline' : null);
  if (typeof p.dyslexiaFont === 'boolean') setAttr(root, 'data-font', p.dyslexiaFont ? 'dyslexic' : null);
}

/** resolvedTheme returns "dark" or "light" for the current attribute, following the OS for "system". */
export function resolvedTheme(root: HTMLElement = document.documentElement, mql?: { matches: boolean }): 'dark' | 'light' {
  const t = root.dataset.theme;
  if (t === 'light' || t === 'dark') return t;
  const m = mql ?? (typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: light)') : { matches: false });
  return m.matches ? 'light' : 'dark';
}

/** nextTheme is the theme the top-bar toggle switches to. */
export function nextTheme(current: 'dark' | 'light'): 'dark' | 'light' {
  return current === 'dark' ? 'light' : 'dark';
}

/** persistTheme stores the choice (member preference, or a cookie for visitors) on the server. */
export async function persistTheme(theme: Theme, url = '/settings/theme'): Promise<boolean> {
  const body = new URLSearchParams({ theme });
  try {
    const res = await fetch(url, {
      method: 'POST',
      body,
      credentials: 'same-origin',
      headers: { 'HX-Request': 'true', Accept: 'application/json' },
    });
    return res.ok;
  } catch {
    return false;
  }
}
