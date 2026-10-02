import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { exportActions } from "./export-actions.mjs";

test("one profile derives action types and versions while preserving host implementations", async () => {
  const dir = await mkdtemp(join(tmpdir(), "agenstra-actions-"));
  try {
    const path = join(dir, "profile.json");
    const profile = { schema: "agenstra.frontend-profile.v1", handler_version: "1", actions: [{ name: "ui.open", input_schema: { type: "object", properties: { id: { type: "string" } }, required: ["id"], additionalProperties: false }, output_schema: { type: "object", properties: { page: { enum: ["detail"] } }, required: ["page"] } }] };
    await writeFile(path, JSON.stringify(profile));
    assert.equal((await exportActions(path, dir)).handlers_created, true);
    assert.match(await readFile(join(dir, "agenstra-actions.d.ts"), "utf8"), /"id": string/);
    await writeFile(join(dir, "agenstra-handlers.js"), "original business implementation");
    profile.handler_version = "2"; await writeFile(path, JSON.stringify(profile));
    assert.equal((await exportActions(path, dir)).handlers_created, false);
    assert.equal(await readFile(join(dir, "agenstra-handlers.js"), "utf8"), "original business implementation");
    assert.match(await readFile(join(dir, "agenstra-profile.js"), "utf8"), /handlerVersion = "2"/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
