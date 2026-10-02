import { readFile, mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

function schemaType(schema, depth = 0) {
  if (!schema || typeof schema !== "object" || depth > 16 || schema.$ref) return "unknown";
  if (Object.hasOwn(schema, "const")) return JSON.stringify(schema.const);
  if (Array.isArray(schema.enum) && schema.enum.length) return schema.enum.map(value => JSON.stringify(value)).join(" | ");
  if (Array.isArray(schema.anyOf)) return schema.anyOf.map(item => schemaType(item, depth + 1)).join(" | ") || "unknown";
  if (Array.isArray(schema.type)) return schema.type.map(type => schemaType({ ...schema, type }, depth + 1)).join(" | ");
  switch (schema.type) {
    case "string": return "string";
    case "integer": case "number": return "number";
    case "boolean": return "boolean";
    case "null": return "null";
    case "array": return `Array<${schemaType(schema.items, depth + 1)}>`;
    case "object": {
      const fields = Object.entries(schema.properties || {}).map(([name, value]) => `${JSON.stringify(name)}${schema.required?.includes(name) ? "" : "?"}: ${schemaType(value, depth + 1)};`);
      if (schema.additionalProperties !== false) fields.push("[key: string]: unknown;");
      return `{ ${fields.join(" ")} }`;
    }
    default: return "unknown";
  }
}

// Regenerates derived types, but never overwrites a host's business handlers.
export async function exportActions(profilePath, destination) {
  const profile = JSON.parse(await readFile(profilePath, "utf8"));
  if (profile.schema !== "agenstra.frontend-profile.v1" || !profile.handler_version || !Array.isArray(profile.actions)) throw new Error("A frontend profile with handler_version and actions is required");
  const seen = new Set();
  for (const action of profile.actions) {
    if (typeof action.name !== "string" || !action.name.startsWith("ui.") || seen.has(action.name) || action.input_schema?.type !== "object" || action.output_schema?.type !== "object") throw new Error("Actions must have unique ui.* names and object input/output schemas");
    seen.add(action.name);
  }
  const target = resolve(destination);
  await mkdir(target, { recursive: true });
  const types = ["// Generated from the host's frontend profile. Regenerate after contract changes.", "export interface ActionContext { commandId: string; runId: string }", "export interface ActionHandlers {", ...profile.actions.map(action => `  ${JSON.stringify(action.name)}: (args: ${schemaType(action.input_schema)}, context: ActionContext) => ${schemaType(action.output_schema)} | Promise<${schemaType(action.output_schema)}>;`), "}", ""].join("\n");
  await writeFile(resolve(target, "agenstra-actions.d.ts"), types);
  await writeFile(resolve(target, "agenstra-profile.js"), `// Generated version. Publish this profile to the server as well.\nexport const handlerVersion = ${JSON.stringify(profile.handler_version)};\n`);
  const handlers = ["// Bind these handlers to the original application's business functions.", 'import { AgenstraActionError } from "./agenstra-client.js";', '/** @type {import("./agenstra-actions.d.ts").ActionHandlers} */', "export const actions = {", ...profile.actions.map(action => `  ${JSON.stringify(action.name)}: async (_args, _context) => { throw new AgenstraActionError("handler_not_implemented"); },`), "};", ""].join("\n");
  let created = true;
  try { await writeFile(resolve(target, "agenstra-handlers.js"), handlers, { flag: "wx" }); }
  catch (error) { if (error.code !== "EEXIST") throw error; created = false; }
  return { destination: target, actions: [...seen], handlers_created: created };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 4) { process.stderr.write("Usage: node web/export-actions.mjs PROFILE_JSON DESTINATION\n"); process.exitCode = 2; }
  else process.stdout.write(JSON.stringify(await exportActions(process.argv[2], process.argv[3]), null, 2) + "\n");
}
