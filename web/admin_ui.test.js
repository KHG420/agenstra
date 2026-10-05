import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";

class Node {
  constructor(id = "") { this.id = id; this.value = ""; this.hidden = false; this.dataset = {}; this.attributes = new Map(); this.listeners = new Map(); }
  setAttribute(name, value) { this.attributes.set(name, value); }
  getAttribute(name) { return this.attributes.get(name); }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, callback) { this.listeners.set(type, callback); }
  querySelector() { return this.heading; }
  focus() { this.focused = true; }
  emit(type, event = {}) { this.listeners.get(type)?.(event); }
}

function fixture(hash = "") {
  const nodes = new Map(), events = new Map();
  const get = id => { if (!nodes.has(id)) nodes.set(id, new Node(id)); return nodes.get(id); };
  const pages = ["releases", "models", "publish", "bindings", "diagnostics", "audit"].map(id => {
    const page = get(id); page.heading = new Node(); page.heading.textContent = id; return page;
  });
  const links = pages.map(page => { const link = new Node(); link.dataset.adminPage = page.id; link.setAttribute("href", "#" + page.id); return link; });
  const tabs = ["services", "purposes"].map(view => {
    const tab = new Node(); tab.dataset.modelView = view; tab.setAttribute("aria-controls", `model-${view}-view`); return tab;
  });
  get("model-purposes-view").hidden = true;
  const location = { hash };
  const document = { getElementById: get, querySelectorAll: selector => ({ "[data-admin-panel]": pages, "[data-admin-page]": links, "[data-model-view]": tabs })[selector] || [] };
  const window = { addEventListener: (type, callback) => events.set(type, callback), scrollTo() {}, installDraftEditor: () => ({}), installModelEditor: () => ({}) };
  vm.runInNewContext(readFileSync(new URL("./admin_ui.js", import.meta.url), "utf8"), { document, window, location });
  return { get, pages, links, tabs, location, navigate: hash => { location.hash = hash; events.get("hashchange")(); } };
}

test("module navigation honors deep links and preserves unsaved editor fields", () => {
  const { get, pages, links, navigate } = fixture("#audit");
  assert.deepEqual(pages.filter(page => !page.hidden).map(page => page.id), ["audit"]);
  get("model-name").value = "unsaved-model";
  get("manifest-editor").value = "unsaved-contract";
  for (const page of pages) {
    navigate("#" + page.id);
    assert.deepEqual(pages.filter(item => !item.hidden).map(item => item.id), [page.id]);
    assert.equal(links.find(link => link.getAttribute("aria-current") === "page").dataset.adminPage, page.id);
  }
  assert.equal(get("model-name").value, "unsaved-model");
  assert.equal(get("manifest-editor").value, "unsaved-contract");
});

test("empty or unknown module routes open model configuration", () => {
  for (const hash of ["", "#missing"]) {
    const { pages } = fixture(hash);
    assert.deepEqual(pages.filter(page => !page.hidden).map(page => page.id), ["models"]);
  }
});

test("model tabs support keyboard selection without replacing the draft", () => {
  const { get, tabs } = fixture();
  get("model-name").value = "unsaved-model";
  let prevented = false;
  tabs[0].emit("keydown", { key: "ArrowRight", preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(get("model-services-view").hidden, true);
  assert.equal(get("model-purposes-view").hidden, false);
  assert.equal(tabs[1].getAttribute("aria-selected"), "true");
  assert.equal(tabs[1].focused, true);
  tabs[1].emit("keydown", { key: "Home", preventDefault() {} });
  assert.equal(get("model-services-view").hidden, false);
  assert.equal(get("model-purposes-view").hidden, true);
  assert.equal(get("model-name").value, "unsaved-model");
});

test("management key disclosure reflects visibility and focuses its field", () => {
  const { get } = fixture();
  get("connection-toggle").emit("click");
  assert.equal(get("connection-panel").hidden, true);
  assert.equal(get("connection-toggle").getAttribute("aria-expanded"), "false");
  get("connection-toggle").emit("click");
  assert.equal(get("connection-panel").hidden, false);
  assert.equal(get("connection-toggle").getAttribute("aria-expanded"), "true");
  assert.equal(get("admin-key").focused, true);
});
