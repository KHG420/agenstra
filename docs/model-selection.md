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

可选限制包括 `max_output_tokens`、`token_limit_field`、`timeout_seconds`、`max_attempts`、`context_window_tokens`、`max_input_tokens`、`protocol_reserve_tokens`。最大尝试次数包含首次请求，1 表示不做传输重试。配置的超时及 token 上限仍受任务冻结的部署限制、总预算和截止时间约束；选大模型不能扩大原有运行预算。已知上下文窗口应配合明确的输出预留。

依据：[OpenAI Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)、[DeepSeek 推理模式](https://api-docs.deepseek.com/guides/thinking_mode)。不同模型的实际参数支持可以不同，接口类型本身不保证模型兼容。

## 验证结构化输出

控制台可以对当前草稿发送一条合成的决策或记忆提取请求，也可以验证尚未保存的配置。检查只做一次传输尝试，不执行宿主能力、不写入用户记忆，不计入某个业务任务预算，但会产生所选 API 的模型费用。

检查结果包含 passed、错误码和分项调用指标。HTTP 200 不是通过标准：响应还需符合 Chat Completions envelope、完整 JSON 及框架决策/记忆契约。JSON 后附 DSML 标签、空内容、错误结构和截断响应均不能通过。通过一个合成样本不能证明业务成功率、审批正确性或长期输出稳定性。

业务运行继续保留原有严格解析与有界纠正，不截取 JSON 前缀或剥除协议片段。`ModelCallMetrics.format_error` 区分 `model_response_invalid`、`model_output_empty`、`model_output_invalid_json`、`model_output_protocol_mismatch`、`model_decision_schema_invalid` 和 `model_memory_schema_invalid`。外层 `model_decision_invalid` 及既有纠正规则保持可用；诊断接口展示具体原因和后续成功纠正的记录。

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

短语检索不等同语义理解，也不能保证英文描述可以自动理解中文同义词。能力描述仍需表达宿主实际业务。调整目录与推理参数后，应验收实际请求总量、审批与最终业务状态；不能仅以 completed 或首轮 token 下降判断优化成功。真实样本和已知限制见[调用分析](model-call-analysis.md)。

### 2026-10-03 真实模型验收

使用隔离的抽奖样本数据库（156 人、初始业务版本 6、两轮历史）运行四个场景。记忆提取配置关闭推理，业务决策配置开启推理、强度 low，能力目录上限为 8。参数由本模块实际发送，网关只转发与记录，不注入推理参数。宿主代码及生产模型默认配置未修改。

| 场景 | 模型请求数（包含记忆提取） | 整体耗时 | 正式审批 | 业务断言 |
| --- | ---: | ---: | ---: | --- |
| 查询活动 | 4 | 11.154 秒 | 0 | 通过 |
| 开始抽奖 | 6 | 18.208 秒 | 1 | 通过 |
| 揭晓结果 | 8 | 31.424 秒 | 1 | 通过 |
| 页面导航与主题 | 6 | 14.069 秒 | 0 | 通过 |

合计 24 次请求，其中 4 次记忆提取、20 次业务决策；已报告输入 94,085 tokens、输出 8,743 tokens。记忆请求在协议中明确发送 `thinking: {"type":"disabled"}`，但供应商没有报告其推理 token 字段，因此此分项仍为未知。20 次业务决策均报告推理分项，合计 6,876 tokens。未填写价格，费用为未知。

四个场景均通过最终业务状态与审批断言，没有观察错误和 HTTP 重试。揭晓场景仍发生一次 `model_decision_schema_invalid`，随后一次格式纠正成功；还存在重复读取。原抽奖目录的四条完整中文指令在首轮均选出目标动作和页面观察能力，但开始场景首轮未选出读取活动能力，实际运行通过一次 `search_capabilities` 找到它。这些限制未宣称修复。

这是单次真实模型验收，同时调整了推理参数与目录上限，不能据此归因某项改动的节省比例，也不能代表生产成功率。可核对的脱敏分项记录见[验收 JSON](model-selection-validation.json)。
