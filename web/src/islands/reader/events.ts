/**
 * Reading events for analytics.Recorder (ADR 0012 §8): a page counts once it
 * was on screen for the dwell threshold, completion fires once at the last
 * page, and leaving early sends a drop. Events are batched and sent with
 * sendBeacon (or a keepalive fetch) so reading never waits on them; the
 * server records "open" itself when it renders the chapter.
 */

export interface ReadEvent { series_id: string; publication_id: string; kind: 'page' | 'complete' | 'drop'; page: number; ms: number }

export type Send = (url: string, body: string) => boolean;

export const DWELL_MS = 2000;
export const MAX_DWELL_MS = 10 * 60 * 1000;
export const MAX_BATCH = 20;

export class ReadingEvents {
  private queue: ReadEvent[] = [];
  private page = 0;
  private since = 0;
  private completed = false;
  private left = false;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly opts: {
      url: string; seriesId: string; publicationId: string; total: number; enabled: boolean;
      send: Send; now?: () => number; flushMs?: number;
    },
  ) {}

  private now(): number {
    return (this.opts.now ?? Date.now)();
  }

  private push(kind: ReadEvent['kind'], page: number, ms = 0): void {
    if (!this.opts.enabled) return;
    this.queue.push({ series_id: this.opts.seriesId, publication_id: this.opts.publicationId, kind, page, ms: Math.round(Math.min(ms, MAX_DWELL_MS)) });
    if (this.queue.length >= MAX_BATCH) this.flush();
    else this.schedule();
  }

  private schedule(): void {
    if (this.timer || this.opts.flushMs === 0) return;
    this.timer = setTimeout(() => {
      this.timer = null;
      this.flush();
    }, this.opts.flushMs ?? 15000);
  }

  /** pageShown records the dwell of the page that was showing and starts timing page. */
  pageShown(page: number): void {
    if (page === this.page) return;
    this.endDwell();
    this.page = page;
    this.lastPage = Math.max(this.lastPage, page);
    this.since = this.now();
    if (page >= this.opts.total && this.opts.total > 0 && !this.completed) {
      this.completed = true;
      this.push('complete', page);
    }
  }

  private endDwell(): void {
    if (this.page <= 0) return;
    const ms = this.now() - this.since;
    if (ms >= DWELL_MS) this.push('page', this.page, ms);
  }

  /** leave closes the session: the last dwell, a drop when unfinished, and a final flush. */
  leave(): void {
    if (this.left) return;
    this.left = true;
    this.endDwell();
    this.page = 0;
    if (!this.completed && this.opts.total > 0) this.push('drop', Math.max(1, this.lastPage));
    this.flush();
  }

  private lastPage = 0;

  /** track remembers the furthest page for the drop event. */
  furthest(page: number): void {
    this.lastPage = Math.max(this.lastPage, page);
  }

  /** flush sends the queued events; it never throws. */
  flush(): void {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    if (!this.queue.length) return;
    const batch = this.queue.splice(0, MAX_BATCH);
    try {
      this.opts.send(this.opts.url, JSON.stringify({ events: batch }));
    } catch {
      /* analytics never breaks reading */
    }
    if (this.queue.length) this.flush();
  }

  pending(): ReadEvent[] {
    return [...this.queue];
  }
}

/** beaconSend posts a batch without waiting for it (sendBeacon, else keepalive fetch). */
export function beaconSend(url: string, body: string): boolean {
  try {
    if (typeof navigator !== 'undefined' && typeof navigator.sendBeacon === 'function') {
      return navigator.sendBeacon(url, new Blob([body], { type: 'application/json' }));
    }
  } catch {
    /* fall back to fetch */
  }
  try {
    void fetch(url, { method: 'POST', body, keepalive: true, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' } }).catch(() => undefined);
    return true;
  } catch {
    return false;
  }
}
