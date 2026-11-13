import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { AgenstraClient } from './agenstra-client.js';

test('workflow methods preserve the integration and stable request identity', async () => {
  const client = new AgenstraClient({ integration: 'records-web', getSession: async () => 'synthetic' });
  const calls = [];
  client.request = async (path, options) => { calls.push({ path, options }); return { run_id: 'run-1' }; };
  await client.listWorkflows();
  await client.startWorkflow('report', { region: 'sea-a' }, { requestId: 'request-1' });
  await client.getWorkflow('run-1');
  await client.cancelWorkflow('run-1');
  await client.resumeWorkflow('run-1');
  assert.equal(calls[0].path, '/web/v1/workflows?integration_id=records-web');
  assert.deepEqual(calls[1].options.body, { workflow: 'report', input: { region: 'sea-a' }, request_id: 'request-1' });
  assert.equal(calls[1].options.method, 'POST');
  assert.match(calls[2].path, /\/runs\/run-1\?integration_id=records-web$/);
  assert.match(calls[3].path, /\/runs\/run-1\/cancel\?integration_id=records-web$/);
  assert.match(calls[4].path, /\/runs\/run-1\/resume\?integration_id=records-web$/);
  const declarations = readFileSync(new URL('./agenstra-client.d.ts', import.meta.url), 'utf8');
  for (const name of ['listWorkflows', 'startWorkflow', 'getWorkflow', 'cancelWorkflow', 'resumeWorkflow']) {
    assert.ok(declarations.includes(name + '('));
  }
  await client.destroy();
});
