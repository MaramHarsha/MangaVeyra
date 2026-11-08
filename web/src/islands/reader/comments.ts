/**
 * Chapter and panel comments in the reader (ADR 0012 §9), through the social
 * module's API. Frozen panels carry no IDs, so a panel comment targets the
 * publication with an anchor {page, panel} (panel = reading order on the
 * page). Until the social API exists every call degrades to "unavailable".
 */
import { call } from './api';

export interface Comment {
  id: string; body: string; anchor?: unknown; created_at?: string; hidden?: boolean;
  author?: { id?: string; username?: string; display_name?: string } | string; author_name?: string;
}

/** authorName is the display name of a comment's author (the social API sends an object). */
export function authorName(c: Comment): string {
  if (typeof c.author === 'string') return c.author;
  return c.author?.display_name || c.author?.username || c.author_name || '';
}

export interface Anchor { page: number; panel: number }

export function listURL(api: string, kind: 'chapter' | 'panel', publicationId: string, seriesId: string, page = 0): string {
  const q = new URLSearchParams({ target_kind: kind, target_id: publicationId, series: seriesId });
  if (page > 0) q.set('page', String(page));
  return `${api}?${q.toString()}`;
}

/** anchorOf reads a comment's {page, panel} anchor (JSON object or string). */
export function anchorOf(c: Comment): Anchor | null {
  let a = c.anchor;
  if (typeof a === 'string') {
    try {
      a = JSON.parse(a);
    } catch {
      return null;
    }
  }
  if (!a || typeof a !== 'object') return null;
  const r = a as Record<string, unknown>;
  const page = Number(r.page);
  const panel = Number(r.panel ?? 0);
  return Number.isInteger(page) && page > 0 && Number.isInteger(panel) && panel >= 0 ? { page, panel } : null;
}

/** items reads a list response ({items:[…]}, {comments:[…]} or a bare array). */
export function items(data: unknown): Comment[] {
  if (Array.isArray(data)) return data as Comment[];
  if (data && typeof data === 'object') {
    const d = data as Record<string, unknown>;
    if (Array.isArray(d.items)) return d.items as Comment[];
    if (Array.isArray(d.comments)) return d.comments as Comment[];
  }
  return [];
}

export type LoadState = 'loading' | 'ready' | 'unavailable';

/** load fetches chapter comments and the panel comments of one page. */
export async function load(api: string, publicationId: string, seriesId: string, page: number, fetchFn: typeof fetch = fetch): Promise<{ state: LoadState; chapter: Comment[]; panel: Comment[] }> {
  const [ch, pn] = await Promise.all([
    call<unknown>('GET', listURL(api, 'chapter', publicationId, seriesId), undefined, fetchFn),
    call<unknown>('GET', listURL(api, 'panel', publicationId, seriesId, page), undefined, fetchFn),
  ]);
  if (!ch.ok && !pn.ok) return { state: 'unavailable', chapter: [], panel: [] };
  const visible = (c: Comment) => !c.hidden;
  const panel = items(pn.data).filter((c) => visible(c) && anchorOf(c)?.page === page);
  return { state: 'ready', chapter: items(ch.data).filter(visible), panel };
}

export function postBody(seriesId: string, publicationId: string, body: string, anchor: Anchor | null) {
  return {
    series_id: seriesId,
    target_kind: anchor && anchor.panel > 0 ? 'panel' : 'chapter',
    target_id: publicationId,
    anchor: anchor && anchor.panel > 0 ? anchor : {},
    body,
  };
}
