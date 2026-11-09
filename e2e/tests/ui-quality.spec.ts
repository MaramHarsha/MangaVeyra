import { test, expect, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join, resolve } from 'node:path';

// UI quality gate: every implemented desktop route, in English and Japanese, must render without console errors,
// failed requests, raw locale keys, broken images, horizontal overflow at 1440 px or serious/critical axe
// (WCAG 2.2 AA) violations. It seeds its own content on the suite server: the demo project, one published chapter
// and a reader account. It reports every failing route at once (soft assertions) and stays under a few minutes.

const ADMIN = { username: 'studio-tester', password: 'MangaVeyra_Test_Only_2026!' };
const READER = { username: 'ui-reader', email: 'ui-reader@example.test', password: 'MangaVeyra_Reader_Only_2026!' };
const repo = resolve(process.cwd(), '..');

function cli(dataDir: string, ...args: string[]): string {
  const binary = resolve(process.cwd(), process.env.MV_E2E_BINARY || '../bin/mangaveyra');
  return execFileSync(binary, args, {
    encoding: 'utf8',
    env: {
      ...process.env, MV_CONFIG: '', MV_MASTER_KEY: '', MV_DATA_DIR: dataDir,
      MV_DB: `sqlite://${join(dataDir, 'mangaveyra.db')}`, MV_STORAGE: 'local', MV_SMTP_HOST: '', MV_LOG_LEVEL: 'error',
    },
  });
}

// Every locale key, so a raw key in rendered text ("studioui.story.title") is recognised.
function localeKeys(): string[] {
  const keys = new Set<string>();
  for (const lang of ['en', 'ja']) {
    const dir = join(repo, 'internal/i18n/locales', lang);
    for (const file of readdirSync(dir)) {
      const walk = (o: Record<string, unknown>, prefix: string) => {
        for (const [k, v] of Object.entries(o)) {
          const key = prefix ? `${prefix}.${k}` : k;
          if (v && typeof v === 'object') walk(v as Record<string, unknown>, key); else keys.add(key);
        }
      };
      walk(JSON.parse(readFileSync(join(dir, file), 'utf8')), '');
    }
  }
  return [...keys];
}

// axe is optional at runtime: it comes from this package's devDependencies, or from MV_AXE_MODULES (a node_modules
// directory holding @axe-core/playwright) when the suite runs against a shared install.
type AxeBuilderCtor = new (opts: { page: Page }) => { withTags(tags: string[]): { analyze(): Promise<{ violations: { id: string; impact?: string | null; nodes: { target: unknown[] }[] }[] }> } };
function loadAxe(): AxeBuilderCtor | null {
  const req = createRequire(import.meta.url);
  for (const spec of ['@axe-core/playwright', process.env.MV_AXE_MODULES ? join(process.env.MV_AXE_MODULES, '@axe-core/playwright') : '']) {
    if (!spec) continue;
    try {
      const mod = req(spec);
      return (mod.AxeBuilder || mod.default) as AxeBuilderCtor;
    } catch { /* try the next location */ }
  }
  return null;
}

async function signIn(page: Page, identifier: string, password: string) {
  await page.goto('/login');
  await page.locator('[name=identifier]').fill(identifier);
  await page.locator('[name=password]').fill(password);
  await page.locator('form[action="/login"] button[type=submit]').click();
  await page.waitForURL((url) => !url.pathname.startsWith('/login'));
}

// Earlier specs may have set the server up; either way the admin ends up signed in.
async function adminSignedIn(page: Page) {
  await page.goto('/setup');
  if (new URL(page.url()).pathname === '/setup/admin') {
    await page.locator('[name=display_name]').fill('Studio Tester');
    await page.locator('[name=username]').fill(ADMIN.username);
    await page.locator('[name=email]').fill('studio-tester@example.test');
    await page.locator('[name=password]').fill(ADMIN.password);
    await page.locator('[name=confirm]').fill(ADMIN.password);
    await page.locator('form[action="/setup/admin"] button[type=submit]').click();
    await page.waitForURL('**/setup/instance');
    await page.locator('form[action="/setup/instance"] button[type=submit]').click();
    await page.waitForURL('**/setup/models');
    await page.locator('form[action="/setup/models"] button[type=submit]').click();
    await page.waitForURL('**/setup/finish');
    await page.locator('form[action="/setup/finish"] button[type=submit]').click();
    await page.waitForURL('**/studio');
  } else if (new URL(page.url()).pathname.startsWith('/login')) {
    await signIn(page, ADMIN.username, ADMIN.password);
  }
}

