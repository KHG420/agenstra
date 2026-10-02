/** Host-side ticket exchange using the application's verified login session. */
export function createAgenstraSessionHandler({ endpoint, authenticateRequest, verifyRequest, resolveAPIKey, fetch: request = globalThis.fetch }) {
  const url = new URL(endpoint);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new TypeError("Agenstra endpoint must be an HTTP service URL");
  if ([authenticateRequest, verifyRequest, resolveAPIKey, request].some(fn => typeof fn !== "function")) throw new TypeError("authenticateRequest, verifyRequest and resolveAPIKey are required");
  const target = endpoint.replace(/\/$/, "") + "/web/v1/token";
  const reply = (code, status) => Response.json({ code }, { status, headers: { "Cache-Control": "no-store" } });
  return async function agentSession(incoming) {
    if (incoming.method !== "POST") return new Response(null, { status: 405, headers: { Allow: "POST", "Cache-Control": "no-store" } });
    try {
      if (!await verifyRequest(incoming)) return reply("agent_session_request_rejected", 403);
      const owner = await authenticateRequest(incoming);
      if (typeof owner !== "string" || !owner) return reply("unauthorized", 401);
      const key = await resolveAPIKey(owner);
      if (typeof key !== "string" || !key) return reply("agent_session_unavailable", 503);
      // Workers implement manual/follow only. Manual also lets us reject a
      // redirect without sending the credential to its destination.
      const response = await request(target, { method: "POST", headers: { Authorization: "Bearer " + key }, redirect: "manual", signal: AbortSignal.timeout(10000) });
      if (!response.ok) return reply("agent_session_unavailable", 503);
      const data = await response.json();
      if (typeof data.token !== "string" || !data.token || !Number.isFinite(data.expires_at) || data.expires_at <= Date.now() / 1000) return reply("agent_session_response_invalid", 502);
      // No long-lived key or additional upstream fields reach the browser.
      return Response.json({ token: data.token, expires_at: data.expires_at }, { headers: { "Cache-Control": "no-store" } });
    } catch { return reply("agent_session_unavailable", 503); }
  };
}
