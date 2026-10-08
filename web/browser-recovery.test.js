import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const response = (data, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => data });
const old = { id: "old-tab", key: "old-key", generation: 1, handler_version: "1", context_revision: 1 };
const replacement = { id: "new-tab", generation: 1, handler_version: "1", context_revision: 0 };
function storage() { const values = new Map(); return { getItem: k => values.get(k) || null, setItem: (k, v) => values.set(k, v), removeItem: k => values.delete(k) }; }
function bridge(t, fetch, store = storage(), version = "1") {
  const c = new AgenstraClient({ integration: "records-web", browser: true, handlerVersion: version, getSession: async () => "ticket", storage: store, fetch });
  c.registerActions({ "ui.navigate": () => ({ page: "orders" }) });
  c.save("browser", old);
  t.after(() => c.destroy({ closeSession: false }));
  return c;
}
function observation(path, version = "1") { return response({ session: { ...(path.includes("new-tab") ? { ...replacement, handler_version: version } : old), context_revision: 1 } }); }

for (const mode of ["send", "run"]) test(`new ${mode} refreshes an idle heartbeat and safely replaces a changed profile with the same request identity`, async t => {
  const submissions = []; let polled = false, recoveries = 0, handlers = 0;
  const c = bridge(t, async (path, options) => {
    if (path.endsWith("/poll")) { polled = true; return response({ commands: [] }); }
    if (path.endsWith("/recover")) { recoveries++; assert.equal(JSON.parse(options.body).acknowledge_unknown, false); return response({ session: replacement, key: "new-key" }); }
    if (path.endsWith("/observation")) return observation(path);
    assert.ok(path.endsWith(mode === "send" ? "/messages" : "/runs"));
    assert.equal(polled, true, "new work was submitted before refreshing the browser heartbeat");
    const body = JSON.parse(options.body); submissions.push(body);
    assert.equal(options.headers["X-Agenstra-Browser-Key"], submissions.length === 1 ? old.key : "new-key");
    return submissions.length === 1 ? response({ code: "browser_profile_changed" }, 409) : response({ run_id: "run" });
  });
  c.browser = old; c.browserPromise = Promise.resolve(old); c.browserConnected = true;
  c.rememberConversation({ id: "history", integration_id: "records-web" });
  c.actions.set("ui.navigate", () => { handlers++; return {}; });
  await (mode === "send" ? c.send("Read details", { clientId: "original-message" }) : c.run("Read details", { requestId: "original-run" }));
  assert.equal(recoveries, 1); assert.equal(submissions.length, 2); assert.equal(handlers, 0);
  assert.deepEqual(submissions.map(body => body.session_id), [old.id, replacement.id]);
  assert.deepEqual(submissions.map(body => body[mode === "send" ? "client_id" : "request_id"]), [mode === "send" ? "original-message" : "original-run", mode === "send" ? "original-message" : "original-run"]);
});

for (const mode of ["send", "run"]) test(`new ${mode} preserves uncertain receipts and refuses profile recovery when the server cannot verify old work`, async t => {
  let submissions = 0;
  const c = bridge(t, async path => {
    if (path.endsWith("/poll")) return response({ commands: [] });
    if (path.endsWith("/recover")) return response({ code: "browser_outcome_unresolved" }, 409);
    submissions++; return response({ code: "browser_profile_changed" }, 409);
  });
  c.browser = old; c.browserPromise = Promise.resolve(old);
  c.rememberConversation({ id: "history", integration_id: "records-web" });
  const receipt = { command: { id: "uncertain", session_id: old.id, generation: 1 }, status: "unknown" };
  c.receipts.uncertain = receipt;
  c.flushReceipts = async () => {};
  await assert.rejects(mode === "send" ? c.send("New work", { clientId: "same-message" }) : c.run("New work", { requestId: "same-run" }), { code: "browser_outcome_unresolved" });
  assert.equal(submissions, 1); assert.equal(c.browser.id, old.id); assert.deepEqual(c.receipts.uncertain, receipt);
});

