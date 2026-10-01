import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";
const response = (data, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => data });
const command = { id: "cmd-1", run_id: "run-1", session_id: "tab-1", generation: 1, action: "ui.navigate", arguments: { page: "orders" }, context_revision: 1 };
function storage() { const data = new Map();return { getItem: key => data.get(key) || null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }; }
function client(fetch, store = storage()) {
  const c = new AgenstraClient({ integration: "erp-web", getSession: async () => "web-ticket", fetch, storage: store });
  c.browser = { id: "tab-1", generation: 1, key: "tab-key", context_revision: 1 };
  return c;
}
test("lost ACK retries the original receipt without rerunning a handler", async () => {
  let calls = 0, acknowledgements = 0;
  const c = client(async (path, options) => {
    if (path.endsWith("/begin")) return response({ accepted: true });
    if (path.endsWith("/result")) {
      const body = JSON.parse(options.body);assert.equal(body.generation, 1);
      acknowledgements++;
      if (acknowledgements === 1) throw new Error("ACK connection lost");
      return response({ status: "succeeded" });
    }
    if (path.endsWith("/context")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
    return response({ status: "waiting" });
  });
  c.registerActions({ "ui.navigate": async () => { calls++; return { page: "orders" }; } });
  await c.executeCommand(command);
  assert.equal(c.receipts[command.id].status, "succeeded");
  await c.executeCommand(command);
  assert.equal(calls, 1);assert.equal(acknowledgements, 2);
  await c.destroy({ closeSession: false });
});
test("restart with an interrupted handler reports uncertainty instead of executing", async () => {
  const store = storage();let calls = 0;const sent = [];
  const original = client(async () => response({}), store);
  original.receipts[command.id] = { command, status: "running" };original.save("receipts", original.receipts);
  await original.destroy({ closeSession: false });
  const resumed = client(async (path, options) => { sent.push(JSON.parse(options.body));return response({ status: "unknown" }); }, store);
  resumed.browser.generation = 2;
  resumed.registerActions({ "ui.navigate": () => { calls++; return {}; } });
  await resumed.executeCommand(command);
  assert.equal(calls, 0);assert.equal(sent[0].status, "unknown");assert.equal(sent[0].generation, 1);
  await resumed.destroy({ closeSession: false });
});
test("heartbeat flush does not declare an active handler interrupted", async () => {
  let resolveHandler;let entered;
  const waiting = new Promise(resolve => { resolveHandler = resolve; });
  const handlerEntered = new Promise(resolve => { entered = resolve; });
  const statuses = [];
  const c = client(async (path, options) => {
    if (path.endsWith("/begin")) return response({ accepted: true });
    if (path.endsWith("/result")) { statuses.push(JSON.parse(options.body).status);return response({ status: "succeeded" }); }
    if (path.endsWith("/context")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
    return response({ status: "waiting" });
  });
  c.registerActions({ "ui.navigate": async () => { entered();await waiting;return { page: "orders" }; } });
  const executing = c.executeCommand(command);await handlerEntered;
  await c.flushReceipts();assert.deepEqual(statuses, []);
  resolveHandler();await executing;
  assert.deepEqual(statuses, ["succeeded"]);
  await c.destroy({ closeSession: false });
});
test("a rejected stale-context claim never runs the page handler", async () => {
  let calls = 0;
  const c = client(async () => response({ accepted: false, command: { status: "failed", error_code: "browser_context_changed" } }));
  c.registerActions({ "ui.navigate": () => { calls++;return {}; } });
  await c.executeCommand(command);assert.equal(calls, 0);
  await c.destroy({ closeSession: false });
});
test("expired tickets refresh once without exposing the host API key", async () => {
  let tickets = 0, requests = 0;
  const c = new AgenstraClient({ integration: "erp", storage: null, getSession: async () => "web-ticket-" + (++tickets), fetch: async (_path, options) => {
    requests++;assert.equal(options.headers.Authorization, "Bearer web-ticket-" + requests);
    return requests === 1 ? response({ code: "unauthorized" }, 401) : response({ run_id: "run-1" });
  } });
  assert.equal((await c.getRun("run-1")).run_id, "run-1");assert.equal(tickets, 2);
  await c.destroy({ closeSession: false });
});
test("destroy aborts pending work and stops reconnection", async () => {
  let started;
  const ready = new Promise(resolve => { started = resolve; });
  const c = client((_path, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });started();
  }));
  const pending = c.getRun("run-1");await ready;
  await c.destroy({ closeSession: false });
  await assert.rejects(pending, { name: "AbortError" });
  await assert.rejects(c.getRun("run-1"), { code: "client_closed" });
  assert.equal(c.aborters.size, 0);
});
test("bridge-only run does not create a chat conversation", async () => {
  const paths = [];
  const c = client(async path => { paths.push(path);return response({ run_id: "run-1" }); });
  c.connectBrowser = async () => c.browser;
  await c.run("Open orders", { requestId: "request-1" });
  assert.deepEqual(paths, ["/browser/v1/runs"]);
  await c.destroy({ closeSession: false });
});

