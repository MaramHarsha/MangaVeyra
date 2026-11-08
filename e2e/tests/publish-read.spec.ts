import { test, expect, type Page, type Browser } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { join, resolve } from 'node:path';

// Phase 3 acceptance (docs/plans/0010-phase-3-publish-and-read.md): a creator
// publishes a chapter; a signed-in reader follows the series, is notified of the
// next chapter, reads it guided with alt-text and downloads it for offline
// reading; an anonymous visitor on a public server reads public chapters and
// cannot open members-only, private or mature series; readers keep the frozen
// copy while the creator keeps editing.

const ADMIN = { username: 'studio-tester', password: 'MangaVeyra_Test_Only_2026!' };
const READER = { username: 'e2e-reader', password: 'MangaVeyra_Reader_Only_2026!' };

function cli(dataDir: string, ...args: string[]): string {
  const binary = resolve(process.cwd(), process.env.MV_E2E_BINARY || '../bin/mangaveyra');
  return execFileSync(binary, args, {
    encoding: 'utf8',
    env: {
      ...process.env,
      MV_CONFIG: '',
      MV_MASTER_KEY: '',
      MV_DATA_DIR: dataDir,
      MV_DB: `sqlite://${join(dataDir, 'mangaveyra.db')}`,
      MV_STORAGE: 'local',
      MV_SMTP_HOST: '',
      MV_LOG_LEVEL: 'error',
    },
  });
}

// Radios in the kit's choice cards and segmented controls are visually hidden;
// people pick them through their labels.
async function choose(page: Page, scope: string, name: string, value: string) {
  const radio = `input[name=${name}][value=${value}]`;
  await page.locator(`${scope} label`, { has: page.locator(radio) }).click();
  await expect(page.locator(`${scope} ${radio}`)).toBeChecked();
}

async function signIn(page: Page, who: { username: string; password: string }) {
  await page.goto('/login');
  await page.locator('[name=identifier]').fill(who.username);
  await page.locator('[name=password]').fill(who.password);
  await page.locator('form[action="/login"] button[type=submit]').click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// The first-playable spec may already have set the server up; either way the
// admin ends up signed in and the server is in public mode.
async function adminOnPublicServer(page: Page) {
  await page.goto('/setup');
  if (new URL(page.url()).pathname === '/setup/admin') {
    await page.locator('[name=display_name]').fill('Studio Tester');
    await page.locator('[name=username]').fill(ADMIN.username);
    await page.locator('[name=email]').fill('studio-tester@example.test');
    await page.locator('[name=password]').fill(ADMIN.password);
    await page.locator('[name=confirm]').fill(ADMIN.password);
    await page.locator('form[action="/setup/admin"] button[type=submit]').click();
    await page.waitForURL('**/setup/instance');
  } else {
    if (new URL(page.url()).pathname.startsWith('/login')) await signIn(page, ADMIN);
    await page.goto('/setup/instance');
  }
  await page.locator('[name=name]').fill('MangaVeyra Test Studio');
  await choose(page, 'form[action="/setup/instance"]', 'mode', 'public');
  await page.locator('form[action="/setup/instance"] button[type=submit]').click();
  await page.waitForURL('**/setup/models');
  await page.locator('form[action="/setup/models"] button[type=submit]').click();
  await page.waitForURL('**/setup/finish');
  const finish = page.locator('form[action="/setup/finish"] button[type=submit]');
  if (await finish.count()) {
    await finish.click();
    await page.waitForURL('**/studio');
  }
}

async function publishChapter(page: Page, projectID: string, number: number): Promise<string[]> {
  await page.goto(`/studio/p/${projectID}/chapters/${number}/publish`);
  await expect(page.getByRole('heading', { name: `Publish chapter ${number}` })).toBeVisible();
  // Alt-text is drafted from the script and lettering without a model call (ADR 0012 §5).
  const drafts = (await page.locator('textarea[name^="alt"]').evaluateAll((els) => els.map((e) => (e as HTMLTextAreaElement).value.trim()))).filter(Boolean);
  expect(drafts.length).toBeGreaterThan(0);
  await page.getByRole('button', { name: /Publish now/ }).click();
  await page.waitForURL(/\/published/, { timeout: 90_000 });
  await expect(page.getByRole('heading', { name: `Chapter ${number} is live` })).toBeVisible();
  return drafts;
}

async function newSignedIn(browser: Browser, who: { username: string; password: string }) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await signIn(page, who);
  return { context, page };
}

