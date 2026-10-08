# 模型配置、选择与调用观测

Agenstra 在框架部署侧管理模型连接和推理参数。宿主仍只负责业务能力接入、授权和业务成功标准；不需要为记忆提取或业务决策编写模型路由。

`agenstra-serve` 使用 `Deployment.NewModel()` 创建模型管理模块。Go 嵌入方也可以把它作为 `NewAgentHost` 的 model 参数。既有自定义 `DecisionModel` 保持可用。

## 配置与选择

部署 JSON 可以增加 `models`。首次启动使用部署配置，管理控制台保存后的配置存放在既有管理数据库的独立 `model_configuration` 表中，后续启动优先读取已保存版本。未配置 `models` 时，仍使用 `AGENT_MODEL`、`AGENT_MODEL_BASE_URL` 和 `AGENT_MODEL_API_KEY` 初始化单一默认配置，不自动改变推理参数。

以下是部署配置中的示例片段。模型名称是占位符，需要替换为对应服务实际提供的模型 ID；它不是模型质量或价格推荐。

```json
{
  "models": {
    "default_profile": "business",
    "memory_extraction_profile": "memory",
    "profiles": {
      "business": {
        "api_type": "openai_chat",
        "model": "your-decision-model",
        "base_url_env": "BUSINESS_MODEL_BASE_URL",
        "api_key_ref": "BUSINESS_MODEL_API_KEY",
        "reasoning_effort": "low",
        "max_output_tokens": 4096
      },
      "memory": {
        "api_type": "deepseek_chat",
        "model": "your-memory-model",
        "base_url_env": "MEMORY_MODEL_BASE_URL",
        "api_key_ref": "MEMORY_MODEL_API_KEY",
        "thinking": "disabled",
        "max_output_tokens": 512
      }
    }
  }
}
```

`decision_profile` 和 `memory_extraction_profile` 留空时继承 `default_profile`。不同配置可以复用同一 API、凭据及模型，只设置不同推理强度。最多保存 32 项配置。

每项连接使用 `base_url` 或 `base_url_env`，二者只能填写一个。基础地址应包含服务所需的路径前缀，例如 `/v1`；框架在其后请求 `/chat/completions`。地址不接受内嵌用户名、密码、query 或 fragment。`api_key_ref` 引用服务端环境变量，或既有管理 `secret_dir` 中的 `secret:NAME` 文件。接口与数据库保存引用，不保存解析后的 API key。

管理页 `/admin` 的“模型配置”支持编辑连接、用途选择、参数、限制和价格，保存整个配置草稿。API key 值继续由服务端环境或 secret 文件提供。管理 API 受既有管理员认证保护；用户的模型数据 consent、能力授权与审批规则保持生效。

新任务会固定所选配置及其版本，包含默认、业务决策、记忆提取所需的连接引用。修改默认模型、模型 ID、参数或删除配置只影响新任务。已有任务从快照恢复配置；凭据引用在调用时解析，因此凭据更新仍由部署管理。直接使用既有单模型接口的任务保留原 `model_changed` 校验。

## 参数适配

| API 接口 | 推理参数 | 输出上限默认字段 |
| --- | --- | --- |
| `compatible_chat` | 不发送供应商推理字段；可以设置温度 | `max_tokens` |
| `openai_chat` | `reasoning_effort`：none、minimal、low、medium、high、xhigh | `max_completion_tokens` |
| `deepseek_chat` | `thinking`：enabled / disabled；开启时 `reasoning_effort`：low、high、max | `max_tokens` |

这是 Chat Completions 协议的三种参数适配方式，也可用于采用对应参数契约的网关。此模块不提供 Responses 或 Anthropic 原生协议。框架不根据模型名称猜测能力，也不自动选择模型。

参数留空时不发送，对应默认由服务决定。DeepSeek 开启或默认推理时温度不生效，因此框架拒绝同时配置温度；关闭推理时不能设置推理强度。OpenAI 显式设置非 none 推理强度时也拒绝同时配置温度。具体模型是否接受某个强度必须通过服务验证；通用接口不会静默转发未知推理字段。

输出策略适用于所有使用本适配器的兼容 Chat Completions 模型，由各模型配置及实际网关能力决定，不按模型名分支。相同模型经过不同网关时，支持程度也可能不同。管理员应分别验证连接、决策输出格式、业务输入契约和完整任务；单次合成检查通过或 HTTP 200 不能证明网关严格执行 Schema，也不能证明任务正确完成。

