// Optional local design evidence. The handoff stays outside this repository.
import { chromium } from 'playwright';
import { readFile, readdir, mkdir } from 'node:fs/promises';
import { resolve, join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const handoff = resolve(process.env.MV_DESIGN_DIR || join(repo, '../MangaVeyra_All_Pages'));
const output = resolve(process.env.MV_DESIGN_SCREENSHOTS || join(repo, 'e2e/test-results/artboards'));
const baseURL = process.env.MV_E2E_BASE_URL || 'http://127.0.0.1:6199';
const sheets = [
  ['07 Screens - Flow 2 Start a Story.dc.html', ['B2', 'B3', 'C1', 'C2', 'C3', 'C4', 'C5']],
  ['09 Screens - Flow 6-7 Tools and System.dc.html', ['G2', 'G11', 'G12', 'D2']],
  ['05 Screens - Flow 3 Make a Page.dc.html', ['D1', 'D3', 'D8', 'D13', 'E2']],
];
const cssDir = join(repo, 'internal/web/static/dist/assets');
const css = (await Promise.all((await readdir(cssDir)).filter(p => p.endsWith('.css')).map(p => readFile(join(cssDir, p), 'utf8')))).join('\n');
const faces = (css.match(/@font-face\{[^}]*\}/g) || []).join('\n');
const icons = (await readFile(join(repo, 'web/node_modules/@tabler/icons-webfont/dist/tabler-icons.min.css'), 'utf8')).replace(/@font-face\s*\{[^}]*\}/g, '');
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ channel: process.env.MV_BROWSER_CHANNEL || undefined, headless: true });
try {
  // The approved source uses inline styles. This is a separate reference-only
  // context; application screenshots and E2E tests enforce the real CSP.
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, bypassCSP: true });
  await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== new URL(baseURL).origin) return route.abort();
    if (url.pathname.startsWith('/brand/')) {
      const file = resolve(handoff, '.' + decodeURIComponent(url.pathname));
      if (!file.startsWith(handoff + '/')) return route.abort();
      try { return await route.fulfill({ body: await readFile(file), contentType: file.endsWith('.svg') ? 'image/svg+xml' : 'image/png' }); }
      catch { return route.abort(); }
    }
    return route.continue();
  });
  const page = await context.newPage();
  await page.goto(baseURL + '/healthz');
  for (const [sheet, ids] of sheets) {
    const source = await readFile(join(handoff, sheet), 'utf8');
    await page.setContent(source.replace(/<script[^>]*>[\s\S]*?<\/script>/g, '').replace(/<link[^>]*>/g, ''));
    await page.addStyleTag({ content: faces + '\n' + icons });
    await page.evaluate(() => document.fonts.ready);
    for (const id of ids) {
      const artboard = page.locator(`[data-screen-label^="${id} –"][data-screen-label$="Desktop"]`).first();
      await artboard.screenshot({ path: join(output, `${id}-reference.png`) });
      console.log(`${id}: 1440×900 desktop reference`);
    }
  }
} finally { await browser.close(); }
