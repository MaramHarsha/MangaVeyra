/**
 * Realtime client for the single per-tab WebSocket at /ws. It subscribes to
 * topics ({"op":"sub","topic":...}), reconnects with backoff and dispatches
 * every server message as a "mv:ws" DOM event (detail = message) plus to
 * registered handlers.
 */
export interface WSMessage {
  topic?: string;
  type: string;
  data?: unknown;
}

type Handler = (m: WSMessage) => void;

export class Realtime {
  private ws: WebSocket | null = null;
  private topics = new Set<string>();
  private handlers = new Map<string, Set<Handler>>();
  private retry = 0;
  private closed = false;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(private readonly url: string, private readonly target: EventTarget = document) {}

  /** connect opens the socket (idempotent). */
  connect(): void {
    if (this.ws || this.closed) return;
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch {
      this.schedule();
      return;
    }
    this.ws = ws;
    ws.addEventListener('open', () => {
      this.retry = 0;
      for (const t of this.topics) this.send({ op: 'sub', topic: t });
    });
    ws.addEventListener('message', (ev) => {
      if (typeof ev.data !== 'string') return;
      let msg: WSMessage;
      try {
        msg = JSON.parse(ev.data) as WSMessage;
      } catch {
        return;
      }
      this.dispatch(msg);
    });
    ws.addEventListener('close', () => {
      this.ws = null;
      this.schedule();
    });
    ws.addEventListener('error', () => ws.close());
  }

  /** subscribe adds a topic (sent now if connected, and after every reconnect). */
  subscribe(topic: string): void {
    if (this.topics.has(topic)) return;
    this.topics.add(topic);
    this.send({ op: 'sub', topic });
  }

  /** unsubscribe removes a topic. */
  unsubscribe(topic: string): void {
    if (!this.topics.delete(topic)) return;
    this.send({ op: 'unsub', topic });
  }

  /**
   * presence sends this tab's presence state on a subscribed topic. It is
   * dropped while offline; callers send it again when the topic reports
   * "subscribed" after a reconnect.
   */
  presence(topic: string, data: unknown): void {
    if (this.topics.has(topic)) this.send({ op: 'presence', topic, data });
  }

  /** on registers a handler for a message type ("*" for all); it returns an unsubscribe function. */
  on(type: string, fn: Handler): () => void {
    let set = this.handlers.get(type);
    if (!set) this.handlers.set(type, (set = new Set()));
    set.add(fn);
    return () => set!.delete(fn);
  }

  /** close stops reconnecting and closes the socket. */
  close(): void {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    this.ws?.close();
  }

  /** backoff returns the reconnect delay for attempt n (1 s … 30 s). */
  static backoff(n: number): number {
    return Math.min(30000, 1000 * 2 ** Math.min(n, 5));
  }

  private dispatch(msg: WSMessage): void {
    for (const key of [msg.type, '*']) this.handlers.get(key)?.forEach((fn) => fn(msg));
    this.target.dispatchEvent(new CustomEvent('mv:ws', { detail: msg }));
  }

  private send(v: unknown): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(v));
  }

  private schedule(): void {
    if (this.closed) return;
    const delay = Realtime.backoff(this.retry++);
    this.timer = setTimeout(() => this.connect(), delay);
  }
}

/** wsURL builds the ws(s):// URL for the current origin. */
export function wsURL(loc: Pick<Location, 'protocol' | 'host'> = location): string {
  return `${loc.protocol === 'https:' ? 'wss' : 'ws'}://${loc.host}/ws`;
}
