import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const command = { id: "original-command", run_id: "original-run", session_id: "original-session", generation: 1,
  action: "ui.read", arguments: {}, context_revision: 1 };
const response = value => ({ status: 200, ok: true, json: async () => value });
function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}
function fixture(t, onBegin) {
  const state = { requests: [], handlers: 0, observations: 0, result: { id: "original-owner" } };
  const client = new AgenstraClient({ integration: "app", storage: null, getSession: async () => "ticket", fetch: async (path, options) => {
    state.requests.push({ path, body: options.body });
    if (path.endsWith("/begin")) return onBegin ? await onBegin(client) : response({ accepted: true });
    if (path.endsWith("/result")) return response({ status: JSON.parse(options.body).status });
    return response({ status: "completed" });
  } });
  client.browser = { id: command.session_id, generation: 1, key: "browser-key", context_revision: 1 };
  client.registerActions({ "ui.read": async () => { state.handlers++;return state.result; } });
  t.after(() => client.destroy({ closeSession: false }));
  return { client, state };
}

test("a disposed client does not create a new command receipt or start a handler", async t => {
  const { client, state } = fixture(t);
  await client.destroy({ closeSession: false });
  await client.executeCommand(command);
  assert.deepEqual(state.requests, []);
  assert.equal(state.handlers, 0);
  assert.deepEqual(client.receipts, {});
});

test("teardown while awaiting prior observations does not call the page reader or begin", async t => {
  const wait = deferred();
  const { client, state } = fixture(t);
  client.observationChain = wait.promise;
  client.options.getPageObservation = () => { state.observations++;return {}; };
  const executing = client.executeCommand(command);
  await client.destroy({ closeSession: false });
  wait.resolve();await executing;
  assert.equal(state.observations, 0);
  assert.equal(state.handlers, 0);
  assert.deepEqual(state.requests, []);
  assert.deepEqual(client.receipts, {});
});

test("a late page observation after teardown cannot publish or begin the old command", async t => {
  const entered = deferred(), wait = deferred();
  const { client, state } = fixture(t);
  client.options.getPageObservation = async () => { entered.resolve();return await wait.promise; };
  const executing = client.executeCommand(command);
  await entered.promise;
  await client.destroy({ closeSession: false });
  wait.resolve({ owner: "new-owner" });await executing;
  assert.deepEqual(state.requests, []);
  assert.equal(state.handlers, 0);
  assert.deepEqual(client.receipts, {});
});

test("teardown while a page upload finishes does not send begin", async t => {
  const entered = deferred(), wait = deferred();
  const { client, state } = fixture(t);
  client.options.getPageObservation = () => ({ page: "changed" });
  client.updatePageObservation = async () => { entered.resolve();await wait.promise; };
  const executing = client.executeCommand(command);
  await entered.promise;
  await client.destroy({ closeSession: false });
  wait.resolve();await executing;
  assert.deepEqual(state.requests, []);
  assert.equal(state.handlers, 0);
  assert.deepEqual(client.receipts, {});
});

test("an accepted begin response after teardown retains uncertainty without invoking its handler", async t => {
  const { client, state } = fixture(t, client => ({ status: 200, ok: true, json: async () => {
    await client.destroy({ closeSession: false });
    return { accepted: true };
  } }));
  await client.executeCommand(command);
  assert.equal(state.requests.length, 1);
  assert.equal(state.handlers, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(client.receipts[command.id].error_code, "browser_handler_outcome_unknown");
});

test("a running listener may dispose the client without starting a host request under a new identity", async t => {
  const { client, state } = fixture(t);
  client.on("action", event => {
    if (event.status !== "running") return;
    state.result = { id: "new-owner" };
    void client.destroy({ closeSession: false });
  });
  await client.executeCommand(command);
  assert.equal(state.handlers, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(Object.hasOwn(client.receipts[command.id], "result"), false);
});

test("a handler already invoked before teardown retains its actual result without later page callbacks", async t => {
  const entered = deferred(), wait = deferred();
  const { client, state } = fixture(t);
  client.options.getPageObservation = () => { state.observations++;return {}; };
  client.actions.set("ui.read", async () => { state.handlers++;entered.resolve();return await wait.promise; });
  const executing = client.executeCommand(command);
  await entered.promise;
  await client.destroy({ closeSession: false });
  wait.resolve({ id: "original-owner", saved: true });await executing;
  assert.equal(state.handlers, 1);
  assert.equal(state.observations, 1);
  assert.equal(client.receipts[command.id].status, "succeeded");
  assert.deepEqual(client.receipts[command.id].result, { id: "original-owner", saved: true });
  assert.equal(state.requests.length, 1);
});

test("a live client still executes and confirms one original handler", async t => {
  const { client, state } = fixture(t);
  await client.executeCommand(command);
  await client.executeCommand(command);
  assert.equal(state.handlers, 1);
  assert.equal(client.receipts[command.id].status, "acked");
});
