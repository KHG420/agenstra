const labels = {
  "zh-CN": { title: "Agenstra 助手", subtitle: "查询数据、执行操作，并同步当前页面", chatOnly: "通过已授权的系统能力完成任务", empty: "从一个具体任务开始", hint: "例如：查询本月订单，再打开其中一笔的详情。", placeholder: "描述你想完成的操作…", send: "发送", queue: "添加任务", stop: "停止任务", approve: "批准操作", reject: "拒绝操作", approval: "执行前请核对操作和参数", input: "请补充以下信息", unknown: "操作结果尚未确认。核对实际页面后再继续；也可以停止此任务。", reconcile: "读取已确认的回执", connected: "已连接", disconnected: "连接中断，正在重试", loading: "正在连接系统…", error: "请求未完成，请检查连接或重试。", retry: "重试发送", statuses: { queued: "等待执行", creating: "准备任务", active: "正在执行", cancelling: "正在停止", completed: "已完成", failed: "未完成", cancelled: "已停止", needs_input: "等待补充信息", needs_approval: "等待确认", waiting: "等待操作结果", needs_reconciliation: "等待核对", needs_authorization: "需要恢复授权" } },
  en: { title: "Agenstra assistant", subtitle: "Query data, take action, and update this page", chatOnly: "Complete tasks using authorized system capabilities", empty: "Start with a specific task", hint: "For example: find this month's orders and open one for review.", placeholder: "Describe what you want to do…", send: "Send", queue: "Queue task", stop: "Stop task", approve: "Approve action", reject: "Reject action", approval: "Review the action and arguments before proceeding", input: "More information is needed", unknown: "The action outcome is unconfirmed. Check the actual page before continuing, or stop this task.", reconcile: "Read the verified receipt", connected: "Connected", disconnected: "Disconnected. Retrying…", loading: "Connecting to your system…", error: "The request did not complete. Check the connection or retry.", retry: "Retry send", statuses: { queued: "Queued", creating: "Preparing", active: "Working", cancelling: "Stopping", completed: "Completed", failed: "Incomplete", cancelled: "Stopped", needs_input: "Waiting for input", needs_approval: "Waiting for approval", waiting: "Waiting for the result", needs_reconciliation: "Needs review", needs_authorization: "Authorization needed" } }
};
const style = `
:host{--agenstra-accent:#116b64;--agenstra-text:#18313b;--agenstra-muted:#51636a;--agenstra-surface:#fff;--agenstra-ground:#f4f6f3;--agenstra-line:#d8e0dc;--agenstra-danger:#a5352c;display:block;height:var(--agenstra-height,560px);min-height:320px;color:var(--agenstra-text);font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans SC",sans-serif;color-scheme:light}
*{box-sizing:border-box}button,textarea{font:inherit}button{cursor:pointer}button:disabled{cursor:wait;opacity:.65}button:focus-visible,textarea:focus-visible,summary:focus-visible{outline:3px solid var(--agenstra-accent);outline-offset:3px}::selection{background:var(--agenstra-accent);color:var(--agenstra-surface)}
.panel{display:flex;flex-direction:column;height:100%;min-width:0;border:1px solid var(--agenstra-line);border-radius:14px;background:var(--agenstra-surface);overflow:hidden}
header{padding:20px 22px 17px;border-bottom:1px solid var(--agenstra-line)}h2{margin:0;font-size:1.08rem;font-weight:650;letter-spacing:-.02em}header p{margin:5px 0 0;color:var(--agenstra-muted);font-size:.82rem}.connection{margin-top:10px;color:var(--agenstra-muted);font-size:.76rem}
.log{flex:1;min-height:0;overflow:auto;padding:22px;scrollbar-color:var(--agenstra-line) transparent;scrollbar-width:thin}.empty{padding:34px 0}.empty h3{margin:0 0 8px;font-size:1.18rem;font-weight:600}.empty p{max-width:34ch;margin:0;color:var(--agenstra-muted);font-size:.9rem}.turn+.turn{margin-top:25px;padding-top:24px;border-top:1px solid var(--agenstra-line)}
.user{margin:0 0 13px 26px;padding:11px 14px;border-radius:10px;background:var(--agenstra-ground);white-space:pre-wrap;overflow-wrap:anywhere}.answer{white-space:pre-wrap;overflow-wrap:anywhere;max-width:70ch}.answer pre{white-space:pre;overflow:auto;padding:12px;background:var(--agenstra-ground);border-radius:8px;font: .82rem/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}.status{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:9px 0;color:var(--agenstra-muted);font-size:.78rem}.status[data-state=failed]{color:var(--agenstra-danger)}
button{border:1px solid var(--agenstra-line);border-radius:7px;background:var(--agenstra-surface);color:var(--agenstra-text);padding:6px 11px;font-size:.82rem;transition:background 140ms ease-out}button:hover{background:var(--agenstra-ground)}.primary{background:var(--agenstra-accent);border-color:var(--agenstra-accent);color:var(--agenstra-surface)}.primary:hover{filter:brightness(.94)}.text-button{padding:2px 0;border:0;color:var(--agenstra-accent);background:transparent;font-size:.78rem}.text-button:hover{text-decoration:underline;text-underline-offset:3px;background:transparent}
.request{padding-top:8px;margin-top:12px}.request p{margin:0 0 10px}.request details{margin:12px 0}.request summary{cursor:pointer;overflow-wrap:anywhere;font-weight:600;font-size:.86rem}.request pre{max-height:220px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;padding:12px;background:var(--agenstra-ground);font:.78rem/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;border-radius:8px}.actions{display:flex;gap:8px;flex-wrap:wrap}.notice{color:var(--agenstra-muted);font-size:.86rem}
form{padding:15px 18px 17px;border-top:1px solid var(--agenstra-line)}label{display:block;margin-bottom:7px;color:var(--agenstra-muted);font-size:.78rem}.composer{display:flex;gap:10px;align-items:flex-end}textarea{flex:1;min-width:0;min-height:60px;max-height:160px;resize:vertical;border:1px solid var(--agenstra-line);border-radius:8px;padding:10px 12px;background:var(--agenstra-surface);color:var(--agenstra-text);caret-color:var(--agenstra-accent)}textarea::placeholder{color:var(--agenstra-muted)}.send{min-height:40px}.feedback{margin:0;padding:0 18px;color:var(--agenstra-danger);font-size:.82rem;overflow-wrap:anywhere}.feedback:not(:empty){padding-top:10px}
@media(prefers-reduced-motion:reduce){button{transition:none}}
`;
function appendAnswer(element, value) {
  // Content is always inserted as text, including model-generated markup.
  // Code fences get readable code blocks without executing HTML.
  const parts = String(value).split(/\x60\x60\x60[^\n]*\n/);
  if (parts.length === 1) { element.textContent = value; return; }
  const chunks = String(value).split("\x60\x60\x60");
  chunks.forEach((chunk, i) => {
    if (i % 2) { const pre = document.createElement("pre"); const code = document.createElement("code"); code.textContent = chunk.replace(/^[^\n]*\n/, "");pre.append(code);element.append(pre); }
    else element.append(document.createTextNode(chunk));
  });
}
export class AgenstraChat extends (globalThis.HTMLElement || class {}) {
  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this.shadowRoot.innerHTML = "<style>" + style + "</style><section class='panel'><header><h2></h2><p class='subtitle'></p><div class='connection' role='status'></div></header><div class='log' role='log' aria-live='polite' aria-relevant='additions text'></div><p class='feedback' role='alert'></p><form><label for='message'></label><div class='composer'><textarea id='message' rows='2' maxlength='12000'></textarea><button class='send primary' type='submit'></button></div></form></section>";
    this.log = this.shadowRoot.querySelector(".log");
    this.input = this.shadowRoot.querySelector("textarea");
    this.form = this.shadowRoot.querySelector("form");
    this.feedback = this.shadowRoot.querySelector(".feedback");
    this.form.addEventListener("submit", event => { event.preventDefault(); this.submit(); });
    this.input.addEventListener("keydown", event => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); this.submit(); } });
  }
  set client(value) { this.detach();this._client = value;if (this.isConnected) this.attach(); }
  get client() { return this._client; }
  connectedCallback() { this.attach(); }
  disconnectedCallback() { this.detach(); }
  attach() {
    if (this.off || !this.client) return;
    this.text = labels[this.getAttribute("lang")] || labels["zh-CN"];
    const t = this.text;
    this.shadowRoot.querySelector("h2").textContent = this.getAttribute("title") || t.title;
    this.shadowRoot.querySelector(".subtitle").textContent = this.client.options.browser ? t.subtitle : t.chatOnly;
    this.shadowRoot.querySelector(".connection").textContent = t.loading;
    this.shadowRoot.querySelector("label").textContent = t.placeholder;
    this.input.placeholder = t.placeholder;
    this.shadowRoot.querySelector(".send").textContent = t.send;
    this.render({ messages: [] });
    this.off = [
      this.client.watchConversation(snapshot => { this.feedback.textContent = "";this.render(snapshot); }),
      this.client.on("connection", state => { this.shadowRoot.querySelector(".connection").textContent = state.status === "connected" ? t.connected : t.disconnected; }),
      this.client.on("error", error => { this.showError(error); })
    ];
  }
  detach() { for (const off of this.off || []) off();this.off = null;this.signature = null; }
  showError(error) { if (error?.name === "AbortError") return;this.feedback.textContent = this.text.error + " (" + (error.code || "connection_error") + ")"; }
  async submit() {
    const text = this.input.value.trim();
    if (!text || this.busy || !this.client) return;
    this.busy = true;this.form.setAttribute("aria-busy", "true");const send = this.shadowRoot.querySelector(".send");send.disabled = true;
    try {
      if (this.awaitingInput) {
        const { run, runtime } = this.awaitingInput;
        await this.client.supplyInput(run.run_id, runtime.input_field, text, run.revision);
      } else {
        if (!this.pendingSend || this.pendingSend.text !== text) this.pendingSend = { text, clientId: this.client.id() };
        await this.client.send(text, this.pendingSend);
      }
      this.pendingSend = null;this.input.value = "";this.feedback.textContent = "";this.render(await this.client.snapshot());this.input.focus();
    } catch (error) { this.showError(error);send.textContent = this.text.retry; }
    finally { this.busy = false;send.disabled = false;this.form.removeAttribute("aria-busy"); }
  }
  button(label, key, action, primary = false) {
    const b = document.createElement("button");b.type = "button";b.textContent = label;b.dataset.focusKey = key;
    if (primary) b.className = "primary";
    b.addEventListener("click", async () => {
      b.disabled = true;
      try { await action();this.feedback.textContent = "";this.render(await this.client.snapshot()); }
      catch (error) { this.showError(error);b.disabled = false; }
    });return b;
  }
  render(snapshot) {
    const signature = JSON.stringify(snapshot.messages);
    if (signature === this.signature) return;
    this.signature = signature;this.awaitingInput = null;
    const focusKey = this.shadowRoot.activeElement?.dataset.focusKey;
    const follow = this.log.scrollHeight - this.log.scrollTop - this.log.clientHeight < 60;
    const scrollTop = this.log.scrollTop;
    this.log.replaceChildren();
    const t = this.text;const messages = snapshot.messages || [];
    if (!messages.length) {
      const empty = document.createElement("div");empty.className = "empty";
      const h = document.createElement("h3");h.textContent = t.empty;const p = document.createElement("p");p.textContent = t.hint;empty.append(h, p);this.log.append(empty);
    }
    for (const message of messages) {
      const turn = document.createElement("article");turn.className = "turn";
      const user = document.createElement("p");user.className = "user";user.textContent = message.text;turn.append(user);
      const run = message.run;const runtime = run?.state?.runtime;
      const state = run?.status || message.status;
      const status = document.createElement("div");status.className = "status";status.dataset.state = state;
      const caption = document.createElement("span");caption.textContent = t.statuses[state] || t.statuses.active;status.append(caption);
      if (!["completed", "failed", "cancelled"].includes(message.status)) {
        const stop = this.button(t.stop, message.id + ":cancel", () => this.client.cancelMessage(message.id));stop.className = "text-button";status.append(stop);
      }
      turn.append(status);
      if (message.answer_markdown) { const answer = document.createElement("div");answer.className = "answer";appendAnswer(answer, message.answer_markdown);turn.append(answer); }
      if (message.error_code) { const p = document.createElement("p");p.className = "notice";p.textContent = t.error + " (" + message.error_code + ")";turn.append(p); }
      if (state === "needs_input") {
        this.awaitingInput = { run, runtime };
        const p = document.createElement("p");p.className = "notice";p.textContent = runtime.input_prompt;turn.append(p);
      }
      if (state === "needs_approval") {
        const request = document.createElement("div");request.className = "request";
        const intro = document.createElement("p");intro.textContent = t.approval;request.append(intro);
        for (const item of runtime.pending || []) {
          if (item.status !== "needs_approval") continue;
          const details = document.createElement("details");details.open = true;
          const summary = document.createElement("summary");summary.textContent = item.call.capability;
          const pre = document.createElement("pre");pre.textContent = JSON.stringify(item.call.arguments, null, 2);details.append(summary, pre);request.append(details);
          const actions = document.createElement("div");actions.className = "actions";
          actions.append(this.button(t.approve, item.invocation_id + ":approve", () => this.client.approve(run.run_id, item, run.revision, true), true), this.button(t.reject, item.invocation_id + ":reject", () => this.client.approve(run.run_id, item, run.revision, false)));request.append(actions);
        }
        turn.append(request);
      }
      if (state === "needs_reconciliation") {
        const p = document.createElement("p");p.className = "notice";p.textContent = t.unknown;turn.append(p);
        for (const item of runtime.pending || []) if (item.status === "unknown" && item.operation) turn.append(this.button(t.reconcile, item.invocation_id + ":reconcile", () => this.client.reconcile(item.invocation_id, run.revision)));
      }
      this.log.append(turn);
    }
    this.shadowRoot.querySelector("label").textContent = this.awaitingInput ? t.input : t.placeholder;
    this.shadowRoot.querySelector(".send").textContent = this.awaitingInput ? t.send : messages.some(m => m.status === "active" || m.status === "creating") ? t.queue : t.send;
    this.log.scrollTop = follow ? this.log.scrollHeight : scrollTop;
    if (focusKey) for (const button of this.log.querySelectorAll("button")) if (button.dataset.focusKey === focusKey) button.focus({ preventScroll: true });
  }
}
if (globalThis.customElements && !customElements.get("agenstra-chat")) customElements.define("agenstra-chat", AgenstraChat);
export function mountAgenstraChat(container, { client, title, locale = "zh-CN" }) {
  const element = document.createElement("agenstra-chat");
  element.setAttribute("lang", locale);
  if (title) element.setAttribute("title", title);
  element.client = client;container.append(element);
  return { element, unmount: () => element.remove() };
}
