# 从零接入一个 REST 能力

本教程使用文件式部署配置演示完整接入。若希望通过 CLI 或 Web 页面发布版本、管理授权和回滚，完成清单后继续阅读[能力管理指南](capability-management.md)。

这份教程用一个**假设已有**的 `GET /records/{record_id}` JSON API 演示从空仓库到可运行 Agent 的步骤。接口名称和数据只是说明格式；本仓库不提交该能力包、技能文件或模拟服务。把命令里的地址、凭据和字段替换成你实际要接入的接口即可。以下命令在仓库根目录运行。

## 1. 安装框架并确认接口契约

需要 Go 1.26+，以及一个能返回 JSON 决策的模型网关：

```sh
go mod download
```

先写下真实 API 的最小契约。例如：

```text
GET https://records-api.example/records/R-1
Authorization: Bearer <API token>

200 application/json
{"id":"R-1","status":"active"}
```

确认路径变量、类型、返回结构、错误码和认证方式。只有真实服务能保证这些约定；框架不会通过一个 HTTP 200 推断任务结果。如果接口会提交作业，还要确认是否有幂等键和可跨连接查询的作业 ID。

## 2. 手写最小 REST 能力包

把自己的接入文件放在忽略提交的 `local/` 下：

```sh
mkdir -p local/packs/records var
cat > local/packs/records/pack.json <<'JSON'
{
  "schema": "agenstra.rest-pack.v2",
  "name": "records",
  "version": "1.0.0",
  "guidance": "Use reviewed record data. Do not invent record fields or unsupported conclusions.",
  "base_url_env": "RECORDS_API_URL",
  "token_env": "RECORDS_API_TOKEN",
  "capabilities": [
    {
      "name": "records.get",
      "description": "Read one record by its exact ID",
      "method": "GET",
      "path": "/records/{record_id}",
      "effect": "read",
      "input_schema": {
        "type": "object",
        "properties": {
          "path": {
            "type": "object",
            "properties": {"record_id": {"type": "string"}},
            "required": ["record_id"],
            "additionalProperties": false
          }
        },
        "required": ["path"],
        "additionalProperties": false
      },
      "output_schema": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "status": {"type": "string"}
        },
        "required": ["id", "status"],
        "additionalProperties": false
      }
    }
  ]
}
JSON
```

`input_schema` 是 Agent 能提交的参数结构；`path.record_id` 必须与 URL 模板同名且必填。`output_schema` 是经过 REST 适配器验证后才能进入 Fact 的结构。这里使用 `additionalProperties: false`，因此接口多返回字段时也要先审查并更新契约。若真实 API 不用 bearer token，删掉 `token_env`，并从下面部署配置删去对应环境映射。

## 3. 加入使用说明

技能说明如何解释能力结果，不增添接口、授权或审批。先创建本地文件：

```sh
mkdir -p local/packs/records/skills/record-rules
cat > local/packs/records/skills/record-rules/SKILL.md <<'MD'
# Record rules

Read the exact record requested by the user. Preserve the returned ID and status.
If the user did not provide an ID, ask for it. Do not treat `active` as a
an approval unless the source API contract explicitly says so.
MD
```

清单要固定该文件的 SHA-256。下面的命令把技能声明写入刚创建的本地包：

```sh
shasum -a 256 local/packs/records/skills/record-rules/SKILL.md
```

将输出的 64 位哈希填入 `local/packs/records/pack.json` 的 `skills` 条目，并在相应能力的 `skills` 数组中填入 `record-rules`：

```json
"skills": [{"name": "record-rules", "description": "How to interpret the record status", "path": "skills/record-rules/SKILL.md", "sha256": "<上一步的哈希>"}]
```

改变技能内容后要重新审查并更新哈希。已有运行保存了包指纹；不要在运行恢复的中途替换包内容或连接端点。

## 4. 绑定用户、权限和模型

创建本地部署配置。`pack_id` 是 `records`；路径相对于此配置文件。`environment` 右侧是进程环境中的变量名，左侧是包将读到的变量名：

```sh
cat > local/deployment.json <<'JSON'
{
  "database_path": "../var/agent.sqlite3",
  "packs": {"records": {"path": "packs/records/pack.json"}},
  "users": {
    "operator": {
      "api_key_env": "AGENT_OPERATOR_API_KEY",
      "packs": {
        "records": {
          "environment": {
            "RECORDS_API_URL": "RECORDS_API_URL",
            "RECORDS_API_TOKEN": "RECORDS_API_TOKEN"
          },
          "granted_capabilities": ["records.get"],
          "approval_capabilities": [],
          "allow_model_data": true
        }
      }
    }
  }
}
JSON
```

`allow_model_data: true` 明确允许此连接的数据进入模型上下文。要处理受限数据，应选择符合数据使用要求的模型与部署环境；设置为 `false` 会阻止当前 Host 的 Agent 运行。每个用户都应有独立 API key，并按需要配置自己的连接身份与能力集合。

用自己的密钥管理方式向进程注入这些变量；以下值仅示意，不是可用凭据：

```sh
export RECORDS_API_URL='https://records-api.example'
export RECORDS_API_TOKEN='replace-with-real-api-token'
export AGENT_OPERATOR_API_KEY='replace-with-unique-random-key'
export AGENT_MODEL='replace-with-json-capable-model'
export AGENT_MODEL_BASE_URL='https://model-gateway.example/v1'
export AGENT_MODEL_API_KEY='replace-with-real-model-key'
```

模型适配器向 `${AGENT_MODEL_BASE_URL}/chat/completions` 发请求，并要求模型返回 `agenstra.decision.v1` 的 JSON 对象。启动前先检查包能被加载，且目录只暴露选定的能力：

