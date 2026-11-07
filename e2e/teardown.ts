import type { FullConfig } from '@playwright/test';
import { rm } from 'node:fs/promises';
import { basename, dirname, resolve } from 'node:path';
import { tmpdir } from 'node:os';

export default async function teardown(config: FullConfig) {
  const path = config.metadata.dataDir;
  if (typeof path !== 'string') return;
  if (resolve(dirname(path)) !== resolve(tmpdir()) || !basename(path).startsWith('mangaveyra-e2e-')) {
    throw new Error('Refusing cleanup of a directory not created by the E2E configuration');
  }
  await rm(path, { recursive: true, force: true });
}