for (const mode of ["send", "run"]) for (const failure of ["lost", "gateway", "changed-again"]) test(`new ${mode} never loops or replaces an uncertain submission after ${failure}`, async t => {
  let submissions = 0, recoveries = 0;
  const c = bridge(t, async path => {
    if (path.endsWith("/poll")) return response({ commands: [] });
    if (path.endsWith("/observation")) return observation(path);
    if (path.endsWith("/recover")) { recoveries++; return response({ session: replacement, key: "new-key" }); }
    submissions++;
    if (failure === "lost") throw new Error("response lost after commit");
    return response({ code: "browser_profile_changed" }, failure === "gateway" ? 502 : 409);
  });
  c.browser = old; c.browserPromise = Promise.resolve(old);
  c.rememberConversation({ id: "history", integration_id: "records-web" });
  await assert.rejects(mode === "send" ? c.send("New work", { clientId: "original" }) : c.run("New work", { requestId: "original" }), error => error[mode === "send" ? "clientId" : "requestId"] === "original");
  assert.equal(submissions, failure === "changed-again" ? 2 : 1);
  assert.equal(recoveries, failure === "changed-again" ? 1 : 0);
});

for (const failure of ["network", "non-2xx", "unconfirmed-200"]) test(`failed browser close preserves the original session after ${failure}`, async t => {
  const store = storage();let closes = 0, resumes = 0;
  const fetch = async (path, options) => {
    if (path.endsWith("/close")) {
      closes++;
      assert.equal(options.headers["X-Agenstra-Browser-Key"], old.key);
      assert.equal(JSON.parse(options.body).generation, closes === 1 ? 1 : 2);
      if (closes === 1 && failure === "network") throw new Error("close response lost");
      if (closes === 1 && failure === "non-2xx") return response({ code: "request_failed" }, 503);
      if (closes === 1) return response({ status: "pending" });
      return response({ status: "closed" });
    }
    if (path.endsWith("/resume")) {
      resumes++;
      assert.equal(options.headers["X-Agenstra-Browser-Key"], old.key);
      assert.deepEqual(JSON.parse(options.body), { generation: 1, request_id: "original-resume" });
      return response({ session: { ...old, generation: 2, context_revision: 2 } });
    }
    if (path.endsWith("/observation")) return response({ session: { ...old, generation: 2, context_revision: 3 } });
    return response({ commands: [] });
  };
  const first = bridge(t, fetch, store);first.browser = old;first.token = "ticket";
  first.browserResume = { session_id: old.id, generation: 1, request_id: "original-resume" };
  first.save("browser_resume", first.browserResume);
  assert.equal(await first.destroy(), undefined);
  assert.deepEqual(first.load("browser"), old);
  assert.deepEqual(first.load("browser_resume"), first.browserResume);

  const resumed = bridge(t, fetch, store);
  await resumed.connectBrowser();clearTimeout(resumed.browserTimer);
  assert.equal(resumes, 1);
  assert.equal(resumed.browser.id, old.id);
  assert.equal(resumed.browser.key, old.key);
  assert.equal(resumed.browser.generation, 2);
  assert.equal(await resumed.destroy(), undefined);
  assert.equal(closes, 2);
  assert.equal(resumed.load("browser"), null);
  assert.equal(resumed.load("browser_resume"), null);
});

for (const version of ["1", "2"]) test(`an idle profile upgrade replaces the connection with client handler version ${version}`, async t => {
  const paths = [], bodies = [];
  const c = bridge(t, async (path, options) => {
    paths.push(path);
    if (path.endsWith("/resume")) return response({ code: "browser_profile_changed" }, 409);
    if (path.endsWith("/recover")) { bodies.push(JSON.parse(options.body)); return response({ session: { ...replacement, handler_version: version }, key: "new-key" }); }
    if (path.endsWith("/observation")) return observation(path, version);
    return response({ commands: [] });
  }, storage(), version);
  await c.connectBrowser();
  assert.equal(c.browser.id, "new-tab");
  assert.equal(c.browser.handler_version, version);
  assert.equal(paths.includes("/browser/v1/sessions"), false);
  assert.equal(paths.some(p => p.endsWith("/resume")), version === "1");
  assert.equal(bodies[0].acknowledge_unknown, false);
  assert.equal(bodies[0].handler_version, version);
  assert.deepEqual(bodies[0].handlers, ["ui.navigate"]);
});

