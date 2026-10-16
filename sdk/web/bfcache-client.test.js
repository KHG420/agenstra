import test from "node:test";
import assert from "node:assert/strict";
import { setImmediate as nextTurn } from "node:timers/promises";
import { AgenstraClient } from "./agenstra-client.js";

const conversation = { id: "conversation-1", integration_id: "records-web", created_at: 1 };
const snapshot = { conversation, messages: [{ id: "message-1", text: "Private history", status: "completed" }] };

function lifecycleClient(t, fetch, getSession = async () => "test-ticket") {
  const listeners = new Map();
  const add = globalThis.addEventListener, remove = globalThis.removeEventListener;
  globalThis.addEventListener = (name, callback) => listeners.set(name, callback);
  globalThis.removeEventListener = (name, callback) => {
    if (listeners.get(name) === callback) listeners.delete(name);
  };
  const client = new AgenstraClient({ integration: "records-web", storage: null,
    getSession, pollInterval: 60000, fetch });
  t.after(async () => {
    try { await client.destroy({ closeSession: false }); }
    finally { globalThis.addEventListener = add; globalThis.removeEventListener = remove; }
  });
  return { client, listeners };
}

test("cached leave cancels only the history read and resumes the same conversation once", { timeout: 2000 }, async t => {
  const ready = Promise.withResolvers(), restored = Promise.withResolvers();
  const calls = [], errors = [], connections = [];
  let historySignal, reads = 0;
  const { client, listeners } = lifecycleClient(t, async (path, options) => {
    calls.push({ path, method: options.method });
    if (path === "/chat/v1/conversations") return Response.json(conversation);
    assert.equal(path, "/chat/v1/conversations/" + conversation.id);
    assert.equal(options.method, "GET");
    reads++;
    if (reads > 1) return Response.json(snapshot);
    historySignal = options.signal;
    return new Promise((_resolve, reject) => {
      options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      ready.resolve();
    });
  });
  client.registerActions({ "ui.read": async () => ({ value: 1 }) });
  client.receipts["command-1"] = { status: "succeeded", result: { value: 1 } };
  client.on("error", error => errors.push(error));
  client.on("connection", event => connections.push(event.status));
  assert.equal((await client.getConversation()).id, conversation.id);
  const stop = client.watchConversation(value => restored.resolve(value));
  t.after(stop);
  await ready.promise;
  listeners.get("pagehide")({ persisted: true });
  assert.equal(historySignal.aborted, true);
  await nextTurn();
  await client.pollChat();
  assert.equal(reads, 1);
  assert.equal(client.closed, false);
  assert.equal(client.actions.has("ui.read"), true);
  assert.deepEqual(client.receipts["command-1"], { status: "succeeded", result: { value: 1 } });
  assert.deepEqual(errors, []);
  assert.deepEqual(connections, []);
  listeners.get("pageshow")({ persisted: true });
  listeners.get("pageshow")({ persisted: true });
  assert.deepEqual(await restored.promise, snapshot);
  assert.equal(reads, 2);
  assert.equal(calls.filter(call => call.method === "POST").length, 1);
  assert.equal((await client.getConversation()).id, conversation.id);
});

test("cached leave does not abort or replay an in-flight message write", { timeout: 2000 }, async t => {
  const ready = Promise.withResolvers(), receipt = Promise.withResolvers();
  const message = { id: "message-2", conversation_id: conversation.id, status: "queued" };
  let writeSignal, writes = 0;
  const { client, listeners } = lifecycleClient(t, async (path, options) => {
    if (path === "/chat/v1/conversations") return Response.json(conversation);
    assert.equal(path, "/chat/v1/conversations/" + conversation.id + "/messages");
    assert.equal(options.method, "POST");
    writes++;
    writeSignal = options.signal;
    ready.resolve();
    return receipt.promise;
  });
  const pending = client.send("Read one record", { clientId: "stable-message" });
  await ready.promise;
  listeners.get("pagehide")({ persisted: true });
  assert.equal(writeSignal.aborted, false);
  assert.equal(client.closed, false);
  receipt.resolve(Response.json(message));
  assert.deepEqual(await pending, message);
  assert.equal(writes, 1);
});

test("cached leave during ticket renewal cannot send a late history retry", { timeout: 2000 }, async t => {
  const renewing = Promise.withResolvers(), ticket = Promise.withResolvers(), restored = Promise.withResolvers();
  let sessions = 0, reads = 0;
  const errors = [];
  const { client, listeners } = lifecycleClient(t, async path => {
    if (path === "/chat/v1/conversations") return Response.json(conversation);
    reads++;
    return reads === 1 ? Response.json({ code: "unauthorized" }, { status: 401 }) : Response.json(snapshot);
  }, async () => {
    sessions++;
    if (sessions === 1) return "old-ticket";
    renewing.resolve();
    return ticket.promise;
  });
  t.after(() => ticket.resolve("fresh-ticket"));
  client.on("error", error => errors.push(error));
  const stop = client.watchConversation(value => restored.resolve(value));
  t.after(stop);
  await renewing.promise;
  listeners.get("pagehide")({ persisted: true });
  ticket.resolve("fresh-ticket");
  await nextTurn();
  assert.equal(reads, 1);
  assert.deepEqual(errors, []);
  listeners.get("pageshow")({ persisted: true });
  assert.deepEqual(await restored.promise, snapshot);
  assert.equal(reads, 2);
  assert.equal(sessions, 2);
});

test("cached leave cancels both reads when a history subscriber was replaced mid-request", { timeout: 2000 }, async t => {
  const firstReady = Promise.withResolvers(), secondReady = Promise.withResolvers(), restored = Promise.withResolvers();
  const signals = [];
  const { client, listeners } = lifecycleClient(t, async (path, options) => {
    if (path === "/chat/v1/conversations") return Response.json(conversation);
    if (signals.length === 2) return Response.json(snapshot);
    signals.push(options.signal);
    return new Promise((_resolve, reject) => {
      options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      (signals.length === 1 ? firstReady : secondReady).resolve();
    });
  });
  const stopFirst = client.watchConversation(() => {});
  t.after(stopFirst);
  await firstReady.promise;
  stopFirst();
  const stopSecond = client.watchConversation(value => restored.resolve(value));
  t.after(stopSecond);
  await secondReady.promise;
  listeners.get("pagehide")({ persisted: true });
  assert.deepEqual(signals.map(signal => signal.aborted), [true, true]);
  await nextTurn();
  listeners.get("pageshow")({ persisted: true });
  assert.deepEqual(await restored.promise, snapshot);
});

test("explicit destroy after cached leave cannot restart history or retain page listeners", { timeout: 2000 }, async t => {
  const ready = Promise.withResolvers();
  let reads = 0;
  const { client, listeners } = lifecycleClient(t, async (path, options) => {
    if (path === "/chat/v1/conversations") return Response.json(conversation);
    reads++;
    return new Promise((_resolve, reject) => {
      options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      ready.resolve();
    });
  });
  const stop = client.watchConversation(() => {});
  t.after(stop);
  await ready.promise;
  const show = listeners.get("pageshow");
  listeners.get("pagehide")({ persisted: true });
  await client.destroy({ closeSession: false });
  show?.({ persisted: true });
  await nextTurn();
  assert.equal(reads, 1);
  assert.equal(listeners.size, 0);
  await assert.rejects(client.getRun("run-1"), { code: "client_closed" });
});