async function mediaImagesLoaded(page: Page): Promise<number> {
  return page.evaluate(() => {
    const imgs = Array.from(document.images).filter((i) => i.currentSrc.includes('/media/') && i.getBoundingClientRect().width > 0);
    return imgs.length > 0 && imgs.every((i) => i.complete && i.naturalWidth > 0) ? imgs.length : 0;
  });
}

async function seriesSettings(page: Page, projectID: string, fields: { visibility?: string; age_rating?: string }) {
  await page.goto(`/studio/p/${projectID}/series`);
  const scope = `form[action="/studio/p/${projectID}/series"]`;
  const form = page.locator(scope);
  if (fields.visibility) await choose(page, scope, 'visibility', fields.visibility);
  if (fields.age_rating) await form.locator('select[name=age_rating]').selectOption(fields.age_rating);
  await form.getByRole('button', { name: 'Save series' }).click();
  await page.waitForLoadState('load');
  await expect(page.locator('[role=alert].mv-field__error, .mv-publish__error')).toHaveCount(0);
}

test('publish → follow → notified → guided read with alt-text → offline download; anonymous access; frozen copies', async ({ page, browser }, info) => {
  test.setTimeout(360_000);
  page.setDefaultTimeout(20_000);
  const dataDir = info.config.metadata.dataDir as string;

  await adminOnPublicServer(page);
  // Earlier specs may leave the admin's interface in Japanese; this flow checks English copy.
  expect((await page.request.patch('/api/v1/auth/me', { data: { locale: 'en' } })).ok()).toBe(true);
  cli(dataDir, 'seed', '--demo', '--owner', ADMIN.username);
  cli(dataDir, 'user', 'create', '--username', READER.username, '--email', 'e2e-reader@example.test', '--password', READER.password, '--role', 'reader');

  // The creator publishes chapter 1; the first publish creates the series.
  await page.goto('/studio/projects');
  await page.getByRole('link', { name: /Tidebreak Courier/ }).first().click();
  const projectID = /\/studio\/p\/([0-9a-z]+)/.exec(page.url())?.[1] ?? '';
  expect(projectID).not.toBe('');
  const drafts = await publishChapter(page, projectID, 1);
  const seriesHref = (await page.getByRole('link', { name: 'View series page' }).getAttribute('href')) ?? '';
  expect(seriesHref).toMatch(/^\/s\/[a-z0-9-]+$/);

  // A signed-in reader follows the series from E5.
  const reader = await newSignedIn(browser, READER);
  await reader.page.goto(seriesHref);
  await reader.page.locator(`form[action="${seriesHref}/follow"] button[type=submit]`).click();
  await expect(reader.page.locator(`form[action="${seriesHref}/unfollow"]`)).toBeVisible();

  // Publishing chapter 2 notifies the follower once (ADR 0012 §10).
  await publishChapter(page, projectID, 2);
  await expect.poll(async () => {
    const res = await reader.page.request.get('/api/v1/notifications');
    return res.ok() ? (await res.text()).includes(`${seriesHref}/2`) : false;
  }, { timeout: 30_000 }).toBe(true);

  // Guided reading with alt-text.
  await reader.page.goto(`${seriesHref}/1`);
  await reader.page.getByRole('button', { name: 'Guided', exact: true }).click();
  const alt = reader.page.locator('.mv-rd-guided__alt');
  await expect(alt).toBeVisible();
  const altText = (await alt.innerText()).trim();
  expect(drafts.some((d) => altText.includes(d))).toBe(true);
  await expect.poll(() => mediaImagesLoaded(reader.page)).toBeGreaterThan(0);

  // Offline download: the service worker cache serves the chapter with the network off.
  await reader.page.getByRole('button', { name: 'Download for offline reading' }).click();
  await expect(reader.page.getByRole('button', { name: 'Downloaded · readable offline' })).toBeVisible({ timeout: 60_000 });
  const cacheNames = await reader.page.evaluate(() => caches.keys());
  expect(cacheNames.some((k) => k.startsWith('mv-offline-'))).toBe(true);
  await reader.page.reload();
  await expect.poll(() => reader.page.evaluate(() => !!navigator.serviceWorker.controller), { timeout: 20_000 }).toBe(true);
  await reader.context.setOffline(true);
  await reader.page.reload();
  await expect.poll(() => mediaImagesLoaded(reader.page), { timeout: 30_000 }).toBeGreaterThan(0);
  await reader.context.setOffline(false);

  // An anonymous visitor on a public server reads the public chapter.
  const anon = await browser.newContext();
  const visitor = await anon.newPage();
  expect((await visitor.goto(`${seriesHref}/1`))?.status()).toBe(200);
  await expect.poll(() => mediaImagesLoaded(visitor)).toBeGreaterThan(0);

  // Readers keep the frozen copy while the creator keeps editing (ADR 0012 §2).
  const editorAPI = `/api/v1/editor/p/${projectID}/chapters/1`;
  const boot = await (await page.request.get(editorAPI)).json();
  const pageID = boot.pages[0].id as string;
  const doc = (await (await page.request.get(`${editorAPI}/pages/${pageID}`)).json()).page;
  const bubble = doc.bubbles.find((b: { texts?: Record<string, { text: string }> }) => b.texts?.en?.text);
  const original = bubble.texts.en.text as string;
  bubble.texts.en.text = 'EDITED AFTER PUBLISHING';
  const saved = await page.request.put(`${editorAPI}/pages/${pageID}`, { data: { page: doc, base_version: doc.version } });
  expect(saved.ok()).toBe(true);
  const frozen = await (await visitor.request.get(`${seriesHref}/1`)).text();
  expect(frozen).not.toContain('EDITED AFTER PUBLISHING');
  expect(frozen).toContain(original.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').split('\n')[0]);

  // Members-only: anonymous visitors get 404 everywhere, members still read.
  await seriesSettings(page, projectID, { visibility: 'members' });
  expect((await visitor.goto(seriesHref))?.status()).toBe(404);
  expect((await visitor.goto(`${seriesHref}/1`))?.status()).toBe(404);
  expect((await reader.page.goto(`${seriesHref}/1`))?.status()).toBe(200);

  // Private: only project members (the reader is not one) may preview.
  await seriesSettings(page, projectID, { visibility: 'private' });
  expect((await reader.page.goto(`${seriesHref}/1`))?.status()).toBe(404);
  expect((await visitor.goto(`${seriesHref}/1`))?.status()).toBe(404);

  // Mature: never shown to anonymous visitors; a member confirms once per series.
  await seriesSettings(page, projectID, { visibility: 'public', age_rating: 'mature' });
  expect((await visitor.goto(`${seriesHref}/1`))?.status()).toBe(404);
  await reader.page.goto(`${seriesHref}/1`);
  await expect(reader.page.getByRole('heading', { name: 'This series is for mature readers' })).toBeVisible();
  await reader.page.getByRole('button', { name: "I'm old enough · Read" }).click();
  await expect(reader.page.getByRole('button', { name: 'Guided', exact: true })).toBeVisible();
  await expect.poll(() => mediaImagesLoaded(reader.page)).toBeGreaterThan(0);

  await anon.close();
  await reader.context.close();
});
