import test from "node:test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, readFile, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";

const execute = promisify(execFile);

test("the export CLI reached through a symlink exports the maintained SDK and matching hashes", async t => {
  const temporary = await mkdtemp(join(tmpdir(), "agenstra-export-cli-"));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const source = fileURLToPath(new URL(".", import.meta.url));
  const command = join(temporary, "linked-export.mjs"), destination = join(temporary, "host-vendor");
  await symlink(join(source, "export-client.mjs"), command);
  const result = await execute(process.execPath, [command, destination]);
  assert.match(result.stdout, /Exported Agenstra integration SDK/);
  const manifest = JSON.parse(await readFile(join(destination, "agenstra-sdk.json"), "utf8"));
  assert.equal(manifest.schema, "agenstra.vendored-sdk.v1");
  for (const [name, expected] of Object.entries(manifest.files)) {
    const exported = await readFile(join(destination, name));
    assert.deepEqual(exported, await readFile(join(source, name)), "CLI omitted or changed " + name);
    assert.equal(createHash("sha256").update(exported).digest("hex"), expected);
  }
  assert.match(await readFile(join(destination, "agenstra-chat.d.ts"), "utf8"), /getSources\?/);
});

test("importing the exporter from node eval accepts a non-file program argument", async t => {
  const temporary = await mkdtemp(join(tmpdir(), "agenstra-export-eval-"));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const destination = join(temporary, "not-created-yet");
  const entry = new URL("./export-client.mjs", import.meta.url).href;
  await execute(process.execPath, ["--input-type=module", "--eval", `const { exportClient } = await import(${JSON.stringify(entry)}); await exportClient(process.argv[1]);`, destination]);
  assert.equal(JSON.parse(await readFile(join(destination, "agenstra-sdk.json"), "utf8")).schema, "agenstra.vendored-sdk.v1");
});
