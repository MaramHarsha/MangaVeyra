import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { join, resolve } from 'node:path';

// Phase 4 acceptance (docs/plans/0011-phase-4-run-a-server-for-others.md): an admin invites a member, the member and
// the owner review a page together, the admin backs the server up and restores it, an agent edits within its scope and
// its publish request waits for a person, and update checks stay off with an unsigned build. The signed install and
// rollback run against a real fake release in internal/update's integration tests.

const ADMIN = { username: 'studio-tester', password: 'MangaVeyra_Test_Only_2026!' };
const MEMBER = { name: 'Invited Letterer', email: 'invited-letterer@example.test', password: 'MangaVeyra_Member_Only_2026!' };

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

async function json(api: APIRequestContext, method: 'GET' | 'POST', path: string, data?: unknown) {
  const res = method === 'GET' ? await api.get(path) : await api.post(path, { data });
  const text = await res.text();
  expect(res.ok(), `${method} ${path} → ${res.status()} ${text.slice(0, 300)}`).toBe(true);
  return text ? JSON.parse(text) : {};
}

// mcpCall sends one JSON-RPC request to /mcp with an agent token and returns the decoded result (SSE or JSON).
async function mcpCall(api: APIRequestContext, token: string, id: number, method: string, params: unknown) {
  const res = await api.post('/mcp', {
    headers: { Authorization: `Bearer ${token}`, Accept: 'application/json, text/event-stream', 'Content-Type': 'application/json' },
    data: { jsonrpc: '2.0', id, method, params },
  });
  const text = await res.text();
  expect(res.ok(), `mcp ${method} → ${res.status()} ${text.slice(0, 300)}`).toBe(true);
  const payload = text.trim().startsWith('{') ? text : (text.split('\n').find((l) => l.startsWith('data:')) || '').slice(5);
  return JSON.parse(payload);
}

