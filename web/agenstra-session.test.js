import test from "node:test";
import assert from "node:assert/strict";
import { createAgenstraSessionHandler } from "./agenstra-session.js";

test("ticket exchange uses verified host identity and returns only a short-lived ticket", async () => {
  const requests = [];
  const expires = Math.floor(Date.now() / 1000) + 900;
  const handler = createAgenstraSessionHandler({
    endpoint: "https://agent.example/agent",
    verifyRequest: request => request.headers.get("Origin") === "https://host.example",
    authenticateRequest: async () => "alice",
    resolveAPIKey: owner => { assert.equal(owner, "alice"); return "long-lived-private-key"; },
    fetch: async (url, options) => { requests.push({ url, options }); return Response.json({ token: "short-ticket", expires_at: expires, private: "never-forward" }); }
  });
  const response = await handler(new Request("https://host.example/api/agent-session", { method: "POST", headers: { Origin: "https://host.example" }, body: JSON.stringify({ owner: "mallory" }) }));
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  assert.deepEqual(await response.json(), { token: "short-ticket", expires_at: expires });
  assert.equal(requests[0].url, "https://agent.example/agent/web/v1/token");
  assert.equal(requests[0].options.headers.Authorization, "Bearer long-lived-private-key");
  assert.equal(requests[0].options.redirect, "error");
});

test("rejected CSRF, missing login and invalid upstream data never expose credentials", async () => {
  let calls = 0;
  const options = { endpoint: "https://agent.example", verifyRequest: () => false, authenticateRequest: () => "alice", resolveAPIKey: () => "private-key", fetch: async () => { calls++; return Response.json({ token: "ticket", expires_at: 0, key: "private-key" }); } };
  const post = () => new Request("https://host.example/session", { method: "POST" });
  let handler = createAgenstraSessionHandler(options);
  assert.equal((await handler(post())).status, 403);
  assert.equal((await handler(new Request("https://host.example/session"))).status, 405);
  assert.equal(calls, 0);
  handler = createAgenstraSessionHandler({ ...options, verifyRequest: () => true, authenticateRequest: () => null });
  assert.equal((await handler(post())).status, 401);
  assert.equal(calls, 0);
  handler = createAgenstraSessionHandler({ ...options, verifyRequest: () => true });
  const response = await handler(post());
  assert.equal(response.status, 502);
  assert.equal((await response.text()).includes("private-key"), false);
  assert.throws(() => createAgenstraSessionHandler({ ...options, endpoint: "https://user:secret@agent.example" }));
  assert.throws(() => createAgenstraSessionHandler({ ...options, verifyRequest: undefined }));
});
