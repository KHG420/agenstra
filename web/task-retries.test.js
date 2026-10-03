import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

for (const lostResponse of ["connection", "JSON"]) {
  test(`a lost browser run ${lostResponse} response exposes its original request identity`, async t => {
    const runs = new Map(), bodies = [];
    let fail = true, generated = 0;
    const c = new AgenstraClient({ integration: "app", browser: true, storage: null, getSession: async () => "ticket",
      fetch: async (_path, options) => {
        const body = JSON.parse(options.body);bodies.push(body);
        if (!runs.has(body.request_id)) runs.set(body.request_id, { run_id: "run-" + runs.size });
        if (fail) {
          fail = false;
          if (lostResponse === "connection") throw new Error("response lost after creation");
          return { ok: true, status: 200, json: async () => { throw new SyntaxError("truncated response"); } };
        }
        return { ok: true, status: 200, json: async () => runs.get(body.request_id) };
      }
    });
    t.after(() => c.destroy({ closeSession: false }));
    c.id = () => "request-" + (++generated);
    c.browser = { id: "tab", key: "key" };
    c.connectBrowser = async () => c.browser;
    const sources = [{ pack_id: "orders", capabilities: ["orders.approve"] }];
    let failure;
    try { await c.run("Approve order 1001", { sources }); } catch (error) { failure = error; }
    assert.equal(failure?.requestId, "request-1");
    const run = await c.run("Approve order 1001", { requestId: failure.requestId, sources });
    assert.equal(run.run_id, "run-0");
    assert.equal(runs.size, 1);assert.equal(generated, 1);
    assert.deepEqual(bodies[0], bodies[1]);
  });
}

test("browser connection failures preserve the caller's run request identity", async t => {
  const c = new AgenstraClient({ integration: "app", browser: true, storage: null, getSession: async () => "ticket" });
  t.after(() => c.destroy({ closeSession: false }));
  c.connectBrowser = async () => { throw new Error("bridge unavailable"); };
  await assert.rejects(c.run("Open orders", { requestId: "original" }), error => error.requestId === "original");
});

test("conversation initialization errors also expose the message client identity", async t => {
  const c = new AgenstraClient({ integration: "app", storage: null, getSession: async () => "ticket" });
  t.after(() => c.destroy({ closeSession: false }));
  c.getConversation = async () => { throw new Error("conversation unavailable"); };
  await assert.rejects(c.send("Approve order 1001", { clientId: "message-original" }), error => error.clientId === "message-original");
});
