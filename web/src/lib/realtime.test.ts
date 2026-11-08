import { describe, expect, it, vi } from 'vitest';
import { Realtime, wsURL } from './realtime';

describe('realtime helpers', () => {
  it('builds same-origin socket URLs', () => {
    expect(wsURL({ protocol: 'https:', host: 'studio.local' })).toBe('wss://studio.local/ws');
    expect(wsURL({ protocol: 'http:', host: 'localhost:8080' })).toBe('ws://localhost:8080/ws');
  });
  it('backs off up to 30 s', () => {
    expect(Realtime.backoff(0)).toBe(1000);
    expect(Realtime.backoff(3)).toBe(8000);
    expect(Realtime.backoff(10)).toBe(30000);
  });
});

describe('presence', () => {
  it('sends presence only on subscribed topics while connected', () => {
    const sent: string[] = [];
    class FakeSocket extends EventTarget {
      static OPEN = 1;
      readyState = 1;
      send(v: string) { sent.push(v); }
      close() {}
    }
    vi.stubGlobal('WebSocket', FakeSocket);
    const rt = new Realtime('ws://test/ws', new EventTarget());
    rt.connect();
    rt.presence('doc:page:a', { x: 0.5, y: 0.5, color: 2 });
    rt.subscribe('doc:page:a');
    rt.presence('doc:page:a', { x: 0.5, y: 0.5, color: 2 });
    expect(sent.map((s) => JSON.parse(s))).toEqual([
      { op: 'sub', topic: 'doc:page:a' },
      { op: 'presence', topic: 'doc:page:a', data: { x: 0.5, y: 0.5, color: 2 } },
    ]);
    vi.unstubAllGlobals();
  });
});