```sh
go run ./cmd/agenstra \
  --pack local/packs/records/pack.json --inspect
```

随后启动服务：

```sh
go run ./cmd/agenstra-serve \
  --config local/deployment.json
```

另一个终端用同一组环境变量调用 `GET /readyz` 和创建任务：

```sh
curl -sS http://127.0.0.1:8091/readyz
curl -sS http://127.0.0.1:8091/runs \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"pack_id":"records","instruction":"查询记录 R-1 的状态，并引用结果说明","request_id":"record-R-1-001"}'
```

从响应取 `run_id`，用 `GET /runs/{run_id}` 读取状态。`queued` 由 worker 异步推进；`completed` 才表示 Agent 已完成回答。如果进入 `needs_input`、`needs_approval` 或 `needs_reconciliation`，按[部署文档](deployment.md)处理。真实模型和外部接口未连通时，目录检查仍可完成，但不能据此声称任务通过。

## 5. 如果已有 OpenAPI 文档

已有 OpenAPI 3.0/3.1 **JSON** 文档时，可以省掉大量手写 schema 工作。把文档留在 `local/`，只选实际要开放的 operationId：

```sh
go run ./cmd/agenstra-import-openapi \
  --spec local/openapi.json \
  --out local/packs/records/pack.json \
  --name records \
  --base-url-env RECORDS_API_URL \
  --token-env RECORDS_API_TOKEN \
  --operation records.get
```

这条命令会覆盖上面手写的 `pack.json`，因此**选择手写或导入其中一种路径**。替换 `records.get` 为真实 `operationId`。导入器保留嵌套 JSON Schema 和本地引用，支持显式 bearer token；不支持的认证、参数序列化和外部引用会报错。请逐项审查生成的 `effect`、路径、类型和响应，再补 `skills`、`approval_required`、`idempotency_header` / `idempotency_argument` 与 `operation`。不要把导入结果当作自动授权清单。

## 6. 把多个已有能力交给 Agent 组合

再接入两个接口时，在同一包的 `capabilities` 中增加两项经过审查的声明，并在部署配置的 `granted_capabilities` 中授权对应名称。可以给每项绑定不同的技能；无需修改 `AgentRuntime`。模型观察到第一项的 Fact 后，下一轮可用 `$fact_value` 引用字段：

```json
{
  "schema": "agenstra.decision.v1",
  "kind": "tool_batch",
  "calls": [{
    "call_ref": "next-step-1",
    "capability": "analysis.compute",
    "arguments": {
      "body": {
        "record_id": {
          "$fact_value": {
            "fact_id": "上一轮返回的真实 Fact UUID",
            "path": ["data", "id"]
          }
        }
      }
    },
    "reason": "Use the observed record ID"
  }]
}
```

示例展示参数绑定语义，不是要由应用手写并发送给 HTTP `/runs` 的内容。真实 `arguments` 必须符合目标能力的 `input_schema`；字段、单位和结果解释仍由技能与服务契约决定。相互依赖的调用要分轮执行，不能把未返回的结果当作已知值。

### 提交型接口和长任务

如果某能力返回 `{"jobId":"J-1","status":"queued"}`，且外部服务提供 `GET /jobs/{job_id}`，提交能力应声明真实支持的幂等方式，并把回执映射成 `OperationBinding`。片段如下（需与实际 `input_schema` / `output_schema` 同步）：

```json
{
  "name": "jobs.create",
  "method": "POST",
  "effect": "write",
  "approval_required": true,
  "idempotency_header": "Idempotency-Key",
  "operation": {
    "id_path": ["jobId"],
    "status_path": ["status"],
    "poll_capability": "jobs.status",
    "poll_argument": ["path", "job_id"],
    "pending_states": ["queued", "running"],
    "success_states": ["succeeded"],
    "failure_states": ["failed", "cancelled"],
    "interval_seconds": 5,
    "timeout_seconds": 3600
  }
}
```

对应 `jobs.status` 需是可授权的读取能力，并接受 `path.job_id`。只有外部 API **实际**按相同 `Idempotency-Key` 保证重复请求不重复创建作业，才可声明该键。没有这一保证的写操作不能靠框架安全重发；不确定状态需要接入方核对。审批同时要在部署配置中授予相应能力，并由用户按运行返回的调用 ID、参数哈希和 `revision` 作出决定。

## 7. MCP 或自定义 SDK

已有 MCP 服务时，用 `agenstra.mcp-pack.v1` 清单声明 stdio 或 streamable HTTP 连接，只暴露审查过的工具。对每个工具保存 `contract_sha256`；它覆盖完整工具契约，而不是只覆盖工具名。`mcp.go` 的 `MCPContractDigest` 接受工具契约的 JSON 映射并计算哈希。MCP 连接所需 URL、token 或 stdio 环境变量仍由部署配置绑定，不能写明文到包中。

特殊 SDK 可实现 `CapabilityProvider`：提供 `Capabilities()`、`Skills()`、`SystemPrompt()`、`Invoke(ctx, name, arguments, invocationContext)` 和 `Close()`，由调用方应用构造按用户隔离的 `ProviderFactory` 传给 `AgentHost`。Provider 负责对输入和返回做验证，返回结构化 `CapabilityResult` 或安全错误码；不要让原始异常、密钥或不可信响应正文进入模型或审计文本。具体协议类型见 [`providers.go`](../providers.go)，持久 Host 接入见 [`host.go`](../host.go)。

完成接入后，用真实 API 和模型验证授权、结果字段、错误情况、数据是否允许送模型、提交幂等、等待恢复与最终回答；自动化测试只能证明框架边界，不能代替具体场景验收。