// enOnly marks editor sub-states (the same island in another mode): their Japanese catalogue is covered by D1, D10
// and E2, so the Japanese pass skips them to keep the gate fast.
type Route = { id: string; path: string; user: 'admin' | 'reader' | 'anon'; status?: number; island?: boolean; enOnly?: boolean };

type Finding = { route: string; locale: string; kind: string; detail: string };

const checksInPage = ({ keyList }: { keyList: string[] }) => {
  const keys = new Set(keyList);
  const out = { rawKeys: [] as string[], brokenImages: [] as string[], badText: [] as string[], hOverflow: '' };
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node: Node | null;
  while ((node = walker.nextNode())) {
    const text = (node.nodeValue || '').trim();
    const parent = node.parentElement;
    if (!text || !parent || ['SCRIPT', 'STYLE', 'TEMPLATE', 'NOSCRIPT'].includes(parent.tagName)) continue;
    for (const m of text.match(/\b[a-z][a-z0-9]*(?:\.[a-z0-9_]+)+\b/g) || []) if (keys.has(m)) out.rawKeys.push(m);
    if (/(^|\s)(undefined|NaN|\[object Object\])(\s|$|[.,])/.test(text)) out.badText.push(text.slice(0, 60));
  }
  for (const img of Array.from(document.images)) if (img.getAttribute('src') && img.complete && img.naturalWidth === 0) out.brokenImages.push(img.getAttribute('src') || '');
  const sw = document.documentElement.scrollWidth;
  if (sw > window.innerWidth + 1) out.hOverflow = `${sw} > ${window.innerWidth}`;
  return out;
};

