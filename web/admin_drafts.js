"use strict";
window.installDraftEditor = ({ api, feedback, names, formatDate, onPublish }) => {
  const $ = (id) => document.getElementById(id);
  const steps = ["basic", "connection", "capabilities", "rules", "skills", "review"];
  const state = { draft: null, dirty: false, skillDirty: false, rawDirty: false, capName: "", skillName: "", step: "basic" };
  const value = (id) => $(id).value.trim();
  const report = (message, kind = "info") => feedback(message, kind, "publish-feedback");
  const isMCP = () => state.draft?.manifest.schema === "agenstra.mcp-pack.v1";
  const capKey = () => isMCP() ? "tools" : "capabilities";
  const capabilities = () => Array.isArray(state.draft?.manifest[capKey()]) ? state.draft.manifest[capKey()] : [];
  function parse(id, label, array = false) {
    let result;
    try { result = JSON.parse($(id).value || (array ? "[]" : "{}")); }
    catch { throw new Error(`${label}不是有效的 JSON，请修正后保存。`); }
    if (!result || typeof result !== "object" || Array.isArray(result) !== array) throw new Error(`${label}必须是 JSON ${array ? "数组" : "对象"}。`);
    return result;
  }
  function option(value, text) { const node = document.createElement("option"); node.value = value; node.textContent = text; return node; }
  function dirty() { state.dirty = true; status(); }
  function status() {
    const d = state.draft;
    $("draft-status").textContent = d
      ? `${d.manifest.name || "未命名草稿"} · ${d.draft_id} · 修订 ${d.revision} · ${state.dirty ? "有未保存修改" : `已保存于 ${formatDate(d.updated_at)}`}`
      : "连接管理服务后创建草稿，每一步都可以保存。";
    $("draft-progress").textContent = d ? `${capabilities().length} 项能力 · ${state.dirty ? "待保存" : `${d.issues.length} 项待检查`}` : "尚未创建草稿";
  }
  async function action(fn) {
    const buttons = [...document.querySelectorAll("#publish button, #publish select, #publish input, #publish textarea")];
    const previous = buttons.map((node) => node.disabled);
    buttons.forEach((node) => { node.disabled = true; });
    try { await fn(); } catch (error) { report(error.message, "error"); }
    finally { buttons.forEach((node, i) => { node.disabled = previous[i]; }); }
  }
  function requireDraft() { if (!state.draft) throw new Error("请先创建或选择一份草稿。"); }
  const knownFields = () => isMCP()
    ? ["name", "effect", "skills", "contract_sha256", "replay", "reference_scope", "approval_required"]
    : ["name", "description", "method", "path", "input_schema", "output_schema", "effect", "skills", "timeout_seconds", "idempotency_header", "approval_required"];
  function flushCapability() {
    const item = capabilities().find((entry) => entry.name === state.capName);
    if (!item) return;
    const name = value("cap-name");
    if (!name) throw new Error("请填写能力名称，或移除此能力。");
    if (capabilities().some((entry) => entry !== item && entry.name === name)) throw new Error(`能力名称 ${name} 已存在，请使用其他名称。`);
    const extra = parse("cap-extra", "补充执行规则");
    if (Object.keys(extra).some((key) => knownFields().includes(key))) throw new Error("补充规则包含表单已有字段，请在对应表单中修改。");
    for (const key of Object.keys(item)) if (!knownFields().includes(key)) delete item[key];
    Object.assign(item, extra, { name, effect: value("cap-effect"), approval_required: $("cap-approval").checked, skills: names(value("cap-skills")) });
    if (isMCP()) Object.assign(item, { contract_sha256: value("cap-hash"), replay: value("cap-replay"), reference_scope: value("cap-reference") });
    else {
      Object.assign(item, { description: value("cap-description"), method: value("cap-method"), path: value("cap-path"), input_schema: parse("cap-input", "输入契约"), output_schema: parse("cap-output", "输出契约"), timeout_seconds: Number(value("cap-timeout")) });
      if (value("cap-idempotency")) item.idempotency_header = value("cap-idempotency"); else delete item.idempotency_header;
    }
    state.capName = name;
  }
  function collect() {
    requireDraft();
    if (state.rawDirty) throw new Error("完整 JSON 有未应用的修改，请先点击“应用 JSON 到草稿”。");
    const m = state.draft.manifest;
    m.name = value("pack-id"); m.version = value("pack-version"); m.guidance = value("pack-guidance");
    if (isMCP()) {
      const source = { ...(m.source || {}), transport: value("mcp-transport"), timeout_seconds: Number(value("mcp-timeout")) };
      if (source.transport === "stdio") {
        delete source.url_env; delete source.token_env;
        source.command = value("mcp-command"); source.args = parse("mcp-args", "命令参数", true); source.environment = parse("mcp-environment", "进程环境映射");
        if (value("mcp-cwd")) source.cwd_env = value("mcp-cwd"); else delete source.cwd_env;
      } else {
        for (const key of ["command", "args", "environment", "cwd_env"]) delete source[key];
        source.url_env = value("mcp-url-env"); if (value("mcp-token-env")) source.token_env = value("mcp-token-env"); else delete source.token_env;
      }
      m.source = source;
    } else {
      m.base_url_env = value("rest-url-env"); m.headers_env = parse("rest-headers", "请求头变量映射");
      if (value("rest-token-env")) m.token_env = value("rest-token-env"); else delete m.token_env;
    }
    flushCapability();
    return m;
  }
  function fillCapability() {
    const items = capabilities();
    if (!items.some((entry) => entry.name === state.capName)) state.capName = items[0]?.name || "";
    for (const id of ["capability-list", "rules-capability"]) {
      $(id).replaceChildren(...(items.length ? items.map((item) => option(item.name, item.name)) : [option("", "先添加能力")]));
      $(id).value = state.capName;
    }
    const item = items.find((entry) => entry.name === state.capName);
    $("capability-fields").hidden = !item; $("rules-fields").hidden = !item;
    if (!item) return;
    const fields = { "cap-name": item.name, "cap-description": item.description, "cap-method": item.method || "GET", "cap-path": item.path, "cap-hash": item.contract_sha256, "cap-effect": item.effect || "read", "cap-timeout": item.timeout_seconds ?? 20, "cap-idempotency": item.idempotency_header, "cap-replay": item.replay || "never", "cap-reference": item.reference_scope || "durable", "cap-skills": (Array.isArray(item.skills) ? item.skills : []).join(", ") };
    for (const [id, v] of Object.entries(fields)) $(id).value = v ?? "";
    $("cap-approval").checked = Boolean(item.approval_required);
    $("cap-input").value = JSON.stringify(item.input_schema || {}, null, 2); $("cap-output").value = JSON.stringify(item.output_schema || {}, null, 2);
    $("cap-extra").value = JSON.stringify(Object.fromEntries(Object.entries(item).filter(([key]) => !knownFields().includes(key))), null, 2);
  }
  function fillSkill() {
    const items = state.draft?.manifest.skills || [];
    $("skill-list").replaceChildren(option("", "添加新说明"), ...items.map((item) => option(item.name, item.name)));
    $("skill-list").value = state.skillName;
    const item = items.find((entry) => entry.name === state.skillName);
    $("skill-name").value = item?.name || ""; $("skill-path").value = item?.path || ""; $("skill-description").value = item?.description || ""; $("skill-content").value = state.draft?.skills[item?.path] || "";
    state.skillDirty = false;
  }
  async function flushSkill() {
    if (!state.skillDirty) return;
    requireDraft();
    const name = value("skill-name");
    if (!name) throw new Error("请填写技能名称，或清空未完成的说明。");
    const items = state.draft.manifest.skills || [];
    const existing = items.find((entry) => entry.name === state.skillName);
    if (items.some((entry) => entry !== existing && entry.name === name)) throw new Error(`技能 ${name} 已存在，请从列表选择后编辑。`);
    const path = value("skill-path") || `skills/${name}/SKILL.md`;
    const content = $("skill-content").value;
    const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(content));
    const sha256 = [...new Uint8Array(hash)].map((v) => v.toString(16).padStart(2, "0")).join("");
    const entry = { name, path, description: value("skill-description"), sha256 };
    if (existing) { delete state.draft.skills[existing.path]; items.splice(items.indexOf(existing), 1, entry); } else items.push(entry);
    state.draft.manifest.skills = items; state.draft.skills[path] = content; state.skillName = name;
    dirty(); fillSkill();
  }
  function showStep(step) {
    state.step = step;
    document.querySelectorAll("[data-panel]").forEach((node) => { node.hidden = node.dataset.panel !== step; });
    document.querySelectorAll("[data-step]").forEach((node) => { if (node.dataset.step === step) node.setAttribute("aria-current", "step"); else node.removeAttribute("aria-current"); });
    $("next-step").hidden = step === "review";
    if (step === "review" && state.draft && !state.rawDirty) $("manifest-editor").value = JSON.stringify(state.draft.manifest, null, 2);
  }
  function renderIssues() {
    const list = $("draft-issues"); list.replaceChildren();
    const issues = state.draft?.issues || [];
    document.querySelectorAll("[data-step]").forEach((node, index) => {
      const labels = ["基本信息", "服务连接", "能力与契约", "执行规则", "使用说明", "检查与发布"];
      const count = issues.filter((issue) => issue.section === node.dataset.step).length;
      node.textContent = `${index + 1}. ${labels[index]}${state.draft && count ? ` · ${count} 项待补充` : ""}`;
    });
    if (!issues.length && state.draft) { const node = document.createElement("li"); node.textContent = "清单校验通过。发布前仍需审查业务语义，并在绑定后验证真实连接和任务。"; list.append(node); }
    for (const issue of issues) {
      const node = document.createElement("li"); const link = document.createElement("button"); link.type = "button"; link.textContent = `${issue.path}：${issue.message}`;
      link.addEventListener("click", () => {
        if (issue.section === "capabilities") { const index = Number(issue.path.match(/\[(\d+)\]/)?.[1]); if (capabilities()[index]) { state.capName = capabilities()[index].name; fillCapability(); } }
        showStep(issue.section);
        if (issue.section === "review") $("manifest-editor").closest("details").open = true;
        const fields = { name: "pack-id", version: "pack-version", guidance: "pack-guidance", base_url_env: "rest-url-env", "source.transport": "mcp-transport", "source.command": "mcp-command", "source.url_env": "mcp-url-env" };
        const capabilityField = issue.path.match(/\]\.(\w+)/)?.[1];
        const capabilityFields = {name:"cap-name",description:"cap-description",path:"cap-path",input_schema:"cap-input",output_schema:"cap-output",contract_sha256:"cap-hash",skills:"cap-skills",effect:"cap-effect",timeout_seconds:"cap-timeout",replay:"cap-replay",reference_scope:"cap-reference"};
        $(fields[issue.path] || capabilityFields[capabilityField] || "publish-title").focus();
      }); node.append(link); list.append(node);
    }
  }
  function fill() {
    const m = state.draft.manifest;
    $("pack-id").value = m.name || ""; $("pack-version").value = m.version || ""; $("pack-guidance").value = m.guidance || "";
    $("rest-url-env").value = m.base_url_env || ""; $("rest-token-env").value = m.token_env || ""; $("rest-headers").value = JSON.stringify(m.headers_env || {}, null, 2);
    const source = m.source || {};
    for (const [id, v] of Object.entries({ "mcp-transport": source.transport || "streamable_http", "mcp-url-env": source.url_env, "mcp-token-env": source.token_env, "mcp-command": source.command, "mcp-cwd": source.cwd_env, "mcp-timeout": source.timeout_seconds ?? 60 })) $(id).value = v ?? "";
    $("mcp-args").value = JSON.stringify(source.args || [], null, 2); $("mcp-environment").value = JSON.stringify(source.environment || {}, null, 2);
    document.querySelectorAll("[data-rest]").forEach((node) => { node.hidden = isMCP(); }); document.querySelectorAll("[data-mcp]").forEach((node) => { node.hidden = !isMCP(); });
    transportView(); fillCapability(); fillSkill(); renderIssues(); showStep(state.step); status();
  }
  function transportView() { $("mcp-http").hidden = value("mcp-transport") === "stdio"; $("mcp-stdio").hidden = value("mcp-transport") !== "stdio"; }
  async function refreshList() {
    const list = await api("/admin/api/drafts");
    $("draft-list").replaceChildren(option("", "选择草稿"), ...list.map((d) => option(d.draft_id, `${d.name || "未命名"} · ${d.version || "未设版本"} · 修订 ${d.revision}`)));
    $("draft-list").value = state.draft?.draft_id || "";
  }
  async function save() {
    collect(); await flushSkill();
    const d = state.draft;
    state.draft = await api(`/admin/api/drafts/${encodeURIComponent(d.draft_id)}`, { method: "PUT", body: { expected_revision: d.revision, manifest: d.manifest, skills: d.skills } });
    state.dirty = false; state.rawDirty = false; fill(); await refreshList();
    report(`草稿已保存（修订 ${state.draft.revision}）。${state.draft.issues.length ? "可继续补充，检查步骤列出了待完成项。" : "完整校验通过，可以发布。"}`, "success");
  }
  async function load(id) {
    if (!id) return;
    if (state.dirty && !window.confirm("有未保存修改。重新读取会丢弃这些修改，确认继续？")) { $("draft-list").value = state.draft?.draft_id || ""; return; }
    state.draft = await api(`/admin/api/drafts/${encodeURIComponent(id)}`); state.dirty = false; state.rawDirty = false; state.capName = ""; state.skillName = ""; fill(); await refreshList(); report("已读取保存的草稿，可以继续编辑。", "success");
  }
  async function merge(section, data) {
    await save(); const d = state.draft;
    state.draft = await api(`/admin/api/drafts/${encodeURIComponent(d.draft_id)}`, { method: "PATCH", body: { expected_revision: d.revision, section, conflict: value("import-conflict"), ...data } });
    state.dirty = false; fill(); await refreshList(); report("本批内容已合并并保存；连接、版本和已有执行规则按所选策略保留。请检查新能力。", "success");
  }
  async function manifestImport() {
    const file = $("manifest-file").files[0]; if (!file) throw new Error("请先选择能力包清单。");
    const raw = JSON.parse(await file.text()); const manifest = raw.manifest || raw; const skills = raw.skills && raw.manifest ? { ...raw.skills } : {};
    for (const entry of manifest.skills || []) {
      if (skills[entry.path] !== undefined) continue;
      const matches = [...$("skill-files").files].filter((file) => { const p = file.webkitRelativePath || file.name; return p === entry.path || p.endsWith(`/${entry.path}`); });
      if (matches.length !== 1) throw new Error(`请选择清单中对应的技能文件：${entry.path}`);
      skills[entry.path] = await matches[0].text();
    }
    return { value: manifest, skills };
  }
  function preview(manifest) {
    const existing = new Set(capabilities().map((item) => item.name));
    const incoming = manifest[capKey()] || []; const conflicts = incoming.filter((item) => existing.has(item.name)).map((item) => item.name);
    const existingSkills = new Set((state.draft?.manifest.skills || []).map((item) => item.name));
    conflicts.push(...(manifest.skills || []).filter((item) => existingSkills.has(item.name)).map((item) => `技能 ${item.name}`));
    $("import-preview").textContent = `本次 ${incoming.length} 项能力。${conflicts.length ? `同名冲突：${conflicts.join("、")}。请确认处理策略。` : "没有同名冲突。"}`;
  }
  $("new-draft").addEventListener("click", () => action(async () => {
    if (state.dirty && !window.confirm("创建新草稿会离开当前未保存的修改，确认继续？")) return;
    const mcp = value("draft-type") === "mcp";
    state.draft = await api(`/admin/api/drafts/draft-${crypto.randomUUID()}`, { method: "PUT", body: { expected_revision: 0, manifest: { schema: mcp ? "agenstra.mcp-pack.v1" : "agenstra.rest-pack.v2", name: "", version: "1.0.0", guidance: "", [mcp ? "tools" : "capabilities"]: [] }, skills: {} } });
    state.dirty = false; state.rawDirty = false; state.capName = ""; state.skillName = ""; state.step = "basic"; fill(); await refreshList(); report("已创建草稿。先填写基本信息，也可以直接保存后下次继续。", "success");
  }));
  $("draft-list").addEventListener("change", (event) => action(() => load(event.target.value)));
  $("reload-draft").addEventListener("click", () => action(() => load(value("draft-list"))));
  document.querySelectorAll("[data-step]").forEach((node) => node.addEventListener("click", () => action(async () => { if (state.draft) { collect(); await flushSkill(); } showStep(node.dataset.step); })));
  $("save-draft").addEventListener("click", () => action(save));
  $("next-step").addEventListener("click", () => action(async () => { await save(); showStep(steps[steps.indexOf(state.step) + 1]); }));
  $("mcp-transport").addEventListener("change", transportView);
  for (const id of ["capability-list", "rules-capability"]) $(id).addEventListener("change", (event) => action(async () => { const target = event.target.value; try { flushCapability(); } catch (err) { $(id).value = state.capName; throw err; } state.capName = target; fillCapability(); }));
  $("add-capability").addEventListener("click", () => action(async () => {
    requireDraft(); flushCapability(); let i = 1; while (capabilities().some((entry) => entry.name === `capability.new${i}`)) i++;
    const item = { name: `capability.new${i}`, effect: "read", skills: [] };
    if (isMCP()) Object.assign(item, { contract_sha256: "", replay: "never", reference_scope: "durable" });
    else Object.assign(item, { description: "", method: "GET", path: "/", input_schema: { type: "object", properties: {}, additionalProperties: false }, output_schema: { type: "object", properties: {}, additionalProperties: false } });
    state.draft.manifest[capKey()] = [...capabilities(), item]; state.capName = item.name; dirty(); fillCapability(); $("cap-name").focus();
  }));
  $("remove-capability").addEventListener("click", () => action(async () => {
    requireDraft(); if (!state.capName || !window.confirm(`从草稿中移除 ${state.capName}？`)) return;
    state.draft.manifest[capKey()] = capabilities().filter((item) => item.name !== state.capName); state.capName = ""; dirty(); fillCapability();
  }));
  $("skill-list").addEventListener("change", (event) => action(async () => { const target = event.target.value; await flushSkill(); state.skillName = target; fillSkill(); }));
  $("apply-skill").addEventListener("click", () => action(async () => { await flushSkill(); report("使用说明已加入当前编辑内容，请保存草稿。", "success"); }));
  $("remove-skill").addEventListener("click", () => action(async () => {
    requireDraft(); const item = (state.draft.manifest.skills || []).find((item) => item.name === state.skillName);
    if (!item || !window.confirm(`从草稿移除 ${item.name}？请同时检查能力中的关联。`)) return;
    state.draft.manifest.skills = state.draft.manifest.skills.filter((entry) => entry !== item); delete state.draft.skills[item.path]; state.skillName = ""; dirty(); fillSkill();
  }));
  $("draft-editor").addEventListener("input", (event) => {
    if (!state.draft) return;
    if (["skill-name", "skill-path", "skill-description", "skill-content"].includes(event.target.id)) state.skillDirty = true;
    if (event.target.id === "manifest-editor") state.rawDirty = true;
    if (!["manifest-file", "skill-files", "openapi-file"].includes(event.target.id)) dirty();
  });
  $("manifest-file").addEventListener("change", () => action(async () => { requireDraft(); const file = $("manifest-file").files[0]; if (!file) return; const raw = JSON.parse(await file.text()); preview(raw.manifest || raw); }));
  $("import-manifest").addEventListener("click", () => action(async () => { requireDraft(); const data = await manifestImport(); preview(data.value); await merge("import", data); }));
  let spec = null;
  $("openapi-file").addEventListener("change", () => action(async () => {
    spec = null; const file = $("openapi-file").files[0]; const list = $("openapi-operations"); list.replaceChildren(); if (!file) return;
    spec = JSON.parse(await file.text());
    for (const [path, methods] of Object.entries(spec.paths || {})) for (const [method, op] of Object.entries(methods)) {
      if (!op?.operationId || !["get", "post", "put", "patch", "delete"].includes(method)) continue;
      const label = document.createElement("label"); label.className = "operation-item"; const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.value = op.operationId;
      const text = document.createElement("span"); text.textContent = `${op.operationId} · ${method.toUpperCase()} ${path}${capabilities().some((item) => item.name === op.operationId) ? " · 同名冲突" : ""}`; label.append(checkbox, text); list.append(label);
    }
    if (!list.children.length) list.textContent = "未找到带 operationId 的受支持操作。";
  }));
  $("generate-draft").addEventListener("click", () => action(async () => {
    requireDraft(); const operations = [...$("openapi-operations").querySelectorAll("input:checked")].map((item) => item.value);
    if (!spec || !operations.length) throw new Error("请选择 OpenAPI 文档，并勾选本次要导入的操作。");
    await merge("openapi", { value: { spec, operations } });
  }));
  $("validate").addEventListener("click", () => action(async () => { await save(); renderIssues(); showStep("review"); }));
  $("publish-draft").addEventListener("click", () => action(async () => {
    await save(); if (state.draft.issues.length) { renderIssues(); throw new Error("请先处理列出的待完成项，再发布。"); }
    const result = await api(`/admin/api/drafts/${encodeURIComponent(state.draft.draft_id)}/publish`, { method: "POST", body: { expected_revision: state.draft.revision } });
    await onPublish(); report(`已发布 ${result.pack_id} ${result.version}。请在版本目录启用，再绑定用户权限。`, "success");
  }));
  $("apply-json").addEventListener("click", () => action(async () => {
    requireDraft(); const manifest = parse("manifest-editor", "完整清单");
    if (!["agenstra.rest-pack.v2", "agenstra.mcp-pack.v1"].includes(manifest.schema)) throw new Error("清单类型必须是 REST v2 或 MCP v1。");
    const key = manifest.schema === "agenstra.mcp-pack.v1" ? "tools" : "capabilities";
    if (manifest[key] !== undefined && (!Array.isArray(manifest[key]) || manifest[key].some((item) => !item || typeof item !== "object" || Array.isArray(item) || typeof item.name !== "string" || !item.name))) throw new Error("能力列表必须是数组，且每项有一个名称。");
    if (manifest.skills !== undefined && (!Array.isArray(manifest.skills) || manifest.skills.some((item) => !item || typeof item !== "object" || !item.name || !item.path))) throw new Error("技能列表必须是数组，且每项有名称和路径。");
    state.draft.manifest = manifest; state.rawDirty = false; state.capName = ""; dirty(); fill(); report("JSON 已应用到编辑内容，请保存草稿。", "success");
  }));
  $("export-draft").addEventListener("click", () => action(async () => {
    collect(); await flushSkill();
    const data = { manifest: state.draft.manifest, skills: state.draft.skills };
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })); const a = document.createElement("a"); a.href = url; a.download = "capability-draft.json"; document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
    report("已导出清单与技能正文；可通过分批导入入口合并到草稿。", "success");
  }));
  window.addEventListener("beforeunload", (event) => { if (state.dirty) { event.preventDefault(); event.returnValue = ""; } });
  return { refreshList };
};
