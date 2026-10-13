# 完成校验与模型评测

`completed` 表示当前运行的回答已结束。业务动作是否完成，还需要可验证的结果契约；Fact 引用检查只保证引用存在，无法证明自然语言中的每句话正确。

回答正文不得包含本次运行已知的内部 Fact ID；引用保留在结构化 `fact_ids` 和业务对象引用中。框架在发布前拒绝这类正文，并通过 `final_internal_fact_id_exposed` 反馈让模型在原预算内修正。该检查仅匹配已有 Fact ID，不过滤普通业务 UUID、不删除已保存证据、不重新执行业务操作，也不替代字段和语义校验。

决策中的自然语言文本按 Unicode 码点计算字符数，与 JSON Schema 的 `maxLength` 一致：调用 `reason` 最多 500 字符，最终 `answer_markdown` 最多 30,000 字符，追问 `prompt` 最多 1,000 字符，业务对象引用 `label` 最多 100 字符。中文和 emoji 各按一个码点计算；组合字符中的每个码点分别计数。例如 200 个汉字占 600 个 UTF-8 字节，仍符合 `reason` 的字符上限。这些限制同时适用于 JSON 正文和类型化输出工具；HTTP 响应大小与模型 Token、上下文预算继续独立执行。

## 写操作后的回答一致性复核

运行出现 `write` 操作结果后，框架在发布最终回答前自动复核一次拟返回的回答。模型上下文的 `action_outcomes` 将每次动作、确定状态和当前结果 Fact 关联起来，区分成功、仅受理、失败、未执行和结果未知，并说明能力的审批要求。复核仍使用本次运行固定的业务决策模型及参数，宿主无需实现文本判断或额外选择模型。

复核只能返回 `final`，不能执行或重新提交业务操作。`final` 新增可选字符串 `completion_feedback`（最多 2,000 个 Unicode 码点），供复核拒绝提前完成：发现原请求中仍有未执行的工作时，返回具体缺项；框架保存草稿和复核决策，将 `completion_incomplete` 反馈写入现有观察，保持任务运行并回到普通决策阶段。后续操作继续经过当前授权、审批、前置校验和回执路径，复核文字本身不能授权或派发调用，也不会自动重放已成功的写入。所有请求消耗原有预算，没有新增无限重试。

已处理所有请求，或有明确缺失前提、审批拒绝或不确定操作阻止推进时，复核返回空字符串并如实说明结果和限制；不能凭空增加验收要求。旧的自定义 `DecisionModel` 可省略该字段；内置 HTTP 模型适配器在两种输出模式下都要求复核显式返回该字符串，缺失字段或返回其他决策种类时拒绝响应，沿用原格式纠正预算。普通决策阶段不能提交非空复核反馈。例：写入后用户明确要求独立读回，但本轮只有写入回执时，复核返回 `{"kind":"final","answer_markdown":"尚未发布的草稿","fact_ids":["真实证据 UUID"],"completion_feedback":"缺少原请求要求的独立读回。"}`，下一轮再由模型通过正常能力调用补做读回。

