# 完成校验与模型评测

`completed` 表示当前运行的回答已结束。业务动作是否完成，还需要可验证的结果契约；Fact 引用检查只保证引用存在，无法证明自然语言中的每句话正确。

## 写操作后的回答一致性复核

运行出现 `write` 操作结果后，框架在发布最终回答前自动复核一次拟返回的回答。模型上下文的 `action_outcomes` 将每次动作、确定状态和当前结果 Fact 关联起来，区分成功、仅受理、失败、未执行和结果未知，并说明能力的审批要求。复核仍使用本次运行固定的业务决策模型及参数，宿主无需实现文本判断或额外选择模型。

复核只能返回 `final`，不能执行或重新提交业务操作。草稿和复核结果都进入现有决策审计；只有复核结果通过 Fact、业务对象引用和已有 `CompletionValidator` 检查后才发布。写操作之外的只读、计算和对话运行不增加复核请求。正常复核增加一次模型请求；格式错误最多使用现有的一次格式纠正，并始终受轮次、Token、输入上下文、超时和费用统计约束。复核无法完成时，任务保持未完成、草稿不发布，已有业务回执继续保留。

遥测中的 `completion_review` 单独记录这部分请求和用量。标准聊天组件直接显示执行回执；任务或回答生成失败不会把已成功的业务操作显示成未执行。自定义界面可使用已有的 `run.state.runtime.invocation_receipts`，无需解析模型文字。

这是针对执行结果叙述的模型语义复核，并不是自由文本正确性的数学证明；确定的执行状态仍以框架回执为准。它不代替业务专用字段规则。具体问题证据、真实模型回放与边界测试见[动作成功但回答否认执行的修复记录](completion-consistency-findings.md)。

嵌入式宿主可配置 `AgentHost.CompletionValidator`；临时运行可配置 `AgentRuntime.CompletionValidator`。回调在引用检查通过后接收完整 Fact、观察、任务、补充输入及拟返回的回答，应保持只读并响应取消。返回 nil 才会完成；返回 `CompletionValidationError` 会把安全的错误码和明确反馈加入观察，让模型在原轮次预算内修正。其他错误仅暴露 `completion_validation_failed`，不会把内部错误详情发给模型。重启后的宿主应使用相同业务校验配置。

`RequireFactValues` 可检查指定能力的最新 Fact、引用和字段值，例如：

```go
check, err := agenstra.RequireFactValues(agenstra.FactRequirement{
    Capability: "job.status",
    Path: []any{"data", "status"},
    Value: "succeeded",
})
if err != nil { return err }
host.CompletionValidator = check
```

不同任务应选择对应的校验规则；不要把业务动作完成规则应用于问候、讨论和解释。需要验证报告中的数字、单位或正式产物时，可组合业务专用回答校验。通用框架不会用字符串匹配替代业务语义判断，也不会因有效的 Fact ID 就保证回答正确。

回归测试覆盖错误回答修正、最新证据、异步状态、失败操作、完整数据及错误脱敏。真实模型评测使用合成工具，不访问业务系统；需显式选择并提供自己的模型网关：

```sh
AGENSTRA_LIVE_EVAL=1 go test -run TestLiveAgentCompletionEvaluation -v
```

环境变量使用 `AGENT_MODEL`、`AGENT_MODEL_BASE_URL` 和 `AGENT_MODEL_API_KEY`。评测打印每个任务的结果、工具调用、模型决策数量和耗时；默认 CI 跳过付费模型调用。测试通过代表这些案例通过，不能替代目标场景的成功率、事实一致性和延迟评测。

针对历史错误回答的真实模型回放：

```sh
AGENSTRA_LIVE_EVAL=1 go test -run TestLiveWriteCompletionConsistencyReplay -v -count=1
```

它注入当时的错误草稿和合成数据，只让复核请求访问模型；不重新操作抽奖软件。可用 `AGENT_MODEL_API_TYPE`、`AGENT_MODEL_THINKING` 和 `AGENT_MODEL_REASONING_EFFORT` 选择网关参数。测试检查流程和引用，语义结论还需检查打印的完整回答；不能把有效引用当成语义通过。

## 独立服务的业务成功配置

部署配置可添加 `completion_checks`，按任务来源能力包分组，无需编写 Go 回调：

```json
{
  "completion_checks": {
    "orders": [
      {"capability": "orders.approve", "path": ["data", "approved"], "value": true}
    ]
  }
}
```

规则只应用于任务实际使用过的能力，因此查询、问候和解释无需调用审批能力。拟完成回答必须引用该能力最新的成功 Fact，字段满足声明值；最新操作失败时不能用更早证据通过。`path` 从完整 Fact.value 开始；通常业务响应位于 `data` 下，应以实际返回结果确认。值支持 JSON 数值、布尔、字符串、对象和 null，JSON 中必须显式提供 `value`。

配置在 HTTP server 启动前编译，错误会阻止启动；已有的 Go `CompletionValidator` 优先。重启或恢复未完成任务时保留相同业务校验配置。它不会验证自然语言的所有陈述，也不替代接口端的业务权限或事务。

## 不确定操作的只读核对配置

业务系统已有“按原请求 ID 查询回执”的接口时，可配置 `reconciliation_checks`：

```json
{
  "reconciliation_checks": {
    "orders": {
      "orders.approve": {
        "verify_capability": "orders.receipt",
        "arguments": {"/query/request_id": ["idempotency_key"]},
        "success_path": ["found"],
        "success_value": true,
        "result_path": ["result"]
      }
    }
  }
}
```

参数映射的键必须符合核对能力输入契约。`/query/request_id` 是 JSON pointer，生成 REST 的 `{query: {request_id: 原幂等键}}`；`request_id` 等普通键生成 MCP 的平坦参数。映射值从 `{arguments, invocation_id, idempotency_key}` 读取，其中 arguments 是原业务调用参数。规则必须至少将原 `idempotency_key` 或 `invocation_id` 传入一个参数，不能以无关联查询结果确认操作。没有合适的查询契约时使用宿主 Go `InvocationReconciler`。

核对能力必须是 `read`、被当前用户授权且无需额外审批。框架使用原运行绑定与契约、检查当前权限，以保存的 owner/run 和核对能力的项目路由身份执行独立只读查询；`success_path` 从查询 CapabilityResult.Data 开始，`result_path` 必须选出原操作完整结果对象。结果还会通过原操作输出契约、作业 ID 和终态检查。查询失败、权限撤销或证据不足时保持暂停，不重放原操作。业务接口须以传入的原请求标识查询权威记录，框架无法替业务系统证明查询实现的正确性。

已有 Go 核验回调优先。浏览器动作仍使用原浏览器回执核对接口。

## 通过服务验收真实接入

[快速接入指南](quick-integration.md)提供 `agenstra-evaluate` 的案例格式和运行命令。它分别验证状态、成功能力、禁止能力与最新被引用的字段值，生成带诊断和预算的 JSON 报告；不自动批准写操作。`run_id` 可用于在用户处理审批或追问后只读复验同一任务。

评测 CLI 对完成任务的字段断言会按运行快照中的 `artifact_ids`，通过带用户认证的 `/runs/{run_id}/artifacts/{id}` 读取完整 Fact，再交给 `EvaluateRun`。产物读取失败或 Fact ID 不匹配会使评测失败。嵌入式直接调用 `EvaluateRun` 时，调用方须提供含完整 Facts 的运行快照；该函数不拥有 Store，也不会自行访问数据库。