test.describe.serial('ui quality', () => {
  let browser: Browser;
  let dataDir: string;
  let routes: Route[] = [];
  const findings: Finding[] = [];
  const keyList = localeKeys();
  const Axe = loadAxe();

  test.beforeAll(async ({ browser: b }, info) => {
    browser = b;
    dataDir = info.config.metadata.dataDir as string;
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    await adminSignedIn(page);
    expect((await page.request.patch('/api/v1/auth/me', { data: { locale: 'en' } })).ok()).toBe(true);
    cli(dataDir, 'seed', '--demo', '--owner', ADMIN.username);
    try {
      cli(dataDir, 'user', 'create', '--username', READER.username, '--email', READER.email, '--password', READER.password, '--role', 'reader');
    } catch { /* created by an earlier run */ }

    await page.goto('/studio/projects');
    const projectHref = await page.locator('a[href^="/studio/p/"]').first().getAttribute('href');
    const project = /\/studio\/p\/([0-9a-z]+)/.exec(projectHref || '')?.[1] ?? '';
    expect(project).toBeTruthy();
    const P = `/studio/p/${project}`;
    const boot = await (await page.request.get(`/api/v1/editor/p/${project}/chapters/1`)).json();
    const pageID: string = boot.pages[0].id;
    const characterHref = await (async () => { await page.goto(`${P}/characters`); return page.locator(`a[href^="${P}/characters/"]:not([href$="/new"])`).first().getAttribute('href'); })();

    // Publish chapter 1 so the reading side has a series, a chapter and a creator profile.
    await page.goto(`${P}/chapters/1/publish`);
    const publish = page.getByRole('button', { name: /Publish now/ });
    if (await publish.count()) {
      await publish.click();
      await expect.poll(async () => (await (await page.request.get(`${P}/chapters/1/published`)).text()).includes('/s/'), { timeout: 60_000 }).toBe(true);
    }
    const published = await (await page.request.get(`${P}/chapters/1/published`)).text();
    const series = (published.match(/href="(\/s\/[a-z0-9-]+)(?:\/1)?"/) || [])[1] ?? '';
    expect(series).toBeTruthy();
    await ctx.close();

    const ED = `${P}/chapters/1/edit?page=1`;
    routes = [
      { id: 'A2 login', path: '/login', user: 'anon' },
      { id: 'A3 signup', path: '/signup', user: 'anon' },
      { id: 'A4 forgot', path: '/forgot', user: 'anon' },
      { id: 'G6 404', path: '/no/such/page', user: 'anon', status: 404 },
      { id: 'G6 offline', path: '/offline', user: 'anon' },
      { id: 'A5 onboarding', path: '/onboarding/2', user: 'admin' },
      { id: 'B1 dashboard', path: '/studio', user: 'admin' },
      { id: 'B2 projects', path: '/studio/projects', user: 'admin' },
      { id: 'B3 wizard', path: '/studio/new/premise', user: 'admin' },
      { id: 'B3 format', path: '/studio/new/format', user: 'admin' },
      { id: 'C1 story', path: `${P}/story`, user: 'admin' },
      { id: 'C1 prose', path: `${P}/story?view=prose`, user: 'admin' },
      { id: 'C2 characters', path: `${P}/characters`, user: 'admin' },
      { id: 'C3 character', path: characterHref || `${P}/characters`, user: 'admin' },
      { id: 'C4 new character', path: `${P}/characters/new`, user: 'admin' },
      { id: 'C5 setting', path: `${P}/setting`, user: 'admin' },
      { id: 'F2 analytics', path: '/studio/analytics', user: 'admin' },
      { id: 'E1 publish', path: `${P}/chapters/1/publish`, user: 'admin' },
      { id: 'E1 published', path: `${P}/chapters/1/published`, user: 'admin' },
      { id: 'series settings', path: `${P}/series`, user: 'admin' },
      { id: 'D1 editor', path: ED, user: 'admin', island: true },
      { id: 'D3 lettering', path: `${ED}&mode=lettering`, user: 'admin', island: true, enOnly: true },
      { id: 'D13 region', path: `${ED}&tool=region`, user: 'admin', island: true, enOnly: true },
      { id: 'D2 layouts', path: `${ED}&panel=layouts`, user: 'admin', island: true, enOnly: true },
      { id: 'D8 variations', path: `${ED}&panel=variations`, user: 'admin', island: true, enOnly: true },
      { id: 'D7 review', path: `${ED}&panel=review`, user: 'admin', island: true, enOnly: true },
      { id: 'D4 sketch', path: `${ED}&tool=sketch`, user: 'admin', island: true, enOnly: true },
      { id: 'D10 history', path: `${P}/chapters/1/pages/${pageID}/history`, user: 'admin', island: true },
      { id: 'E2 export', path: `${P}/chapters/1/export`, user: 'admin', island: true },
      { id: 'E6 explore', path: '/explore', user: 'reader' },
      { id: 'E8 search', path: '/search?q=tide', user: 'reader' },
      { id: 'E5 series', path: series, user: 'reader' },
      { id: 'E3 reader', path: `${series}/1`, user: 'reader', island: true },
      { id: 'F1 profile', path: `/u/${ADMIN.username}`, user: 'reader' },
      { id: 'E7 library', path: '/library/following', user: 'reader' },
      { id: 'E7 history', path: '/library/history', user: 'reader' },
      { id: 'G4 notifications', path: '/notifications', user: 'reader' },
      { id: 'G2 models', path: '/models', user: 'admin' },
      { id: 'G11 routing', path: '/models/routing', user: 'admin' },
      { id: 'G12 custom provider', path: '/models/providers/new/connect', user: 'admin' },
      { id: 'G13 agents', path: '/agents', user: 'admin' },
      { id: 'G9 queue', path: '/queue', user: 'admin' },
      { id: 'G1 account', path: '/settings/account', user: 'admin' },
      { id: 'G1 appearance', path: '/settings/appearance', user: 'admin' },
      { id: 'G1 language', path: '/settings/language', user: 'admin' },
      { id: 'G1 reading', path: '/settings/reading', user: 'admin' },
      { id: 'G1 notifications', path: '/settings/notifications', user: 'admin' },
      { id: 'G1 accessibility', path: '/settings/accessibility', user: 'admin' },
      { id: 'G1 data', path: '/settings/data', user: 'admin' },
      { id: 'G10 backup', path: '/admin/backup', user: 'admin' },
      { id: 'G5 admin', path: '/admin', user: 'admin' },
      { id: 'G5 users', path: '/admin/users', user: 'admin' },
      { id: 'G5 rules', path: '/admin/rules', user: 'admin' },
      { id: 'G5 server', path: '/admin/server', user: 'admin' },
      { id: 'G14 update', path: '/admin/update', user: 'admin' },
    ];
  });

  for (const locale of ['en', 'ja'] as const) {
    test(`every route renders cleanly in ${locale}`, async () => {
      test.setTimeout(600_000);
      const contexts: Partial<Record<Route['user'], BrowserContext>> = {};
      const contextFor = async (user: Route['user']) => {
        if (contexts[user]) return contexts[user]!;
        const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: locale === 'ja' ? 'ja-JP' : 'en-US' });
        if (user === 'anon') {
          await ctx.addCookies([{ name: 'mv_lang', value: locale, url: ctx.pages()[0]?.url() || (test.info().project.use.baseURL as string) }]);
        } else {
          const page = await ctx.newPage();
          const who = user === 'admin' ? ADMIN : READER;
          await signIn(page, who.username, who.password);
          expect((await page.request.patch('/api/v1/auth/me', { data: { locale } })).ok()).toBe(true);
          await page.close();
        }
        contexts[user] = ctx;
        return ctx;
      };
      // Contexts are created up front, then routes run six at a time: the checks are independent per page.
      for (const user of ['anon', 'admin', 'reader'] as const) await contextFor(user);
      const visit = async (route: Route) => {
        await test.step(`${route.id} ${route.path}`, async () => {
          const ctx = await contextFor(route.user);
          const page = await ctx.newPage();
          const log: string[] = [];
          page.on('pageerror', (e) => log.push(`pageerror: ${e.message}`));
          page.on('console', (m) => { if (m.type() === 'error') log.push(`console: ${m.text()}`); });
          page.on('requestfailed', (r) => { if (!/\/sw\.js/.test(r.url())) log.push(`request failed: ${new URL(r.url()).pathname} ${r.failure()?.errorText ?? ''}`); });
          page.on('response', (r) => {
            const path = new URL(r.url()).pathname;
            if (r.status() >= 400 && !(route.status && r.status() === route.status && r.request().isNavigationRequest())) log.push(`${r.status()} ${path}`);
          });
          const response = await page.goto(route.path, { waitUntil: 'load' });
          const status = response?.status();
          if (status !== (route.status ?? 200)) findings.push({ route: route.id, locale, kind: 'status', detail: `${status} ${page.url()}` });
          await page.evaluate(() => document.fonts.ready);
          if (route.island) { try { await page.waitForLoadState('networkidle', { timeout: 4000 }); } catch { /* keep going */ } }
          await page.waitForTimeout(route.island ? 500 : 100);
          const result = await page.evaluate(checksInPage, { keyList });
          if (result.rawKeys.length) findings.push({ route: route.id, locale, kind: 'raw locale key', detail: [...new Set(result.rawKeys)].join(', ') });
          if (result.badText.length) findings.push({ route: route.id, locale, kind: 'placeholder text', detail: result.badText.join(' | ') });
          if (result.brokenImages.length) findings.push({ route: route.id, locale, kind: 'broken image', detail: result.brokenImages.join(', ') });
          if (result.hOverflow) findings.push({ route: route.id, locale, kind: 'horizontal overflow at 1440', detail: result.hOverflow });
          // The browser logs the navigation's own 4xx as a console error; that is the page under test, not a defect.
          const errors = log.filter((l) => !(route.status && l.includes(`status of ${route.status}`)));
          if (errors.length) findings.push({ route: route.id, locale, kind: 'console or network error', detail: [...new Set(errors)].join(' | ') });
          if (Axe) {
            const { violations } = await new Axe({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
            const serious = violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
            if (serious.length) findings.push({ route: route.id, locale, kind: 'axe', detail: serious.map((v) => `${v.id}[${v.impact}]×${v.nodes.length} ${v.nodes.slice(0, 2).map((n) => n.target.join(' ')).join('; ')}`).join(' || ') });
          }
          await page.close();
        });
      };
      const queue = routes.filter((r) => locale === 'en' || !r.enOnly);
      await Promise.all(Array.from({ length: 6 }, async () => { for (let r = queue.shift(); r; r = queue.shift()) await visit(r); }));
      for (const ctx of Object.values(contexts)) await ctx?.close();
      if (!Axe) test.info().annotations.push({ type: 'warning', description: 'axe-core not found: install e2e devDependencies or set MV_AXE_MODULES; accessibility checks were skipped.' });
      const mine = findings.filter((f) => f.locale === locale);
      expect.soft(mine, mine.map((f) => `${f.route} (${f.locale}) ${f.kind}: ${f.detail}`).join('\n')).toEqual([]);
    });
  }
});
