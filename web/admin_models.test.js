import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";

class Node {
  constructor() { this.children = []; this.value = ""; this.listeners = new Map(); }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  addEventListener(type, callback) { this.listeners.set(type, callback); }
  querySelectorAll() { return []; }
  checkValidity() { return true; }
  reportValidity() { return true; }
  async emit(type) { this.listeners.get(type)?.({ preventDefault() {} }); await new Promise(resolve => setImmediate(resolve)); }
}

function fixture() {
  const nodes = new Map(), requests = [];
  const get = id => { if (!nodes.has(id)) nodes.set(id, new Node()); return nodes.get(id); };
  const window = {};
  vm.runInNewContext(readFileSync(new URL("./admin_models.js", import.meta.url), "utf8"), { window, document: { getElementById: get, createElement: () => new Node() }, structuredClone });
  const config = { default_profile: "business", memory_extraction_profile: "memory", profiles: {
    business: { api_type: "deepseek_chat", model: "business-model", base_url: "https://example.com", api_key_ref: "MODEL_KEY", thinking: "enabled", reasoning_effort: "low" },
    memory: { api_type: "deepseek_chat", model: "memory-model", base_url: "https://example.com", api_key_ref: "MODEL_KEY", thinking: "disabled" },
  } };
  const editor = window.installModelEditor({ feedback() {}, revealProfile() { get("model-services-view").hidden = false; get("model-purposes-view").hidden = true; }, api: async (path, options) => {
    requests.push({ path, options: structuredClone(options) });
    if (!options) return { available: true, revision: 2, config: structuredClone(config) };
    if (options.method === "PUT") return { revision: 3, config: structuredClone(options.body.config) };
    return { passed: true, purpose: "decision", metrics: { attempts: 1, usage_available: false, input_tokens: 0, output_tokens: 0 } };
  } });
  return { editor, get, requests };
}

test("saving profile edits preserves purpose selection and expected revision", async () => {
  const { editor, get, requests } = fixture();
  await editor.refresh();
  get("model-name").value = "business-next";
  await get("model-profile-form").emit("input");
  get("model-decision").value = "business";
  await get("model-decision").emit("change");
  await get("models-save").emit("click");
  const save = requests.find(request => request.options?.method === "PUT");
  assert.equal(save.options.body.expected_revision, 2);
  assert.equal(save.options.body.config.profiles.business.model, "business-next");
  assert.equal(save.options.body.config.profiles.business.reasoning_effort, "low");
  assert.equal(save.options.body.config.memory_extraction_profile, "memory");
  assert.equal(save.options.body.config.profiles.memory.thinking, "disabled");
});

test("unknown provider usage is displayed as unknown while reported zero stays zero", () => {
  const { editor } = fixture();
  const container = new Node();
  editor.renderUsage(container, {
    decision: { requests: 1, usage_available: false, input_tokens: 0, output_tokens: 0 },
    memory_extraction: { requests: 1, reported_requests: 1, input_tokens: 10, output_tokens: 6, cached_input_tokens: 0, reasoning_output_tokens: 0 },
  });
  const rows = container.children[0].children[0].children[1].children;
  assert.equal(rows[0].children[2].textContent, "未知 / 未知");
  assert.equal(rows[0].children[3].textContent, "未知");
  assert.equal(rows[1].children[3].textContent, "0");
  assert.equal(rows[1].children[4].textContent, "0");
});

test("changing the edited profile clears an earlier validation result", async () => {
  const { editor, get } = fixture();
  await editor.refresh();
  get("model-check-result").append(new Node());
  get("model-profile-list").value = "memory";
  await get("model-profile-list").emit("change");
  assert.equal(get("model-check-result").children.length, 0);
  assert.equal(get("model-reasoning").value, "disabled");
});

test("completion review usage has its own label", () => {
  const { editor } = fixture();
  const container = new Node();
  editor.renderUsage(container, { completion_review: { requests: 1, reported_requests: 1, input_tokens: 20, output_tokens: 8 } });
  const row = container.children[0].children[0].children[1].children[0];
  assert.equal(row.children[0].textContent, "回答复核");
  assert.equal(row.children[1].textContent, "1");
});

test("saving from purpose selection reveals invalid profile fields without submitting", async () => {
  const { editor, get, requests } = fixture();
  await editor.refresh();
  get("model-name").value = "";
  await get("model-profile-form").emit("input");
  get("model-profile-form").checkValidity = () => false;
  get("model-profile-form").reportValidity = () => false;
  get("model-services-view").hidden = true;
  get("model-purposes-view").hidden = false;
  await get("models-save").emit("click");
  assert.equal(get("model-services-view").hidden, false);
  assert.equal(get("model-purposes-view").hidden, true);
  assert.equal(requests.some(request => request.options?.method === "PUT"), false);
});
