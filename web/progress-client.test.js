import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

function client() { return new AgenstraClient({ integration: "records", storage: null, getSession: async () => "ticket", pollInterval: 1 }); }

test("run events and steering carry the cursor, revision and retry identity", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  const requests = [];
  c.request = async (path, options) => { requests.push({ path, options }); return {}; };
  await c.getRunEvents("run-1", { after: 42, limit: 20 });
  await c.steerRun("run-1", "Use metric units", 5, { requestId: "request-1" });
  assert.equal(requests[0].path, "/web/v1/runs/run-1/events?after=42&limit=20");
  assert.deepEqual(requests[1], { path: "/web/v1/runs/run-1/steer", options: { method: "POST", body: { request_id: "request-1", text: "Use metric units", revision: 5 } } });
  c.request = async () => { throw new Error("lost response"); };
  await assert.rejects(c.steerRun("run-1", "Use metric units", 5, { requestId: "request-1" }), error => error.requestId === "request-1");
});

test("generic reconciliation submits the original invocation binding without client results", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  const requests = [];
  c.request = async (path, options) => { requests.push({ path, options }); return { status: "queued" }; };
  const invocation = { invocation_id: "original-1", arguments_sha256: "digest", data: { invented: true } };
  await c.reconcileInvocation("run-1", invocation, 9);
  assert.deepEqual(requests, [{ path: "/web/v1/runs/run-1/reconcile", options: { method: "POST", body: { invocation_id: "original-1", arguments_sha256: "digest", revision: 9 } } }]);
});

test("watchRun drains event pages before stopping a terminal run and advances the cursor", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  const order = [], cursors = [];
  c.getRun = async () => { order.push("status"); return { run_id: "run-1", status: "completed" }; };
  c.getRunEvents = async (_id, { after }) => {
    order.push("events"); cursors.push(after);
    return after === 7 ? Array.from({ length: 100 }, (_, i) => ({ sequence: i + 8, event: { kind: "call_finished" } })) : [{ sequence: 108, event: { kind: "run_stopped" } }];
  };
  const snapshots = [];
  await new Promise(resolve => c.watchRun("run-1", snapshot => { snapshots.push(snapshot); if (snapshot.cursor === 108) resolve(); }, { after: 7 }));
  assert.deepEqual(order, ["status", "events", "status", "events"]);
  assert.deepEqual(cursors, [7, 107]);
  assert.equal(snapshots[1].events[0].event.kind, "run_stopped");
  assert.equal(c.runWatchers.size, 0);
});

test("unsubscribe during an outstanding request prevents callbacks and later polls", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  let release;
  c.getRun = () => new Promise(resolve => { release = resolve; });
  c.getRunEvents = async () => { assert.fail("unsubscribed watcher fetched events"); };
  const stop = c.watchRun("run-1", () => assert.fail("late callback"));
  stop(); stop();
  release({ status: "running" });
  await Promise.resolve();
  assert.equal(c.runWatchers.size, 0);
});

test("watchRun retries without skipping events and destroy removes every watcher", async () => {
  const c = client();
  const cursors = [];
  c.getRun = async () => ({ status: "running" });
  c.getRunEvents = async (_id, { after }) => {
    cursors.push(after);
    if (cursors.length === 1) throw new Error("temporary failure");
    return [{ sequence: 9, event: { kind: "model_decided" } }];
  };
  let errors = 0;
  c.on("error", () => errors++);
  await new Promise(resolve => c.watchRun("run-1", snapshot => { assert.equal(snapshot.cursor, 9); resolve(); }, { after: 8 }));
  await c.destroy({ closeSession: false });
  assert.deepEqual(cursors, [8, 8]);
  assert.equal(errors, 1);
  assert.equal(c.runWatchers.size, 0);
});

test("unsubscribing inside a run callback leaves no polling timer", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let reads = 0;
  c.getRun = async () => { reads++; return { status: "running" }; };
  c.getRunEvents = async () => [];
  let stop;
  await new Promise(resolve => {
    stop = c.watchRun("run-1", () => { stop(); resolve(); });
  });
  assert.equal(c.runWatchers.size, 0);
  let pendingTimers = 0;
  const timeout = globalThis.setTimeout;
  // Verify scheduling itself, since a stopped poll would otherwise mask the leak.
  t.mock.method(globalThis, "setTimeout", (...args) => { pendingTimers++; return timeout(...args); });
  c.getRun = async () => ({ status: "running" });
  await new Promise(resolve => {
    stop = c.watchRun("run-2", () => { stop(); resolve(); });
  });
  assert.equal(pendingTimers, 0);
  t.mock.timers.tick(10);
  assert.equal(reads, 1);
});

test("unsubscribing inside an error listener leaves no polling timer", async t => {
  const c = client();
  t.after(() => c.destroy({ closeSession: false }));
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let pendingTimers = 0;
  const timeout = globalThis.setTimeout;
  t.mock.method(globalThis, "setTimeout", (...args) => { pendingTimers++; return timeout(...args); });
  const failure = new Error("request failed");
  c.getRun = async () => { throw failure; };
  let stop;
  const error = await new Promise(resolve => {
    c.on("error", value => { stop(); resolve(value); });
    stop = c.watchRun("run-1", () => assert.fail("failed request produced a snapshot"));
  });
  assert.equal(error, failure);
  assert.equal(c.runWatchers.size, 0);
  assert.equal(pendingTimers, 0);
});