可选 `decision_output_mode` 选择业务决策输出：省略或 `json_object` 保持现有 JSON 内容；`output_tools` 使用每种决策一个类型化输出工具，通过 `tool_calls` 返回该决策的参数。适配器只从已登记的工具名赋予固定 `schema` 与 `kind`，随后使用原有 `agenstra.decision.v1` 校验；模型参数不包含这两个固定字段。此设置不影响记忆提取。仅对已实测支持该协议的网关启用；接受参数不等于网关严格执行 Schema。未知或多个输出工具、错误类型、重复键和不匹配的决策均被拒绝，仍使用原有本地校验、授权与预算。工具定义和输出提示计入实际请求预算。不会从普通文字中提取或修复 JSON，也不会自动改用另一种输出方式掩盖异常。已有任务继续使用创建时冻结的配置。

宿主沿用已有业务能力绑定和接线；部署管理员在框架开发者控制台的模型配置中保存输出策略即可。实际业务输入仍按该能力的 `input_schema` 校验，合法的决策输出不能代替业务参数或任务结果验收。

单次能力调用优先通过 `submit_tool_call` 直接提交 `call_ref`、`capability`、`arguments`、`reason`；适配器将对象包装为原有 `tool_batch` 的一项，减少单次调用的数组嵌套。多个独立调用仍使用 `submit_tool_batch` 的原生 `calls` 数组。两者都只提出决策，业务参数、授权、审批和真实回执继续由运行内核校验。字符串形式的对象或数组不会被自动转换；减少包装不能保证模型遵守业务契约。对象输出工具的参考实现见 [Pydantic AI](https://github.com/pydantic/pydantic-ai/blob/72d89d136b5d155e52f5c2b054175420459fb6f4/pydantic_ai_slim/pydantic_ai/_output.py#L1546-L1578)。

网关仍可能在 `parallel_tool_calls=false` 时返回多个输出工具。框架拒绝整个响应，在原有纠正次数与预算内明确反馈每轮只能有一个输出工具，包括检查 Fact 时也须逐轮进行，不选择其中一个执行或自动合并。类型化输出的纠正提示移除旧内容 JSON 示例，保留字段、引用和完成复核约束。明确反馈多个结构化输出的参考实现见 [LangChain](https://github.com/langchain-ai/langchain/blob/007cc15b713cea17df63ba603aca6c7b27086638/libs/langchain_v1/langchain/agents/factory.py#L1287-L1308)。

可选限制包括 `max_output_tokens`、`token_limit_field`、`timeout_seconds`、`max_attempts`、`context_window_tokens`、`max_input_tokens`、`protocol_reserve_tokens`。最大尝试次数包含首次请求，1 表示不做传输重试。配置的超时及 token 上限仍受任务冻结的部署限制、总预算和截止时间约束；选大模型不能扩大原有运行预算。已知上下文窗口应配合明确的输出预留。

依据：[OpenAI Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)、[DeepSeek 推理模式](https://api-docs.deepseek.com/guides/thinking_mode)。不同模型的实际参数支持可以不同，接口类型本身不保证模型兼容。

## 验证结构化输出

控制台可以对当前草稿发送一条合成的决策或记忆提取请求，也可以验证尚未保存的配置。检查只做一次传输尝试，不执行宿主能力、不写入用户记忆，不计入某个业务任务预算，但会产生所选 API 的模型费用。

检查结果包含 passed、错误码和分项调用指标。HTTP 200 不是通过标准：响应还需符合 Chat Completions envelope、完整 JSON 及框架决策/记忆契约。JSON 后附 DSML 标签、空内容、错误结构和截断响应均不能通过。通过一个合成样本不能证明业务成功率、审批正确性或长期输出稳定性。

业务运行继续保留原有严格解析与有界纠正，不截取 JSON 前缀或剥除协议片段。`ModelCallMetrics.format_error` 区分 `model_response_invalid`、`model_output_empty`、`model_output_invalid_json`、`model_output_protocol_mismatch`、`model_decision_schema_invalid` 和 `model_memory_schema_invalid`。外层 `model_decision_invalid` 及既有纠正规则保持可用；诊断接口展示具体原因和后续成功纠正的记录。

首次决策说明与格式纠正说明都提供本地 `call_ref` 的现有约束：`^[a-z][a-z0-9-]{0,63}$`。模型可以使用 `read-1`、`read-2` 等新引用；能力名称、操作名称和参数值继续遵守各自契约，包括操作名称里的大写字母。这个引用仅用于本次运行的调用身份，框架仍严格校验，不自动改写模型输出。提供输出约束及有界纠正的取舍参考 [Pydantic AI 的输出契约提示](https://github.com/pydantic/pydantic-ai/blob/721c78d6014f197c595a9c2a83789aa4dbf6d008/pydantic_ai_slim/pydantic_ai/_output.py#L722-L733) 和 [校验错误转为重试反馈](https://github.com/pydantic/pydantic-ai/blob/721c78d6014f197c595a9c2a83789aa4dbf6d008/pydantic_ai_slim/pydantic_ai/_output.py#L124-L126)。Agenstra 沿用自己的安全错误码和恢复预算。

输出上限包含供应商计入输出的推理 tokens。`finish_reason=length` 返回 `model_output_truncated`；兼容网关返回空白正文、报告的输出量达到本次实际请求上限时，也归入该错误，而非普通 JSON 格式错误。没有已知请求上限或有效输出量时，不猜测空正文的原因。完整且通过契约校验的正文不会仅因用量达到上限而被拒绝。

业务决策遇到输出耗尽时，在现有每个决策或完成复核阶段最多两次模型请求（含首次请求）的范围内，提示模型缩短决策、分步处理并使用已有回执。恢复继续消耗原有轮次、累计 token 和时间预算，不自动提高配置的输出限制，不执行被截断的工具调用；完成复核仍只允许 final。每次失败与恢复请求分别保留用量，输出耗尽恢复不计作 `format_recovery_requests`。记忆抽取和控制台合成检查保留直接返回错误的行为。

依据：[Roo Code 的推理及输出预算](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/shared/api.ts#L99)、[Cline 的摘要预算调整及有界输出恢复](https://github.com/cline/cline/blob/faf05ef067dd4f5908c9e909dd757581089905ca/sdk/CHANGELOG.md#L72)。这些实现提供预算和恢复策略的参考，具体额度仍须通过部署方选定的模型验证。

## 指标与费用

每次调用记录配置名称、请求模型、响应模型、API 类型、推理设置、总输入/输出、缓存输入、推理输出和耗时。缓存输入兼容 OpenAI 的 `prompt_tokens_details.cached_tokens` 与 DeepSeek 的 `prompt_cache_hit_tokens`；推理输出读取 `completion_tokens_details.reasoning_tokens`。不保存推理正文。

供应商未提供或报告不合法的分项保持未知；真实报告的 0 保留为 0。`ModelUsage` 按用途和任务合计累计已知值，并提供 reported_requests、cached_input_requests、reasoning_output_requests、priced_requests 说明覆盖范围。invalid_responses 统计格式失败，format_recovery_requests 统计紧随无效决策的即时纠正请求，retry_attempts 统计 HTTP 重试；它们不等同于全部业务恢复动作。

控制台任务诊断展示用途及合计指标。总预算继续计入完整输入、输出和既有未知请求估算，不扣除缓存输入，不重复增加已经包含在输出中的推理 tokens。

配置可增加：

```json
{
  "prices": {
    "input_per_million": 1,
    "output_per_million": 2,
    "cached_input_per_million": 0.1
  }
}
```

上面仅是计算示例，单位为美元 / 百万 tokens。价格需由使用者按实际服务填写。提供缓存价格后，单次费用按普通输入与缓存输入分别计算；供应商未报告缓存量时该次费用保持未知。不提供缓存价格时，输入统一按配置的输入价格估算。只有部分请求可估价时，合计也是部分估算，不能当作账单。

## 管理 API 与 CLI

- `GET /admin/api/models`：读取配置和 revision；自定义模型未接入模块时返回 available=false。
- `PUT /admin/api/models`：提交 `{"expected_revision":N,"config":{...}}`。并发修改返回 HTTP 409 的 model_revision_conflict。
- `POST /admin/api/models/check`：提交 profile、purpose（decision / memory_extraction）及可选 config 草稿，返回合成检查结果。

```sh
go run ./cmd/agenstra-manage model list
go run ./cmd/agenstra-manage model configure models.json --revision 0
go run ./cmd/agenstra-manage model check business --purpose decision
go run ./cmd/agenstra-manage model check memory --purpose memory_extraction --config models.json
```

CLI 使用既有 AGENSTRA_SERVER / AGENSTRA_ADMIN_API_KEY。models.json 包含 ModelConfiguration 本身，不包含外层 models 键。保存不会自动执行收费检查；验证与业务验收由使用者明确触发。

## 能力选择与业务验收

中文完整指令的初始能力检索使用 Han 短语与原有名称、描述、输入字段评分，仍只读取固定目录和当前授权，不新增模型请求、分词依赖或业务 IO。选择浏览器动作时优先保留已授权的 ui.get_context 前置能力。目录默认上限继续为 0（全部）；可通过既有 max_context_capabilities 启用限量选择。非常小的限制可能先显示前置能力，并需要后续搜索。

短语检索不等同语义理解，也不能保证英文描述可以自动理解中文同义词。能力描述仍需表达宿主实际业务。调整目录与推理参数后，应验收实际请求总量、审批与最终业务状态；不能仅以 completed 或首轮 token 下降判断优化成功。
