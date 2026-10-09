import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

test("chat and browser tasks send an explicit source scope without account claims", async t => {
  const requests = [];
  const client = new AgenstraClient({ integration: "home", storage: null, getSession: async () => "ticket",
    fetch: async (path, options) => {
      const body = JSON.parse(options.body);
      requests.push({ path, body });
      assert.equal(options.headers.Authorization, "Bearer ticket");
      assert.equal(body.owner_id, undefined);
      assert.equal(body.target_subject, undefined);
      return { ok: true, status: 200, json: async () => ({ id: "message", run_id: "run" }) };
    }
  });
  t.after(() => client.destroy({ closeSession: false }));
  client.getConversation = async () => ({ id: "conversation" });
  client.connectBrowser = async () => {};
  client.browser = { id: "tab", key: "tab-key" };
  const sources = [{ pack_id: "location", capabilities: ["query"] }, { pack_id: "weather", capabilities: ["query"] }];
  await client.send("Weather", { clientId: "message-one", sources });
  await client.run("Weather", { requestId: "run-one", sources });
  assert.deepEqual(requests[0], { path: "/chat/v1/conversations/conversation/messages", body: { client_id: "message-one", text: "Weather", sources, session_id: "tab" } });
  assert.deepEqual(requests[1], { path: "/browser/v1/runs", body: { integration_id: "home", session_id: "tab", instruction: "Weather", sources, request_id: "run-one" } });
});
