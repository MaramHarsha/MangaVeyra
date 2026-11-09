import { test, expect, type Page } from '@playwright/test';

async function adminSignedIn(page: Page) {
  await page.goto('/setup');
  if (new URL(page.url()).pathname === '/setup/admin') {
    await page.locator('[name=display_name]').fill('Studio Tester');
    await page.locator('[name=username]').fill('studio-tester');
    await page.locator('[name=email]').fill('studio-tester@example.test');
    await page.locator('[name=password]').fill('MangaVeyra_Test_Only_2026!');
    await page.locator('[name=confirm]').fill('MangaVeyra_Test_Only_2026!');
    await page.locator('form[action="/setup/admin"] button[type=submit]').click();
    for (const step of ['instance', 'models', 'finish']) {
      await page.waitForURL(`**/setup/${step}`);
      await page.locator(`form[action="/setup/${step}"] button[type=submit]`).click();
    }
    await page.waitForURL('**/studio');
  } else {
    await page.goto('/login');
    await page.locator('[name=identifier]').fill('studio-tester');
    await page.locator('[name=password]').fill('MangaVeyra_Test_Only_2026!');
    await page.locator('form[action="/login"] button[type=submit]').click();
    await page.waitForURL((url) => !url.pathname.startsWith('/login'));
  }
  expect((await page.request.patch('/api/v1/auth/me', { data: { locale: 'en' } })).ok()).toBe(true);
}

test('import original pages into a private editable project; settings and operations render without JavaScript', async ({ page, context, browser }, info) => {
  test.setTimeout(240_000);
  const origin = new URL(info.project.use.baseURL as string).origin;
  const external: string[] = [], errors: string[] = [];
  await context.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (['http:', 'https:'].includes(url.protocol) && url.origin !== origin) {
      external.push(url.origin);
      return route.abort();
    }
    return route.continue();
  });
  page.on('pageerror', (error) => {
    if (error.message !== 'Transition was aborted because of invalid state. ViewTransition opt-in disabled') errors.push(error.message);
  });
  await adminSignedIn(page);
  await page.goto('/admin/backup');
  // Draw original test pages locally; no fixture download or image provider.
  const pngs = await page.evaluate(() => [1, 2].map((number) => {
    const canvas = document.createElement('canvas');
    canvas.width = 256;
    canvas.height = 384;
    const drawing = canvas.getContext('2d')!;
    drawing.fillStyle = 'white';
    drawing.fillRect(0, 0, 256, 384);
    drawing.strokeStyle = 'black';
    drawing.lineWidth = 4;
    drawing.strokeRect(12, 12, 232, 360);
    drawing.beginPath();
    drawing.moveTo(24, 270);
    drawing.lineTo(232, 270 - 30 * number);
    drawing.stroke();
    drawing.fillStyle = 'black';
    drawing.font = '14px sans-serif';
    drawing.fillText(`Rin's harbour walk: ${number}`, 24, 48);
    return canvas.toDataURL('image/png').split(',')[1];
  }));
  await page.locator('input[name=files]').setInputFiles(pngs.map((png, index) => ({
    name: `Saltglass-Import-${index + 1}.png`, mimeType: 'image/png', buffer: Buffer.from(png, 'base64'),
  })));
  await page.locator('form[action^="/imports"] button[type=submit]').click();
  const imported = page.locator('.mv-import__row').filter({ hasText: 'Saltglass-Import-1.png' });
  const open = imported.getByRole('link');
  await expect(open).toBeVisible({ timeout: 60_000 });
  await open.click();
  await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
  const editorURL = new URL(page.url()).pathname;
  const match = /\/studio\/p\/([^/]+)\/chapters\/([^/]+)\/edit/.exec(editorURL);
  expect(match).not.toBeNull();
  const [, projectID, chapterID] = match!;
  const api = `/api/v1/editor/p/${projectID}/chapters/${chapterID}`;
  const bootResponse = await page.request.get(api);
  expect(bootResponse.ok()).toBe(true);
  const boot = await bootResponse.json();
  expect(boot.pages).toHaveLength(2);
  for (const summary of boot.pages) {
    const response = await page.request.get(`${api}/pages/${summary.id}`);
    expect(response.ok()).toBe(true);
    const doc = (await response.json()).page;
    expect([doc.width, doc.height]).toEqual([256, 384]);
    expect(doc.panels.length).toBeGreaterThan(0);
    const art = doc.panels.flatMap((panel: { layers: { asset?: unknown; provenance: string }[] }) => panel.layers).filter((layer: { asset?: unknown }) => layer.asset);
    expect(art.length).toBeGreaterThan(0);
    expect(art.every((layer: { provenance: string }) => layer.provenance === 'human')).toBe(true);
  }
  // An import is private and cannot be opened through another browser's editor API.
  const anonymous = await browser.newContext();
  try {
    const response = await anonymous.request.get(new URL(api, origin).href, { maxRedirects: 0 });
    expect(response.status()).toBe(401);
  } finally { await anonymous.close(); }

  const noJS = await browser.newContext({
    baseURL: origin, javaScriptEnabled: false, storageState: await context.storageState(),
    viewport: { width: 1440, height: 900 },
  });
  try {
    const proof = await noJS.newPage();
    for (const path of [
      '/studio', '/studio/projects', '/models', '/models/routing', '/queue', '/notifications', '/agents',
      '/settings/account', '/settings/appearance', '/settings/language', '/settings/reading',
      '/settings/notifications', '/settings/accessibility', '/settings/data',
      '/admin/reports', '/admin/users', '/admin/disputes', '/admin/rules', '/admin/server', '/admin/backup', '/admin/update/settings',
      '/explore', '/search?q=Saltglass', '/library/following', '/library/history', '/library/downloads', '/library/bookmarks',
    ]) {
      await test.step(`server-rendered ${path}`, async () => {
        expect((await proof.goto(path))?.status(), path).toBe(200);
        expect(new URL(proof.url()).pathname).not.toBe('/login');
        await expect(proof.locator('h1').first()).toBeVisible();
      });
    }
    await proof.goto(editorURL);
    await expect(proof.locator('svg.mv-ed__proof')).toBeVisible();
    await proof.screenshot({ path: info.outputPath('import-nojs-desktop.png'), animations: 'disabled' });
  } finally { await noJS.close(); }

  await page.setViewportSize({ width: 390, height: 844 });
  for (const path of ['/studio/projects', '/models', '/notifications', '/admin/backup']) {
    expect((await page.goto(path))?.status()).toBe(200);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${path} overflows at 390px`).toBe(true);
    await page.screenshot({ path: info.outputPath(`${path.split('/').filter(Boolean).join('-')}-390.png`), animations: 'disabled' });
  }
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});
