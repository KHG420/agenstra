# 宿主运行观测与控制

实施顺序：有效配置快照；最终上下文测量；累计预算与辅助模型调用；HTTP/SDK；模型容量与输出预留；投影阈值与控制；执行阶段与宿主方法。

新 run 在 `effective_config` 保存执行设置。恢复沿用快照；旧 run 没有快照时明确标注 `source=current_host`。租约、实时授权和服务级并发继续由当前 Host 控制。配置不含模型密钥或业务凭证。

字符、模型 token、累计 run 预算与持久状态字节分别计量。未知容量使用 null；保留量和结果未知的请求不可显示为供应商确认消耗。上下文观测来自最终投影，不在查询时重建或调用模型。

`runtime.context_telemetry` 在最终提示和投影组装后测量。字符组成（含 JSON 框架开销）可相加；候选大小是默认事实预览/观察窗口之后、压力裁剪之前的投影。省略数量包含默认裁剪。必须内容超限也记录测量，不执行模型 IO；完整事实不受影响。投影 ID、轮次和采样时间标识最近一次输入，工具期间不表示预测的下一次输入。

`AgentHost.GetTelemetry` 只读取持久快照并执行 owner/policy 检查。`budget.tokens` 分开供应商报告、估计、活动请求保留和未知结果保留；未配置总预算时 limit/remaining 为 null。保留量继续占用预算。`usage_by_purpose` 区分 decision、completion_review 与 memory_extraction；辅助调用也计入累计预算，响应丢失保留检查点。写操作后的回答复核复用已固定的决策模型，管理界面将其用量显示为“回答复核”。

## HTTP 与 SDK

`GET /runs/{id}/telemetry` 和 `GET /web/v1/runs/{id}/telemetry` 返回同一份 `RunTelemetry`。Web 路由还检查浏览器/聊天 run 绑定。普通 run 响应携带同版本 `telemetry`；JS `getRunTelemetry(id)` 可独立读取，`watchRun()` 随现有 run 请求返回它，不额外轮询。模型与记忆事件附带当时的 context/budget，事件序列仍用于补读。

```js
const stop = client.watchRun(runId, ({ run, telemetry, events }) => {
  console.log(run.status, telemetry?.context?.characters_remaining);
});
```

## 模型容量

`model_context_window_tokens`、`max_model_input_tokens`、`model_output_reserve_tokens` 和 `model_protocol_reserve_tokens` 可显式配置。容量未知为 null。窗口已知时必须提供输出预留或已知输出上限；有效输入上限取宿主/模型输入限制与窗口扣除输出和协议预留的较小值。超限投影继续裁剪，必需内容仍超限则禁止模型 IO。

自定义模型可实现可选 `ModelInfoProvider` 与 `ModelInputMeasurer`，现有 `DecisionModel` 不变。HTTP 模型支持 `CountInputTokens` 回调，接收实际序列化协议正文；未提供时以 UTF-8 字节加 framing 估计并明确标注，不能等同供应商账单。响应报告值另存 `reported_input_tokens`。

## 投影策略控制

`settings.context_policy` 支持 `trigger_ratio` 和 `target_ratio`；默认 0/0 延续硬上限行为，启用时要求 0 < target <= trigger <= 1。token 上限已知时按 token 比例触发，否则按字符比例。目标是软目标，必要信息放不进目标但能放进硬上限时继续执行，并返回 target_met=false。strategy=projection，原始状态保持完整。

`POST /runs/{id}/context-policy`（Web 同路径前缀）接受 `{request_id, revision, policy}`；SDK `setContextPolicy(id, policy, revision, {requestId})`。请求写入现有事件日志，下一次决策前应用；复用 requestId 可恢复丢失响应。只改变投影软策略，硬上限与实时授权不受影响。指标返回实际策略、单位、原因和目标达成状态。

## 执行阶段与功能发现

telemetry.execution 提供模型决策、记忆提取、重试退避、工具执行及等待阶段，附阶段时间、retry_at、next_wake_at、活动调用、审批与异步期限、取消请求。HTTP 模型支持 WithModelRequestObserver；Host 将重试状态写入现有事件日志，回调保存失败时不发送下一次请求。host_operations 表示当前服务租约/并发配置，与 run 的执行快照分开。

`GET /runtime-info?pack_id=...` 与 `GET /web/v1/integrations/{id}/runtime-info` 返回授权范围内的功能、模型公开信息和设置；SDK 为 getRuntimeInfo()。增加 cancelRun、resumeRun 和 getArtifact；resumeRun 是现有驱动入口，不表示恢复已终止的失败 run。

对话设置 max_conversation_history_messages、max_conversation_history_characters、max_conversation_messages 默认 6/1500/500；0 延用默认。历史组装也遵守 30,000 字符指令上限，必要时移出更早历史。ChatMessage.context_selection 保存实际纳入/省略消息数、截断数量和字符大小；重启复用冻结指令与说明。

本轮完成以上七项。普通 run 用户暂停、单步执行与模型内容流仍属于独立的执行/协议能力，不由观测接口推断支持；功能发现明确反映实际支持。

CLI 支持 --max-context-characters、--context-window-tokens、--max-input-tokens、--output-reserve-tokens、--protocol-reserve-tokens、--context-trigger-ratio 和 --context-target-ratio。嵌入式 AgentRuntime.Run/Result 也返回最后一次 context_telemetry 和证据进度。
