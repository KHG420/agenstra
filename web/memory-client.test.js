import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

test("host memory CRUD uses the authenticated integration and current revision", async t => {
  const requests = [];
  const memory = { id: "memory-1", revision: 4, value: "zh-CN" };
  const c = new AgenstraClient({ integration: "records web", storage: null, getSession: async () => "ticket",
    fetch: async (path, options) => {
      assert.equal(options.headers.Authorization, "Bearer ticket");
      requests.push({ path, method: options.method, body: options.body && JSON.parse(options.body) });
      const data = path.includes("/history?") ? { revisions: [memory], evidence: [] } : path.includes("&limit=") ? [memory] : memory;
      return { ok: true, status: 200, json: async () => data };
    }
  });
  t.after(() => c.destroy({ closeSession: false }));
  assert.equal((await c.listMemories({ limit: 20, offset: 40 }))[0].id, memory.id);
  assert.equal((await c.getMemory(memory.id)).revision, 4);
  const update = { scope: "user", key: "report.language", value: "zh-CN", kind: "preference", revision: 3 };
  await c.setMemory(update);
  assert.equal((await c.memoryHistory(memory.id)).revisions[0].id, memory.id);
  await c.deleteMemory(memory.id, 4);
  assert.deepEqual(requests, [
    { path: "/web/v1/memories?integration_id=records%20web&limit=20&offset=40", method: "GET", body: undefined },
    { path: "/web/v1/memories/memory-1?integration_id=records%20web", method: "GET", body: undefined },
    { path: "/web/v1/memories?integration_id=records%20web", method: "POST", body: update },
    { path: "/web/v1/memories/memory-1/history?integration_id=records%20web", method: "GET", body: undefined },
    { path: "/web/v1/memories/memory-1?integration_id=records%20web", method: "DELETE", body: { revision: 4 } }
  ]);
	assert.ok(!c.browser);
	assert.ok(!c.conversation);
});

test("memory management exposes revision conflicts and uses default pagination", async t => {
  const paths = [];
  const c = new AgenstraClient({ integration: "erp", storage: null, getSession: async () => "ticket",
    fetch: async path => {
      paths.push(path);
      return { ok: false, status: 409, json: async () => ({ code: "revision_conflict", retryable: false }) };
    }
  });
  t.after(() => c.destroy({ closeSession: false }));
  await assert.rejects(c.deleteMemory("memory-1", 1), { code: "revision_conflict", status: 409 });
  await assert.rejects(c.listMemories(), { code: "revision_conflict" });
  assert.equal(paths[1], "/web/v1/memories?integration_id=erp&limit=100&offset=0");
});

test("memory reads renew an expired ticket through the existing authentication flow", async t => {
  let tickets = 0, requests = 0;
  const c = new AgenstraClient({ integration: "erp", storage: null, getSession: async () => "ticket-" + (++tickets),
    fetch: async (_path, options) => {
      requests++;
      assert.equal(options.headers.Authorization, "Bearer ticket-" + requests);
      return requests === 1
        ? { ok: false, status: 401, json: async () => ({ code: "unauthorized" }) }
        : { ok: true, status: 200, json: async () => ({ id: "memory-1" }) };
    }
  });
  t.after(() => c.destroy({ closeSession: false }));
  assert.equal((await c.getMemory("memory-1")).id, "memory-1");
  assert.equal(tickets, 2);
});
