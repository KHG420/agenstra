import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { exportClient } from './export-client.mjs';

test('host SDK export is byte-identical and records verifiable provenance', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'agenstra-sdk-'));
  try {
    await exportClient(dir);
    const manifest = JSON.parse(await readFile(join(dir, 'agenstra-sdk.json'), 'utf8'));
    for (const name of Object.keys(manifest.files)) {
      const original = await readFile(new URL(name, import.meta.url));
      const copied = await readFile(join(dir, name));
      assert.deepEqual(copied, original);
      assert.equal(createHash('sha256').update(copied).digest('hex'), manifest.files[name]);
    }
    await exportClient(dir);
    assert.deepEqual(JSON.parse(await readFile(join(dir, 'agenstra-sdk.json'), 'utf8')), manifest);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
