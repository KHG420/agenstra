import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const conversation = { id: "history", integration_id: "records", created_at: 1 };
const snapshot = { conversation, messages: [{ id: "first", text: "Saved history", status: "completed" }] };

test("unchanged conversation polls preserve the displayed history and renew their ticket", async t => {
  const requests = [], events = [], errors = [], connections = [];
  let reads = 0, sessions = 0;
  const client = new AgenstraClient({ integration: "records", storage: null, pollInterval: 60000,
    getSession: async () => "ticket-" + ++sessions,
    fetch: async (_path, options) => {
      requests.push(options.headers);
      reads++;
      if (reads === 1) return Response.json(snapshot, { headers: { ETag: 'W/"history-1"' } });
      if (reads === 2) return Response.json({ code: "unauthorized" }, { status: 401 });
      if (reads === 3) return new Response(null, { status: 304 });
      return Response.json({ conversation, messages: [...snapshot.messages, { id: "second", status: "active" }] });
    } });
  t.after(() => client.destroy({ closeSession: false }));
  client.rememberConversation(conversation);client.chatWatchers = 1;
  client.on("conversation", value => events.push(value));
  client.on("error", error => errors.push(error));
  client.on("connection", event => connections.push(event.status));
  await client.pollChat();clearTimeout(client.chatTimer);
  await client.pollChat();clearTimeout(client.chatTimer);
  assert.equal(requests[1]["If-None-Match"], 'W/"history-1"');
  assert.equal(requests[2]["If-None-Match"], 'W/"history-1"');
  assert.equal(requests[2].Authorization, "Bearer ticket-2");
  assert.deepEqual(errors, []);assert.deepEqual(events, [snapshot]);
  assert.deepEqual(connections, ["connected", "connected"]);
  await client.pollChat();clearTimeout(client.chatTimer);
  assert.equal(events.length, 2);assert.equal(events[1].messages.at(-1).status, "active");
  await client.pollChat();clearTimeout(client.chatTimer);
  assert.equal(requests.at(-1)["If-None-Match"], undefined, "an active or older server without ETag must still return fresh data");
});

test("a new subscriber receives history and validators never cross a conversation selection", async t => {
  const requests = [];
  const client = new AgenstraClient({ integration: "records", storage: null, pollInterval: 60000,
    getSession: async () => "ticket", fetch: async (path, options) => {
      const id = path.split("/").at(-1);requests.push({ id, tag: options.headers["If-None-Match"] });
      return Response.json({ conversation: { ...conversation, id }, messages: snapshot.messages }, { headers: { ETag: 'W/"' + id + '"' } });
    } });
  t.after(() => client.destroy({ closeSession: false }));
  client.rememberConversation(conversation);client.chatWatchers = 1;
  await client.pollChat();clearTimeout(client.chatTimer);
  const received = Promise.withResolvers();
  const stop = client.watchConversation(value => received.resolve(value));t.after(stop);
  await client.pollChat();clearTimeout(client.chatTimer);
  assert.equal((await received.promise).conversation.id, conversation.id);
  assert.equal(requests[1].tag, undefined);
  await client.selectConversation("other");
  await client.pollChat();clearTimeout(client.chatTimer);
  assert.equal(requests.at(-1).id, "other");assert.equal(requests.at(-1).tag, undefined);
});
