import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const response = (data, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => data });
const command = { id: "cmd-1", run_id: "run-1", session_id: "tab-1", generation: 1, action: "ui.navigate", arguments: { page: "orders" }, context_revision: 1 };
function client(t, fetch) {
  const c = new AgenstraClient({ integration: "app", storage: null, getSession: async () => "ticket", fetch });
  c.browser = { id: "tab-1", generation: 1, key: "key", context_revision: 1 };
  t.after(() => c.destroy({ closeSession: false }));
  return c;
}

for (const failingStep of ["page getter", "queued observation"]) {
  test(`${failingStep} failure before begin cannot create an unknown business outcome`, async t => {
    let ready = false, begins = 0, handlers = 0;
    const results = [], errors = [];
    const c = client(t, async (path, options) => {
      if (path.endsWith("/begin")) { begins++;return response({ accepted: true }); }
      if (path.endsWith("/result")) {
        results.push(JSON.parse(options.body).status);
        return begins ? response({ status: "succeeded" }) : response({ code: "browser_result_conflict" }, 409);
      }
      if (path.endsWith(command.id)) return response({ status: "dispatched" });
      return response({ status: "completed" });
    });
    c.pageObservation = { page: "home" };
    c.options.getPageObservation = () => {
      if (!ready && failingStep === "page getter") throw new Error("page unavailable");
      return { page: "home" };
    };
    if (failingStep === "queued observation") c.observationChain = Promise.reject(new Error("upload unavailable"));
    c.on("error", error => errors.push(error));
    c.registerActions({ "ui.navigate": () => { handlers++;return { page: "orders" }; } });
    await c.executeCommand(command);
    assert.equal(begins, 0);assert.equal(handlers, 0);
    assert.deepEqual(results, []);
    assert.equal(c.receipts[command.id], undefined);
    assert.equal(errors.length, 1);
    ready = true;c.observationChain = Promise.resolve();
    await c.executeCommand(command);
    assert.equal(begins, 1);assert.equal(handlers, 1);
    assert.deepEqual(results, ["succeeded"]);
    assert.equal(c.receipts[command.id].status, "acked");
  });
}

test("a lost begin response remains unknown and never retries the handler", async t => {
  let begins = 0, handlers = 0;
  const statuses = [];
  const c = client(t, async (path, options) => {
    if (path.endsWith("/begin")) { begins++;throw new Error("begin response lost"); }
    if (path.endsWith("/result")) { statuses.push(JSON.parse(options.body).status);return response({ status: "unknown" }); }
    return response({ status: "needs_reconciliation" });
  });
  c.registerActions({ "ui.navigate": () => { handlers++;return {}; } });
  await c.executeCommand(command);await c.executeCommand(command);
  assert.equal(begins, 1);assert.equal(handlers, 0);
  assert.deepEqual(statuses, ["unknown", "unknown"]);
  assert.equal(c.receipts[command.id].status, "unknown");
});

test("receipt retry failures are reported without rejecting dispatched execution", async t => {
  const failure = new Error("receipt response lost"), errors = [];
  const c = client(t, async () => { throw failure; });
  c.receipts[command.id] = { command, status: "succeeded", result: { page: "orders" } };
  c.on("error", error => errors.push(error));
  await c.executeCommand(command);
  assert.deepEqual(errors, [failure]);
  assert.equal(c.receipts[command.id].status, "succeeded");
});
