import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

test("uploads use multipart auth and send stable conversation file IDs", async t => {
  const requests = [];
  const c = new AgenstraClient({ integration: "chat", getSession: async () => "ticket", storage: null, fetch: async (url, options) => {
    requests.push({ url, options });
    return { ok: true, status: 200, json: async () => url.includes("attachments?") ? { id: "file-1", conversation_id: "chat-1" } : {} };
  } });
  t.after(() => c.destroy({ closeSession: false }));
  c.getConversation = async () => ({ id: "chat-1" });
  const file = await c.uploadAttachment(new Blob(["PDF"]), { clientId: "retry-1" });
  assert.equal(file.id, "file-1");
  assert.equal(requests[0].options.headers.Authorization, "Bearer ticket");
  assert.equal(requests[0].options.headers["Content-Type"], undefined);
  assert.ok(requests[0].options.body instanceof FormData);
  assert.equal(await requests[0].options.body.get("file").text(), "PDF");
  await c.send("Read this", { clientId: "message-1", attachments: [file.id] });
  assert.deepEqual(JSON.parse(requests[1].options.body).attachments, ["file-1"]);
  await c.readAttachment("file-1", { page: 2, count: 1, vision: true });
  assert.match(requests[2].url, /attachments\/file-1\?page=2&count=1&vision=true$/);
  await c.deleteAttachment("file-1");
  assert.equal(requests[3].options.method, "DELETE");
});

test("destroy aborts an active file upload", async () => {
  let entered;
  const started = new Promise(resolve => { entered = resolve; });
  const c = new AgenstraClient({ integration: "chat", getSession: async () => "ticket", storage: null, fetch: async (_url, options) => {
    entered();
    return new Promise((_resolve, reject) => options.signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true }));
  } });
  const uploading = c.uploadAttachment(new Blob(["file"]), { conversationId: "chat-1" });
  await started;
  await c.destroy({ closeSession: false });
  await assert.rejects(uploading, { name: "AbortError" });
});