草稿和复核结果都进入现有决策审计。框架先检查草稿的 Fact 引用、标量业务对象引用及正文中的内部 Fact ID；这些本地规则已能确定无效的草稿直接返回普通决策纠正，保留原错误码、执行证据和预算，不发送无效草稿进行模型复核。有效草稿仍须复核，复核结果再次通过同一组本地规则和已有 `CompletionValidator` 检查后才发布。宿主完成校验不在草稿阶段执行。先验证输出再处理有效输出的顺序参考 [Pydantic AI 输出工具处理](https://github.com/pydantic/pydantic-ai/blob/72d89d136b5d155e52f5c2b054175420459fb6f4/pydantic_ai_slim/pydantic_ai/tool_manager.py#L968-L982)。

写操作之外的只读、计算和对话运行不增加复核请求。正常复核增加一次模型请求；格式错误最多使用现有的一次格式纠正，并始终受轮次、Token、输入上下文、超时和费用统计约束。复核无法完成时，任务保持未完成、草稿不发布，已有业务回执继续保留。该反馈方式参考 [Pydantic AI 将输出校验失败反馈回模型循环](https://github.com/pydantic/pydantic-ai/blob/72d89d136b5d155e52f5c2b054175420459fb6f4/pydantic_ai_slim/pydantic_ai/_agent_graph.py#L2393-L2399)。

使用 `output_tools` 时，复核请求只提供 `submit_decision` 的 `final` 分支及上述反馈字段，通过明确的 `tool_choice` 选择该函数，保持单个输出决策限制；内置复核工具 Schema 要求显式提供 `completion_feedback`。业务调用、追问、搜索和检查工具不作为此阶段的可选输出。普通决策工具不暴露复核反馈。已保存调用及结果仍作为历史证据呈现，输入测量与实际请求一致。该限制依据框架已有的复核阶段，不按模型名称分支；JSON 正文复核使用同一反馈语义，记忆提取不变。此取舍参考 [Pydantic AI 按可选工具限制模型请求](https://github.com/pydantic/pydantic-ai/blob/72d89d136b5d155e52f5c2b054175420459fb6f4/pydantic_ai_slim/pydantic_ai/models/openai.py#L1583-L1616)。筛选输出工具使模型请求与运行内核一致，不能保证最终回答不混用字段来源或遗漏用户要求的数据。

JSON 正文复核同样只宣告 `final`：移除框架普通决策的种类列表和调用示例，保留宿主规则与证据，并明确复核字段及阶段限制。网关声明支持 JSON 或工具 Schema 不能代替本地检查；适配器会再次校验复核的决策种类和反馈字段，缺失字段不能当作“通过复核”。

复核输入在分配上下文预算前移除执行能力目录、当前检查的能力 Schema、能力搜索结果及搜索提示，让预算优先用于原任务、补充输入、拟完成草稿和模型可见证据。`capabilities` 仅保留已有 Fact 和对应观察所关联、当前仍获授权的能力名称与描述，供复核理解结果中的业务字段与执行元数据；不携带输入 Schema、操作绑定或未执行能力的说明。终态 Fact 来自轮询能力时，也保留原动作的结果说明。该投影仍纳入原字符与 Token 预算，不增加执行权限。Fact、按原顺序保留的有界观察、动作回执、检查结果、技能约定和记忆仍按原上下文规则提供。两种 HTTP 输出模式都直接使用这份累计证据，不再在复核请求末尾重复构造一条历史 `tool_batch` 决策及其结果；普通执行阶段继续提供原调用与实际回执。复核反馈返回普通决策后，框架重新生成该阶段的授权能力目录，未改动保存的契约或运行证据。

这一取舍参考 [LangGraph 的 evaluator-optimizer 示例](https://docs.langchain.com/oss/python/langgraph/workflows-agents#evaluator-optimizer)：评估节点使用专门的结构化输出和待评判产物，反馈再返回生成节点；同时参考 [Pydantic AI 按当前上下文筛选工具](https://github.com/pydantic/pydantic-ai/blob/72d89d136b5d155e52f5c2b054175420459fb6f4/pydantic_ai_slim/pydantic_ai/toolsets/filtered.py)。Agenstra 使用已有复核阶段实现输入投影，减少执行与复核混淆及无用输入；语义漏检和步骤顺序仍须通过目标模型的真实任务验收。

工具输出复核把本轮实际最终草稿表示为历史 `submit_decision` 调用，并配对“尚未发布、需要复核”的框架反馈。任务、补充输入、观察和 Fact 保持原值；草稿只在历史调用参数中提供一次，不加入系统指令，也不表示任何业务操作成功。复核仍只能返回 `final`，缺少原请求的工作仍须通过 `completion_feedback` 拒绝发布。此消息组织参考 [Pydantic AI 保存模型响应并按工具调用身份关联反馈的实现](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/_agent_graph.py)，不引入其运行库。需要原始推理正文的 `deepseek_chat` 在未明确关闭 thinking 时继续使用数据形式的草稿，框架不伪造推理正文。JSON 输出和普通业务历史保持原有行为，新增消息计入完整输入测量；这项组织方式本身不保证模型或网关始终遵守工具输出协议。

遥测中的 `completion_review` 单独记录这部分请求和用量。标准聊天组件直接显示执行回执；任务或回答生成失败不会把已成功的业务操作显示成未执行。自定义界面可使用已有的 `run.state.runtime.invocation_receipts`，无需解析模型文字。

这是针对执行结果叙述的模型语义复核，并不是自由文本正确性的数学证明；确定的执行状态仍以框架回执为准。它不代替业务专用字段规则。

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

用户拒绝审批的调用没有执行，不要求提供业务成功 Fact，可如实结束回答；即使规则设置 `required: true`，拒绝审批也允许结束。拒绝不会跳过同一能力其他已执行调用或其他能力的证据检查，之前的失败也不会因后续拒绝而消失。框架仍对写操作的最终回答进行一致性复核，区分未执行与成功。

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
