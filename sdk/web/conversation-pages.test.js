import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

test("original conversation pages retain the selected conversation and request cursor", async t => {
  const requests = [];
  const client = new AgenstraClient({ integration: "records", storage: null,
    getSession: async () => "ticket", fetch: async (path, options) => {
      requests.push({ path, options });
      return Response.json({ conversation: { id: "original", integration_id: "records" }, messages: [{ id: "earlier", sequence: 42 }], next_before_sequence: 42 });
    } });
  t.after(() => client.destroy({ closeSession: false }));
  client.rememberConversation({ id: "current", integration_id: "records" });
  const page = await client.getConversationMessages({ conversationId: "original", beforeSequence: 92, limit: 50 });
  assert.equal(page.next_before_sequence, 42);
  assert.equal(requests[0].path, "/chat/v1/conversations/original?before_sequence=92&limit=50");
  assert.equal(requests[0].options.headers.Authorization, "Bearer ticket");
  assert.equal(client.conversation.id, "current");
});
