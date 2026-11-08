/** Small JSON helpers for the reader's same-origin API calls. */

export interface Result<T> { ok: boolean; status: number; data: T | null; offline: boolean }

export async function call<T>(method: string, url: string, body?: unknown, fetchFn: typeof fetch = fetch): Promise<Result<T>> {
  try {
    const res = await fetchFn(url, {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let data: T | null = null;
    if (res.status !== 204) {
      try {
        data = (await res.json()) as T;
      } catch {
        data = null;
      }
    }
    return { ok: res.ok, status: res.status, data, offline: false };
  } catch {
    return { ok: false, status: 0, data: null, offline: true };
  }
}

/** PendingProgress keeps the last unsent reading position so it syncs when the reader is back online. */
const PENDING_KEY = 'mv:reader:pending-progress';

export interface ProgressBody { series_id: string; publication_id: string; page: number; percent: number; completed: boolean }

export function rememberPending(p: ProgressBody): void {
  try {
    const all = JSON.parse(localStorage.getItem(PENDING_KEY) || '{}') as Record<string, ProgressBody>;
    all[p.publication_id] = p;
    localStorage.setItem(PENDING_KEY, JSON.stringify(all));
  } catch {
    /* storage unavailable: the position is simply not kept */
  }
}

export function takePending(): ProgressBody[] {
  try {
    const all = JSON.parse(localStorage.getItem(PENDING_KEY) || '{}') as Record<string, ProgressBody>;
    localStorage.removeItem(PENDING_KEY);
    return Object.values(all);
  } catch {
    return [];
  }
}
