/**
 * Per-series reading settings. Members keep mode, direction, background and
 * zoom on the server (reader_prefs); settings without a column there
 * (brightness, tap zones, reduced motion, panel comments, read-aloud) and
 * everything for anonymous visitors stay in this browser's localStorage,
 * always behind try/catch (private windows and blocked storage throw).
 */
import type { Background, Direction, Mode, Prefs } from './types';

export interface LocalPrefs {
  mode?: Mode;
  direction?: Direction;
  background?: Background;
  zoom?: number;
  brightness?: number;
  tap_zones?: boolean;
  reduced_motion?: boolean;
  panel_comments?: boolean;
  read_aloud?: boolean;
  settings_open?: boolean;
}

/** Settings the reader works with: the server prefs plus device-only values. */
export interface ReaderSettings extends Prefs { brightness: number; settings_open: boolean | null }

const KEY = 'mv:reader:';
const MODES: Mode[] = ['paged', 'spread', 'guided', 'webtoon', 'auto'];
const DIRS: Direction[] = ['rtl', 'ltr', 'vertical'];
const BGS: Background[] = ['ink', 'grey', 'paper'];

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

/** storage returns window.localStorage, or null when the browser blocks it. */
export function storage(): StorageLike | null {
  try {
    return typeof window !== 'undefined' ? window.localStorage : null;
  } catch {
    return null;
  }
}

export function clampZoom(z: unknown): number {
  const n = typeof z === 'number' && Number.isFinite(z) ? z : 1;
  return Math.min(3, Math.max(0.5, Math.round(n * 100) / 100));
}

export function clampBrightness(b: unknown): number {
  const n = typeof b === 'number' && Number.isFinite(b) ? b : 1;
  return Math.min(1, Math.max(0.4, Math.round(n * 100) / 100));
}

/** sanitize keeps only well-formed values. */
export function sanitize(raw: unknown): LocalPrefs {
  if (!raw || typeof raw !== 'object') return {};
  const r = raw as Record<string, unknown>;
  const out: LocalPrefs = {};
  if (MODES.includes(r.mode as Mode)) out.mode = r.mode as Mode;
  if (DIRS.includes(r.direction as Direction)) out.direction = r.direction as Direction;
  if (BGS.includes(r.background as Background)) out.background = r.background as Background;
  if (typeof r.zoom === 'number') out.zoom = clampZoom(r.zoom);
  if (typeof r.brightness === 'number') out.brightness = clampBrightness(r.brightness);
  for (const k of ['tap_zones', 'reduced_motion', 'panel_comments', 'read_aloud', 'settings_open'] as const) {
    if (typeof r[k] === 'boolean') out[k] = r[k] as boolean;
  }
  return out;
}

export function loadLocal(store: StorageLike | null, seriesId: string): LocalPrefs {
  if (!store) return {};
  try {
    const raw = store.getItem(KEY + seriesId);
    return raw ? sanitize(JSON.parse(raw)) : {};
  } catch {
    return {};
  }
}

export function saveLocal(store: StorageLike | null, seriesId: string, p: LocalPrefs): boolean {
  if (!store) return false;
  try {
    store.setItem(KEY + seriesId, JSON.stringify(sanitize(p)));
    return true;
  } catch {
    return false;
  }
}

/**
 * merge combines the server's resolved prefs with the device's per-series
 * values. Signed-in members take mode, direction, background and zoom from
 * the server once they stored them; everything else comes from the device.
 */
export function merge(server: Prefs, local: LocalPrefs, signedIn: boolean, webtoon: boolean): ReaderSettings {
  const fromServer = signedIn && server.stored;
  const out: ReaderSettings = {
    ...server,
    brightness: clampBrightness(local.brightness ?? 1),
    tap_zones: local.tap_zones ?? server.tap_zones,
    reduced_motion: local.reduced_motion ?? server.reduced_motion,
    panel_comments: local.panel_comments ?? server.panel_comments,
    read_aloud: local.read_aloud ?? server.read_aloud,
    settings_open: local.settings_open ?? null,
  };
  if (!fromServer) {
    if (local.mode) out.mode = local.mode;
    if (local.direction) out.direction = local.direction;
    if (local.background) out.background = local.background;
    if (local.zoom !== undefined) out.zoom = clampZoom(local.zoom);
  }
  if (webtoon) {
    out.direction = 'vertical';
    if (out.mode !== 'guided') out.mode = 'webtoon';
  } else if (out.direction === 'vertical') {
    out.direction = server.direction === 'vertical' ? 'rtl' : server.direction;
  }
  return out;
}

/** serverRow is what PUT /api/v1/reader/prefs/{series} stores. */
export function serverRow(s: ReaderSettings, mode: Exclude<Mode, 'auto'>): { mode: string; direction: string; background: string; zoom: number } {
  return { mode, direction: s.direction, background: s.background, zoom: clampZoom(s.zoom) };
}

/** localRow is what the device keeps for the series. */
export function localRow(s: ReaderSettings, signedIn: boolean): LocalPrefs {
  const out: LocalPrefs = {
    brightness: s.brightness, tap_zones: s.tap_zones, reduced_motion: s.reduced_motion,
    panel_comments: s.panel_comments, read_aloud: s.read_aloud,
  };
  if (s.settings_open !== null) out.settings_open = s.settings_open;
  if (!signedIn) Object.assign(out, { mode: s.mode, direction: s.direction, background: s.background, zoom: s.zoom });
  return out;
}
