import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient, AgenstraActionError } from "./agenstra-client.js";
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
    if (path.endsWith("/observation")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
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
    if (path.endsWith("/observation")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
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
  test(`executing an action preserves the server ACK when ${failStep} subsequently fails`, async t => {
    const store = storage();let calls = 0, ack = 0, recovery = 0, fail = true, completed = false;
    const fetch = async (path, options) => {
      if (path.endsWith("/begin")) return response({ accepted: true });
      if (path.endsWith("/result")) { ack++;assert.equal(JSON.parse(options.body).status, "succeeded");return response({ status: "succeeded" }); }
      if (path.endsWith("/reconcile")) {
        recovery++;
        if (fail && failStep === "reconcile") { fail = false;throw new Error("lost reconciliation response"); }
        completed = true;return response({ status: "queued" });
      }
      if (fail && failStep === "getRun") { fail = false;throw new Error("lost run response"); }
      return response({ status: completed ? "completed" : "needs_reconciliation", revision: 2 });
    };
    const first = client(fetch, store);
    t.after(() => first.destroy({ closeSession: false }));
    first.registerActions({ "ui.navigate": () => { calls++;return { page: "orders" }; } });
    await first.executeCommand(command);
    assert.equal(first.receipts[command.id].status, "confirmed");
    await first.destroy({ closeSession: false });
    const resumed = client(fetch, store);
    t.after(() => resumed.destroy({ closeSession: false }));
    resumed.registerActions({ "ui.navigate": () => { calls++;return { page: "orders" }; } });
    await resumed.executeCommand(command);await resumed.flushReceipts();
    assert.equal(calls, 1);assert.equal(ack, 1);
    assert.equal(recovery, failStep === "reconcile" ? 2 : 1);
    assert.equal(resumed.receipts[command.id].status, "acked");
  });
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
    if (path.endsWith("/observation")) return response({ session: { id: "tab-1", generation: 1, context_revision: 2 } });
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

test("the host selects a conversation by id and sends no agent context", async t => {
  const paths = [], events = [], bodies = [];
  const c = new AgenstraClient({ integration: "erp", storage: storage(), getSession: async () => "ticket", fetch: async (path, options) => {
    paths.push(path);
    if (options.body) bodies.push(JSON.parse(options.body));
    if (path.endsWith("/messages")) return response({ id: "message-1", conversation_id: "selected" });
    return response({ conversation: { id: "selected", integration_id: "erp" }, messages: [{ id: "prior" }] });
  } });
  t.after(() => c.destroy({ closeSession: false }));
  c.on("conversation", snapshot => events.push(snapshot));
  await c.selectConversation("selected");
  await c.send("Continue", { clientId: "stable" });
  assert.deepEqual(paths, ["/chat/v1/conversations/selected", "/chat/v1/conversations/selected/messages"]);
  assert.deepEqual(bodies, [{ client_id: "stable", text: "Continue", session_id: "" }]);
  assert.equal(events[0].messages[0].id, "prior");
  assert.equal(typeof c.setContext, "undefined");
});

test("conversation creation and listing use the current integration", async t => {
  const requests = [];
  const c = client(async (path, options) => {
    requests.push({ path, body: options.body && JSON.parse(options.body) });
    if (options.method === "POST") return response({ id: "new", integration_id: "erp-web" });
    return response([{ id: "new", integration_id: "erp-web" }]);
  });
  t.after(() => c.destroy({ closeSession: false }));
  await c.createConversation();
  assert.equal((await c.listConversations())[0].id, "new");
  assert.deepEqual(requests, [
    { path: "/chat/v1/conversations", body: { integration_id: "erp-web" } },
    { path: "/chat/v1/conversations?integration_id=erp-web", body: undefined }
  ]);
});

test("invalid selections preserve the previous selected conversation", async t => {
  const c = client(async path => {
    if (path.endsWith("/missing")) return response({ code: "not_found" }, 404);
    return response({ conversation: { id: "other", integration_id: "another-app" }, messages: [] });
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.rememberConversation({ id: "current", integration_id: "erp-web" });
  await assert.rejects(c.selectConversation("missing"), { code: "not_found" });
  await assert.rejects(c.selectConversation("other"), { code: "conversation_integration_mismatch" });
  assert.equal((await c.getConversation()).id, "current");
  assert.equal(c.load("conversation"), "current");
});

test("concurrent selection and sending preserve the host's selection order", async t => {
  let releaseFirst, entered;
  const firstEntered = new Promise(resolve => { entered = resolve; });
  const delayed = new Promise(resolve => { releaseFirst = resolve; });
  const paths = [];
  const c = client(async path => {
    paths.push(path);
    if (path.endsWith("/first")) { entered(); await delayed; }
    const id = path.endsWith("/first") ? "first" : "second";
    return response({ conversation: { id, integration_id: "erp-web" }, messages: [] });
  });
  t.after(() => c.destroy({ closeSession: false }));
  const first = c.selectConversation("first");await firstEntered;
  const second = c.selectConversation("second");
  const sent = c.send("Use the selected conversation", { clientId: "stable" });
  releaseFirst();await Promise.all([first, second, sent]);
  assert.equal(c.conversation.id, "second");
  assert.equal(paths.at(-1), "/chat/v1/conversations/second/messages");
  assert.equal(paths.includes("/chat/v1/conversations"), false);
});

test("a late poll from a previous selection cannot replace the host's displayed history", async t => {
  let releaseOld, entered;
  const reading = new Promise(resolve => { entered = resolve; });
  const oldResponse = new Promise(resolve => { releaseOld = resolve; });
  const c = client(async path => {
    const id = path.endsWith("/old") ? "old" : "new";
    if (id === "old") { entered();await oldResponse; }
    return response({ conversation: { id, integration_id: "erp-web" }, messages: [] });
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.rememberConversation({ id: "old", integration_id: "erp-web" });
  c.chatWatchers = 1;
  const events = [];
  c.on("conversation", snapshot => events.push(snapshot.conversation.id));
  const polling = c.pollChat();await reading;
  await c.selectConversation("new");releaseOld();await polling;
  assert.deepEqual(events, ["new"]);
});

test("reload restores only the selected id and asks the framework for conversation state", async t => {
  const store = storage();
  const original = client(async () => response({}), store);
  original.rememberConversation({ id: "saved", integration_id: "erp-web" });
  await original.destroy({ closeSession: false });
  const paths = [];
  const resumed = client(async path => {
    paths.push(path);
    return response({ conversation: { id: "saved", integration_id: "erp-web" }, messages: [] });
  }, store);
  t.after(() => resumed.destroy({ closeSession: false }));
  assert.equal((await resumed.getConversation()).id, "saved");
  assert.deepEqual(paths, ["/chat/v1/conversations/saved"]);
});

test("switching away and back to the same id still fences an older poll", async t => {
  let releaseOld, entered, reads = 0;
  const reading = new Promise(resolve => { entered = resolve; });
  const oldResponse = new Promise(resolve => { releaseOld = resolve; });
  const c = client(async path => {
    const id = path.endsWith("/first") ? "first" : "second";
    const read = ++reads;
    if (read === 1) { entered();await oldResponse; }
    return response({ conversation: { id, integration_id: "erp-web" }, messages: [{ status: read === 1 ? "active" : "completed" }] });
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.rememberConversation({ id: "first", integration_id: "erp-web" });
  c.chatWatchers = 1;
  const events = [];
  c.on("conversation", snapshot => events.push(snapshot.conversation.id));
  const polling = c.pollChat();await reading;
  await c.selectConversation("second");await c.selectConversation("first");
  releaseOld();await polling;
  assert.deepEqual(events, ["second", "first"]);
});

test("concurrent first sends create only one default conversation", async t => {
  let creates = 0;
  const c = client(async (path, options) => {
    if (path === "/chat/v1/conversations") { creates++;return response({ id: "default", integration_id: "erp-web" }); }
    assert.equal(path, "/chat/v1/conversations/default/messages");
    return response({ client_id: JSON.parse(options.body).client_id });
  });
  t.after(() => c.destroy({ closeSession: false }));
  await Promise.all([c.send("One", { clientId: "one" }), c.send("Two", { clientId: "two" })]);
  assert.equal(creates, 1);
});

test('a proven non-committed failure returns a failed receipt; ordinary exceptions remain unknown', async () => {
  for (const [error, status] of [[new AgenstraActionError('permission_denied', 'Not allowed'), 'failed'], [new Error('Network lost after saving'), 'unknown']]) {
    const results = [];
    const c = client(async (path, options) => {
      if (path.endsWith('/begin')) return response({ accepted: true });
      if (path.endsWith('/result')) { results.push(JSON.parse(options.body)); return response({ status }); }
      return response({ status: 'waiting' });
    });
    c.registerActions({ 'ui.navigate': async () => { throw error; } });
    await c.executeCommand(command);
    assert.equal(results[0].status, status);
    assert.equal(results[0].error_code, status === 'failed' ? 'permission_denied' : 'browser_handler_outcome_unknown');
    await c.destroy({ closeSession: false });
  }
});
test('an unchanged handler result does not advance page revision and stale manual changes are refreshed by heartbeat', async () => {
  const updates = [];
  let page = 'home';
  const c = client(async (path, options) => {
    if (path.endsWith('/begin')) return response({ accepted: true });
    if (path.endsWith('/observation')) { updates.push(JSON.parse(options.body)); return response({ session: { id: 'tab-1', generation: 1, context_revision: updates.length + 1 } }); }
    if (path.endsWith('/poll')) return response({ commands: [] });
    return response({ status: 'succeeded' });
  });
  c.options.getPageObservation = () => ({ page }); c.pageObservation = { page: 'home' };
  c.registerActions({ 'ui.navigate': async () => ({ page: 'home' }) });
  await c.executeCommand(command); assert.equal(updates.length, 0);
  page = 'orders'; await c.pollBrowser(); assert.equal(updates.length, 1);
  assert.deepEqual(updates[0].observation, { page: 'orders' });
  await c.destroy({ closeSession: false });
});

test('public HTTP pages without randomUUID still get stable valid UUID request IDs', async () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  const native = globalThis.crypto;
  const c = client(async () => response({}));
  try {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: bytes => native.getRandomValues(bytes) } });
    const first = c.id(), second = c.id();
    assert.match(first, /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/);
    assert.notEqual(first, second);
  } finally { Object.defineProperty(globalThis, 'crypto', original); await c.destroy({ closeSession: false }); }
});

test("browser key rejection does not exchange another login ticket", async t => {
  let tickets = 0, requests = 0;
  const c = new AgenstraClient({ integration: "lottery", storage: null,
    getSession: async () => { tickets++; return "ticket"; },
    fetch: async () => { requests++; return response({ code: "browser_session_invalid" }, 401); },
  });
  t.after(() => c.destroy({ closeSession: false }));
  await assert.rejects(c.request("/browser/v1/sessions/tab/observation", { method: "POST", browserKey: "key", body: {} }), { code: "browser_session_invalid" });
  assert.equal(tickets, 1); assert.equal(requests, 1);
});

test("chat polling cannot report connected while the browser bridge failed", async t => {
  const states = [];
  const c = new AgenstraClient({ integration: "lottery", storage: null, browser: true, handlerVersion: "1", getSession: async () => "ticket",
    fetch: async path => {
      if (path.endsWith("/sessions")) return response({ session: { id: "tab", generation: 1, context_revision: 0 }, key: "key" });
      if (path.endsWith("/observation")) return response({ code: "browser_session_invalid" }, 401);
      if (path.endsWith("/conversations")) return response({ id: "chat", integration_id: "lottery" });
      return response({ conversation: { id: "chat", integration_id: "lottery" }, messages: [] });
    },
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.on("connection", state => states.push(state.status));
  await assert.rejects(c.connectBrowser(), { code: "browser_session_invalid" });
  c.chatWatchers = 1;
  await c.pollChat();
  assert.deepEqual(states, ["disconnected"]);
});

test("transient browser polling failure recovers its connected state", async t => {
  let fail = true;
  const states = [];
  const c = new AgenstraClient({ integration: "lottery", storage: null, browser: true, getSession: async () => "ticket", fetch: async () => {
    if (fail) throw new Error("temporary transport loss");
    return response({ commands: [] });
  } });
  t.after(() => c.destroy({ closeSession: false }));
  c.browser = { id: "tab", generation: 1, key: "key" };
  c.on("connection", state => states.push(state.status));
  await c.pollBrowser(); clearTimeout(c.browserTimer);
  fail = false; await c.pollBrowser();
  assert.deepEqual(states, ["disconnected", "connected"]);
});
