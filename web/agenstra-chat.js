/** Optional UI. Importing the headless client never loads this component. */
const labels = {
  "zh-CN": { title: "Agenstra 助手", subtitle: "查询数据、执行操作，并同步当前页面", chatOnly: "通过已授权的系统能力完成任务", empty: "从一个具体任务开始", hint: "描述要查询的信息或要执行的操作。", placeholder: "描述你想完成的操作…", choose: "请选择…", send: "发送", queue: "添加任务", stop: "停止任务", approve: "批准操作", reject: "拒绝操作", approval: "执行前请核对操作和参数", input: "请补充以下信息", invalidInput: "请按提示填写有效信息。", invalidChoice: "请选择列表中的一个选项。", invalidDate: "请输入有效日期。", unknown: "操作结果尚未确认。核对实际页面后再继续；也可以停止此任务。", unknownStopped: "操作结果仍待核对。核对只更新结果，任务仍保持停止。", reconcile: "读取已确认的回执", connected: "已连接", disconnected: "连接中断，正在重试", loading: "正在连接系统…", error: "请求未完成，请检查连接或重试。", retry: "重试发送", statuses: { queued: "等待执行", creating: "准备任务", active: "正在执行", cancelling: "正在停止", completed: "已完成", failed: "未完成", cancelled: "已停止", needs_input: "等待补充信息", needs_approval: "等待确认", waiting: "等待操作结果", needs_reconciliation: "等待核对", needs_authorization: "需要恢复授权" } },
  en: { title: "Agenstra assistant", subtitle: "Query data, take action, and update this page", chatOnly: "Complete tasks using authorized system capabilities", empty: "Start with a specific task", hint: "Describe the information you need or the action you want to take.", placeholder: "Describe what you want to do…", choose: "Choose…", send: "Send", queue: "Queue task", stop: "Stop task", approve: "Approve action", reject: "Reject action", approval: "Review the action and arguments before proceeding", input: "More information is needed", invalidInput: "Enter a value that matches the request.", invalidChoice: "Choose an option from the list.", invalidDate: "Enter a valid date.", unknown: "The action outcome is unconfirmed. Check the actual page before continuing, or stop this task.", unknownStopped: "The operation result still needs verification. Verification only updates the result; this task stays stopped.", reconcile: "Read the verified receipt", connected: "Connected", disconnected: "Disconnected. Retrying…", loading: "Connecting to your system…", error: "The request did not complete. Check the connection or retry.", retry: "Retry send", statuses: { queued: "Queued", creating: "Preparing", active: "Working", cancelling: "Stopping", completed: "Completed", failed: "Incomplete", cancelled: "Stopped", needs_input: "Waiting for input", needs_approval: "Waiting for approval", waiting: "Waiting for the result", needs_reconciliation: "Needs review", needs_authorization: "Authorization needed" } }
};
const outcomeLabels = {
  "zh-CN": {
    succeeded: capability => `业务操作已成功：${capability}`,
    accepted: capability => `业务操作已受理，最终结果尚未确认：${capability}`,
    failed: capability => `业务操作失败：${capability}`,
    unknown: capability => `业务操作结果尚未确认：${capability}`,
    unavailable: "执行回执已保留，但结果详情未能保存。请核对业务系统中的实际结果。",
    incomplete: "任务未完成。请以上方回执判断操作结果，避免重复提交已成功的操作。"
  },
  en: {
    succeeded: capability => `Action succeeded: ${capability}`,
    accepted: capability => `Action accepted; final outcome unconfirmed: ${capability}`,
    failed: capability => `Action failed: ${capability}`,
    unknown: capability => `Action outcome unconfirmed: ${capability}`,
    unavailable: "The execution receipt was retained, but result details could not be saved. Check the actual result in your system.",
    incomplete: "The task is incomplete. Check the action receipts above and avoid resubmitting actions that succeeded."
  }
};
const style = `
:host{--agenstra-accent:#116b64;--agenstra-text:#18313b;--agenstra-muted:#51636a;--agenstra-surface:#fff;--agenstra-ground:#f4f6f3;--agenstra-line:#d8e0dc;--agenstra-danger:#a5352c;display:block;height:var(--agenstra-height,560px);min-height:320px;color:var(--agenstra-text);font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans SC",sans-serif;color-scheme:light}
*{box-sizing:border-box}button,textarea,select,input{font:inherit}button{cursor:pointer}button:disabled{cursor:wait;opacity:.65}button:focus-visible,textarea:focus-visible,select:focus-visible,input:focus-visible,summary:focus-visible{outline:3px solid var(--agenstra-accent);outline-offset:3px}::selection{background:var(--agenstra-accent);color:var(--agenstra-surface)}
.panel{display:flex;flex-direction:column;height:100%;min-width:0;border:1px solid var(--agenstra-line);border-radius:14px;background:var(--agenstra-surface);overflow:hidden}
header{padding:20px 22px 17px;border-bottom:1px solid var(--agenstra-line)}h2{margin:0;font-size:1.08rem;font-weight:650;letter-spacing:-.02em}header p{margin:5px 0 0;color:var(--agenstra-muted);font-size:.82rem}.connection{margin-top:10px;color:var(--agenstra-muted);font-size:.76rem}
.log{flex:1;min-height:0;overflow:auto;padding:22px;scrollbar-color:var(--agenstra-line) transparent;scrollbar-width:thin}.empty{padding:34px 0}.empty h3{margin:0 0 8px;font-size:1.18rem;font-weight:600}.empty p{max-width:34ch;margin:0;color:var(--agenstra-muted);font-size:.9rem}.turn+.turn{margin-top:25px;padding-top:24px;border-top:1px solid var(--agenstra-line)}
.user{margin:0 0 13px 26px;padding:11px 14px;border-radius:10px;background:var(--agenstra-ground);white-space:pre-wrap;overflow-wrap:anywhere}.answer{white-space:pre-wrap;overflow-wrap:anywhere;max-width:70ch}.answer p{margin:0 0 10px}.answer ul,.answer ol{margin:8px 0 12px;padding-inline-start:24px}.answer li{white-space:normal}.answer code{font:.86em ui-monospace,SFMono-Regular,Menlo,monospace}.answer-table{max-width:100%;overflow:auto;margin:12px 0;white-space:normal}.answer table{border-collapse:collapse;font-size:.9rem;font-variant-numeric:tabular-nums}.answer th,.answer td{padding:8px 12px;border:1px solid var(--agenstra-line);text-align:start;min-width:8ch}.answer th{background:var(--agenstra-ground);font-weight:600}.answer-table:focus-visible{outline:3px solid var(--agenstra-accent);outline-offset:3px}.answer pre{white-space:pre;overflow:auto;padding:12px;background:var(--agenstra-ground);border-radius:8px;font: .82rem/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}.status{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:9px 0;color:var(--agenstra-muted);font-size:.78rem}.status[data-state=failed]{color:var(--agenstra-danger)}
button{border:1px solid var(--agenstra-line);border-radius:7px;background:var(--agenstra-surface);color:var(--agenstra-text);padding:6px 11px;font-size:.82rem;transition:background 140ms ease-out}button:hover{background:var(--agenstra-ground)}.primary{background:var(--agenstra-accent);border-color:var(--agenstra-accent);color:var(--agenstra-surface)}.primary:hover{filter:brightness(.94)}.text-button{padding:2px 0;border:0;color:var(--agenstra-accent);background:transparent;font-size:.78rem}.text-button:hover{text-decoration:underline;text-underline-offset:3px;background:transparent}
.request{padding-top:8px;margin-top:12px}.request p{margin:0 0 10px}.request details{margin:12px 0}.request summary{cursor:pointer;overflow-wrap:anywhere;font-weight:600;font-size:.86rem}.request pre{max-height:220px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;padding:12px;background:var(--agenstra-ground);font:.78rem/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;border-radius:8px}.actions{display:flex;gap:8px;flex-wrap:wrap}.notice{color:var(--agenstra-muted);font-size:.86rem}.action-outcome{color:var(--agenstra-text);overflow-wrap:anywhere}
form{padding:15px 18px 17px;border-top:1px solid var(--agenstra-line)}label{display:block;margin-bottom:7px;color:var(--agenstra-muted);font-size:.78rem}.composer{display:flex;gap:10px;align-items:flex-end}textarea,.composer select,.composer input[type=date]{flex:1;min-width:0;min-height:40px;border:1px solid var(--agenstra-line);border-radius:8px;padding:10px 12px;background:var(--agenstra-surface);color:var(--agenstra-text)}textarea{min-height:60px;max-height:160px;resize:vertical;caret-color:var(--agenstra-accent)}textarea::placeholder{color:var(--agenstra-muted)}.send{min-height:40px}.feedback{margin:0;padding:0 18px;color:var(--agenstra-danger);font-size:.82rem;overflow-wrap:anywhere}.feedback:not(:empty){padding-top:10px}
@media(prefers-reduced-motion:reduce){button{transition:none}}
`;
// A small Markdown subset built entirely from DOM nodes. HTML, links and images
// remain literal text; model output never enters an HTML parser.
function appendInline(element, text) {
  const pattern = /\*\*([^*\n]+)\*\*|`([^`\n]+)`/g;
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset) element.append(document.createTextNode(text.slice(offset, match.index)));
    const node = document.createElement(match[1] === undefined ? "code" : "strong");
    node.textContent = match[1] ?? match[2];element.append(node);
    offset = match.index + match[0].length;
  }
  if (offset < text.length) element.append(document.createTextNode(text.slice(offset)));
}
function tableCells(line) {
  return line.trim().replace(/^\|/, "").replace(/(?<!\\)\|$/, "").split(/(?<!\\)\|/).map(cell => cell.trim().replace(/\\\|/g, "|"));
}
export function appendAnswer(element, value) {
  const text = String(value ?? "");
  if (!/```|\*\*|`|^\s*(?:[-*] |\d+\. )|\|/m.test(text)) { element.textContent = text;return; }
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    if (!lines[i].trim()) { i++;continue; }
    if (/^\s*```/.test(lines[i])) {
      const content = [];i++;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) content.push(lines[i++]);
      const pre = document.createElement("pre"), code = document.createElement("code");
      code.textContent = content.join("\n") + (i < lines.length ? "\n" : "");
      pre.append(code);element.append(pre);i++;continue;
    }
    const header = tableCells(lines[i]);
    const separator = i + 1 < lines.length ? tableCells(lines[i + 1]) : [];
    if (lines[i].includes("|") && separator.length === header.length && separator.every(cell => /^:?-{3,}:?$/.test(cell))) {
      const wrap = document.createElement("div"), table = document.createElement("table"), head = document.createElement("thead"), row = document.createElement("tr"), body = document.createElement("tbody");
      wrap.className = "answer-table";wrap.tabIndex = 0;
      header.forEach(cell => { const th = document.createElement("th");th.scope = "col";appendInline(th, cell);row.append(th); });
      head.append(row);table.append(head, body);i += 2;
      while (i < lines.length && lines[i].includes("|")) {
        const cells = tableCells(lines[i]);if (cells.length !== header.length) break;
        const tr = document.createElement("tr");
        cells.forEach(cell => { const td = document.createElement("td");appendInline(td, cell);tr.append(td); });
        body.append(tr);i++;
      }
      wrap.append(table);element.append(wrap);continue;
    }
    const list = /^\s*([-*]|\d+\.)\s+(.+)$/.exec(lines[i]);
    if (list) {
      const ordered = /\d/.test(list[1]), node = document.createElement(ordered ? "ol" : "ul");
      if (ordered) node.start = parseInt(list[1], 10);
      while (i < lines.length) {
        const item = /^\s*([-*]|\d+\.)\s+(.+)$/.exec(lines[i]);
        if (!item || /\d/.test(item[1]) !== ordered) break;
        const li = document.createElement("li");appendInline(li, item[2]);node.append(li);i++;
      }
      element.append(node);continue;
    }
    const paragraph = document.createElement("p");appendInline(paragraph, lines[i++]);element.append(paragraph);
  }
}
export class AgenstraChat extends (globalThis.HTMLElement || class {}) {
  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this.shadowRoot.innerHTML = "<style>" + style + "</style><section class='panel'><header><h2></h2><p class='subtitle'></p><div class='connection' role='status'></div></header><div class='log' role='log' aria-live='polite' aria-relevant='additions text'></div><p id='composer-feedback' class='feedback' role='alert'></p><form><label for='message'></label><div class='composer'><textarea id='message' rows='2'></textarea><button class='send primary' type='submit'></button></div></form></section>";
    this.log = this.shadowRoot.querySelector(".log");
    this.input = this.shadowRoot.querySelector("textarea");
    this.choice = document.createElement("select");this.choice.id = "message-choice";this.choice.hidden = true;this.choice.disabled = true;this.choice.required = true;
    this.dateInput = document.createElement("input");this.dateInput.id = "message-date";this.dateInput.type = "date";this.dateInput.hidden = true;this.dateInput.disabled = true;this.dateInput.required = true;
    this.input.after(this.choice, this.dateInput);
    this.form = this.shadowRoot.querySelector("form");
    this.feedback = this.shadowRoot.querySelector(".feedback");
    this.form.addEventListener("submit", event => { event.preventDefault(); this.submit(); });
    this.input.addEventListener("keydown", event => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); this.submit(); } });
    for (const control of [this.choice, this.dateInput]) control.addEventListener("keydown", event => { if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); this.submit(); } });
  }
  set client(value) { this.detach(); this.pendingSend = null; this.conversationId = null; this.diagnostics = new Map(); this._client = value; if (this.isConnected) this.attach(); }
  get client() { return this._client; }
  connectedCallback() { this.attach(); }
  disconnectedCallback() { this.detach(); }
  attach() {
    if (this.off || !this.client) return;
    this.text = labels[this.getAttribute("lang")] || labels["zh-CN"];
    const t = this.text;
    this.shadowRoot.querySelector("h2").textContent = this.getAttribute("title") || t.title;
    this.shadowRoot.querySelector(".subtitle").textContent = this.getAttribute("subtitle") ?? (this.client.options.browser ? t.subtitle : t.chatOnly);
    this.shadowRoot.querySelector(".connection").textContent = t.loading;
    this.shadowRoot.querySelector("label").textContent = this.getAttribute("placeholder") ?? t.placeholder;
    this.input.placeholder = this.getAttribute("placeholder") ?? t.placeholder;
    this.shadowRoot.querySelector(".send").textContent = t.send;
    this.render({ messages: [] });
    const epoch = this.epoch;
    this.off = [
      this.client.watchConversation(snapshot => { if (epoch !== this.epoch) return; this.render(snapshot); }),
      this.client.on("connection", state => { this.shadowRoot.querySelector(".connection").textContent = state.status === "connected" ? t.connected : t.disconnected; }),
      this.client.on("error", error => { this.showError(error); })
    ];
  }
  detach() { this.epoch = (this.epoch || 0) + 1; for (const off of this.off || []) off(); this.off = null; this.signature = null; }
  activeInput() { return this.choice.hidden ? this.dateInput.hidden ? this.input : this.dateInput : this.choice; }
  showError(error) {
    if (error?.name === "AbortError") return;
    const invalid = error?.status === 422 || error?.code === "input_invalid";
    const type = this.awaitingInput?.runtime?.input_schema?.type;
    const hint = type === "enum" ? this.text.invalidChoice : type === "date" ? this.text.invalidDate : this.text.invalidInput;
    this.feedback.textContent = invalid && this.awaitingInput ? hint + " " + (this.awaitingInput.runtime.input_prompt || "") : this.text.error + " (" + (error.code || "connection_error") + ")";
    if (invalid && this.awaitingInput) this.activeInput().setAttribute("aria-invalid", "true");
  }
  async submit() {
    const activeInput = this.activeInput();
    const text = activeInput.value.trim();
    if (!text || this.busy || !this.client) return;
    const schema = this.awaitingInput?.runtime?.input_schema;
    const length = Array.from(text).length;
    if ((schema?.type === "string" && ((schema.min_length && length < schema.min_length) || (schema.max_length && length > schema.max_length))) || (!this.awaitingInput && length > 12000)) {
      this.showError({ code: "input_invalid", status: 422 });return;
    }
    const client = this.client, epoch = this.epoch;
    this.busy = true;this.form.setAttribute("aria-busy", "true");const send = this.shadowRoot.querySelector(".send");send.disabled = true;
    try {
      if (this.awaitingInput) {
        const { run, runtime } = this.awaitingInput;
        await client.supplyInput(run.run_id, runtime.input_field, text, run.revision);
      } else {
        if (!this.pendingSend || this.pendingSend.text !== text) this.pendingSend = { text, clientId: this.client.id() };
        await client.send(text, this.pendingSend);
      }
      const snapshot = await client.snapshot();
      if (epoch !== this.epoch || client !== this.client) return;
      this.pendingSend = null; activeInput.value = ""; this.feedback.textContent = ""; activeInput.removeAttribute("aria-invalid"); this.render(snapshot); this.activeInput().focus();
    } catch (error) { if (epoch === this.epoch) { this.showError(error); send.textContent = this.text.retry; } }
    finally { this.busy = false;send.disabled = false;this.form.removeAttribute("aria-busy"); }
  }
  button(label, key, action, primary = false) {
    const b = document.createElement("button");b.type = "button";b.textContent = label;b.dataset.focusKey = key;
    if (primary) b.className = "primary";
    b.addEventListener("click", async () => {
      b.disabled = true;
      const client = this.client, epoch = this.epoch;
      try { await action(); const snapshot = await client.snapshot(); if (epoch !== this.epoch || client !== this.client) return; this.feedback.textContent = ""; this.signature = null; this.render(snapshot); }
      catch (error) { if (epoch === this.epoch) { this.showError(error); b.disabled = false; } }
    });return b;
  }
  render(snapshot) {
    // Server telemetry timestamps/deadlines change on every poll. Only its
    // visible tool counter belongs in the render identity; replacing the log
    // for a clock tick invalidates approval buttons and text selections.
    const signature = JSON.stringify([snapshot.conversation?.id, snapshot.messages?.map(message => {
      if (!message.run) return message;
      const { telemetry, ...run } = message.run;
      return { ...message, run, toolCalls: telemetry?.budget?.tool_calls?.used };
    })]);
    if (signature === this.signature) return;
    if (this.conversationId && this.conversationId !== snapshot.conversation?.id) {
      this.pendingSend = null;this.input.value = "";if (this.choice) this.choice.value = "";if (this.dateInput) this.dateInput.value = "";this.inputRequestKey = null;
    }
    this.conversationId = snapshot.conversation?.id;
    this.signature = signature;this.awaitingInput = null;
    const focusKey = this.shadowRoot.activeElement?.dataset.focusKey;
    const follow = this.log.scrollHeight - this.log.scrollTop - this.log.clientHeight < 60;
    const scrollTop = this.log.scrollTop;
    this.log.replaceChildren();
    const t = this.text;const messages = snapshot.messages || [];
    if (!messages.length) {
      const empty = document.createElement("div");empty.className = "empty";
      const h = document.createElement("h3");h.textContent = this.getAttribute("empty-title") ?? t.empty;const p = document.createElement("p");p.textContent = this.getAttribute("empty-hint") ?? t.hint;empty.append(h, p);this.log.append(empty);
    }
    for (const message of messages) {
      const turn = document.createElement("article");turn.className = "turn";
      const user = document.createElement("p");user.className = "user";user.textContent = message.text;turn.append(user);
      for (const input of message.input_history || []) {
        if (input.prompt) { const prompt = document.createElement("div");prompt.className = "answer";prompt.textContent = input.prompt;turn.append(prompt); }
        const supplement = document.createElement("p");supplement.className = "user";supplement.textContent = input.text;turn.append(supplement);
      }
      const run = message.run;const runtime = run?.state?.runtime;
      const state = run?.status || message.status;
      const status = document.createElement("div");status.className = "status";status.dataset.state = state;
      const caption = document.createElement("span");caption.textContent = t.statuses[state] || t.statuses.active;status.append(caption);
      if (!["completed", "failed", "cancelled"].includes(message.status)) {
        const stop = this.button(t.stop, message.id + ":cancel", () => this.client.cancelMessage(message.id));stop.className = "text-button";status.append(stop);
      }
      if (run?.telemetry?.budget) {
        const detail = document.createElement("span");
        detail.textContent = (this.getAttribute("lang") === "en" ? "Tool calls: " : "业务调用：") + run.telemetry.budget.tool_calls.used;
        status.append(detail);
      }
      turn.append(status);
      const runId = run?.run_id || message.run_id;
      if (runId) {
        const client = this.client, epoch = this.epoch;
        const diagnostic = this.button(this.getAttribute("lang") === "en" ? "Inspect task" : "查看任务诊断", message.id + ":diagnostics", async () => {
          const result = await client.getRunDiagnostics(runId);
          if (epoch !== this.epoch || client !== this.client) return;
          this.diagnostics ||= new Map(); this.diagnostics.set(runId, result);
        });
        diagnostic.className = "text-button"; turn.append(diagnostic);
        const report = this.diagnostics?.get(runId);
        if (report && report.status === state && (!run || report.revision === run.revision)) {
          const summary = document.createElement("p"); summary.className = "notice";
          const seconds = (report.elapsed_ms / 1000).toFixed(1);
          const calls = report.budget?.tool_calls?.used ?? 0;
          summary.textContent = this.getAttribute("lang") === "en" ? `Elapsed: ${seconds}s · Tool calls: ${calls}` : `耗时：${seconds} 秒 · 业务调用：${calls}`;
          turn.append(summary);
          for (const finding of report.findings || []) {
            const p = document.createElement("p"); p.className = "notice";
            p.textContent = (finding.recovered ? (this.getAttribute("lang") === "en" ? "Recovered: " : "已恢复：") : "") + finding.message + (finding.actionable === false ? "" : " " + finding.next_action) + " (" + finding.code + ")"; turn.append(p);
          }
        }
      }
      // Execution receipts remain authoritative even if final answer generation
      // fails or the task stops. A queued submission is never rendered as success.
      const receipts = new Map((runtime?.invocation_receipts || []).map(receipt => [receipt.invocation_id, receipt]));
      for (const item of runtime?.pending || []) if (item.receipt) receipts.set(item.invocation_id, item.receipt);
      const outcomes = outcomeLabels[this.getAttribute("lang")] || outcomeLabels["zh-CN"];
      for (const receipt of receipts.values()) {
        if (receipt.effect !== "write") continue;
        const p = document.createElement("p");p.className = "notice action-outcome";
        const describe = typeof outcomes[receipt.status] === "function" ? outcomes[receipt.status] : outcomes.unknown;
        p.textContent = describe(receipt.capability);turn.append(p);
        if (receipt.result_error_code) { const detail = document.createElement("p");detail.className = "notice";detail.textContent = outcomes.unavailable;turn.append(detail); }
      }
      if (message.answer_markdown) { const answer = document.createElement("div");answer.className = "answer";appendAnswer(answer, message.answer_markdown);turn.append(answer); }
      for (const ref of message.result_refs || runtime?.result_refs || []) {
        const p = document.createElement("p");p.className = "notice";
        p.textContent = [ref.label || ref.entity_type || (this.getAttribute("lang") === "en" ? "Result" : "业务对象"), ref.id].join(": ");turn.append(p);
      }
      if (message.error_code) { const p = document.createElement("p");p.className = "notice";p.textContent = (Array.from(receipts.values()).some(receipt => receipt.effect === "write") ? outcomes.incomplete : t.error) + " (" + message.error_code + ")";turn.append(p); }
      if (state === "needs_input") {
        this.awaitingInput = { run, runtime, messageID: message.id };
        const p = document.createElement("p");p.id = "input-prompt-" + message.id;p.className = "notice";p.textContent = runtime.input_prompt;turn.append(p);
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
      const unknownItems = (runtime?.pending || []).filter(item => item.status === "unknown" || item.status === "in_flight" || item.poll_in_flight || (item.status === "waiting" && item.operation));
      if (state === "needs_reconciliation" || (["cancelled", "failed"].includes(state) && unknownItems.some(item => item.operation?.binding?.poll_capability !== "ui.command_status"))) {
        const p = document.createElement("p");p.className = "notice";p.textContent = ["cancelled", "failed"].includes(state) ? t.unknownStopped : t.unknown;turn.append(p);
        for (const item of unknownItems) {
          const browser = item.operation?.binding?.poll_capability === "ui.command_status";
          if (browser && state !== "needs_reconciliation") continue;
          turn.append(this.button(t.reconcile, item.invocation_id + ":reconcile", () => browser
            ? this.client.reconcile(item.invocation_id, run.revision)
            : this.client.reconcileInvocation(run.run_id, item, run.revision)));
        }
      }
      if (state === "needs_authorization") turn.append(this.button(this.getAttribute("lang") === "en" ? "Resume after restoring access" : "恢复权限后继续", message.id + ":resume", () => this.client.resumeRun(run.run_id)));
      this.log.append(turn);
    }
    const requestKey = this.awaitingInput ? [this.awaitingInput.run.run_id, this.awaitingInput.run.revision, this.awaitingInput.runtime.input_field].join(":") : null;
    if (requestKey !== this.inputRequestKey) {
      this.input.value = "";
      if (this.choice) this.choice.value = "";
      if (this.dateInput) this.dateInput.value = "";
      for (const control of [this.input, this.choice, this.dateInput]) control?.removeAttribute?.("aria-invalid");
      if (this.feedback) this.feedback.textContent = "";
      this.inputRequestKey = requestKey;
    }
    const label = this.shadowRoot.querySelector("label");
    label.textContent = this.awaitingInput ? t.input : (this.getAttribute("placeholder") ?? t.placeholder);
    const schema = this.awaitingInput?.runtime?.input_schema;
    if (this.choice && this.dateInput) {
      this.choice.hidden = schema?.type !== "enum";
      this.dateInput.hidden = schema?.type !== "date";
      this.input.hidden = !this.choice.hidden || !this.dateInput.hidden;
      this.choice.disabled = this.choice.hidden;
      this.dateInput.disabled = this.dateInput.hidden;
      this.input.disabled = this.input.hidden;
      if (!this.choice.hidden) {
        const selected = this.choice.value;
        const placeholder = document.createElement("option");placeholder.value = "";placeholder.textContent = t.choose;placeholder.disabled = true;
        this.choice.replaceChildren(placeholder, ...(schema.enum || []).map(value => { const option = document.createElement("option");option.value = value;option.textContent = value;return option; }));
        if ((schema.enum || []).includes(selected)) this.choice.value = selected;
        else this.choice.value = "";
      }
      const active = this.activeInput();
      label.setAttribute("for", active.id);
      active.setAttribute("aria-describedby", [this.awaitingInput ? "input-prompt-" + this.awaitingInput.messageID : "", "composer-feedback"].filter(Boolean).join(" "));
    }
    this.shadowRoot.querySelector(".send").textContent = this.awaitingInput ? t.send : messages.some(m => m.status === "active" || m.status === "creating") ? t.queue : t.send;
    this.log.scrollTop = follow ? this.log.scrollHeight : scrollTop;
    if (focusKey) for (const button of this.log.querySelectorAll("button")) if (button.dataset.focusKey === focusKey) button.focus({ preventScroll: true });
  }
}
if (globalThis.customElements && !customElements.get("agenstra-chat")) customElements.define("agenstra-chat", AgenstraChat);
export function mountAgenstraChat(container, { client, title, locale = "zh-CN", subtitle, emptyTitle, emptyHint, placeholder }) {
  const element = document.createElement("agenstra-chat");
  element.setAttribute("lang", locale);
  if (title) element.setAttribute("title", title);
  for (const [name, value] of Object.entries({ subtitle, "empty-title": emptyTitle, "empty-hint": emptyHint, placeholder })) {
    if (value !== undefined) element.setAttribute(name, value);
  }
  element.client = client;container.append(element);
  return { element, unmount: () => element.remove() };
}