test('invite → review together → back up and restore → agent within limits → update checks stay off', async ({ page, browser }, info) => {
  test.setTimeout(360_000);
  page.setDefaultTimeout(20_000);
  const dataDir = info.config.metadata.dataDir as string;
  await adminSignedIn(page);
  expect((await page.request.patch('/api/v1/auth/me', { data: { locale: 'en' } })).ok()).toBe(true);
  cli(dataDir, 'seed', '--demo', '--owner', ADMIN.username);

  // G5: the admin invites a member as a creator; the invite link opens sign-up.
  await page.goto('/admin/users?dialog=invite');
  const inviteForm = page.locator('form[action="/admin/invites"]');
  await inviteForm.locator('select[name=role]').selectOption('creator');
  await inviteForm.locator('button[type=submit]').click();
  await page.waitForLoadState('load');
  // The invite link sits in a read-only field that its Copy button names.
  const linkField = await page.locator('[data-copy^="invite-"]').first().getAttribute('data-copy');
  const link = await page.locator(`input[id="${linkField}"]`).inputValue();
  expect(link).toContain('/signup?invite=');

  const memberCtx = await browser.newContext();
  const member = await memberCtx.newPage();
  await member.goto(new URL(link).pathname + new URL(link).search);
  await member.locator('[name=display_name]').fill(MEMBER.name);
  await member.locator('[name=email]').fill(MEMBER.email);
  await member.locator('[name=password]').fill(MEMBER.password);
  await member.locator('[name=agree]').check({ force: true });
  await member.locator('form[action="/signup"] button[type=submit]').click();
  await member.waitForURL((url) => !url.pathname.startsWith('/signup'));
  const me = await json(member.request, 'GET', '/api/v1/auth/me');
  const memberUsername: string = me.username ?? me.user?.username;
  expect(memberUsername).toBeTruthy();

  // D7: the owner adds the member as a letterer limited to the lettering layer; the member comments on a panel and
  // the owner asks for changes.
  await page.goto('/studio/projects');
  await page.getByRole('link', { name: /Tidebreak Courier/ }).first().click();
  const projectID = /\/studio\/p\/([0-9a-z]+)/.exec(page.url())?.[1] ?? '';
  const collab = `/api/v1/collab/p/${projectID}`;
  await json(page.request, 'POST', `${collab}/members`, { username: memberUsername, role: 'letterer', scope: { layer: 'lettering' } });
  const boot = await json(page.request, 'GET', `/api/v1/editor/p/${projectID}/chapters/1`);
  const pageID: string = boot.pages[0].id;
  const doc = (await json(page.request, 'GET', `/api/v1/editor/p/${projectID}/chapters/1/pages/${pageID}`)).page;
  const panelID: string = doc.panels[0].id;
  const created = await json(member.request, 'POST', `${collab}/pages/${pageID}/comments`, { panel_id: panelID, body: 'Bubble 2 crowds the face.' });
  const commentID: string = (created.comments as { id: string; body: string }[]).find((c) => c.body === 'Bubble 2 crowds the face.')?.id ?? '';
  expect(commentID).toBeTruthy();
  await json(page.request, 'POST', `${collab}/comments/${commentID}/status`, { status: 'needs_changes' });
  const review = JSON.stringify(await json(member.request, 'GET', `${collab}/pages/${pageID}/review`));
  expect(review).toContain('Bubble 2 crowds the face.');
  expect(review).toContain('needs_changes');
  // The lettering-only member cannot change art: a page save that touches a layer is refused.
  doc.panels[0].layers[0].opacity = 0.5;
  const artSave = await member.request.put(`/api/v1/editor/p/${projectID}/chapters/1/pages/${pageID}`, { data: { page: doc, base_version: doc.version } });
  expect(artSave.status()).toBeGreaterThanOrEqual(400);

  // G13: the owner creates an agent token that may edit; the agent's publish request waits for a person.
  await page.goto('/agents');
  await page.getByRole('link', { name: 'Create token' }).first().click();
  await page.locator('#create-token [name=name]').fill('E2E agent');
  // Edit is on by default for a new token.
  await expect(page.locator('form[action="/agents/tokens"] input[name=edit]')).toBeChecked();
  await page.locator('form[action="/agents/tokens"] button[type=submit]').click();
  await page.waitForLoadState('load');
  const token = ((await page.locator('body').innerText()).match(/mva_[A-Za-z0-9_-]+/) || [''])[0];
  expect(token).toMatch(/^mva_/);
  await mcpCall(page.request, token, 1, 'initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '1' } });
  const tools = JSON.stringify(await mcpCall(page.request, token, 2, 'tools/list', {}));
  expect(tools).toContain('request_human_action');
  expect(tools).not.toMatch(/"name":"(publish|delete_project|unlock)/);
  await mcpCall(page.request, token, 3, 'tools/call', {
    name: 'request_human_action', arguments: { project_id: projectID, action: 'publish', chapter_id: boot.chapter.id, note: 'Chapter 1 is ready.' },
  });
  await page.goto('/agents');
  await expect(page.getByText('Blocked · waiting for you').first()).toBeVisible();

  // G10: back up now, then restore that archive; the project survives.
  await page.goto('/admin/backup');
  expect((await page.request.post('/admin/backup/now', { form: {} })).status()).toBeLessThan(400);
  let archiveID = '';
  await expect.poll(async () => {
    const html = await (await page.request.get('/admin/backup')).text();
    archiveID = (html.match(/name="archive" value="([0-9a-z]+)"/) || ['', ''])[1];
    return archiveID;
  }, { timeout: 60_000 }).not.toBe('');
  expect((await page.request.post('/admin/backup/restore', { form: { archive: archiveID, confirm: 'restore' } })).status()).toBeLessThan(400);
  await expect.poll(async () => (await (await page.request.get('/admin/backup')).text()).includes('Restored from a backup'), { timeout: 90_000 }).toBe(true);
  await page.goto('/studio/projects');
  await expect(page.getByRole('link', { name: /Tidebreak Courier/ }).first()).toBeVisible();

  // G14: this build has no trusted release key, so update checks never contact anyone.
  await page.goto('/admin/update/settings');
  await expect(page.getByText('This build has no trusted release key', { exact: false }).first()).toBeVisible();

  await memberCtx.close();
});
