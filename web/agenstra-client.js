/** Framework-independent browser client. Credentials are provided by the host. */
export class AgenstraError extends Error {
  constructor(code, status = 0) { super(code); this.name = "AgenstraError"; this.code = code; this.status = status; }
}
/** Throw only when the host has evidence the action did not commit a side effect. */
export class AgenstraActionError extends Error {
  constructor(code, message = code) {
    if (!/^[a-z][a-z0-9_]{0,95}$/.test(code)) throw new TypeError("Invalid action error code");
    super(message); this.name = "AgenstraActionError"; this.code = code;
  }
}
export class AgenstraClient {
  constructor(options) {
    if (!options?.integration || typeof options.getSession !== "function") throw new TypeError("integration and getSession are required");
    this.options = options;
    this.endpoint = (options.endpoint || "").replace(/\/$/, "");
    this.fetch = options.fetch || globalThis.fetch.bind(globalThis);
    this.actions = new Map();
    this.listeners = new Map();
    this.storage = options.storage;
    if (this.storage === undefined) { try { this.storage = globalThis.sessionStorage; } catch { this.storage = null; } }
    this.prefix = "agenstra:v1:" + this.endpoint + ":" + options.integration;
    this.token = null;
    this.closed = false;
    this.pageObservation = {};
    this.observationChain = Promise.resolve();
    this.selectionChain = Promise.resolve();
    this.selectionRevision = 0;
    this.receipts = this.load("receipts") || {};
    this.browserRecovery = this.load("browser_recovery");
    this.chatWatchers = 0;
  this.runWatchers = new Set();
    this.aborters = new Set();
    this.browserEpoch = 0;
    this.browserConnected = false;
    this.pagehide = () => { this.destroy({ closeSession: false }); };
    globalThis.addEventListener?.("pagehide", this.pagehide);
  }
  on(name, callback) {
    if (!this.listeners.has(name)) this.listeners.set(name, new Set());
    this.listeners.get(name).add(callback);
    return () => this.listeners.get(name)?.delete(callback);
  }
  emit(name, value) { for (const fn of this.listeners.get(name) || []) { try { fn(value); } catch (error) { this.options.onListenerError?.(error); } } }
  load(key) { try { return JSON.parse(this.storage?.getItem(this.prefix + ":" + key) || "null"); } catch { return null; } }
  save(key, value) { try { this.storage?.setItem(this.prefix + ":" + key, JSON.stringify(value)); } catch { /* Server claims still prevent execution on replay. */ } }
  remove(key) { try { this.storage?.removeItem(this.prefix + ":" + key); } catch { /* Storage may be disabled by the host. */ } }
  id() {
    if (typeof globalThis.crypto.randomUUID === "function") return globalThis.crypto.randomUUID();
    const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
    const hex = Array.from(bytes, n => n.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  async request(path, { method = "GET", body, browserKey, retry = true } = {}) {
    if (this.closed) throw new AgenstraError("client_closed");
    if (!this.token) {
      if (!this.sessionPromise) {
        this.sessionPromise = Promise.resolve().then(() => this.options.getSession()).then(session => {
          this.token = typeof session === "string" ? session : session.token;
        }).finally(() => { this.sessionPromise = null; });
      }
      await this.sessionPromise;
    }
    if (this.closed) throw new AgenstraError("client_closed");
    const token = this.token;
    const aborter = new AbortController();
    this.aborters.add(aborter);
    try {
      const headers = { Authorization: "Bearer " + token };
      if (body !== undefined) headers["Content-Type"] = "application/json";
      if (browserKey) headers["X-Agenstra-Browser-Key"] = browserKey;
      const response = await this.fetch(this.endpoint + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: aborter.signal, credentials: "same-origin" });
      const data = await response.json();
      const code = data.code || data.detail?.code;
      if (response.status === 401 && retry && code === "unauthorized") {
        // A late rejection only invalidates the credential used by this request.
        if (this.token === token) this.token = null;
        return await this.request(path, { method, body, browserKey, retry: false });
      }
      if (!response.ok) throw new AgenstraError(data.code || data.detail?.code || "request_failed", response.status);
      return data;
    } finally { this.aborters.delete(aborter); }
  }
  registerActions(actions) {
    if (this.browserPromise) throw new AgenstraError("register_actions_before_connect");
    for (const [name, handler] of Object.entries(actions)) {
      if (!name.startsWith("ui.") || typeof handler !== "function" || this.actions.has(name)) throw new TypeError("invalid or duplicate action: " + name);
      this.actions.set(name, handler);
    }
    return this;
  }
  async connectBrowser() {
    if (!this.options.browser) return null;
    if (this.recoveryPromise) return this.recoveryPromise;
    if (this.browserPromise) return this.browserPromise;
    this.browserPromise = this.connectBrowserOnce().catch(error => { this.browserPromise = null; this.browserConnected = false; this.emit("connection", { status: "disconnected" }); throw error; });
    return this.browserPromise;
  }
  async connectBrowserOnce() {
    const saved = this.browser || this.load("browser");
    if (saved) {
      this.browser = saved;
      try {
        if (this.browserRecovery || saved.handler_version !== this.options.handlerVersion) {
          if (saved.handler_version !== this.options.handlerVersion) this.emit("connection", { status: "profile_changed" });
          await this.replaceBrowser(false);
        }
        else {
          try {
            const data = await this.request("/browser/v1/sessions/" + saved.id + "/resume", { method: "POST", browserKey: saved.key, body: { generation: saved.generation } });
            this.browser = { ...data.session, key: saved.key };
          } catch (error) {
            if (error.code !== "browser_profile_changed") throw error;
            this.emit("connection", { status: "profile_changed" });
            await this.replaceBrowser(false);
          }
        }
      } catch (error) {
        // A different signed-in owner cannot resume the previous user's session.
        if (!["not_found", "browser_session_invalid", "browser_generation_changed"].includes(error.code)) throw error;
        this.browser = null; this.browserRecovery = null; this.remove("browser"); this.remove("browser_recovery"); this.receipts = {}; this.save("receipts", {});
      }
    }
    if (!this.browser) {
      const data = await this.request("/browser/v1/sessions", { method: "POST", body: { integration_id: this.options.integration, handler_version: this.options.handlerVersion, handlers: [...this.actions.keys()] } });
      this.browser = { ...data.session, key: data.key };
    }
    this.save("browser", this.browser);
    await this.flushReceipts();
    await this.publishPageObservation();
    if (!this.closed) this.browserTimer = setTimeout(() => this.pollBrowser(), 0);
    this.browserConnected = true;
    this.emit("connection", { status: "connected" });
    return this.browser;
  }
  async replaceBrowser(acknowledgeUnknown) {
    const browser = this.browser || this.load("browser");
    if (!browser) throw new AgenstraError("browser_session_unavailable");
    let recovery = this.browserRecovery;
    if (!recovery || recovery.session_id !== browser.id) {
      recovery = { session_id: browser.id, generation: browser.generation, handler_version: this.options.handlerVersion, handlers: [...this.actions.keys()], request_id: this.id(), acknowledge_unknown: acknowledgeUnknown };
      this.browserRecovery = recovery;
      this.save("browser_recovery", recovery);
    }
    let data;
    try {
      data = await this.request("/browser/v1/sessions/" + browser.id + "/recover", { method: "POST", browserKey: browser.key, body: { generation: recovery.generation, handler_version: recovery.handler_version, handlers: recovery.handlers, request_id: recovery.request_id, acknowledge_unknown: recovery.acknowledge_unknown } });
    } catch (error) {
      // A definite rejection permits a new decision; a lost response must retry
      // the original ID so it cannot create another replacement or lose its key.
      if (error instanceof AgenstraError && error.status >= 400 && error.status < 500) { this.browserRecovery = null; this.remove("browser_recovery"); }
      if (error.code === "browser_outcome_unresolved") this.emit("reconciliation", { status: "unknown" });
      throw error;
    }
    if (this.closed) throw new AgenstraError("client_closed");
    this.browser = { ...data.session, key: data.key };
    this.browserRecovery = null;
    this.save("browser", this.browser); this.remove("browser_recovery");
  }
  async recoverBrowser({ acknowledgeUnknown = false } = {}) {
    if (this.closed) throw new AgenstraError("client_closed");
    if (!this.options.browser) throw new AgenstraError("browser_integration_unavailable");
    if (this.recoveryPromise) return this.recoveryPromise;
    const connecting = this.browserPromise;
    this.recoveryPromise = (async () => {
      if (connecting) await connecting.catch(() => {});
      if (this.executing) throw new AgenstraError("browser_recovery_busy");
      this.browserEpoch++; clearTimeout(this.browserTimer);
      await this.observationChain.catch(() => {});
      if (this.flushPromise) await this.flushPromise.catch(() => {});
      await this.replaceBrowser(acknowledgeUnknown);
      await this.publishPageObservation();
      if (!this.closed) this.browserTimer = setTimeout(() => this.pollBrowser(), 0);
      this.browserConnected = true;
      this.emit("connection", { status: "connected" });
      return this.browser;
    })();
    try {
      const browser = await this.recoveryPromise;
      this.browserPromise = Promise.resolve(browser);
      return browser;
    } catch (error) { this.browserPromise = null; this.browserConnected = false; this.emit("connection", { status: "disconnected" }); throw error; }
    finally { this.recoveryPromise = null; }
  }
  updatePageObservation(observation) {
    // Page observations are tool data, never the conversation's agent context.
    // Serialize updates so page revisions cannot race in one tab.
    this.pendingPageObservation = structuredClone(observation);
    if (!this.browser) return Promise.resolve();
    return this.publishPageObservation();
  }
  publishPageObservation() {
    this.observationChain = this.observationChain.catch(() => {}).then(() => this.publishPageObservationOnce());
    return this.observationChain;
  }
  async publishPageObservationOnce() {
    if (!this.browser) return;
    const browser = this.browser, epoch = this.browserEpoch;
    // Freeze the bytes to publish before awaiting the server. A host may return
    // a live state object, and an attempted upload is not a confirmed snapshot.
    const observation = structuredClone(this.options.getPageObservation ? await this.options.getPageObservation() : (this.pendingPageObservation ?? this.pageObservation));
    const published = this.pageObservationBinding;
    if (published?.sessionId === browser.id && published.generation === browser.generation && published.revision === browser.context_revision && JSON.stringify(observation) === JSON.stringify(this.pageObservation)) return;
    const data = await this.request("/browser/v1/sessions/" + browser.id + "/observation", { method: "POST", browserKey: browser.key, body: { generation: browser.generation, revision: browser.context_revision, observation } });
    if (this.closed || epoch !== this.browserEpoch) return;
    this.browser = { ...data.session, key: browser.key };
    this.pageObservation = structuredClone(observation);
    this.pageObservationBinding = { sessionId: this.browser.id, generation: this.browser.generation, revision: this.browser.context_revision };
    this.save("browser", this.browser);
  }
  async pollBrowser() {
    if (this.closed) return;
    const epoch = this.browserEpoch;
    try {
      const latest = this.options.getPageObservation ? await this.options.getPageObservation() : (this.pendingPageObservation ?? this.pageObservation);
      if (JSON.stringify(latest) !== JSON.stringify(this.pageObservation)) await this.updatePageObservation(latest);
      if (epoch !== this.browserEpoch) return;
      await this.flushReceipts();
      if (epoch !== this.browserEpoch) return;
      const data = await this.request("/browser/v1/sessions/" + this.browser.id + "/poll", { method: "POST", browserKey: this.browser.key, body: { generation: this.browser.generation } });
      if (this.closed || epoch !== this.browserEpoch) return;
      if (!this.browserConnected) { this.browserConnected = true; this.emit("connection", { status: "connected" }); }
      if (data.blocked_unknown) this.emit("reconciliation", { status: "unknown" });
      for (const command of data.commands) void this.executeCommand(command);
    } catch (error) {
      if (this.closed || epoch !== this.browserEpoch) return;
      this.browserConnected = false;
      this.emit("error", error);
      this.emit("connection", { status: "disconnected" });
      if (["browser_generation_changed", "browser_session_invalid"].includes(error.code)) return;
    }
    if (!this.closed && epoch === this.browserEpoch && !this.recoveryPromise) this.browserTimer = setTimeout(() => this.pollBrowser(), this.options.pollInterval || 1000);
  }
  async executeCommand(command) {
    if (this.executing) return;
    const cached = this.receipts[command.id];
    if (cached) {
      try { await this.flushReceipts(); } catch (error) { this.emit("error", error); }
      return;
    }
    this.executing = true;
    this.activeCommand = command.id;
    let beginRequested = false;
    try {
      // Persist before claiming. A crash cannot cause the handler to be rerun.
      const receipt = { command, status: "starting" };
      this.receipts[command.id] = receipt; this.save("receipts", this.receipts);
      await this.observationChain;
      // Observe page changes before claiming an action against a page revision.
      if (this.options.getPageObservation) {
        const latest = await this.options.getPageObservation();
        if (JSON.stringify(latest) !== JSON.stringify(this.pageObservation)) await this.updatePageObservation(latest);
      }
      beginRequested = true;
      const begun = await this.request("/browser/v1/commands/" + command.id + "/begin", { method: "POST", browserKey: this.browser.key, body: { generation: command.generation } });
      if (!begun.accepted) {
        receipt.status = "acked"; this.save("receipts", this.receipts); return;
      }
      receipt.status = "running"; this.save("receipts", this.receipts);
      this.emit("action", { command, status: "running" });
      const handler = this.actions.get(command.action);
      if (!handler) throw new AgenstraError("browser_handler_unavailable");
      const result = structuredClone((await handler(structuredClone(command.arguments), { commandId: command.id, runId: command.run_id })) ?? {});
      receipt.status = "succeeded"; receipt.result = result;
      this.save("receipts", this.receipts);
      try {
        const latest = this.options.getPageObservation ? await this.options.getPageObservation() : (this.pendingPageObservation ?? this.pageObservation);
        if (JSON.stringify(latest) !== JSON.stringify(this.pageObservation)) await this.updatePageObservation(latest);
      } catch (error) { this.emit("error", error); }
      await this.flushReceipts();
      this.emit("action", { command, status: ["confirmed", "acked"].includes(receipt.status) ? "succeeded" : "unknown" });
    } catch (error) {
      const receipt = this.receipts[command.id];
      // A server-confirmed result stays confirmed when the following run read
      // or reconciliation fails. Retry that recovery work, not the result.
      if (receipt?.status === "starting" && !beginRequested) {
        // No claim was sent and no handler ran. Leave the original command
        // available for dispatch after page synchronization recovers.
        delete this.receipts[command.id]; this.save("receipts", this.receipts);
      } else if (receipt && !["succeeded", "confirmed", "acked"].includes(receipt.status)) {
        const definite = receipt.status === "running" && error instanceof AgenstraActionError;
        receipt.status = definite ? "failed" : "unknown";
        receipt.error_code = definite ? error.code : "browser_handler_outcome_unknown";
        this.save("receipts", this.receipts);
        try { await this.flushReceipts(); } catch { /* Retain the receipt for reconnection. */ }
      }
      this.emit("error", error);
    } finally { this.executing = false; this.activeCommand = null; }
  }
  async flushReceipts() {
    if (this.flushPromise) return this.flushPromise;
    this.flushPromise = this.flushReceiptsOnce().finally(() => { this.flushPromise = null; });
    return this.flushPromise;
  }
  async flushReceiptsOnce() {
    if (!this.browser) return;
    for (const receipt of Object.values(this.receipts)) {
      if (receipt.status === "acked") continue;
      if (receipt.status === "confirmed") { await this.finishReceipt(receipt); continue; }
      if (receipt.command.id === this.activeCommand && ["starting", "running"].includes(receipt.status)) continue;
      if (receipt.status === "starting" || receipt.status === "running") {
        receipt.status = "unknown"; receipt.error_code = "browser_client_interrupted"; this.save("receipts", this.receipts);
      }
      const command = receipt.command;
      if (command.session_id !== this.browser.id) continue;
      let result;
      try { result = await this.request("/browser/v1/commands/" + command.id + "/result", { method: "POST", browserKey: this.browser.key, body: { generation: command.generation, status: receipt.status, result: receipt.result || null, error_code: receipt.error_code || "" } }); }
      catch (error) {
        if (error.code === "browser_result_invalid") { receipt.status = "unknown"; receipt.result = null; receipt.error_code = "browser_result_invalid"; this.save("receipts", this.receipts); continue; }
        if (error.code === "browser_result_conflict") {
          const existing = await this.request("/browser/v1/commands/" + command.id, { browserKey: this.browser.key });
          if (["failed", "expired", "cancelled"].includes(existing.status)) { receipt.status = "acked"; this.save("receipts", this.receipts); continue; }
        }
        throw error;
      }
      if (result.status === "unknown") { this.emit("reconciliation", result); continue; }
      receipt.status = "confirmed"; this.save("receipts", this.receipts);
      await this.finishReceipt(receipt);
    }
  }
  async finishReceipt(receipt) {
    // Keep the recovery work durable after ACK. A lost get/reconcile response,
    // or the Host entering reconciliation later, must be retried on reload.
    const run = await this.getRun(receipt.command.run_id);
    if (run.status === "needs_reconciliation") await this.reconcile(receipt.command.id, run.revision);
    if (["completed", "failed", "cancelled"].includes(run.status)) {
      receipt.status = "acked"; this.save("receipts", this.receipts);
    }
  }
  changeConversation(operation) {
    // Selection, restoration and creation share a queue. A late response cannot
    // overwrite a newer selection or create an extra default conversation.
    const pending = this.selectionChain.then(() => {
      if (this.closed) throw new AgenstraError("client_closed");
      return operation();
    });
    this.selectionChain = pending.catch(() => {});
    return pending;
  }
  rememberConversation(conversation) {
    if (this.closed) throw new AgenstraError("client_closed");
    if (conversation.integration_id !== this.options.integration) throw new AgenstraError("conversation_integration_mismatch");
    this.conversation = conversation;
    this.selectionRevision++;
    this.save("conversation", conversation.id);
    return conversation;
  }
  memoryPath(id = "", suffix = "") {
    return "/web/v1/memories" + (id ? "/" + encodeURIComponent(id) : "") + suffix + "?integration_id=" + encodeURIComponent(this.options.integration);
  }
  listMemories({ limit = 100, offset = 0 } = {}) { return this.request(this.memoryPath() + "&limit=" + encodeURIComponent(limit) + "&offset=" + encodeURIComponent(offset)); }
  getMemory(id) { return this.request(this.memoryPath(id)); }
  setMemory(update) { return this.request(this.memoryPath(), { method: "POST", body: update }); }
  deleteMemory(id, revision) { return this.request(this.memoryPath(id), { method: "DELETE", body: { revision } }); }
  memoryHistory(id) { return this.request(this.memoryPath(id, "/history")); }
  listConversations() {
    return this.request("/chat/v1/conversations?integration_id=" + encodeURIComponent(this.options.integration));
  }
  selectConversation(id) {
    if (typeof id !== "string" || !id) return Promise.reject(new TypeError("conversation id is required"));
    return this.changeConversation(async () => {
      const snapshot = await this.request("/chat/v1/conversations/" + encodeURIComponent(id));
      const conversation = this.rememberConversation(snapshot.conversation);
      this.emit("conversation", snapshot);
      return conversation;
    });
  }
  createConversation() {
    return this.changeConversation(async () => {
      const conversation = this.rememberConversation(await this.request("/chat/v1/conversations", { method: "POST", body: { integration_id: this.options.integration } }));
      this.emit("conversation", { conversation, messages: [] });
      return conversation;
    });
  }
  getConversation() {
    return this.changeConversation(() => this.conversation || this.ensureConversation());
  }
  async ensureConversation() {
    const saved = this.load("conversation");
    if (saved) {
      try { const data = await this.request("/chat/v1/conversations/" + encodeURIComponent(saved)); return this.rememberConversation(data.conversation); }
      catch (error) { if (!["not_found", "conversation_integration_mismatch"].includes(error.code)) throw error; this.remove("conversation"); }
    }
    return this.rememberConversation(await this.request("/chat/v1/conversations", { method: "POST", body: { integration_id: this.options.integration } }));
  }
  async snapshot() { const conversation = await this.getConversation(); return this.request("/chat/v1/conversations/" + conversation.id); }
  async send(text, { clientId = this.id(), sources = [] } = {}) {
    try {
      const conversation = await this.getConversation();
      await this.connectBrowser();
      return await this.request("/chat/v1/conversations/" + conversation.id + "/messages", { method: "POST", browserKey: this.browser?.key, body: { client_id: clientId, text, ...(sources.length ? { sources } : {}), session_id: this.browser?.id || "" } });
    }
    catch (error) { error.clientId = clientId; throw error; }
  }
  watchConversation(callback) {
    const off = this.on("conversation", callback);
    this.chatWatchers++;
    if (this.chatWatchers === 1) { void this.connectBrowser().catch(error => this.emit("error", error)); this.pollChat(); }
    let watching = true;
    return () => { if (!watching) return; watching = false; off(); this.chatWatchers--; if (!this.chatWatchers) clearTimeout(this.chatTimer); };
  }
  async pollChat() {
    if (this.closed || !this.chatWatchers) return;
    try {
      const conversation = await this.getConversation();
      const selectionRevision = this.selectionRevision;
      const snapshot = await this.request("/chat/v1/conversations/" + conversation.id);
      if (selectionRevision === this.selectionRevision && snapshot.conversation.id === this.conversation?.id) this.emit("conversation", snapshot);
      if (!this.options.browser || this.browserConnected) this.emit("connection", { status: "connected" });
    }
    catch (error) { this.emit("error", error); this.emit("connection", { status: "disconnected" }); }
    if (!this.closed && this.chatWatchers) this.chatTimer = setTimeout(() => this.pollChat(), this.options.pollInterval || 1000);
  }
  getRun(id) { return this.request("/web/v1/runs/" + id); }
  async setContextPolicy(id, policy, revision, { requestId = this.id() } = {}) {
    try { return await this.request("/web/v1/runs/" + encodeURIComponent(id) + "/context-policy", { method: "POST", body: { request_id: requestId, revision, policy } }); }
    catch (error) { error.requestId = requestId; throw error; }
  }
  getRuntimeInfo() { return this.request("/web/v1/integrations/" + encodeURIComponent(this.options.integration) + "/runtime-info"); }
  cancelRun(id) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/cancel", { method: "POST", body: {} }); }
  resumeRun(id) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/resume", { method: "POST", body: {} }); }
  getArtifact(id, artifactId) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/artifacts/" + encodeURIComponent(artifactId)); }
  getRunTelemetry(id) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/telemetry"); }
  getRunDiagnostics(id) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/diagnostics"); }
  getRunEvents(id, { after = 0, limit = 100 } = {}) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/events?after=" + after + "&limit=" + limit); }
  async steerRun(id, text, revision, { requestId = this.id() } = {}) {
    try { return await this.request("/web/v1/runs/" + encodeURIComponent(id) + "/steer", { method: "POST", body: { request_id: requestId, text, revision } }); }
    catch (error) { error.requestId = requestId; throw error; }
  }
  watchRun(id, callback, { after = 0 } = {}) {
    if (this.closed) throw new AgenstraError("client_closed");
    let stopped = false, timer, cursor = after;
    const stop = () => { stopped = true; clearTimeout(timer); this.runWatchers.delete(stop); };
    const poll = async () => {
      if (stopped || this.closed) return;
      try {
        // Read status before events so a terminal snapshot cannot hide its
        // final event. Drain full pages before stopping.
        const run = await this.getRun(id);
        if (stopped || this.closed) return;
        const page = await this.getRunEvents(id, { after: cursor });
        if (stopped || this.closed) return;
        const events = page.filter(item => Number.isSafeInteger(item.sequence) && item.sequence > cursor);
        for (const item of events) cursor = Math.max(cursor, item.sequence);
        try { callback({ run, telemetry: run.telemetry ?? null, events, cursor }); } catch (error) { this.options.onListenerError?.(error); }
        if (["completed", "failed", "cancelled"].includes(run.status) && page.length < 100) { stop(); return; }
        timer = setTimeout(poll, page.length === 100 ? 0 : (this.options.pollInterval || 1000));
      } catch (error) {
        if (stopped || this.closed) return;
        this.emit("error", error);
        timer = setTimeout(poll, this.options.pollInterval || 1000);
      }
    };
    this.runWatchers.add(stop);
    void poll();
    return stop;
  }
  supplyInput(id, field, text, revision) { return this.request("/web/v1/runs/" + id + "/input", { method: "POST", body: { field, text, revision } }); }
  approve(id, invocation, revision, approved) { return this.request("/web/v1/runs/" + id + "/approval", { method: "POST", body: { invocation_id: invocation.invocation_id, arguments_sha256: invocation.arguments_sha256, revision, approved } }); }
  cancelMessage(id) { return this.request("/chat/v1/messages/" + id + "/cancel", { method: "POST", body: {} }); }
  reconcile(id, revision) { return this.request("/browser/v1/commands/" + id + "/reconcile", { method: "POST", body: { revision } }); }
  reconcileInvocation(id, invocation, revision) { return this.request("/web/v1/runs/" + encodeURIComponent(id) + "/reconcile", { method: "POST", body: { invocation_id: invocation.invocation_id, arguments_sha256: invocation.arguments_sha256, revision } }); }
  async run(instruction, { requestId = this.id(), sources = [] } = {}) {
    try {
      await this.connectBrowser();
      return await this.request("/browser/v1/runs", { method: "POST", browserKey: this.browser.key, body: { integration_id: this.options.integration, session_id: this.browser.id, instruction, ...(sources.length ? { sources } : {}), request_id: requestId } });
    } catch (error) { error.requestId = requestId; throw error; }
  }
  async destroy({ closeSession = true } = {}) {
    if (this.closed) return;
    this.closed = true;
    clearTimeout(this.browserTimer);clearTimeout(this.chatTimer);
  for (const stop of this.runWatchers) stop();
    globalThis.removeEventListener?.("pagehide", this.pagehide);
    for (const aborter of this.aborters) aborter.abort();
    this.listeners.clear();
    if (closeSession && this.browser && this.token) {
      const aborter = new AbortController();const timer = setTimeout(() => aborter.abort(), 2000);
      try { await this.fetch(this.endpoint + "/browser/v1/sessions/" + this.browser.id + "/close", { method: "POST", headers: { Authorization: "Bearer " + this.token, "Content-Type": "application/json", "X-Agenstra-Browser-Key": this.browser.key }, body: JSON.stringify({ generation: this.browser.generation }), signal: aborter.signal, credentials: "same-origin", keepalive: true }); } catch { /* The server expires interrupted actions independently. */ }
      finally { clearTimeout(timer); }
      this.remove("browser");
    }
  }
}
export function createAgenstraClient(options) { return new AgenstraClient(options); }
