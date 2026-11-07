import { test, expect, type Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';

async function setup(page: Page) {
  await page.goto('/setup/admin');
  await page.locator('[name=display_name]').fill('Studio Tester');
  await page.locator('[name=username]').fill('studio-tester');
  await page.locator('[name=email]').fill('studio-tester@example.test');
  await page.locator('[name=password]').fill('MangaVeyra_Test_Only_2026!');
  await page.locator('[name=confirm]').fill('MangaVeyra_Test_Only_2026!');
  await page.locator('form[action="/setup/admin"] button[type=submit]').click();
  await page.waitForURL('**/setup/instance');
  await page.locator('[name=name]').fill('MangaVeyra Test Studio');
  await page.locator('form[action="/setup/instance"] button[type=submit]').click();
  await page.waitForURL('**/setup/models');
  await expect(page.locator('input[type=password]')).toHaveCount(0);
  await page.locator('form[action="/setup/models"] button[type=submit]').click();
  await page.waitForURL('**/setup/finish');
  await page.locator('form[action="/setup/finish"] button[type=submit]').click();
  await page.waitForURL('**/studio');
}

// The editor autosaves; Cmd/Ctrl+S saves at once. The footer reports the state.
async function savePage(page: Page) {
  await page.keyboard.press('ControlOrMeta+KeyS');
  await expect(page.locator('[data-save-state]')).toHaveAttribute('data-save-state', 'saved');
}

function archiveEntries(data: Buffer): string[] {
  // Inspect the ZIP central directory independently of the application writer.
  let end = -1;
  for (let i = data.length - 22; i >= Math.max(0, data.length - 65557); i--) {
    if (data.readUInt32LE(i) === 0x06054b50) { end = i; break; }
  }
  expect(end).toBeGreaterThanOrEqual(0);
  const entries: string[] = [];
  let offset = data.readUInt32LE(end + 16);
  const count = data.readUInt16LE(end + 10);
  for (let i = 0; i < count; i++) {
    expect(data.readUInt32LE(offset)).toBe(0x02014b50);
    const n = data.readUInt16LE(offset + 28), extra = data.readUInt16LE(offset + 30), comment = data.readUInt16LE(offset + 32);
    entries.push(data.subarray(offset + 46, offset + 46 + n).toString('utf8'));
    offset += 46 + n + extra + comment;
  }
  return entries;
}

test('fresh install → script → characters → layered art → lettering → print and comic exports', async ({ page, context, browser }, info) => {
  test.setTimeout(180_000);
  const origin = new URL(info.project.use.baseURL as string).origin;
  const unexpectedRequests: string[] = [];
  const loadedScripts: string[] = [];
  const appErrors: string[] = [];
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.protocol === 'http:' || url.protocol === 'https:') {
      if (url.origin !== origin) { unexpectedRequests.push(url.origin); return route.abort(); }
    }
    return route.continue();
  });
  page.on('request', request => { if (request.resourceType() === 'script') loadedScripts.push(request.url()); });
  page.on('pageerror', error => {
    // Chromium may abort an optional native cross-document transition when a
    // very fast placeholder job immediately redirects. It has no app stack.
    if (error.message === 'Transition was aborted because of invalid state. ViewTransition opt-in disabled') return;
    appErrors.push(error.message);
  });

  await test.step('complete setup without provider keys', async () => {
    await setup(page);
    await page.goto('/models');
    await page.getByRole('link', { name: /^Image / }).click();
    await expect(page.getByText('Placeholder art', { exact: true }).first()).toBeVisible();
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G2-desktop.png') });
    await page.goto('/models/routing');
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G11-desktop.png') });
    await page.goto('/models/providers/new/connect');
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G12-desktop.png') });
  });

  let story = '', project = '', chapter = '', character = '';
  await test.step('create an original project through every wizard step and wait for its script', async () => {
    await page.goto('/studio/new/premise');
    await page.locator('[name=title]').fill('Saltglass Letters');
    await page.locator('[name=premise]').fill('An apprentice courier delivers letters to a harbour district that disappears with the tide.');
    await page.locator('[data-wizard-next]').click();
    await page.locator('label').filter({ has: page.locator('[name=genres][value=mystery]') }).click();
    await page.locator('[data-wizard-next]').click();
    await page.locator('[data-wizard-next]').click();
    await page.locator('[name=pages_target]').fill('2');
    await page.screenshot({ animations: 'disabled', path: info.outputPath('B3-desktop.png') });
    await page.locator('[data-wizard-next]').click();
    await page.locator('[name=chapter_title]').fill('The Last Footbridge');
    await page.locator('[data-wizard-submit]').click();
    await page.waitForURL(/\/studio\/p\/[^/]+\/story\/[^/]+/);
    await expect(page.locator('form[data-view-panel=panels]')).toBeVisible();
    story = new URL(page.url()).pathname;
    [, , , project, , chapter] = story.split('/');
    expect(project).toMatch(/^[a-z0-9]{26}$/);
    expect(chapter).toMatch(/^[a-z0-9]{26}$/);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('C1-desktop.png') });
    expect(loadedScripts.some(url => url.includes('island-editor'))).toBe(false);
  });

  await test.step('generate four character sheets and keep a real reference', async () => {
    await page.goto(`/studio/p/${project}/characters/new`);
    await page.locator('[name=name]').fill('Sora Vale');
    await page.locator('[name=role]').selectOption('lead');
    await page.locator('[name=look]').fill('A courier in a short sea-blue coat carrying a paper satchel.');
    await page.locator('[name=personality]').fill('Observant, hopeful, and afraid of deep water.');
    await page.getByRole('button', { name: 'Generate reference sheet', exact: true }).click();
    await expect(page.locator('.mv-var__image')).toHaveCount(4);
    await expect(page.locator('#bible-sheet-job')).toHaveCount(0);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('C4-desktop.png') });
    await page.getByRole('button', { name: 'Keep this look', exact: true }).click();
    await page.waitForURL(/\/characters\/[^/?]+\?saved=sheet/);
    character = new URL(page.url()).pathname.split('/').at(-1)!;
    await expect(page.getByRole('heading', { name: 'Sora Vale', exact: true })).toBeVisible();
    await expect(page.locator('.mv-bible__portrait img')).toBeVisible();
    await page.screenshot({ animations: 'disabled', path: info.outputPath('C3-desktop.png') });
    await page.goto(`/studio/p/${project}/characters`);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('C2-desktop.png') });
    await page.goto(`/studio/p/${project}/setting`);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('C5-desktop.png') });
    await page.goto('/studio/projects');
    await page.screenshot({ animations: 'disabled', path: info.outputPath('B2-desktop.png') });
  });

  let editor = '';
  await test.step('make pages, add a character layer, generate four variations and keep one', async () => {
    await page.goto(story);
    await page.getByRole('button', { name: 'Make pages from script', exact: true }).click();
    await page.waitForURL(/\/chapters\/[^/]+\/edit\?job=/);
    await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
    const add = page.getByRole('combobox', { name: 'Add character', exact: true });
    await expect(add).toBeEnabled();
    await expect(page.locator('.mv-ed__stage canvas')).toBeVisible();
    await page.getByRole('button', { name: 'Page actions', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Change layout', exact: true }).click();
    const tilt = page.getByRole('switch', { name: 'Tilt gutters for action', exact: true });
    await expect(tilt).toBeChecked();
    await tilt.press('Space');
    await expect(tilt).not.toBeChecked();
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D2-desktop.png') });
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.locator('[data-save-state]')).toHaveAttribute('data-save-state', 'saved');
    await add.selectOption(character);
    await savePage(page);
    await page.getByRole('button', { name: /^Regenerate panel/ }).click();
    await expect(page.locator('input[name=variation]')).toHaveCount(4);
    const variationsURL = new URL(page.url());
    variationsURL.searchParams.set('panel', 'variations');
    await page.goto(variationsURL.href);
    await expect(page.locator('input[name=variation]')).toHaveCount(4);
    await page.locator('label').filter({ has: page.locator('input[name=variation][value="1"]') }).click();
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D8-desktop.png') });
    await page.getByRole('button', { name: /^Keep 2/ }).click();
    await expect(page.getByText('Variation applied', { exact: true })).toBeVisible();
    editor = new URL(page.url()).pathname;
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D1-desktop.png') });
  });

  await test.step('paint a region mask and keep one bounded edit', async () => {
    await page.getByRole('button', { name: 'Region', exact: true }).click();
    const mask = page.locator('canvas[aria-label="Paint the region mask"]');
    await expect(mask).toBeVisible();
    const box = (await mask.boundingBox())!;
    await page.mouse.move(box.x + box.width * .48, box.y + box.height * .48);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * .56, box.y + box.height * .53, { steps: 6 });
    await page.mouse.up();
    await page.getByRole('textbox', { name: 'Describe the change', exact: true }).fill('Add a small original wave emblem to this part of the coat.');
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D13-desktop.png') });
    const generated = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith('/generate'));
    await page.getByRole('button', { name: 'Generate region variations', exact: true }).click();
    const response = await generated;
    expect(response.status(), await response.text()).toBe(202);
    await expect(page.locator('input[name=variation]')).toHaveCount(4);
    await expect(page.getByRole('slider', { name: 'Compare before and after', exact: true })).toBeVisible();
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D13-result-desktop.png') });
    await page.getByRole('button', { name: /^Keep 1/ }).click();
    await expect(page.getByText('Variation applied', { exact: true })).toBeVisible();
  });

  await test.step('edit structured lettering with undo, redo and a saved page revision', async () => {
    await page.getByRole('button', { name: 'Lettering', exact: true }).click();
    await page.getByRole('button', { name: 'Speech', exact: true }).click();
    const text = page.getByLabel('Text', { exact: true });
    await text.fill('The harbour remembers every footstep.');
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(text).not.toHaveValue('The harbour remembers every footstep.');
    await page.getByRole('button', { name: 'Redo', exact: true }).click();
    await expect(text).toHaveValue('The harbour remembers every footstep.');
    await savePage(page);
    const saved = await (await page.request.get(`/api/v1/editor/p/${project}/chapters/${chapter}`)).json();
    const speech = saved.current.page.bubbles.find((b: { texts: Record<string, { text: string }> }) => b.texts.en?.text === 'The harbour remembers every footstep.');
    expect(speech.tail.enabled).toBe(true);
    expect(speech.tail.tip.x).toBeGreaterThanOrEqual(speech.box.x);
    expect(speech.tail.tip.x).toBeLessThanOrEqual(speech.box.x + speech.box.w);
    expect(Math.abs(speech.tail.tip.y - (speech.box.y + speech.box.h))).toBeLessThanOrEqual(48);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D3-desktop.png') });
    await page.reload();
    await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
    await page.getByRole('button', { name: 'Lettering', exact: true }).click();
    await page.locator('.mv-ed__reading-select', { hasText: 'The harbour remembers every footstep.' }).first().click();
    await expect(page.getByLabel('Text', { exact: true })).toHaveValue('The harbour remembers every footstep.');
  });

  await test.step('preserve a conflicted draft and enforce page locks', async () => {
    const other = await context.newPage();
    await other.goto(editor);
    await expect(other.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
    await other.getByRole('textbox', { name: /^Prompt/ }).fill('A human-reviewed harbour scene with an empty tide gate.');
    await savePage(other);
    await other.close();
    const text = page.getByLabel('Text', { exact: true });
    const rejected = page.waitForResponse(r => r.request().method() === 'PUT' && r.url().includes('/api/v1/editor/'));
    await text.fill('Keep this unsaved conflict draft.');
    expect((await rejected).status()).toBe(409);
    await expect(page.locator('[data-save-state]')).toHaveAttribute('data-save-state', 'conflict');
    await expect(text).toHaveValue('Keep this unsaved conflict draft.');
    await page.getByRole('button', { name: 'Reload latest', exact: true }).click();
    await page.getByRole('button', { name: 'Keep editing', exact: true }).click();
    await expect(text).toHaveValue('Keep this unsaved conflict draft.');
    await page.getByRole('button', { name: 'Reload latest', exact: true }).click();
    await page.getByRole('button', { name: 'Discard changes', exact: true }).click();
    await page.locator('.mv-ed__reading-select', { hasText: 'The harbour remembers every footstep.' }).first().click();
    await expect(text).toHaveValue('The harbour remembers every footstep.');
    await page.getByRole('button', { name: 'Page actions', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Lock page', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Speech', exact: true })).toBeDisabled();
    await page.getByRole('button', { name: 'Pages', exact: true }).click();
    await expect(page.getByRole('button', { name: /^Regenerate panel/ })).toBeDisabled();
    await page.getByRole('button', { name: 'Page actions', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Unlock page', exact: true }).click();
    await page.getByRole('dialog', { name: 'Unlock', exact: true }).getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(page.getByRole('button', { name: /^Regenerate panel/ })).toBeEnabled();
  });

  await test.step('open existing pages from the Story editor after page saves', async () => {
    await page.goto(story);
    await page.getByRole('link', { name: 'Make pages from script', exact: true }).first().click();
    await page.waitForURL(/\/chapters\/[^/]+\/edit$/);
    await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
  });

  await test.step('keep locked lettering read-only in the editor', async () => {
    const api = `/api/v1/editor/p/${project}/chapters/${chapter}`;
    const data = await (await page.request.get(api)).json();
    const current = data.current.page;
    const panel = current.panels[0];
    const letter = panel.layers.find((layer: { kind: string }) => layer.kind === 'lettering');
    expect(letter).toBeTruthy();
    const locked = await page.request.post(`${api}/pages/${current.id}/lock`, {
      data: { panel_id: panel.id, layer_id: letter.id, locked: true, base_version: current.version },
    });
    expect(locked.status()).toBe(200);
    await page.goto(editor);
    await page.getByRole('button', { name: 'Lettering', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Speech', exact: true })).toBeDisabled();
    await expect(page.getByLabel('Text', { exact: true })).toBeDisabled();
    const latest = await locked.json();
    const unlocked = await page.request.post(`${api}/pages/${current.id}/lock`, {
      data: { panel_id: panel.id, layer_id: letter.id, locked: false, confirm: true, base_version: latest.page.version },
    });
    expect(unlocked.status()).toBe(200);
    await page.goto(editor);
    await page.getByRole('button', { name: 'Lettering', exact: true }).click();
    await expect(page.getByLabel('Text', { exact: true })).toBeEnabled();
  });

  await test.step('export a boxed KDP PDF and a CBZ with original credits', async () => {
    await page.getByRole('link', { name: 'Export', exact: true }).click();
    await page.locator('.mv-exportui__canvas img').evaluate((image: HTMLImageElement) => image.decode());
    await page.screenshot({ animations: 'disabled', path: info.outputPath('E2-form-desktop.png') });
    await page.locator('[name=trim_id]').selectOption('kdp-5x7.5');
    await page.locator('[name=dpi]').selectOption('150');
    await page.getByRole('button', { name: 'Export PDF', exact: true }).click();
    const pdfLink = page.getByRole('link', { name: /Download/ }).last();
    await expect(pdfLink).toBeVisible();
    const pdfDownload = page.waitForEvent('download');
    await pdfLink.click();
    const pdf = await pdfDownload;
    expect(pdf.suggestedFilename()).toMatch(/\.pdf$/);
    const pdfPath = info.outputPath(pdf.suggestedFilename());
    await pdf.saveAs(pdfPath);
    const bytes = await readFile(pdfPath);
    expect(bytes.subarray(0, 5).toString()).toBe('%PDF-');
    expect(bytes.includes(Buffer.from('/TrimBox'))).toBe(true);
    expect(bytes.includes(Buffer.from('/BleedBox'))).toBe(true);
    await page.goto(`/studio/p/${project}/chapters/${chapter}/export`);
    await page.locator('label').filter({ has: page.locator('[name=format][value=cbz]') }).click();
    await page.locator('[name=dpi]').selectOption('150');
    await page.getByRole('button', { name: 'Export CBZ', exact: true }).click();
    const cbzLink = page.getByRole('link', { name: /Download/ }).last();
    await expect(cbzLink).toBeVisible();
    const cbzDownload = page.waitForEvent('download');
    await cbzLink.click();
    const cbz = await cbzDownload;
    expect(cbz.suggestedFilename()).toMatch(/\.cbz$/);
    const cbzPath = info.outputPath(cbz.suggestedFilename());
    await cbz.saveAs(cbzPath);
    const entries = archiveEntries(await readFile(cbzPath));
    expect(entries).toContain('ComicInfo.xml');
    expect(entries.filter(name => name.endsWith('.png'))).toHaveLength(3); // two pages plus credits
    await page.screenshot({ animations: 'disabled', path: info.outputPath('E2-desktop.png') });
    await page.goto(`/studio/p/${project}/chapters/${chapter}/export`);
    await page.locator('label').filter({ has: page.locator('[name=format][value=png]') }).click();
    await page.locator('[name=dpi]').selectOption('150');
    await page.getByRole('button', { name: 'Export PNG', exact: true }).click();
    const pngLink = page.getByRole('link', { name: /Download/ }).last();
    await expect(pngLink).toBeVisible();
    const pngDownload = page.waitForEvent('download');
    await pngLink.click();
    const png = await pngDownload;
    expect(png.suggestedFilename()).toMatch(/\.zip$/);
    const pngPath = info.outputPath(png.suggestedFilename());
    await png.saveAs(pngPath);
    expect(archiveEntries(await readFile(pngPath)).filter(name => name.endsWith('.png'))).toHaveLength(3);
  });

  await test.step('inspect the real queue and explicitly read categorized notifications', async () => {
    await page.goto('/queue');
    await expect(page.getByRole('heading', { name: 'Job queue', exact: true })).toBeVisible();
    expect(await page.locator('[data-job-id]').count()).toBeGreaterThan(0);
    await expect(page.locator('.mv-queue__summary-card')).toHaveCount(3);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G9-desktop.png') });
    await page.waitForFunction(() => 'mv' in window);
    const snapshot = await page.locator('#queue-live').innerHTML();
    await context.route('**/queue/status*', route => route.abort());
    await page.evaluate(() => document.dispatchEvent(new CustomEvent('mv:ws', { detail: { type: 'job.progress' } })));
    await expect(page.locator('[data-queue-error]')).toBeVisible();
    expect(await page.locator('#queue-live').innerHTML()).toBe(snapshot);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G9-refresh-error-desktop.png') });
    await context.unroute('**/queue/status*');
    await page.goto('/queue');
    await page.locator('.mv-queue__actions summary').first().focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('menuitem', { name: 'View details', exact: true }).first()).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('.mv-queue__actions summary').first()).toBeFocused();
    await page.locator('.mv-queue__job').first().click();
    await expect(page.locator('.mv-queue__detail')).toBeVisible();
    await expect(page.locator('.mv-queue__detail')).not.toContainText('An apprentice courier delivers letters');
    await page.locator('.mv-queue__events summary').click();
    expect(await page.locator('.mv-queue__events li').count()).toBeGreaterThan(0);

    // Ordinary single jobs stay quiet; the existing notifier sends failures
    // and completed batch jobs. Exercise G4 with its real in-app test action.
    for (let i = 0; i < 2; i++) expect((await page.request.post('/api/v1/notifications/test')).status()).toBe(201);
    const before = (await (await page.request.get('/api/v1/notifications/unread-count')).json()).unread;
    expect(before).toBeGreaterThan(0);
    await page.goto('/notifications');
    await expect(page.getByRole('heading', { name: 'Notifications', exact: true })).toBeVisible();
    await expect(page.locator('.mv-sidebar')).toHaveCount(0);
    expect((await (await page.request.get('/api/v1/notifications/unread-count')).json()).unread).toBe(before);
    await page.screenshot({ animations: 'disabled', path: info.outputPath('G4-desktop.png') });
    await page.getByRole('link', { name: 'Reading', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'No notifications yet', exact: true })).toBeVisible();
    await page.getByRole('link', { name: 'System', exact: true }).click();
    await page.locator('.mv-notifications__item').first().click();
    await page.waitForURL('**/notifications?category=system');
    expect((await (await page.request.get('/api/v1/notifications/unread-count')).json()).unread).toBe(before - 1);
    await page.goto('/notifications');
    const allRead = page.getByRole('button', { name: 'Mark all read', exact: true });
    if (await allRead.isEnabled()) await allRead.click();
    await expect(page.getByRole('button', { name: 'Mark all read', exact: true })).toBeDisabled();
    await expect(page.locator('.mv-bell')).toHaveAttribute('aria-label', 'Notifications');

    for (const locale of ['ja', 'en']) {
      expect((await page.request.patch('/api/v1/auth/me', { data: { locale } })).ok()).toBe(true);
      for (const path of ['/queue', '/notifications']) {
        await page.goto(path);
        await expect(page.locator('html')).toHaveAttribute('lang', locale);
        await page.screenshot({ animations: 'disabled', path: info.outputPath(`${path.slice(1)}-${locale}-desktop.png`) });
      }
    }
    expect((await page.request.post('/settings/theme', { form: { theme: 'light' } })).ok()).toBe(true);
    await page.emulateMedia({ reducedMotion: 'reduce' });
    for (const path of ['/queue', '/notifications']) {
      await page.goto(path);
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      expect(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true);
      await page.screenshot({ animations: 'disabled', path: info.outputPath(`${path.slice(1)}-paper-reduced-motion.png`) });
    }
    expect((await page.request.post('/settings/theme', { form: { theme: 'dark' } })).ok()).toBe(true);
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    for (const path of ['/queue', '/notifications']) {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(path);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ animations: 'disabled', path: info.outputPath(`${path.slice(1)}-responsive.png`) });
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  });

  await test.step('complete all custom-provider steps against a local fake model catalogue', async () => {
    const calls: string[] = [];
    const fake = createServer((req, res) => {
      calls.push(`${req.method} ${req.url}`);
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ data: [{ id: 'tidebreak-test' }] }));
    });
    await new Promise<void>(resolve => fake.listen(0, '127.0.0.1', resolve));
    try {
      const address = fake.address();
      if (!address || typeof address === 'string') throw new Error('No fake model address');
      await page.goto('/models/providers/new/connect');
      await page.locator('[name=name]').fill('Tidebreak local test');
      await page.locator('[name=endpoint]').fill(`http://127.0.0.1:${address.port}/v1`);
      await page.locator('[name=model]').fill('tidebreak-test');
      // Private-network endpoints are off by default; the admin enables them here.
      await expect(page.getByRole('switch', { name: 'Allow private-network endpoints on this server', exact: true })).toBeChecked();
      await page.getByRole('button', { name: 'Next: test', exact: true }).click();
      await page.waitForURL('**/models/providers/new/test?id=*');
      await page.screenshot({ animations: 'disabled', path: info.outputPath('G12-test-desktop.png') });
      await expect(page.getByText('Success', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Next: describe', exact: true }).click();
      await page.waitForURL('**/models/providers/new/describe?id=*');
      await page.screenshot({ animations: 'disabled', path: info.outputPath('G12-describe-desktop.png') });
      await page.getByRole('button', { name: 'Next: route', exact: true }).click();
      await page.waitForURL('**/models/providers/new/route?id=*');
      await page.screenshot({ animations: 'disabled', path: info.outputPath('G12-route-desktop.png') });
      await page.getByRole('button', { name: 'Connect provider', exact: true }).click();
      await page.waitForURL('**/models?cap=text');
      await expect(page.getByRole('switch', { name: 'Use Tidebreak local test', exact: true })).toBeChecked();
      expect(calls).toEqual(['GET /v1/models']);
    } finally {
      await new Promise<void>((resolve, reject) => fake.close(error => error ? reject(error) : resolve()));
    }
  });

  await test.step('retain server-rendered screens and the page proof without JavaScript', async () => {
    const noJS = await browser.newContext({ baseURL: origin, javaScriptEnabled: false, storageState: await context.storageState(), viewport: { width: 1440, height: 900 } });
    try {
      const proof = await noJS.newPage();
      for (const path of ['/studio/projects', `/studio/p/${project}/characters`, `/studio/p/${project}/setting`, '/models', '/models/routing', '/queue', '/notifications']) {
        const response = await proof.goto(path);
        expect(response?.status()).toBe(200);
        await expect(proof.locator('h1').first()).toBeVisible();
      }
      await proof.goto(editor);
      await expect(proof.locator('svg.mv-ed__proof')).toBeVisible();
      await expect(proof.getByRole('link', { name: 'Export', exact: true })).toBeVisible();
    } finally { await noJS.close(); }
  });

  // Phone layouts are deferred to the end-of-project optimisation pass (owner, 2026-11-07; plan 0004).
  await test.step.skip('keep desktop-derived pages usable at phone width', async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    const viewportIssues: unknown[] = [];
    for (const path of ['/studio/projects', '/studio/characters', '/studio/setting', '/models?cap=image', '/models/routing', '/models/providers/new/connect', story, `/studio/p/${project}/characters`, `/studio/p/${project}/characters/${character}`, `/studio/p/${project}/characters/new`, `/studio/p/${project}/setting`, `/studio/p/${project}/chapters/${chapter}/export`, editor]) {
      await page.goto(path);
      if (path === editor) await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
      if (!await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)) viewportIssues.push({ path, issue: 'horizontal overflow' });
      const smallTargets = await page.locator('button.mv-btn, a.mv-btn[href], button.mv-iconlink, a.mv-iconlink[href], .mv-seg__item, select, summary').evaluateAll(elements => elements.flatMap(element => {
        if (element.matches(':disabled, [aria-disabled=true]')) return [];
        const box = element.getBoundingClientRect();
        if (!box.width || !box.height || getComputedStyle(element).visibility === 'hidden') return [];
        return box.width < 44 || box.height < 44 ? [{ label: element.getAttribute('aria-label') || element.textContent?.trim().slice(0, 45), width: box.width, height: box.height }] : [];
      }));
      if (smallTargets.length) viewportIssues.push({ path, smallTargets });
    }
    await page.screenshot({ animations: 'disabled', path: info.outputPath('D1-responsive.png') });
    expect(viewportIssues, 'Phone overflow and touch targets across every built screen').toEqual([]);
  });
  expect(unexpectedRequests).toEqual([]);
  expect(appErrors).toEqual([]);
});

