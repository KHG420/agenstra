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

test("cancelled runs expose verification only for unknown non-browser calls", async () => {
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
    chat.text = { statuses: { cancelled: "Stopped" }, placeholder: "Message", send: "Send", unknown: "Verify outcome", unknownStopped: "Verification only updates the result; this task stays stopped.", reconcile: "Verify" };
    const calls = [];
    chat._client = { reconcileInvocation: (...args) => { calls.push(args); } };
    const actions = new Map();
    chat.button = (label, key, action) => { actions.set(key, action); return new Element(); };
    chat.render({ conversation: { id: "conversation" }, messages: [{ id: "message", run_id: "run", status: "cancelled", text: "Submit", run: { run_id: "run", status: "cancelled", revision: 8, state: { runtime: { pending: [{ invocation_id: "write", status: "unknown" }, { invocation_id: "polling", status: "waiting", operation: { binding: { poll_capability: "orders.status" } } }, { invocation_id: "poll-started", status: "prepared", poll_in_flight: true }, { invocation_id: "browser", status: "waiting", operation: { binding: { poll_capability: "ui.command_status" } } }] } } } }] });
    assert.equal(actions.has("write:reconcile"), true);
    assert.equal(actions.has("polling:reconcile"), true);
    assert.equal(actions.has("poll-started:reconcile"), true);
    assert.equal(actions.has("browser:reconcile"), false);
    assert.equal(chat.log.children[0].children.some(child => child.textContent === chat.text.unknownStopped), true);
    actions.get("write:reconcile")();
    assert.deepEqual(calls[0].slice(0, 2), ["run", { invocation_id: "write", status: "unknown" }]);
    assert.equal(calls[0][2], 8);
  } finally { globalThis.document = previous; }
});

test("structured controls keep the right draft, label and keyboard submission", async () => {
  const previousDocument = globalThis.document;
  const previousShadow = AgenstraChat.prototype.attachShadow;
  class Element {
    constructor(tag = "div") { this.tag = tag; this.children = []; this.dataset = {}; this.attrs = {}; this.listeners = {}; this.value = ""; this.textContent = ""; this.hidden = false; this.scrollHeight = 0; this.scrollTop = 0; this.clientHeight = 100; }
    append(...children) { this.children.push(...children); }
    after(...children) { this.afterNodes = children; }
    replaceChildren(...children) { this.children = children; }
    setAttribute(name, value) { this.attrs[name] = String(value); }
    removeAttribute(name) { delete this.attrs[name]; }
    addEventListener(name, callback) { this.listeners[name] = callback; }
    querySelectorAll() { return []; }
    focus() { this.focused = true; }
  }
  const elements = new Map();
  const root = { querySelector: selector => { if (!elements.has(selector)) elements.set(selector, new Element(selector)); return elements.get(selector); } };
  globalThis.document = { createElement: tag => new Element(tag) };
  AgenstraChat.prototype.attachShadow = function () { this.shadowRoot = root; return root; };
  const inputSnapshot = (conversation, revision, field, schema) => ({ conversation: { id: conversation }, messages: [{ id: "message", text: "Task", status: "active", run_id: "run", run: { run_id: "run", revision, status: "needs_input", state: { runtime: { input_field: field, input_prompt: "Choose destination", input_schema: schema, pending: [] } } } }] });
  try {
    const chat = new AgenstraChat();
    assert.equal(chat.choice.disabled, true, "hidden required choice must not block form submission");
    assert.equal(chat.dateInput.disabled, true, "hidden required date must not block form submission");
    chat.getAttribute = () => "en";
    chat.text = { statuses: { needs_input: "Waiting" }, placeholder: "Message", input: "More information", choose: "Choose…", send: "Send", queue: "Queue", stop: "Stop", invalidInput: "Enter a valid value.", invalidChoice: "Choose an option from the list.", invalidDate: "Enter a valid date.", error: "Error", retry: "Retry" };
    const calls = [];
    let failure = false;
    let current = inputSnapshot("conversation-a", 2, "destination", { type: "enum", enum: ["A", "B"] });
    chat._client = { supplyInput: async (...args) => { calls.push(args); if (failure) throw { status: 422, code: "input_invalid" }; }, snapshot: async () => current };
    chat.epoch = 1;
    chat.render(current);
    assert.equal(chat.choice.hidden, false);
    assert.equal(chat.choice.disabled, false);
    assert.equal(chat.dateInput.disabled, true);
    assert.equal(chat.input.disabled, true);
    assert.equal(chat.choice.required, true);
    assert.equal(chat.choice.value, "");
    assert.equal(chat.choice.children[0].value, "");
    assert.equal(chat.choice.children[0].disabled, true);
    assert.equal(root.querySelector("label").attrs.for, chat.choice.id);
    assert.match(chat.choice.attrs["aria-describedby"], /input-prompt-message/);
    assert.equal(root.querySelector("label").textContent, "More information");
    await chat.submit();
    assert.equal(calls.length, 0, "empty enum should not submit");
    chat.choice.value = "B";
    current = { ...current, messages: [{ ...current.messages[0], update: 1 }] };
    chat.render(current);
    assert.equal(chat.choice.value, "B", "polling should keep the same request draft");
    failure = true;
    await chat.submit();
    assert.equal(calls.length, 1);
    assert.equal(chat.choice.value, "B", "422 must keep the draft");
    assert.equal(chat.choice.attrs["aria-invalid"], "true");
    assert.match(root.querySelector(".feedback").textContent, /Choose destination/);
    failure = false;
    const date = inputSnapshot("conversation-a", 3, "date", { type: "date" });
    chat.render(date);
    assert.equal(chat.choice.value, "", "new request clears enum draft");
    assert.equal(chat.choice.attrs["aria-invalid"], undefined, "new request clears the old validation state");
    assert.equal(root.querySelector(".feedback").textContent, "", "new request clears the old error");
    assert.equal(chat.dateInput.hidden, false);
    assert.equal(chat.choice.disabled, true);
    assert.equal(chat.dateInput.disabled, false);
    assert.equal(chat.input.disabled, true);
    assert.equal(root.querySelector("label").attrs.for, chat.dateInput.id);
    chat.dateInput.value = "2026-10-02";
    let prevented = false;
    chat.dateInput.listeners.keydown({ key: "Enter", isComposing: false, preventDefault: () => { prevented = true; } });
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(prevented, true);
    assert.deepEqual(calls[1], ["run", "date", "2026-10-02", 3]);
    chat.dateInput.value = "2026-11-03";
    chat.render(inputSnapshot("conversation-b", 1, "date", { type: "date" }));
    assert.equal(chat.dateInput.value, "", "conversation switch must clear the previous date");
    chat.render({ conversation: { id: "conversation-b" }, messages: [] });
    assert.equal(chat.input.disabled, false);
    assert.equal(chat.choice.disabled, true);
    assert.equal(chat.dateInput.disabled, true);
  } finally {
    globalThis.document = previousDocument;
    if (previousShadow) AgenstraChat.prototype.attachShadow = previousShadow;
    else delete AgenstraChat.prototype.attachShadow;
  }
});
