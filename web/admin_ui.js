(() => {
  "use strict";

  const state = { token: "", overview: null, openapiSpec: null };
  const $ = (id) => document.getElementById(id);
  const messages = {
    model_revision_conflict: "模型配置已被其他管理员更新。请重新读取后合并修改。",
    model_configuration_invalid: "模型配置不完整，请检查名称、模型 ID 和凭据引用。",
    model_selection_invalid: "所选模型配置不存在，请检查默认模型与用途选择。",
    model_endpoint_invalid: "模型地址无效，请填写 HTTP(S) 基础地址，或检查地址环境变量。",
    model_parameters_invalid: "模型参数超出支持范围，请检查限制与数值。",
    model_parameters_unsupported: "所选 API 不支持这组推理或温度参数，请调整参数后验证。",
    model_credentials_unavailable: "服务端无法读取模型凭据，请检查环境变量或 secret 文件引用。",
    model_prices_invalid: "价格必须是非负有限数值。",
    model_management_unavailable: "当前服务使用自定义模型，请使用部署的模型模块启用选择功能。",
    draft_not_found: "草稿不存在，请刷新草稿列表。",
    draft_revision_conflict: "草稿已在其他页面或 CLI 更新。当前修改仍在页面中；请导出后重新读取并合并。",
    draft_item_conflict: "存在同名能力或技能。请检查导入预览，并明确选择保留或替换。",
    draft_incomplete: "草稿尚未完整，请根据检查结果补齐后发布。",
    invalid_draft_field: "这个字段不属于所选配置步骤。",
    unsupported_pack_schema: "请选择与草稿类型一致的 REST v2 或 MCP v1 清单。",
    admin_unauthorized: "管理员密钥无效。请检查密钥后重新连接。",
    invalid_capability_pack: "能力包契约未通过校验，请检查清单和技能文件。",
    skill_files_mismatch: "技能文件与清单列出的路径不一致。",
    skill_digest_mismatch: "技能文件内容与清单中的 SHA-256 不一致。",
    invalid_skill_path: "技能路径无效；请使用能力包内的相对路径。",
    skill_path_conflict: "技能文件路径相互冲突；文件不能同时作为目录使用。",
    version_already_published: "这个版本已发布过不同内容。请使用新版本号。",
    release_path_conflict: "已发布版本目录存在冲突，请检查服务器上的发布文件。",
    release_not_active: "请先启用一个版本，再配置用户连接。",
    binding_capability_missing: "授权列表包含目标版本中不存在的能力。",
    invalid_environment_ref: "环境变量映射只能引用大写变量名或已配置的 secret:NAME 文件。",
    connection_unavailable: "连接引用的地址或密钥不可读取，请检查服务端配置。",
    binding_contains_credentials: "连接指纹变量中包含凭据；请移除该变量。",
    revision_conflict: "启用版本已被其他管理员修改。请刷新后重试。",
    release_tampered: "已发布文件的内容发生变化，系统已拒绝使用。",
    openapi_import_failed: "OpenAPI 草稿生成失败。请检查文档和所选操作。",
    mcp_discovery_source_invalid: "MCP 连接配置不完整，请检查连接方式、地址变量和超时。",
    mcp_discovery_failed: "读取 MCP 工具失败。请检查服务地址、凭据和工具列表协议。",
  };

  function feedback(message, kind = "info", location = "feedback") {
    const target = $(location);
    target.textContent = message;
    target.dataset.kind = kind;
  }

  async function api(path, options = {}) {
    const headers = { Authorization: `Bearer ${state.token}` };
    if (options.body !== undefined) headers["Content-Type"] = "application/json";
    const response = await fetch(path, {
      method: options.method || "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      cache: "no-store",
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      if (response.status === 401) {
        state.token = "";
        $("connection-state").textContent = "未连接";
        $("connection-state").classList.remove("connected");
      }
      const code = data.detail?.code || data.code || `HTTP ${response.status}`;
      throw new Error(messages[code] || data.detail?.message || `操作失败：${code}`);
    }
    return data;
  }

  function cell(text, className = "") {
    const element = document.createElement("td");
    element.textContent = String(text);
    if (className) element.className = className;
    return element;
  }

  function empty(container, text) {
    const paragraph = document.createElement("p");
    paragraph.className = "empty";
    paragraph.textContent = text;
    container.replaceChildren(paragraph);
  }

  function formatDate(seconds) {
    return new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false });
  }

  function renderReleases() {
    const rows = $("release-rows");
    const releases = state.overview.releases;
    rows.replaceChildren();
    if (!releases.length) {
      const row = document.createElement("tr");
      const item = cell("尚无已发布版本。请从清单开始发布第一个能力包。", "empty");
      item.colSpan = 6;
      row.append(item);
      rows.append(row);
      return;
    }
    for (const release of releases) {
      const row = document.createElement("tr");
      row.append(cell(release.pack_id, "strong"));
      row.append(cell(release.version));
      row.append(cell(release.capabilities.length));
      const hash = cell(release.digest.slice(0, 12), "hash");
      hash.title = release.digest;
      row.append(hash);
      const status = cell(release.active ? "当前启用" : "已发布");
      status.className = release.active ? "status active" : "status";
      row.append(status);
      const action = document.createElement("td");
      if (release.active) {
        action.textContent = "—";
      } else {
        const button = document.createElement("button");
        button.className = "button small secondary";
        button.type = "button";
        button.textContent = "启用此版本";
        button.addEventListener("click", async () => {
          button.disabled = true;
          feedback(`正在启用 ${release.pack_id} ${release.version}…`, "info", "release-feedback");
          try {
            await api(`/admin/api/packs/${encodeURIComponent(release.pack_id)}/activate`, {
              method: "POST",
              body: { digest: release.digest, expected_revision: release.revision },
            });
            await refresh();
            feedback(`已启用 ${release.pack_id} ${release.version}，新任务将使用此版本。`, "success", "release-feedback");
          } catch (error) {
            feedback(error.message, "error", "release-feedback");
          } finally {
            button.disabled = false;
          }
        });
        action.append(button);
      }
      row.append(action);
      rows.append(row);
    }
  }

  function option(value, label) {
    const element = document.createElement("option");
    element.value = value;
    element.textContent = label;
    return element;
  }

  function renderBindings() {
    const owners = $("binding-owner");
    const packs = $("binding-pack");
    const previousOwner = owners.value;
    const previousPack = packs.value;
    owners.replaceChildren(option("", "选择用户"));
    for (const owner of state.overview.users) owners.append(option(owner, owner));
    packs.replaceChildren(option("", "选择已启用能力包"));
    const active = state.overview.releases.filter((item) => item.active);
    for (const release of active) packs.append(option(release.pack_id, release.pack_id));
    owners.value = previousOwner;
    packs.value = previousPack;

    const list = $("binding-list");
    list.replaceChildren();
    const bindings = state.overview.bindings;
    if (!bindings.length) {
      empty(list, "尚无连接授权。启用版本后，为用户绑定能力和环境变量引用。");
    } else {
      for (const binding of bindings) {
        const item = document.createElement("button");
        item.type = "button";
        item.className = "binding-item";
        const title = document.createElement("strong");
        title.textContent = `${binding.owner_id} · ${binding.pack_id}`;
        const caption = document.createElement("span");
        caption.textContent = binding.enabled
          ? `${binding.config.granted_capabilities.length} 项授权能力 · 更新于 ${formatDate(binding.updated_at)}`
          : "已停用";
        item.append(title, caption);
        item.addEventListener("click", () => {
          owners.value = binding.owner_id;
          packs.value = binding.pack_id;
          fillBinding();
          $("binding-form").scrollIntoView({ behavior: "smooth", block: "center" });
        });
        list.append(item);
      }
    }
    fillBinding();
  }

  function fillBinding() {
    const matching = state.overview?.bindings.find(
      (item) => item.owner_id === $("binding-owner").value && item.pack_id === $("binding-pack").value
    );
    const config = matching?.config;
    $("binding-environment").value = JSON.stringify(config?.environment || {}, null, 2);
    $("binding-grants").value = (config?.granted_capabilities || []).join(", ");
    $("binding-approvals").value = (config?.approval_capabilities || []).join(", ");
    $("binding-model-data").checked = Boolean(config?.allow_model_data);
    $("binding-identity-env").value = (config?.binding_environment || []).join(", ");
    $("binding-delegations").value = JSON.stringify(config?.delegations || {}, null, 2);
    $("binding-identity").value = config?.identity ? JSON.stringify(config.identity, null, 2) : "";
    $("disable-binding").disabled = !matching?.enabled;
    $("check-binding").disabled = !matching?.enabled;
  }

  function renderAudit() {
    const list = $("audit-list");
    list.replaceChildren();
    if (!state.overview.audit.length) {
      empty(list, "发布和授权操作会显示在这里。");
      return;
    }
    const labels = { publish: "发布版本", activate: "启用版本", bind: "保存连接", disable_binding: "停用连接", save_draft: "保存草稿" };
    for (const event of state.overview.audit) {
      const row = document.createElement("div");
      row.className = "audit-row";
      const time = document.createElement("time");
      time.textContent = formatDate(event.created_at);
      const action = document.createElement("strong");
      action.textContent = labels[event.action] || event.action;
      const pack = document.createElement("span");
      pack.textContent = event.pack_id;
      row.append(time, action, pack);
      list.append(row);
    }
  }

  async function refresh() {
    if (!state.token) {
      feedback("请先输入管理员密钥。", "error");
      return;
    }
    try {
      state.overview = await api("/admin/api/overview");
    } catch (error) {
      $("connection-state").textContent = "未连接";
      $("connection-state").classList.remove("connected");
      throw error;
    }
    renderReleases();
    renderBindings();
    renderAudit();
    await drafts.refreshList();
    await models.refresh();
    $("connection-state").textContent = "已连接";
    $("connection-state").classList.add("connected");
  }

  function names(value) {
    return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))];
  }

  $("connect-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    state.token = $("admin-key").value;
    try {
      await refresh();
      $("admin-key").value = "";
      feedback("已连接管理服务。", "success");
    } catch (error) {
      state.token = "";
      feedback(error.message, "error");
    }
  });

  $("refresh").addEventListener("click", async () => {
    try { await refresh(); feedback("目录已刷新。", "success"); }
    catch (error) { feedback(error.message, "error"); }
  });

  $("binding-owner").addEventListener("change", fillBinding);
  $("binding-pack").addEventListener("change", fillBinding);
  $("binding-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    try {
      let environment;
      try { environment = JSON.parse($("binding-environment").value); }
      catch { throw new Error("环境变量映射不是有效的 JSON。"); }
      if (!environment || Array.isArray(environment) || typeof environment !== "object") {
        throw new Error("环境变量映射必须是 JSON 对象。");
      }
      const owner = $("binding-owner").value;
      const pack = $("binding-pack").value;
      await api(`/admin/api/bindings/${encodeURIComponent(owner)}/${encodeURIComponent(pack)}`, {
        method: "PUT",
        body: {
          environment,
          granted_capabilities: names($("binding-grants").value),
          approval_capabilities: names($("binding-approvals").value),
          allow_model_data: $("binding-model-data").checked,
          binding_environment: names($("binding-identity-env").value),
          delegations: JSON.parse($("binding-delegations").value || "{}"),
          identity: $("binding-identity").value.trim() ? JSON.parse($("binding-identity").value) : null,
        },
      });
      await refresh();
      feedback(`已保存 ${owner} 对 ${pack} 的连接与授权。`, "success", "binding-feedback");
    } catch (error) { feedback(error.message, "error", "binding-feedback"); }
  });

  $("disable-binding").addEventListener("click", async () => {
    const owner = $("binding-owner").value;
    const pack = $("binding-pack").value;
    if (!owner || !pack || !window.confirm(`停用 ${owner} 对 ${pack} 的连接？现有任务也将失去访问权限。`)) return;
    try {
      await api(`/admin/api/bindings/${encodeURIComponent(owner)}/${encodeURIComponent(pack)}`, { method: "DELETE" });
      await refresh();
      feedback(`已停用 ${owner} 对 ${pack} 的连接。`, "success", "binding-feedback");
    } catch (error) { feedback(error.message, "error", "binding-feedback"); }
  });

  $("check-binding").addEventListener("click", async () => {
    const owner = $("binding-owner").value;
    const pack = $("binding-pack").value;
    if (!owner || !pack) return;
    try {
      const result = await api(`/admin/api/bindings/${encodeURIComponent(owner)}/${encodeURIComponent(pack)}/check`, { method: "POST" });
      feedback(`契约与连接配置可加载，发现 ${result.capabilities.length} 项能力和 ${result.skills.length} 份使用说明。请用代表性任务验证实际业务接口。`, "success", "binding-feedback");
    } catch (error) { feedback(error.message, "error", "binding-feedback"); }
  });
  const drafts = window.installDraftEditor({ api, feedback, names, formatDate, onPublish: refresh, getOverview: () => state.overview });
  const models = window.installModelEditor({ api, feedback });

  let diagnosticBusy = false;
  let diagnosticCreate = null;
  async function userRequest(path, body) {
    const key = $("diagnostic-key").value.trim();
    if (!key) throw new Error("请填写接入用户的 API key。");
    const response = await fetch(path, { method: body === undefined ? "GET" : "POST", headers: { Authorization: `Bearer ${key}`, ...(body === undefined ? {} : { "Content-Type": "application/json" }) }, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store", redirect: "error" });
    const data = await response.json();
    if (!response.ok) throw new Error(messages[data.detail?.code || data.code] || `用户请求未完成：${data.detail?.code || data.code || response.status}`);
    return data;
  }
  async function inspectRun() {
    const id = $("diagnostic-run").value.trim();
    if (!id) throw new Error("请填写任务 ID，或先创建一条试运行任务。");
    const path = `/runs/${encodeURIComponent(id)}`;
    const run = await userRequest(path);
    const report = await userRequest(path + "/diagnostics");
    const result = $("diagnostic-result"); result.replaceChildren();
    const heading = document.createElement("h3"); heading.textContent = `任务 ${report.run_id} · ${report.status}`; result.append(heading);
    const metrics = document.createElement("p"); metrics.textContent = `耗时 ${(report.elapsed_ms / 1000).toFixed(1)} 秒 · 业务调用 ${report.budget.tool_calls.used} 次 · 模型预算计入 ${report.budget.tokens.charged_tokens} tokens`; result.append(metrics);
    models.renderUsage(result, report.budget.usage_by_purpose, report.budget.usage);
    for (const finding of report.findings) {
      const item = document.createElement("div"); item.className = "diagnostic-finding";
      const title = document.createElement("strong"); title.textContent = finding.message;
      const next = document.createElement("p"); next.textContent = finding.next_action;
      const code = document.createElement("code"); code.textContent = `${finding.category} · ${finding.code}${finding.capability ? " · " + finding.capability : ""}`;
      item.append(title, next, code); result.append(item);
    }
    const runtime = run.state.runtime;
    if (runtime.answer_markdown) { const answer = document.createElement("pre"); answer.textContent = runtime.answer_markdown; result.append(answer); }
    async function perform(path, body) {
      await diagnosticAction(async () => { await userRequest(path, body); await inspectRun(); });
    }
    function button(label, path, body) {
      const node = document.createElement("button"); node.type = "button"; node.className = "button secondary"; node.textContent = label;
      node.addEventListener("click", () => perform(path, body)); result.append(node);
    }
    if (run.status === "needs_approval") for (const item of runtime.pending || []) if (item.status === "needs_approval") {
      const args = document.createElement("pre"); args.textContent = item.call.capability + "\n" + JSON.stringify(item.call.arguments, null, 2); result.append(args);
      const body = { invocation_id: item.invocation_id, arguments_sha256: item.arguments_sha256, revision: run.revision };
      button("批准此操作", path + "/approval", { ...body, approved: true }); button("拒绝此操作", path + "/approval", { ...body, approved: false });
    }
    if (run.status === "needs_input") {
      const label = document.createElement("label"); label.htmlFor = "diagnostic-supplement"; label.textContent = runtime.input_prompt;
      const input = document.createElement("input"); input.type = "text"; input.id = "diagnostic-supplement"; result.append(label, input);
      const send = document.createElement("button"); send.type = "button"; send.className = "button secondary"; send.textContent = "提交补充信息";
      send.addEventListener("click", () => { if (input.value.trim()) perform(path + "/input", { field: runtime.input_field, text: input.value.trim(), revision: run.revision }); }); result.append(send);
    }
    if (run.status === "needs_authorization") button("恢复权限后继续", path + "/resume", {});
    if (run.status === "needs_reconciliation") for (const item of runtime.pending || []) if (item.status === "unknown" || item.status === "in_flight") button("用服务端业务证据核对", path + "/reconcile", { invocation_id: item.invocation_id, arguments_sha256: item.arguments_sha256, revision: run.revision });
    if (!["completed", "failed", "cancelled"].includes(run.status)) button("停止此任务", path + "/cancel", {});
    feedback("已读取任务证据。执行或恢复后，点击“读取任务诊断”查看最新状态。", "success", "diagnostic-feedback");
  }
  async function diagnosticAction(fn) {
    if (diagnosticBusy) return;
    diagnosticBusy = true;
    const nodes = [...$("diagnostic-form").querySelectorAll("button")]; nodes.forEach(node => { node.disabled = true; });
    try { await fn(); } catch (error) { feedback(error.message, "error", "diagnostic-feedback"); }
    finally { diagnosticBusy = false; nodes.forEach(node => { node.disabled = false; }); }
  }
  $("diagnostic-form").addEventListener("submit", event => {
    event.preventDefault();
    diagnosticAction(async () => {
      const pack = $("diagnostic-pack").value.trim(), instruction = $("diagnostic-instruction").value.trim();
      if (!pack || !instruction) throw new Error("请填写能力包和一条代表性业务任务。");
      if (!diagnosticCreate || diagnosticCreate.pack_id !== pack || diagnosticCreate.instruction !== instruction) {
        const requestId = crypto.randomUUID?.() || Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
        diagnosticCreate = { pack_id: pack, instruction, request_id: requestId };
      }
      const run = await userRequest("/runs", diagnosticCreate);
      if (!run.run_id) throw new Error(`创建结果尚未确认。请保持能力包和任务内容不变后重试，请求 ID：${diagnosticCreate.request_id}`);
      $("diagnostic-run").value = run.run_id; diagnosticCreate = null; await inspectRun();
    });
  });
  $("inspect-run").addEventListener("click", () => diagnosticAction(inspectRun));
})();
