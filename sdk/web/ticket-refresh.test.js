import test from "node:test";
import assert from "node:assert/strict";
import { AgenstraClient } from "./agenstra-client.js";

const response = (data, status = 200) => ({ status, ok: status >= 200 && status < 300, json: async () => data });
const tick = () => new Promise(resolve => setImmediate(resolve));

test("parallel initial SDK requests share one host ticket exchange", async t => {
  let release, tickets = 0, requests = 0;
  const ticket = new Promise(resolve => { release = resolve; });
  const c = new AgenstraClient({ integration: "app", storage: null,
    getSession: () => { tickets++;return ticket; },
    fetch: async (_path, options) => { requests++;assert.equal(options.headers.Authorization, "Bearer shared");return response({}); }
  });
  t.after(() => c.destroy({ closeSession: false }));
  const first = c.getRun("one"), second = c.getRun("two");
  release({ token: "shared" });await Promise.all([first, second]);
  assert.equal(tickets, 1);assert.equal(requests, 2);
});

test("simultaneous expired requests share one replacement ticket", async t => {
  let release, tickets = 0, requests = 0;
  const replacement = new Promise(resolve => { release = resolve; });
  const c = new AgenstraClient({ integration: "app", storage: null,
    getSession: () => { tickets++;return replacement; },
    fetch: async (_path, options) => {
      requests++;
      return options.headers.Authorization === "Bearer expired" ? response({ code: "unauthorized" }, 401) : response({});
    }
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.token = "expired";
  const first = c.getRun("one"), second = c.getRun("two");
  await tick();release("replacement");await Promise.all([first, second]);
  assert.equal(tickets, 1);assert.equal(requests, 4);
});

test("a late 401 from an old ticket cannot discard its valid replacement", async t => {
  let release, entered, tickets = 0;
  const delayed = new Promise(resolve => { release = resolve; });
  const pending = new Promise(resolve => { entered = resolve; });
  const c = new AgenstraClient({ integration: "app", storage: null,
    getSession: async () => "replacement-" + (++tickets),
    fetch: async (path, options) => {
      if (options.headers.Authorization === "Bearer expired") {
        if (path.endsWith("two")) { entered();await delayed; }
        return response({ code: "unauthorized" }, 401);
      }
      return response({});
    }
  });
  t.after(() => c.destroy({ closeSession: false }));
  c.token = "expired";
  const first = c.getRun("one"), second = c.getRun("two");
  await pending;await first;release();await second;
  assert.equal(tickets, 1);assert.equal(c.token, "replacement-1");
});

test("a failed shared ticket exchange releases all waiters and permits a later attempt", async t => {
  let reject, tickets = 0, requests = 0;
  const ticket = new Promise((_resolve, fail) => { reject = fail; });
  const failure = new Error("host login service unavailable");
  const c = new AgenstraClient({ integration: "app", storage: null,
    getSession: () => ++tickets === 1 ? ticket : Promise.resolve("replacement"),
    fetch: async () => { requests++;return response({}); }
  });
  t.after(() => c.destroy({ closeSession: false }));
  const outcomes = Promise.allSettled([c.getRun("one"), c.getRun("two")]);
  await tick();reject(failure);
  assert.deepEqual(await outcomes, [{ status: "rejected", reason: failure }, { status: "rejected", reason: failure }]);
  assert.equal(requests, 0);assert.equal(tickets, 1);
  await c.getRun("three");assert.equal(tickets, 2);assert.equal(requests, 1);
});