test("unknown receipts require explicit recovery and preserve the selected conversation and old evidence", async t => {
  let calls = 0; const attempts = [];
  const c = bridge(t, async (path, options) => {
    if (path.endsWith("/resume")) return response({ code: "browser_profile_changed" }, 409);
    if (path.endsWith("/recover")) {
      const body = JSON.parse(options.body); attempts.push(body);
      return body.acknowledge_unknown ? response({ session: replacement, key: "new-key" }) : response({ code: "browser_outcome_unresolved" }, 409);
    }
    if (path.endsWith("/observation")) return observation(path);
    return response({ commands: [] });
  });
  c.actions.set("ui.navigate", () => { calls++; return {}; });
  c.rememberConversation({ id: "history", integration_id: "records-web" });
  const command = { id: "uncertain", session_id: old.id, run_id: "old-run", generation: 1 };
  c.receipts[command.id] = { command, status: "unknown", error_code: "browser_handler_outcome_unknown" }; c.save("receipts", c.receipts);
  await assert.rejects(c.connectBrowser(), { code: "browser_outcome_unresolved" });
  await c.recoverBrowser({ acknowledgeUnknown: true });
  assert.equal(attempts.length, 2); assert.equal(attempts[1].acknowledge_unknown, true);
  assert.equal(c.browser.id, "new-tab"); assert.equal(c.load("conversation"), "history");
  assert.equal(c.receipts.uncertain.status, "unknown"); assert.equal(calls, 0);
});

test("blocked browser connections still allow history selection and cancellation, but refuse new messages", async t => {
  const paths = [];
  const c = bridge(t, async path => {
    paths.push(path);
    if (path.endsWith("/resume")) return response({ code: "browser_profile_changed" }, 409);
    if (path.endsWith("/recover")) return response({ code: "browser_recovery_run_active" }, 409);
    if (path.endsWith("/cancel")) return response({ id: "old-message", status: "cancelling" });
    return response({ conversation: { id: "history", integration_id: "records-web" }, messages: [{ id: "old-message", status: "active" }] });
  });
  c.save("conversation", "history");
  await assert.rejects(c.connectBrowser(), { code: "browser_recovery_run_active" });
  assert.equal((await c.snapshot()).messages[0].id, "old-message");
  await c.selectConversation("history"); await c.cancelMessage("old-message");
  await assert.rejects(c.send("Do something new"), { code: "browser_recovery_run_active" });
  assert.equal(paths.some(p => p.endsWith("/messages")), false);
});

test("a lost recovery response reuses the original request and acknowledgement after reload", async t => {
  const store = storage(), attempts = []; let lost = true;
  const fetch = async (path, options) => {
    if (path.endsWith("/recover")) {
      attempts.push(JSON.parse(options.body));
      if (lost) { lost = false; throw new Error("response lost after commit"); }
      return response({ session: replacement, key: "new-key" });
    }
    if (path.endsWith("/observation")) return observation(path);
    return response({ commands: [] });
  };
  const first = bridge(t, fetch, store); first.browser = old;
  await assert.rejects(first.recoverBrowser({ acknowledgeUnknown: true }), /response lost/);
  await first.destroy({ closeSession: false });
  const resumed = bridge(t, fetch, store);
  await resumed.connectBrowser();
  assert.deepEqual(attempts[0], attempts[1]); assert.equal(attempts[1].acknowledge_unknown, true);
  assert.equal(resumed.browser.id, "new-tab"); assert.equal(resumed.load("browser_recovery"), null);
});

test("a lost resume response preserves the active session and reuses its request after reload", async t => {
  const store = storage(), attempts = [];
  let generation = 1, committed, creates = 0;
  const fetch = async (path, options) => {
    if (path.endsWith("/resume")) {
      const body = JSON.parse(options.body); attempts.push(body);
      if (generation === 1) {
        generation++;
        committed = body.request_id;
        throw new Error("resume response lost after commit");
      }
      if (!committed || body.request_id !== committed || body.generation !== 1) return response({ code: "browser_generation_changed" }, 409);
      return response({ session: { ...old, generation, context_revision: 2 } });
    }
    if (path === "/browser/v1/sessions") { creates++; return response({ session: replacement, key: "new-key" }); }
    if (path.endsWith("/observation")) return response({ session: { ...old, generation, context_revision: 2 } });
    return response({ commands: [] });
  };
  const first = bridge(t, fetch, store);
  first.rememberConversation({ id: "active-history", integration_id: "records-web" });
  await assert.rejects(first.connectBrowser(), /resume response lost/);
  await first.destroy({ closeSession: false });
  const resumed = bridge(t, fetch, store);
  await resumed.connectBrowser();
  assert.equal(creates, 0);
  assert.equal(resumed.browser.id, old.id);
  assert.equal(resumed.browser.generation, 2);
  assert.equal(resumed.load("conversation"), "active-history");
  assert.equal(typeof attempts[0].request_id, "string");
  assert.ok(attempts[0].request_id);
  assert.deepEqual(attempts[1], attempts[0]);
  assert.equal(resumed.load("browser_resume"), null);
});

