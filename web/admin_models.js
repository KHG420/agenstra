(function () {
  "use strict";
  const reasoningOptions = {
    compatible_chat: [["", "服务默认"]],
    openai_chat: [["", "服务默认"], ["none", "关闭推理"], ["minimal", "minimal"], ["low", "low"], ["medium", "medium"], ["high", "high"], ["xhigh", "xhigh"]],
    deepseek_chat: [["", "服务默认"], ["disabled", "关闭推理"], ["enabled", "开启推理（默认强度）"], ["low", "low"], ["high", "high"], ["max", "max"]],
  };
  const numericFields = { max_output_tokens: "model-output-limit", timeout_seconds: "model-timeout", max_attempts: "model-attempts", context_window_tokens: "model-window", max_input_tokens: "model-input-limit", protocol_reserve_tokens: "model-protocol-reserve", temperature: "model-temperature" };
  const checkErrors = {
    model_output_protocol_mismatch: "响应混入原生工具协议，请检查网关的结构化输出适配。",
    model_output_invalid_json: "模型返回的内容不是完整 JSON。",
    model_response_invalid: "API 响应不符合 Chat Completions 协议。",
    model_output_empty: "模型返回空内容。",
    model_output_truncated: "输出达到上限并被截断，请检查输出预算。",
    model_decision_schema_invalid: "JSON 未满足框架的决策结构。",
    model_memory_schema_invalid: "JSON 未满足记忆提取结构。",
  };

  function installModelEditor({ api, feedback }) {
    const $ = id => document.getElementById(id);
    let config = null, revision = 0, editing = "", busy = false, dirty = false;
    function status(text) { $("models-status").textContent = text; }
    function option(value, text) { const node = document.createElement("option"); node.value = value; node.textContent = text; return node; }
    function selectors() {
      const ids = Object.keys(config.profiles).sort();
      for (const [id, field, inherit] of [["model-default", "default_profile", false], ["model-decision", "decision_profile", true], ["model-memory", "memory_extraction_profile", true]]) {
        const select = $(id); select.replaceChildren();
        if (inherit) select.append(option("", "使用默认模型配置"));
        ids.forEach(name => select.append(option(name, name)));
        select.value = config[field] || "";
      }
      const list = $("model-profile-list"); list.replaceChildren(option("", "添加模型配置")); ids.forEach(id => list.append(option(id, id))); list.value = editing;
    }
    function reasoning(value = "") {
      const type = $("model-api-type").value;
      $("model-reasoning").replaceChildren(...reasoningOptions[type].map(([v, label]) => option(v, label)));
      $("model-reasoning").value = value;
      $("model-parameter-help").textContent = type === "deepseek_chat" ? "DeepSeek 开启推理时温度不生效；关闭推理时不能同时设置推理强度。" : type === "openai_chat" ? "推理强度是否可用取决于模型。明确开启推理时不能同时配置温度，请通过合成请求验证。" : "通用接口不发送推理字段。使用对应供应商接口可选择推理参数。";
    }
    function loadProfile(id) {
      $("model-check-result").replaceChildren();
      editing = id;
      const p = config.profiles[id] || { api_type: "compatible_chat" };
      $("model-profile-id").value = id; $("model-profile-id").disabled = !!id;
      $("model-api-type").value = p.api_type || "compatible_chat";
      for (const [field, element] of Object.entries(numericFields)) $(element).value = p[field] ?? "";
      $("model-name").value = p.model || ""; $("model-base-url").value = p.base_url || "";
      $("model-url-env").value = p.base_url_env || ""; $("model-key-ref").value = p.api_key_ref || "";
      $("model-token-field").value = p.token_limit_field || "";
      $("model-input-price").value = p.prices?.input_per_million ?? "";
      $("model-output-price").value = p.prices?.output_per_million ?? "";
      $("model-cache-price").value = p.prices?.cached_input_per_million ?? "";
      reasoning(p.reasoning_effort || p.thinking || "");
      $("model-remove").disabled = !id;
    }
    function number(id) {
      if ($(id).value.trim() === "") return undefined;
      const value = Number($(id).value);
      if (!Number.isFinite(value) || value < 0) throw new Error("数值设置必须是非负有限数值。");
      return value;
    }
    function applyProfile() {
      if (!$("model-profile-form").reportValidity()) throw new Error("请补齐模型配置中的必填字段。");
      const id = $("model-profile-id").value.trim();
      if (!/^[A-Za-z][A-Za-z0-9_.-]{0,127}$/.test(id)) throw new Error("配置名称需要以英文字母开头，只能使用字母、数字、点、下划线和短横线。");
      if (!editing && config.profiles[id]) throw new Error("配置名称已存在，请从列表中选择后编辑。");
      const p = { api_type: $("model-api-type").value, model: $("model-name").value.trim(), api_key_ref: $("model-key-ref").value.trim() };
      const base = $("model-base-url").value.trim(), env = $("model-url-env").value.trim();
      if (!!base === !!env) throw new Error("API 地址和地址环境变量需要填写其中一项。");
      if (base) p.base_url = base; else p.base_url_env = env;
      for (const [field, element] of Object.entries(numericFields)) { const n = number(element); if (n !== undefined) p[field] = n; }
      const setting = $("model-reasoning").value;
      if (p.api_type === "openai_chat" && setting) p.reasoning_effort = setting;
      if (p.api_type === "deepseek_chat" && setting) {
        p.thinking = setting === "disabled" ? "disabled" : "enabled";
        if (!["disabled", "enabled"].includes(setting)) p.reasoning_effort = setting;
      }
      const tokenField = $("model-token-field").value; if (tokenField) p.token_limit_field = tokenField;
      const input = number("model-input-price"), output = number("model-output-price"), cached = number("model-cache-price");
      if (input !== undefined || output !== undefined || cached !== undefined) {
        if (input === undefined || output === undefined) throw new Error("估算费用需要同时填写输入和输出价格，免费项可以填 0。");
        p.prices = { input_per_million: input, output_per_million: output }; if (cached !== undefined) p.prices.cached_input_per_million = cached;
      }
      config.profiles[id] = p; editing = id;
      if (!config.default_profile) config.default_profile = id;
      dirty = false; selectors(); loadProfile(id);
      status(`配置草稿已修改 · 服务端版本 ${revision} · 点击“保存全部配置”后生效。`);
      return id;
    }
    function selectedConfig() {
      config.default_profile = $("model-default").value;
      config.decision_profile = $("model-decision").value;
      config.memory_extraction_profile = $("model-memory").value;
      return config;
    }
    async function action(fn) {
      if (busy) return; busy = true;
      const buttons = [...$("models").querySelectorAll("button")]; buttons.forEach(node => { node.disabled = true; });
      try { await fn(); } catch (error) { feedback(error.message, "error", "models-feedback"); }
      finally { busy = false; buttons.forEach(node => { node.disabled = false; }); $("model-remove").disabled = !editing; }
    }
    async function refresh() {
      // Global catalog refresh must not discard a model form being edited.
      if (config) return;
      const data = await api("/admin/api/models");
      $("models-editor").hidden = !data.available;
      if (!data.available) { status("当前服务使用自定义模型。部署服务使用 ModelManager 后可在这里配置。"); return; }
      config = structuredClone(data.config); revision = data.revision;
      editing = config.decision_profile || config.default_profile;
      selectors(); loadProfile(editing); dirty = false;
      status(`服务端配置版本 ${revision} · 选择已保存。`);
    }
    $("model-profile-form").addEventListener("input", () => { dirty = true; status("编辑内容尚未保存。验证或保存时会应用当前编辑。"); });
    $("model-api-type").addEventListener("change", () => reasoning());
    $("model-profile-list").addEventListener("change", () => {
      const next = $("model-profile-list").value;
      try { if (dirty) { selectedConfig(); applyProfile(); } loadProfile(next); $("model-profile-list").value = next; dirty = false; }
      catch (error) { $("model-profile-list").value = editing; feedback(error.message, "error", "models-feedback"); }
    });
    $("model-profile-form").addEventListener("submit", event => { event.preventDefault(); action(async () => { selectedConfig(); applyProfile(); feedback("已应用到草稿，请保存全部配置。", "info", "models-feedback"); }); });
    for (const id of ["model-default", "model-decision", "model-memory"]) $(id).addEventListener("change", () => { selectedConfig(); status("用途选择尚未保存。请保存全部配置。"); });
    $("model-remove").addEventListener("click", () => action(async () => {
      selectedConfig();
      if ([config.default_profile, config.decision_profile, config.memory_extraction_profile].includes(editing)) throw new Error("此配置仍被默认或用途选择引用，请先选择其他配置。");
      delete config.profiles[editing]; editing = ""; selectors(); loadProfile(""); dirty = false; status("配置已从草稿移除，请保存全部配置。");
    }));
    $("models-reload").addEventListener("click", () => action(async () => { config = null; await refresh(); feedback("已重新读取服务端配置。", "success", "models-feedback"); }));
    $("models-save").addEventListener("click", () => action(async () => {
      selectedConfig(); if (dirty || !editing && $("model-profile-id").value.trim()) applyProfile();
      const data = await api("/admin/api/models", { method: "PUT", body: { expected_revision: revision, config } });
      config = structuredClone(data.config); revision = data.revision; selectors(); status(`服务端配置版本 ${revision} · 新任务使用此配置。`);
      feedback("模型配置已保存。已有任务继续使用创建时的配置。", "success", "models-feedback");
    }));
    $("models-check").addEventListener("click", () => action(async () => {
      selectedConfig(); const profile = applyProfile();
      feedback("正在验证结构化输出…", "info", "models-feedback");
      const data = await api("/admin/api/models/check", { method: "POST", body: { profile, purpose: $("model-check-purpose").value, config } });
      const result = $("model-check-result"); result.replaceChildren();
      const p = document.createElement("p"); p.textContent = data.passed ? "当前样本通过结构化输出验证。" : checkErrors[data.error_code] || `验证未通过：${data.error_code}`; result.append(p);
      renderUsage(result, { [data.purpose]: { ...data.metrics, requests: data.metrics.attempts, elapsed_ms: data.metrics.elapsed_ms, cost_available: data.metrics.estimated_cost_usd !== undefined } });
      feedback(data.passed ? "验证通过；草稿仍需保存后用于新任务。" : "验证未通过，请检查模型服务或参数。", data.passed ? "success" : "error", "models-feedback");
    }));
    return { refresh, renderUsage };
  }

  function renderUsage(container, byPurpose = {}, total) {
    const entries = Object.entries(byPurpose); if (!entries.length) return;
    if (total) entries.push(["total", total]);
    const wrap = document.createElement("div"); wrap.className = "table-wrap usage-table";
    const table = document.createElement("table"), head = document.createElement("thead"), row = document.createElement("tr");
    for (const label of ["调用用途", "请求", "输入 / 输出", "已知缓存输入", "已知推理输出", "格式异常 / 纠正", "HTTP 重试", "调用耗时", "已估费用（美元）"]) { const th = document.createElement("th"); th.scope = "col"; th.textContent = label; row.append(th); }
    head.append(row); table.append(head); const body = document.createElement("tbody");
    for (const [purpose, usage] of entries) {
      const item = document.createElement("tr");
      const reported = usage.usage_available !== false && usage.reported_requests !== 0;
      const values = [purpose === "total" ? "合计" : purpose === "memory_extraction" ? "记忆提取" : purpose === "completion_review" ? "回答复核" : "业务决策", usage.requests, reported ? `${usage.input_tokens ?? "未知"} / ${usage.output_tokens ?? "未知"}` : "未知 / 未知", usage.cached_input_tokens ?? "未知", usage.reasoning_output_tokens ?? "未知", `${usage.invalid_responses ?? (usage.format_error ? 1 : 0)} / ${usage.format_recovery_requests || 0}`, usage.retry_attempts || 0, `${((usage.elapsed_ms || 0) / 1000).toFixed(1)} 秒`, usage.cost_available ? Number(usage.estimated_cost_usd).toFixed(6) : "未知"];
      for (const value of values) { const td = document.createElement("td"); td.textContent = String(value); item.append(td); } body.append(item);
    }
    table.append(body); wrap.append(table); container.append(wrap);
    const note = document.createElement("small"); note.textContent = "分项仅累计供应商已报告的数值；费用可能只覆盖部分请求。缓存和推理 tokens 均计入原有总预算。"; container.append(note);
  }
  window.installModelEditor = installModelEditor;
})();
