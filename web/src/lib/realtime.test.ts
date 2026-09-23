import { describe, expect, it } from 'vitest';
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
