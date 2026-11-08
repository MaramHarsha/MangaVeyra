import { describe, expect, it } from 'vitest';
import { DWELL_MS, ReadingEvents } from './events';

function setup(enabled = true, total = 3) {
  let now = 0;
  const sent: { url: string; body: unknown }[] = [];
  const ev = new ReadingEvents({
    url: '/api/v1/reader/events', seriesId: 's', publicationId: 'p', total, enabled, flushMs: 0,
    now: () => now, send: (url, body) => { sent.push({ url, body: JSON.parse(body) }); return true; },
  });
  return { ev, sent, tick: (ms: number) => (now += ms) };
}

describe('reading events', () => {
  it('counts a page once it was on screen for the dwell threshold', () => {
    const { ev, sent, tick } = setup();
    ev.pageShown(1);
    tick(DWELL_MS - 1);
    ev.pageShown(2); // page 1 was too short
    tick(5000);
    ev.pageShown(3); // page 2 counts; page 3 is the last page
    ev.flush();
    const events = sent.flatMap((s) => (s.body as { events: { kind: string; page: number; ms: number }[] }).events);
    expect(events.map((e) => `${e.kind}:${e.page}`)).toEqual(['page:2', 'complete:3']);
    expect(events[0].ms).toBe(5000);
  });
  it('sends a drop when the reader leaves early, and completion only once', () => {
    const { ev, sent, tick } = setup();
    ev.pageShown(1);
    tick(3000);
    ev.pageShown(2);
    tick(3000);
    ev.leave();
    ev.leave();
    const events = sent.flatMap((s) => (s.body as { events: { kind: string; page: number }[] }).events);
    expect(events.map((e) => `${e.kind}:${e.page}`)).toEqual(['page:1', 'page:2', 'drop:2']);
  });
  it('sends nothing when no recorder is registered', () => {
    const { ev, sent, tick } = setup(false);
    ev.pageShown(1);
    tick(9000);
    ev.leave();
    expect(sent).toHaveLength(0);
  });
  it('never throws when sending fails', () => {
    const ev = new ReadingEvents({ url: '/x', seriesId: 's', publicationId: 'p', total: 1, enabled: true, flushMs: 0, send: () => { throw new Error('offline'); } });
    ev.pageShown(1);
    expect(() => ev.leave()).not.toThrow();
  });
});
