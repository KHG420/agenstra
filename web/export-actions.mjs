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
      if (schema.additionalProperties !== false || Object.keys(schema.patternProperties || {}).length) fields.push("[key: string]: unknown;");
      return fields.length ? `{ ${fields.join(" ")} }` : "Record<string, never>";
    }
    default: return "unknown";
  }
}

const byteLength = value => new TextEncoder().encode(value).length;
function boundedString(value, limit) { return typeof value === "string" && byteLength(value) > 0 && byteLength(value) <= limit; }
function knownFields(value, names, label) {
  if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).some(key => !names.includes(key))) throw new Error(`${label}: unexpected fields or invalid object`);
}
function checkSchema(schema, label) {
  if (!schema || schema.type !== "object") throw new Error(`${label}: object schema required`);
  const walk = value => {
    if (Array.isArray(value)) { value.forEach(walk);return; }
    if (!value || typeof value !== "object") return;
    for (const [key, child] of Object.entries(value)) {
      if (["$id", "$anchor", "$dynamicAnchor", "$dynamicRef"].includes(key)) throw new Error(`${label}: unsupported ${key}`);
      if (key === "$schema" && child !== "https://json-schema.org/draft/2020-12/schema") throw new Error(`${label}: JSON Schema 2020-12 required`);
      if (key === "$ref" && (typeof child !== "string" || !child.startsWith("#/"))) throw new Error(`${label}: local schema references required`);
      if (["properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"].includes(key)) {
        if (child && typeof child === "object") Object.values(child).forEach(walk);
      } else if (["allOf", "anyOf", "oneOf", "prefixItems", "items", "additionalItems", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "contains", "not", "if", "then", "else", "contentSchema"].includes(key)) walk(child);
    }
  };
  walk(schema);
}

// Catch profile wiring mistakes before touching generated files. The server
// remains responsible for compiling and validating complete JSON Schemas.
function checkProfile(profile) {
  knownFields(profile, ["schema", "version", "handler_version", "context_schema", "actions"], "profile");
  if (profile.schema !== "agenstra.frontend-profile.v1" || !boundedString(profile.version, 80) || !boundedString(profile.handler_version, 80) || !Array.isArray(profile.actions) || profile.actions.length > 200) throw new Error("Invalid frontend profile schema, version, handler_version or actions");
  checkSchema(profile.context_schema, "context_schema");
  const seen = new Set();
  for (const action of profile.actions) {
    knownFields(action, ["name", "description", "input_schema", "output_schema", "effect", "approval_required", "timeout_seconds"], "action");
    if (typeof action.name !== "string" || !/^ui\.[A-Za-z0-9_.-]+$/.test(action.name) || byteLength(action.name) > 128 || ["ui.get_context", "ui.command_status"].includes(action.name) || seen.has(action.name)) throw new Error("Actions must have unique, non-reserved ui.* names");
    if (!boundedString(action.description, 1000)) throw new Error(`${action.name}: description required (maximum 1000 bytes)`);
    if (action.effect !== undefined && !["", "read", "write", "destructive"].includes(action.effect)) throw new Error(`${action.name}: invalid effect`);
    if (action.approval_required !== undefined && typeof action.approval_required !== "boolean") throw new Error(`${action.name}: approval_required must be boolean`);
    if (action.timeout_seconds !== undefined && (!Number.isInteger(action.timeout_seconds) || action.timeout_seconds < 0 || action.timeout_seconds > 300)) throw new Error(`${action.name}: timeout_seconds must be 0..300 (0 uses the default)`);
    checkSchema(action.input_schema, `${action.name}.input_schema`);
    checkSchema(action.output_schema, `${action.name}.output_schema`);
    seen.add(action.name);
  }
  return seen;
}

// Regenerates derived types, but never overwrites a host's business handlers.
export async function exportActions(profilePath, destination) {
  const profile = JSON.parse(await readFile(profilePath, "utf8"));
  const seen = checkProfile(profile);
  const target = resolve(destination);
  await mkdir(target, { recursive: true });
  const types = ["// Generated from the host's frontend profile. Regenerate after contract changes.", "export interface ActionContext { commandId: string; runId: string }", "export interface ActionHandlers {", ...profile.actions.map(action => `  ${JSON.stringify(action.name)}: (args: ${schemaType(action.input_schema)}, context: ActionContext) => ${schemaType(action.output_schema)} | Promise<${schemaType(action.output_schema)}>;`), "}", ""].join("\n");
  await writeFile(resolve(target, "agenstra-actions.d.ts"), types);
  await writeFile(resolve(target, "agenstra-profile.js"), `// Generated version. Publish this profile to the server as well.\nexport const handlerVersion = ${JSON.stringify(profile.handler_version)};\n`);
  await writeFile(resolve(target, "agenstra-profile.d.ts"), `export declare const handlerVersion: ${JSON.stringify(profile.handler_version)};\n`);
  const handlers = ["// Bind these handlers to the original application's business functions.", 'import { AgenstraActionError } from "./agenstra-client.js";', '/** @type {import("./agenstra-actions.d.ts").ActionHandlers} */', "export const actions = {", ...profile.actions.map(action => `  ${JSON.stringify(action.name)}: async () => { throw new AgenstraActionError("handler_not_implemented"); },`), "};", ""].join("\n");
  let created = true;
  try { await writeFile(resolve(target, "agenstra-handlers.js"), handlers, { flag: "wx" }); }
  catch (error) { if (error.code !== "EEXIST") throw error; created = false; }
  return { destination: target, actions: [...seen], handlers_created: created };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 4) { process.stderr.write("Usage: node web/export-actions.mjs PROFILE_JSON DESTINATION\n"); process.exitCode = 2; }
  else process.stdout.write(JSON.stringify(await exportActions(process.argv[2], process.argv[3]), null, 2) + "\n");
}
