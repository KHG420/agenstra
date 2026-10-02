import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

test("telemetry uses the scoped run route and watchRun forwards the same revision", async t => {
  const c = new AgenstraClient({ integration: "records", storage: null, getSession: async () => "ticket" });
  t.after(() => c.destroy({ closeSession: false }));
  c.request = async path => { assert.equal(path, "/web/v1/runs/run%2Fone/telemetry"); return { run_revision: 9 }; };
  assert.equal((await c.getRunTelemetry("run/one")).run_revision, 9);
  const telemetry = { run_revision: 10, context: { input_characters: 500 } };
  c.getRun = async () => ({ status: "completed", revision: 10, telemetry });
  c.getRunEvents = async () => [];
  await new Promise(resolve => c.watchRun("run-1", snapshot => { assert.equal(snapshot.telemetry, telemetry); assert.equal(snapshot.run.revision, snapshot.telemetry.run_revision); resolve(); }));
});
