// @vitest-environment jsdom
import { afterEach, expect, it } from 'vitest';
import { updateUnread } from './unread';

afterEach(() => { document.body.innerHTML = ''; });

it('updates a previously empty bell for screen readers and clears it again', () => {
  document.body.innerHTML = '<a data-unread-base="Notifications" data-unread-label="{count} unread" aria-label="Notifications"><span data-unread-dot hidden></span><span data-unread-count hidden></span></a>';
  updateUnread(3);
  expect(document.querySelector('a')!.getAttribute('aria-label')).toBe('Notifications · 3 unread');
  expect(document.querySelector<HTMLElement>('[data-unread-dot]')!.hidden).toBe(false);
  expect(document.querySelector<HTMLElement>('[data-unread-count]')!.hidden).toBe(false);
  updateUnread(0);
  expect(document.querySelector('a')!.getAttribute('aria-label')).toBe('Notifications');
  expect(document.querySelector<HTMLElement>('[data-unread-dot]')!.hidden).toBe(true);
});

it('preserves the Japanese count template and ignores invalid counts', () => {
  document.body.innerHTML = '<a data-unread-base="通知" data-unread-label="未読 {count} 件"><span data-unread-count></span></a>';
  updateUnread(12);
  expect(document.querySelector('a')!.getAttribute('aria-label')).toBe('通知 · 未読 12 件');
  updateUnread(Number.NaN);
  expect(document.querySelector('a')!.getAttribute('aria-label')).toBe('通知');
});
