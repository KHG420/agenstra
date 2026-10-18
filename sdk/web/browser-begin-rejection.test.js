import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient, AgenstraError } from "./agenstra-client.js";

const command = { id: "original-command", run_id: "original-run", session_id: "original-session", generation: 1,
  profile_digest: "profile", action: "ui.update", arguments: { notes: "saved once" },
  arguments_sha256: "a".repeat(64), context_revision: 1 };
const response = (value, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => value });

function fixture(t, rejection = 403, proof = () => ({ ...command, status: "dispatched" })) {
  const state = { authorized: false, begins: 0, handlers: 0, proofs: 0, handlerError: null, results: [], errors: [] };
  const client = new AgenstraClient({ integration: "app", storage: null, getSession: async () => "ticket", fetch: async (path, options) => {
    if (path.endsWith("/begin")) {
      state.begins++;
      if (!state.authorized) {
        if (rejection === "network") throw new Error("begin response unavailable");
        return response({ code: "capability_not_granted" }, rejection);
      }
      return response({ accepted: true });
    }
    if (path === "/browser/v1/commands/"+command.id) { state.proofs++;return response(proof()); }
    if (path.endsWith("/result")) {
      const body = JSON.parse(options.body);
      state.results.push(body);
      return response({ status: body.status });
    }
    return response({ status: "completed" });
  } });
  client.browser = { id: command.session_id, generation: 1, key: "browser-key", context_revision: 1 };
  client.registerActions({ "ui.update": async () => { state.handlers++;if (state.handlerError) throw state.handlerError;return { notes: "saved once" }; } });
  client.on("error", error => state.errors.push(error.code));
  t.after(() => client.destroy({ closeSession: false }));
  return { client, state };
}

for (const status of [403, 409, 422, 429]) {
  test(`rejected begin ${status} with the original unclaimed command can recover without an unknown receipt`, async t => {
    const { client, state } = fixture(t, status);
    await client.executeCommand(command);
    assert.equal(state.handlers, 0);
    assert.equal(state.begins, 1);
    assert.equal(state.proofs, 1);
    assert.equal(client.receipts[command.id], undefined);
    assert.deepEqual(state.results, []);
    assert.deepEqual(state.errors, ["capability_not_granted"]);
    state.authorized = true;
    await client.flushReceipts();
    await client.executeCommand(command);
    await client.executeCommand(command);
    assert.equal(state.begins, 2);
    assert.equal(state.handlers, 1);
    assert.deepEqual(state.results.map(result => result.status), ["succeeded"]);
  });
}

test("lost begin responses retain uncertainty without consulting a possibly later unclaimed state", async t => {
  const { client, state } = fixture(t, "network");
  await client.executeCommand(command);
  assert.equal(state.handlers, 0);
  assert.equal(state.proofs, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(state.results[0].error_code, "browser_handler_outcome_unknown");
});

test("an original queued command remains eligible after a rejected begin", async t => {
  const { client, state } = fixture(t, 403, () => ({ ...command, status: "queued" }));
  await client.executeCommand(command);
  assert.equal(state.handlers, 0);
  assert.equal(client.receipts[command.id], undefined);
  assert.deepEqual(state.results, []);
});

test("a server error after begin retains uncertainty without trusting unclaimed proof", async t => {
  const { client, state } = fixture(t, 503);
  await client.executeCommand(command);
  assert.equal(state.handlers, 0);
  assert.equal(state.proofs, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(state.results[0].status, "unknown");
});

for (const status of ["running", "unknown", "succeeded"]) {
  test(`rejected begin cannot discard a command already ${status}`, async t => {
    const { client, state } = fixture(t, 403, () => ({ ...command, status }));
    await client.executeCommand(command);
    assert.equal(state.handlers, 0);
    assert.equal(client.receipts[command.id].status, "unknown");
    assert.equal(state.results[0].status, "unknown");
  });
}

test("an unavailable command proof retains the original uncertainty", async t => {
  const { client, state } = fixture(t, 403, () => { throw new Error("proof unavailable"); });
  await client.executeCommand(command);
  assert.equal(state.handlers, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(state.results[0].status, "unknown");
});

test("a handler error after an accepted begin cannot use unclaimed recovery", async t => {
  const { client, state } = fixture(t);
  state.authorized = true;
  state.handlerError = new AgenstraError("capability_not_granted", 403);
  await client.executeCommand(command);
  assert.equal(state.handlers, 1);
  assert.equal(state.proofs, 0);
  assert.equal(client.receipts[command.id].status, "unknown");
  assert.equal(state.results[0].status, "unknown");
});

for (const field of ["id", "run_id", "session_id", "generation", "profile_digest", "action", "arguments_sha256", "context_revision"]) {
  test(`an unclaimed proof with a different ${field} cannot erase original uncertainty`, async t => {
    const { client, state } = fixture(t, 403, () => ({ ...command, status: "dispatched", [field]: typeof command[field] === "number" ? 2 : "different" }));
    await client.executeCommand(command);
    assert.equal(state.handlers, 0);
    assert.equal(client.receipts[command.id].status, "unknown");
    assert.equal(state.results[0].status, "unknown");
  });
}