for (const failStep of ["getRun", "reconcile"]) {
  test(`ACK persists recovery work when ${failStep} fails and the client reloads`, async () => {
    const store = storage();let ack = 0, recovery = 0, fail = true, completed = false;
    const fetch = async path => {
      if (path.endsWith("/result")) { ack++;return response({ status: "succeeded" }); }
      if (path.endsWith("/reconcile")) {
        recovery++;
        if (fail && failStep === "reconcile") { fail = false;throw new Error("lost reconciliation response"); }
        completed = true;return response({ status: "queued" });
      }
      if (fail && failStep === "getRun") { fail = false;throw new Error("lost run response"); }
      return response({ status: completed ? "completed" : "needs_reconciliation", revision: 2 });
    };
    const first = client(fetch, store);
    first.receipts[command.id] = { command, status: "succeeded", result: { page: "orders" } };
    await assert.rejects(first.flushReceipts());assert.equal(first.receipts[command.id].status, "confirmed");
    await first.destroy({ closeSession: false });
    const resumed = client(fetch, store);
    await resumed.flushReceipts();await resumed.flushReceipts();
    assert.equal(ack, 1);assert.equal(recovery, failStep === "reconcile" ? 2 : 1);
    assert.equal(resumed.receipts[command.id].status, "acked");
    await resumed.destroy({ closeSession: false });
  });
}

test("a confirmed receipt retries when Host enters reconciliation after ACK", async () => {
  let reads = 0, recovery = 0;
  const c = client(async path => {
    if (path.endsWith("/reconcile")) { recovery++;return response({ status: "queued" }); }
    reads++;return response({ status: reads === 1 ? "waiting" : "needs_reconciliation", revision: 3 });
  });
  c.receipts[command.id] = { command, status: "confirmed" };
  await c.flushReceipts();await c.flushReceipts();assert.equal(recovery, 1);
  await c.destroy({ closeSession: false });
});

test("destroy while acquiring a ticket cannot start a later request", async () => {
  let provideTicket, entered;let requests = 0;
  const started = new Promise(resolve => { entered = resolve; });
  const ticket = new Promise(resolve => { provideTicket = resolve; });
  const c = new AgenstraClient({ integration: "erp", storage: null,
    getSession: () => { entered();return ticket; },
    fetch: async () => { requests++;return response({}); }
  });
  const pending = c.getRun("run-1");await started;
  await c.destroy();provideTicket("web-ticket");
  await assert.rejects(pending, { code: "client_closed" });assert.equal(requests, 0);
});

test("an invalid browser result cannot emit a confirmed success", async () => {
  const events = [];
  const c = client(async path => {
    if (path.endsWith("/begin")) return response({ accepted: true });
    if (path.endsWith("/context")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
    if (path.endsWith("/result")) return response({ code: "browser_result_invalid" }, 422);
    return response({});
  });
  c.on("action", event => events.push(event.status));
  c.registerActions({ "ui.navigate": () => ({ page: 42 }) });
  await c.executeCommand(command);
  assert.deepEqual(events, ["running", "unknown"]);
  assert.equal(c.receipts[command.id].status, "unknown");
  await c.destroy({ closeSession: false });
});