test("a resume gateway error retains its in-memory identity when storage is disabled", async t => {
  const attempts = [];
  const c = bridge(t, async (path, options) => {
    if (path.endsWith("/resume")) {
      attempts.push(JSON.parse(options.body));
      return attempts.length === 1 ? response({ code: "request_failed" }, 502) : response({ session: { ...old, generation: 2 } });
    }
    if (path.endsWith("/observation")) return response({ session: { ...old, generation: 2, context_revision: 2 } });
    return response({ commands: [] });
  }, null);
  c.browser = old;
  await assert.rejects(c.connectBrowser(), { status: 502 });
  await c.connectBrowser();
  assert.deepEqual(attempts[0], attempts[1]);
  assert.equal(c.browser.generation, 2);
  assert.equal(c.browserResume, null);
});

for (const persisted of [true, false]) test(`a gateway error retains the recovery request with storage ${persisted ? "enabled" : "disabled"}`, async t => {
  const attempts = [];
  const c = bridge(t, async (path, options) => {
    if (path.endsWith("/recover")) {
      attempts.push(JSON.parse(options.body));
      if (attempts.length === 1) return response({ code: "request_failed" }, 502);
      return response({ session: replacement, key: "new-key" });
    }
    if (path.endsWith("/observation")) return observation(path);
    return response({ commands: [] });
  }, persisted ? storage() : null);
  c.browser = old;
  await assert.rejects(c.recoverBrowser({ acknowledgeUnknown: true }), { status: 502 });
  await c.recoverBrowser();
  assert.deepEqual(attempts[0], attempts[1]);
  assert.equal(c.browser.id, "new-tab");
});

test("recovery fences late poll dispatches and coalesces concurrent recovery and connection", async t => {
  let releasePoll, pollEntered, releaseRecovery, recoveryEntered; let recoveries = 0, executions = 0;
  const oldPoll = new Promise(r => { releasePoll = r; }), polling = new Promise(r => { pollEntered = r; });
  const recovery = new Promise(r => { releaseRecovery = r; }), recovering = new Promise(r => { recoveryEntered = r; });
  const c = bridge(t, async path => {
    if (path.includes("old-tab") && path.endsWith("/poll")) { pollEntered(); await oldPoll; return response({ commands: [{ id: "old-command", session_id: old.id, generation: 1, action: "ui.navigate", arguments: {} }] }); }
    if (path.endsWith("/recover")) { recoveries++; recoveryEntered(); await recovery; return response({ session: replacement, key: "new-key" }); }
    if (path.endsWith("/observation")) return observation(path);
    return response({ commands: [] });
  });
  c.browser = old; c.actions.set("ui.navigate", () => { executions++; return {}; });
  const poll = c.pollBrowser(); await polling;
  const first = c.recoverBrowser(); await recovering;
  const second = c.recoverBrowser(), connecting = c.connectBrowser();
  releaseRecovery(); await Promise.all([first, second, connecting]);
  releasePoll(); await poll;
  assert.equal(recoveries, 1); assert.equal(executions, 0); assert.equal(c.browser.id, "new-tab");
});

test("recovery drains queued observations without accepting their stale response", async t => {
  let releaseOld, entered;
  const delayed = new Promise(r => { releaseOld = r; }), reading = new Promise(r => { entered = r; });
  const c = bridge(t, async path => {
    if (path.includes("old-tab") && path.endsWith("/observation")) { entered(); await delayed; return observation(path); }
    if (path.endsWith("/recover")) return response({ session: replacement, key: "new-key" });
    if (path.endsWith("/observation")) return observation(path);
    return response({ commands: [] });
  });
  c.browser = old;
  const pending = c.publishPageObservation(); await reading;
  const recovering = c.recoverBrowser();
  releaseOld(); await Promise.all([recovering, pending]);
  assert.equal(c.browser.id, "new-tab"); assert.equal(c.browser.key, "new-key");
});

test("an executing local handler prevents recovery before any network mutation", async t => {
  let requests = 0;
  const c = bridge(t, async () => { requests++; return response({}); });
  c.browser = old; c.executing = true;
  await assert.rejects(c.recoverBrowser({ acknowledgeUnknown: true }), { code: "browser_recovery_busy" });
  assert.equal(requests, 0); assert.equal(c.browser.id, old.id);
});
