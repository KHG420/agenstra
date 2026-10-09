import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { exportActions } from "./export-actions.mjs";

test("large catalogs can separate ordinary and destructive actions up to the server limit", async () => {
  const dir = await mkdtemp(join(tmpdir(), "agenstra-large-catalog-"));
  try {
    const path = join(dir, "profile.json");
    const profile = { schema: "agenstra.frontend-profile.v1", version: "1", handler_version: "1", context_schema: { type: "object" }, actions: Array.from({ length: 200 }, (_, i) => ({ name: `ui.action_${i}`, description: "Host operation", input_schema: { type: "object" }, output_schema: { type: "object" }, effect: i % 2 ? "destructive" : "write", approval_required: Boolean(i % 2) })) };
    await writeFile(path, JSON.stringify(profile));
    assert.equal((await exportActions(path, dir)).actions.length, 200);
    const types = await readFile(join(dir, "agenstra-actions.d.ts"), "utf8");
    profile.actions.push({ ...profile.actions[0], name: "ui.overflow" });
    await writeFile(path, JSON.stringify(profile));
    await assert.rejects(exportActions(path, dir), /Invalid frontend profile/);
    assert.equal(await readFile(join(dir, "agenstra-actions.d.ts"), "utf8"), types);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("one profile derives action types and versions while preserving host implementations", async () => {
  const dir = await mkdtemp(join(tmpdir(), "agenstra-actions-"));
  try {
    const path = join(dir, "profile.json");
    const profile = { schema: "agenstra.frontend-profile.v1", version: "1", handler_version: "1", context_schema: { type: "object" }, actions: [{ name: "ui.open", description: "Open an object", input_schema: { type: "object", properties: { id: { type: "string" } }, required: ["id"], additionalProperties: false }, output_schema: { type: "object", properties: { page: { enum: ["detail"] } }, required: ["page"] } }] };
    await writeFile(path, JSON.stringify(profile));
    assert.equal((await exportActions(path, dir)).handlers_created, true);
    assert.match(await readFile(join(dir, "agenstra-actions.d.ts"), "utf8"), /"id": string/);
    await writeFile(join(dir, "agenstra-handlers.js"), "original business implementation");
    profile.handler_version = "2"; await writeFile(path, JSON.stringify(profile));
    assert.equal((await exportActions(path, dir)).handlers_created, false);
    assert.equal(await readFile(join(dir, "agenstra-handlers.js"), "utf8"), "original business implementation");
    assert.match(await readFile(join(dir, "agenstra-profile.js"), "utf8"), /handlerVersion = "2"/);
    // Strict TypeScript hosts resolve .js imports through the adjacent .d.ts.
    assert.equal(await readFile(join(dir, "agenstra-profile.d.ts"), "utf8"), 'export declare const handlerVersion: "2";\n');
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("invalid profiles fail before altering any host files", async () => {
  const dir = await mkdtemp(join(tmpdir(), "agenstra-profile-check-"));
  const valid = { schema: "agenstra.frontend-profile.v1", version: "1", handler_version: "1", context_schema: { type: "object" }, actions: [{ name: "ui.open", description: "Open an object", input_schema: { type: "object" }, output_schema: { type: "object" } }] };
  try {
    const path = join(dir, "profile.json");
    await writeFile(join(dir, "agenstra-actions.d.ts"), "existing types");
    for (const mutate of [
      p => { p.actions[0].name = "ui.get_context"; },
      p => { p.actions[0].effect = "execute"; },
      p => { p.actions[0].timeout_seconds = 301; },
      p => { p.actions[0].timeout_seconds = 1.5; },
      p => { p.actions[0].description = ""; },
      p => { p.actions[0].approval_required = "false"; },
      p => { p.version = ""; },
      p => { delete p.context_schema; },
      p => { p.actions.push(p.actions[0]); },
      p => { p.actions[0].input_schema.properties = { id: { $ref: "https://example.com/schema" } }; },
      p => { p.actions[0].input_schema.$schema = "http://json-schema.org/draft-07/schema#"; },
      p => { p.actions[0].typo = true; }
    ]) {
      const profile = structuredClone(valid);mutate(profile);
      await writeFile(path, JSON.stringify(profile));
      await assert.rejects(exportActions(path, dir));
      assert.equal(await readFile(join(dir, "agenstra-actions.d.ts"), "utf8"), "existing types");
      await assert.rejects(readFile(join(dir, "agenstra-handlers.js")), { code: "ENOENT" });
    }
    // Ordinary property names and literal enum data are not schema directives.
    valid.actions[0].input_schema.properties = { $id: { type: "string" }, mode: { enum: [{ $ref: "ordinary data" }] } };
    await writeFile(path, JSON.stringify(valid));
    await exportActions(path, dir);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("closed empty object contracts stay objects rather than accepting primitives", async () => {
  const dir = await mkdtemp(join(tmpdir(), "agenstra-empty-action-"));
  try {
    const path = join(dir, "profile.json");
    await writeFile(path, JSON.stringify({ schema: "agenstra.frontend-profile.v1", version: "1", handler_version: "1", context_schema: { type: "object" }, actions: [{ name: "ui.close", description: "Close a panel", input_schema: { type: "object", additionalProperties: false }, output_schema: { type: "object", additionalProperties: false } }] }));
    await exportActions(path, dir);
    const types = await readFile(join(dir, "agenstra-actions.d.ts"), "utf8");
    assert.match(types, /args: Record<string, never>/);
    assert.match(types, /=> Record<string, never> \| Promise<Record<string, never>>/);
    assert.doesNotMatch(types, /\{\s*\}/);
    // Pattern-constrained objects can still contain keys even when additional
    // properties are disabled; do not mistake them for the empty object.
    const profile = JSON.parse(await readFile(path, "utf8"));
    profile.actions[0].input_schema.patternProperties = { "^x": { type: "string" } };
    await writeFile(path, JSON.stringify(profile)); await exportActions(path, dir);
    assert.match(await readFile(join(dir, "agenstra-actions.d.ts"), "utf8"), /args: \{ \[key: string\]: unknown;/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
