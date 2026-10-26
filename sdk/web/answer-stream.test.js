import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const encoder = new TextEncoder();
const event = (text, phase, run_id = "run") => `event: answer\ndata: ${JSON.stringify({ run_id, text, phase })}\n\n`;
function response(text) {
  const bytes = encoder.encode(text);
  return new Response(new ReadableStream({ start(controller) {
    for (const byte of bytes) controller.enqueue(new Uint8Array([byte]));
    controller.close();
  } }), { headers: { "Content-Type": "text/event-stream" } });
}
function client(fetch, getSession = async () => "ticket") { return new AgenstraClient({ integration: "app", fetch, getSession, storage: null }); }

test("answer snapshots decode split UTF8, replace rejected drafts and finish", async () => {
  const updates = [];
  const c = client(async (_url, init) => {
    assert.equal(init.credentials, "same-origin"); assert.equal(init.method, "GET");
    assert.equal(init.headers.Authorization, "Bearer ticket");
    return response(event("中🚀", "generating") + event("must clear", "waiting") + event("new", "checking") + event("approved", "completed"));
  });
  const stop = c.watchAnswerStream("run", update => updates.push(update));
  await stop.done;
  assert.deepEqual(updates.map(u => [u.text, u.phase]), [["中🚀", "generating"], ["", "waiting"], ["new", "checking"], ["approved", "completed"]]);
  assert.equal(c.runWatchers.size, 0); assert.equal(c.aborters.size, 0);
  await c.destroy();
});

test("answer stream renews one rejected ticket and reconnects only the read", async () => {
  let sessions = 0, requests = 0;
  const updates = [];
  const c = client(async (_url, init) => {
    assert.equal(init.method, "GET");
    requests++;
    if (requests === 1) return Response.json({ code: "unauthorized" }, { status: 401 });
    assert.equal(init.headers.Authorization, "Bearer ticket-2");
    return response(requests === 2 ? event("draft", "generating") : event("saved", "completed"));
  }, async () => `ticket-${++sessions}`);
  const stop = c.watchAnswerStream("run", u => updates.push(u)); await stop.done;
  assert.equal(sessions, 2); assert.equal(requests, 3);
  assert.ok(updates.some(u => u.phase === "reconnecting" && u.text === ""));
  assert.equal(updates.at(-1).text, "saved"); await c.destroy();
});

for (const mode of ["stop", "abort", "destroy", "suspended"]) test(`answer subscription ${mode} releases its stream without cancelling the run`, async () => {
  let cancelled = 0, requests = 0, ready;
  const shown = new Promise(resolve => { ready = resolve; });
  const c = client(async (url, init) => {
    requests++;
    assert.ok(url.endsWith("/answer-stream")); assert.equal(init.method, "GET");
    return new Response(new ReadableStream({ start(controller) { controller.enqueue(encoder.encode(event("draft", "generating"))); }, cancel() { cancelled++; } }), { headers: { "content-type": "text/event-stream" } });
  });
  const controller = new AbortController();
  const stop = c.watchAnswerStream("run", ready, { signal: controller.signal }); await shown;
  if (mode === "stop") stop();
  if (mode === "abort") controller.abort();
  if (mode === "destroy") await c.destroy();
  if (mode === "suspended") { c.pagehide({ persisted: true }); await new Promise(resolve => setTimeout(resolve, 20)); assert.equal(requests, 1); stop(); }
  await stop.done;
  assert.equal(cancelled, 1); assert.equal(requests, 1);
  assert.equal(c.runWatchers.size, 0); assert.equal(c.answerReadAborters.size, 0); assert.equal(c.aborters.size, 0);
  await c.destroy();
});

test("cancelling while the host obtains a session does not keep done pending", async () => {
  const c = client(async () => { throw new Error("request must not start"); }, () => new Promise(() => {}));
  const stop = c.watchAnswerStream("run", () => {});
  await Promise.resolve(); stop(); await stop.done;
  await c.destroy();
});

for (const phase of ["failed", "cancelled"]) test(`${phase} clears the draft`, async () => {
  const updates = [];
  const c = client(async () => response(event("draft", "generating") + event("private rejected text", phase)));
  await c.watchAnswerStream("run", u => updates.push(u)).done;
  assert.equal(updates.at(-1).text, ""); assert.equal(updates.at(-1).phase, phase); await c.destroy();
});

test("wrong-run frames and revoked access clear the draft and stop", async () => {
  for (const result of [response(event("other-owner", "generating", "other")), Response.json({ code: "access_denied" }, { status: 403 })]) {
    const updates = [], errors = [];
    let requests = 0;
    const c = client(async () => { requests++; return result; }); c.on("error", e => errors.push(e));
    await c.watchAnswerStream("run", u => updates.push(u)).done;
    assert.deepEqual(updates, [{ run_id: "run", text: "", phase: "failed" }]);
    assert.equal(requests, 1); assert.equal(errors.length, 1); await c.destroy();
  }
});
