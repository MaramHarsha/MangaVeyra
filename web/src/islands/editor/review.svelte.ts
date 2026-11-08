/**
 * ReviewStore holds the D7 review of the page in the editor: it loads
 * /api/v1/collab/p/{project}/pages/{page}/review, sends comments, replies,
 * statuses, approvals and member changes, and reloads when another editor
 * changes the review (collab.changed on the page topic).
 */
import type {MemberScope, ReviewData, Status} from './collab';

export class ReviewError extends Error {constructor(readonly status: number, message: string) {super(message)}}

/** call sends JSON and returns the parsed body; errors carry the server's field or general message. */
export async function call<T>(url: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(url, {method, credentials: 'same-origin', cache: 'no-store', headers: body === undefined ? undefined : {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    const fields = data?.error?.fields as Record<string, string> | undefined;
    const field = fields ? Object.values(fields)[0] : '';
    throw new ReviewError(response.status, field || data?.error?.message || String(response.status));
  }
  return data as T;
}

export class ReviewStore {
  data = $state.raw<ReviewData | null>(null);
  error = $state('');
  busy = $state(false);
  loading = $state(false);
  pageID = '';
  private seq = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(readonly base: string, private offline: string) {}

  async load(pageID: string): Promise<void> {
    if (pageID !== this.pageID) this.data = null;
    this.pageID = pageID;
    const seq = ++this.seq;
    if (!pageID) return;
    this.loading = true;
    try {
      const out = await call<ReviewData>(`${this.base}/pages/${pageID}/review`);
      if (seq === this.seq) { this.data = out; this.error = ''; }
    } catch (e) {
      if (seq === this.seq) this.error = this.message(e);
    } finally {
      if (seq === this.seq) this.loading = false;
    }
  }

  /** changed reacts to a realtime message: another editor changed this page's review. */
  changed(m: {type?: string; data?: unknown}): void {
    if (m?.type !== 'collab.changed' || (m.data as {page_id?: string})?.page_id !== this.pageID) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.load(this.pageID), 150);
  }

  dispose(): void { clearTimeout(this.timer); this.seq++; }

  comment(panelID: string, body: string, assignee = ''): Promise<boolean> {
    return this.act(() => call<ReviewData>(`${this.base}/pages/${this.pageID}/comments`, 'POST', assignee ? {panel_id: panelID, body, assignee_id: assignee} : {panel_id: panelID, body}));
  }
  reply(id: string, body: string): Promise<boolean> { return this.act(() => call<ReviewData>(`${this.base}/comments/${id}/replies`, 'POST', {body})); }
  status(id: string, status: Status): Promise<boolean> { return this.act(() => call<ReviewData>(`${this.base}/comments/${id}/status`, 'POST', {status})); }
  approve(version: number): Promise<boolean> { return this.act(() => call<ReviewData>(`${this.base}/pages/${this.pageID}/approval`, 'POST', {version})); }
  addMember(username: string, role: string, scope: MemberScope): Promise<boolean> {
    return this.act(async () => { await call(`${this.base}/members`, 'POST', {username, role, scope}); return call<ReviewData>(`${this.base}/pages/${this.pageID}/review`); });
  }
  updateMember(userID: string, role: string, scope: MemberScope): Promise<boolean> {
    return this.act(async () => { await call(`${this.base}/members/${userID}`, 'PATCH', {role, scope}); return call<ReviewData>(`${this.base}/pages/${this.pageID}/review`); });
  }
  removeMember(userID: string): Promise<boolean> {
    return this.act(async () => { await call(`${this.base}/members/${userID}`, 'DELETE'); return call<ReviewData>(`${this.base}/pages/${this.pageID}/review`); });
  }

  private async act(fn: () => Promise<ReviewData>): Promise<boolean> {
    if (this.busy) return false;
    const page = this.pageID;
    this.busy = true;
    try {
      const out = await fn();
      if (out.page_id === this.pageID) { this.seq++; this.data = out; }
      this.error = '';
      return page === this.pageID;
    } catch (e) {
      this.error = this.message(e);
      return false;
    } finally {
      this.busy = false;
    }
  }

  private message(e: unknown): string { return e instanceof ReviewError ? e.message : this.offline; }
}
