import test from "node:test";
import assert from "node:assert/strict";
import { appendAnswer, AgenstraChat } from "./agenstra-chat.js";
import { AgenstraClient } from "./agenstra-client.js";

test("model HTML and code fence content remain text", () => {
  const previous = globalThis.document;
  class Element {
    constructor(tag) { this.tag = tag; this.children = []; this.textContent = ""; }
    append(...children) { this.children.push(...children); }
    set innerHTML(value) { assert.fail("untrusted HTML reached an HTML parser: " + value); }
  }
  globalThis.document = { createElement: tag => new Element(tag), createTextNode: text => Object.assign(new Element("text"), { textContent: text }) };
  try {
    const raw = new Element("answer"); appendAnswer(raw, '<img src=x onerror="alert(1)">');
    assert.equal(raw.textContent, '<img src=x onerror="alert(1)">');
    const fenced = new Element("answer"); appendAnswer(fenced, "Before\n```html\n<script>unsafe()</script>\n```\nAfter");
    assert.equal(fenced.children[1].tag, "pre");
    assert.equal(fenced.children[1].children[0].textContent, "<script>unsafe()</script>\n");
  } finally { globalThis.document = previous; }
});

test("diagnostics use owner-bound web routes and encode the run identifier", async () => {
  const client = new AgenstraClient({ integration: "records", getSession: async () => "ticket", storage: null });
  client.request = async path => path;
  assert.equal(await client.getRunDiagnostics("run/1"), "/web/v1/runs/run%2F1/diagnostics");
  await client.destroy({ closeSession: false });
});

test("completed messages keep diagnostics and late reports cannot cross clients", async () => {
  const previous = globalThis.document;
  class Element {
    constructor() { this.children = []; this.dataset = {}; this.textContent = ""; }
    append(...children) { this.children.push(...children); }
    replaceChildren(...children) { this.children = children; }
    querySelectorAll() { return []; }
  }
  globalThis.document = { createElement: () => new Element() };
  try {
    const chat = Object.create(AgenstraChat.prototype);
    chat.log = Object.assign(new Element(), { scrollHeight: 0, scrollTop: 0, clientHeight: 100 });
    chat.input = { value: "" };
    const labels = new Map();
    chat.shadowRoot = { querySelector: name => { if (!labels.has(name)) labels.set(name, new Element()); return labels.get(name); } };
    chat.getAttribute = () => "en";
    chat.text = { statuses: { completed: "Completed" }, placeholder: "Message", send: "Send" };
    let resolveReport;
    const requested = [];
    chat._client = { getRunDiagnostics: id => { requested.push(id); return new Promise(resolve => { resolveReport = resolve; }); } };
    chat.epoch = 1;
    let inspect;
    chat.button = (label, key, action) => { inspect = action; return Object.assign(new Element(), { textContent: label }); };
    const snapshot = { conversation: { id: "conversation" }, messages: [{ id: "message", run_id: "finished-run", status: "completed", text: "Query" }] };
    chat.render(snapshot);
    assert.equal(typeof inspect, "function");
    const report = inspect();
    assert.deepEqual(requested, ["finished-run"]);
    chat.client = { getRunDiagnostics: async () => ({}) };
    resolveReport({ status: "completed", findings: [{ message: "Previous user's result" }] });
    await report;
    assert.equal(chat.diagnostics.size, 0);
  } finally { globalThis.document = previous; }
});
