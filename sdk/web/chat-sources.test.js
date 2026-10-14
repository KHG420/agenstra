import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraChat } from "./agenstra-chat.js";

function composer(send) {
  const chat = Object.create(AgenstraChat.prototype);
  chat.input = { value: "Query my usage", focus() {}, removeAttribute() {} };
  chat.choice = { hidden: true };chat.dateInput = { hidden: true };
  chat.form = { setAttribute() {}, removeAttribute() {} };
  chat.feedback = { textContent: "" };chat.text = { error: "Request failed", retry: "Retry" };
  chat.shadowRoot = { querySelector: () => ({ disabled: false, textContent: "Send" }) };
  chat._client = { id: () => "stable-client-id", send, snapshot: async () => ({ messages: [] }) };
  chat.render = () => {};chat.epoch = 1;
  return chat;
}

test("chat freezes the host's selected sources with the message identity across a lost response", async () => {
  const selected = [{ pack_id: "host-native", capabilities: ["usage.read"] }];
  const submitted = [];let resolutions = 0;
  const chat = composer(async (text, options) => {
    submitted.push({ text, clientId: options.clientId, sources: options.sources });
    if (submitted.length === 1) throw Object.assign(new Error("response lost"), { code: "connection_error" });
  });
  chat.getSources = async () => { resolutions++;return selected; };
  await chat.submit();
  assert.equal(submitted.length, 1);
  assert.deepEqual(submitted[0].sources, [{ pack_id: "host-native", capabilities: ["usage.read"] }]);
  selected[0].capabilities.push("records.write");
  await chat.submit();
  assert.equal(resolutions, 1, "retry must keep its original scope rather than resolve a new one");
  assert.deepEqual(submitted[1], submitted[0]);
  assert.deepEqual(submitted[1].sources, [{ pack_id: "host-native", capabilities: ["usage.read"] }]);
  assert.equal(chat.pendingSend, null);
});

test("a failed source resolution dispatches no message and a later attempt may resolve it", async () => {
  const submitted = [];const chat = composer(async (text, options) => { submitted.push({ text, options }); });
  let first = true;
  chat.getSources = async () => { if (first) { first = false;throw Object.assign(new Error("host not ready"), { code: "host_unavailable" }); } return []; };
  await chat.submit();
  assert.equal(submitted.length, 0);
  assert.match(chat.feedback.textContent, /host_unavailable/);
  await chat.submit();
  assert.equal(submitted.length, 1);
  assert.deepEqual(submitted[0].options.sources, []);
});

test("late source resolution cannot submit through a replaced client", async () => {
  let resolve;
  const selected = new Promise(done => { resolve = done; });
  const submitted = [];const chat = composer(async () => { submitted.push("old client"); });
  chat.getSources = () => selected;
  const sending = chat.submit();
  chat.client = { send: async () => { submitted.push("new client"); } };
  resolve([{ pack_id: "private", capabilities: ["usage.read"] }]);
  await sending;
  assert.deepEqual(submitted, []);
});

test("chat without a source selector keeps its existing send contract", async () => {
  const submitted = [];const chat = composer(async (text, options) => { submitted.push({ text, options }); });
  await chat.submit();
  assert.deepEqual(submitted, [{ text: "Query my usage", options: { text: "Query my usage", clientId: "stable-client-id" } }]);
});
