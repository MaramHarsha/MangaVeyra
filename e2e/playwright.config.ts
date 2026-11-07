import { defineConfig } from '@playwright/test';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const baseURL = process.env.MV_E2E_BASE_URL || 'http://127.0.0.1:6199';
const dataDir = mkdtempSync(join(tmpdir(), 'mangaveyra-e2e-'));

export default defineConfig({
  testDir: './tests',
  timeout: 120_000,
  expect: { timeout: 20_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  metadata: { dataDir },
  globalTeardown: './teardown.ts',
  use: {
    baseURL,
    viewport: { width: 1440, height: 900 },
    channel: process.env.MV_BROWSER_CHANNEL || undefined,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: `${process.env.MV_E2E_BINARY || '../bin/mangaveyra'} serve --dev`,
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    timeout: 30_000,
    env: {
      MV_CONFIG: '',
      MV_MASTER_KEY: '',
      MV_ADDR: new URL(baseURL).host,
      MV_PUBLIC_URL: baseURL,
      MV_DATA_DIR: dataDir,
      MV_DB: `sqlite://${join(dataDir, 'mangaveyra.db')}`,
      MV_STORAGE: 'local',
      MV_WORKER_TOKEN: 'mangaveyra-e2e-worker-only',
      MV_SMTP_HOST: '',
      MV_LOG_LEVEL: 'error',
    },
  },
});