test('all ten content languages render and export in English and Japanese desktop interfaces', async ({ page, context, browser }, info) => {
  test.setTimeout(360_000);
  const origin = new URL(info.project.use.baseURL as string).origin;
  const external: string[] = [], errors: string[] = [];
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if ((url.protocol === 'http:' || url.protocol === 'https:') && url.origin !== origin) {
      external.push(url.origin);
      return route.abort();
    }
    return route.continue();
  });
  const recordError = (e: Error) => {
    if (e.message !== 'Transition was aborted because of invalid state. ViewTransition opt-in disabled') errors.push(e.message);
  };
  context.on('page', p => p.on('pageerror', recordError));
  page.on('pageerror', recordError);
  await page.goto('/setup/admin');
  if (new URL(page.url()).pathname === '/setup/admin') {
    await setup(page);
  } else {
    await page.goto('/login');
    await page.locator('[name=identifier]').fill('studio-tester');
    await page.locator('[name=password]').fill('MangaVeyra_Test_Only_2026!');
    await page.locator('form[action="/login"] button[type=submit]').click();
    await page.waitForURL('**/studio');
  }
  // Keep the language matrix offline even if another flow installed a fake provider.
  const route = await page.request.post('/models/routing', { form: { mode: 'local_only', licence_guard: 'true' } });
  expect(route.ok()).toBe(true);
  const languages = [
    ['en', 'Lighthouse Letters', 'The Last Ferry'], ['ja', '灯台の手紙', '最後の渡し'],
    ['ko', '등대의 편지', '마지막 배'], ['zh-Hans', '灯塔来信', '最后一班船'],
    ['zh-Hant', '燈塔來信', '最後一班船'], ['es', 'Cartas del faro', 'El último ferri'],
    ['fr', 'Lettres du phare', 'Le dernier bac'], ['de', 'Briefe vom Leuchtturm', 'Die letzte Fähre'],
    ['pt', 'Cartas do farol', 'O último barco'], ['hi', 'प्रकाशस्तंभ के पत्र', 'आख़िरी नाव'],
  ];
  for (const ui of ['en', 'ja']) {
    expect((await page.request.patch('/api/v1/auth/me', { data: { locale: ui } })).ok()).toBe(true);
    for (const [lang, title, chapterTitle] of languages) {
      await test.step(`${ui} interface / ${lang} manuscript`, async () => {
        const prefix = `${ui}-${lang}`;
        const response = await page.goto('/studio/new/premise');
        expect(response?.headers()['content-language']).toBe(ui);
        await page.locator('[name=title]').fill(title);
        await page.locator('[name=premise]').fill('A courier carries letters to a lighthouse that only appears at low tide.');
        await page.locator('[data-wizard-next]').click();
        await page.locator('label').filter({ has: page.locator('[name=genres][value=mystery]') }).click();
        await page.locator('[data-wizard-next]').click();
        await page.locator('[data-wizard-next]').click();
        await page.locator('[name=pages_target]').fill('1');
        await page.locator('[data-wizard-next]').click();
        await expect(page.locator('[name=language] option')).toHaveCount(10);
        await page.locator('[name=language]').selectOption(lang);
        await page.locator('[name=chapter_title]').fill(chapterTitle);
        await page.locator('[data-wizard-submit]').click();
        await page.waitForURL(/\/studio\/p\/[^/]+\/story\/[^/]+/);
        await expect(page.locator('form[data-view-panel=panels]')).toBeVisible();
        const [, , , project, , chapter] = new URL(page.url()).pathname.split('/');
        await page.screenshot({ animations: 'disabled', path: info.outputPath(`${prefix}-C1.png`) });
        await page.locator('form[action$="/pages"] button[type=submit]').first().click();
        await page.waitForURL(/\/chapters\/[^/]+\/edit/);
        await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
        const editor = new URL(page.url()).pathname;
        const api = `/api/v1/editor/p/${project}/chapters/${chapter}`;
        const boot = await (await page.request.get(api)).json();
        expect(boot.lang).toBe(ui);
        expect(boot.project.language).toBe(lang);
        expect(boot.current.page.bubbles.length).toBeGreaterThan(0);
        for (const bubble of boot.current.page.bubbles) {
          expect(bubble.texts[lang].text.length).toBeGreaterThan(0);
          expect(!!bubble.vertical).toBe(['ja', 'zh-Hant'].includes(lang));
        }
        for (const layout of boot.current.lettering) {
          expect(layout.lang).toBe(lang);
          expect(layout.text_path.length).toBeGreaterThan(0);
          expect(layout.overflow).toBe(false);
        }
        await page.screenshot({ animations: 'disabled', path: info.outputPath(`${prefix}-D1.png`) });
        await page.goto(editor + '?mode=lettering');
        await expect(page.locator('[data-island=editor]')).toHaveAttribute('data-island-mounted', 'true');
        await page.screenshot({ animations: 'disabled', path: info.outputPath(`${prefix}-D3.png`) });
        const noJS = await browser.newContext({ baseURL: origin, javaScriptEnabled: false, storageState: await context.storageState(), viewport: { width: 1440, height: 900 } });
        try {
          const proof = await noJS.newPage();
          await proof.goto(editor);
          await expect(proof.locator('svg.mv-ed__proof')).toBeVisible();
          expect(await proof.locator('svg.mv-ed__proof path').count()).toBeGreaterThan(0);
          await proof.screenshot({ animations: 'disabled', path: info.outputPath(`${prefix}-nojs.png`) });
        } finally { await noJS.close(); }
        // Every format in every content language; repeat PDF in the Japanese UI.
        for (const format of ui === 'en' ? ['pdf', 'cbz', 'png'] : ['pdf']) {
          await page.goto(`/studio/p/${project}/chapters/${chapter}/export`);
          await page.locator('.mv-exportui__canvas img').evaluate((image: HTMLImageElement) => image.decode());
          await page.locator('[name=dpi]').selectOption('150');
          if (format !== 'pdf') await page.locator('label').filter({ has: page.locator(`[name=format][value=${format}]`) }).click();
          await page.screenshot({ animations: 'disabled', path: info.outputPath(`${prefix}-E2-${format}.png`) });
          await page.locator('[data-export-submit]').click();
          const link = page.locator('a[href$="/download"]').last();
          await expect(link).toBeVisible();
          const downloading = page.waitForEvent('download');
          await link.click();
          const download = await downloading;
          const extension = format === 'png' ? 'zip' : format;
          expect(download.suggestedFilename()).toBe(`${title.replaceAll(' ', '-')}-ch-001.${extension}`);
          const file = info.outputPath(`${prefix}-${format}.${extension}`);
          await download.saveAs(file);
          const bytes = await readFile(file);
          if (format === 'pdf') expect(bytes.subarray(0, 5).toString()).toBe('%PDF-');
          else {
            const entries = archiveEntries(bytes);
            expect(entries.filter(name => name.endsWith('.png'))).toHaveLength(2);
            if (format === 'cbz') expect(entries).toContain('ComicInfo.xml');
          }
        }
      });
    }
  }
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});
