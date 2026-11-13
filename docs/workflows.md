# 持久工作流

宿主用受信任的定义组合已有能力与 Agent 节点。Agenstra 负责依赖调度、检查点、异步等待、当前授权、精确审批、恢复和取消。业务数据、算法、分析规则、PDF 生成与下载授权由宿主能力提供。

工作流保存在既有 run checkpoint 中，使用同一个 Host、worker、租约、调用日志和 SQLite / PostgreSQL 存储。宿主在启动服务前配置 `AgentHost.Workflows`，独立服务也可在部署 JSON 中配置 `workflows`，以 pack ID 为键。每次新运行冻结定义、版本、输入、能力目录与模型配置。修改配置影响后续新运行。

## 宿主声明

```go
host.Workflows = map[string][]agenstra.WorkflowDefinition{
    "reports": {{
        Name: "regional-report", Version: "1", Description: "生成区域报告",
        InputSchema: agenstra.JSON{
            "type": "object",
            "properties": agenstra.JSON{"region": agenstra.JSON{"type": "string"}},
            "required": []string{"region"}, "additionalProperties": false,
        },
        Steps: []agenstra.WorkflowStep{
            {ID: "collect", Capability: "reports.start", Arguments: agenstra.JSON{
                "region": agenstra.JSON{"$workflow": "/input/region"},
            }},
            {ID: "pdf", DependsOn: []string{"collect"}, Capability: "reports.pdf", Arguments: agenstra.JSON{
                "id": agenstra.JSON{"$workflow": "/steps/collect/data/id"},
            }},
        },
        Output: agenstra.JSON{"file": agenstra.JSON{"$workflow": "/steps/pdf/data"}},
        Answer: "报告已生成。",
    }},
}
run, err := host.CreateWorkflow(ctx, owner, "reports", "regional-report",
    agenstra.JSON{"region": "region-a"}, requestID)
```

`requestID` 必填，同一 owner 的重复请求返回同一运行。变更工作流名、pack 或输入后复用该 ID 会返回 `request_id_conflict`。提交只保存并排队，业务调用由既有 worker 执行。嵌入式宿主通过原有 `WakeDue` 驱动，也可使用启用 worker 的 `NewHTTPServer`。

节点的 `capability` 与 `agent` 二选一。`depends_on` 声明 DAG 依赖，引用其他节点必须声明直接或传递依赖。独立工具节点按现有 `MaxConcurrentTools` 和 provider 的并发声明执行，单次最多四个。当前实现按就绪节点分批推进。

`arguments`、`output` 和 `answer` 支持显式 JSON Pointer 引用。工具节点输出从其 Fact 的 `/data` 读取，Agent 节点默认输出为 `{"answer_markdown":"..."}`。工具引用保持完整 Fact 身份，审批或重启后执行仍检查引用有效期、连接与 `model_output` 可见范围。引用输入中的 `$fact_id`、`$fact_value` 保留为框架关键字，工具参数拒绝将其作为普通输入对象使用。

`when` 比较一个解析后的值与 JSON 字面量。条件不满足时节点为 `skipped`，其引用值为 `null`。依赖该节点的后续节点仍会检查自己的条件。

```json
{
  "id": "export",
  "depends_on": ["collect"],
  "when": {"value": {"$workflow": "/input/export_pdf"}, "equals": true},
  "capability": "reports.pdf",
  "arguments": {"id": {"$workflow": "/steps/collect/data/id"}}
}
```

## 异步业务作业

在已有能力的 `OperationBinding` 声明回执与查询能力，工作流直接复用 Host 的异步处理。路径相对于 `CapabilityResult.Data`。

```go
Operation: &agenstra.OperationBinding{
    IDPath: []any{"id"}, StatusPath: []any{"status"},
    PollCapability: "reports.status", PollArgument: []string{"id"},
    PendingStates: []string{"queued", "running"},
    SuccessStates: []string{"completed"}, FailureStates: []string{"failed", "cancelled"},
    IntervalSeconds: 2, TimeoutSeconds: 390, ReconcileOnTimeout: true,
},
```

等待期间保存原作业 ID 并释放运行槽位，轮询消耗 `MaxPollCalls`。确定性节点和等待阶段不调用模型。重启后沿原调用与回执恢复。`replay=idempotent` 要求宿主接口真实支持相同幂等键，无法确认结果的写操作使用原有 reconciliation 流程。

普通 Agent 的 `workflows.start` 进入结果核对时，由框架按原 invocation 定位已创建的子工作流，核对归属、父运行及终态，再接回原调用。核对沿用当前授权与原参数摘要。业务节点的外部结果继续由宿主核对器核验。

工作流取消会停止本地调度并请求取消其 Agent 子运行。远端作业的取消由宿主显式提供业务能力，已提交操作的真实回执继续保留。

## Agent 节点

```json
{
  "id": "analyze",
  "depends_on": ["collect"],
  "agent": {
    "instruction": "依据所给证据生成报告摘要",
    "capabilities": [],
    "output_schema": {
      "type": "object",
      "properties": {"summary": {"type": "string"}},
      "required": ["summary"],
      "additionalProperties": false
    }
  },
  "arguments": {"report": {"$workflow": "/steps/collect/data"}}
}
```

Agent 节点创建普通的持久 run。它继承当前账号、固定发布与模型配置、工作流剩余预算，并将工具范围与 `capabilities` 取交集。空列表表示只分析给定输入。子运行使用现有的模型包装、计费、审批、追问和完成校验。模型用量归属实际子运行，工作流预算汇总其消耗。

存在 `output_schema` 时，Agent 最终回答必须为满足该 schema 的 JSON 对象，后续节点直接引用该对象字段。缺省时引用 `/steps/analyze/answer_markdown`。宿主按业务需要核实分析内容与正式产物。

父运行等待 Agent 时释放 worker 槽位，`MaxConcurrentRuns=1` 也可推进。Agent 子节点使用显式传入的 `arguments`，宿主应把所需历史、记忆与业务数据作为适合该任务的输入。工作流定义与初始输入在运行中固定，用户可通过既有接口回答 Agent 子运行的追问。工作流本体的 `steer` 返回 `workflow_input_immutable`。

## 权限与 Agent 调用

宿主沿用现有 `PolicyResolver` 或部署连接的授权配置，按需要授予 `workflows.list`、`workflows.start`、`workflows.status`、`workflows.cancel`，同时授权各业务节点与异步查询能力。创建时检查全部节点声明的权限，实际执行、轮询与恢复时重新检查当前授权。目录按可用节点权限过滤。

启用工作流的普通 Agent 通过以下框架能力发现并执行受信任定义。

| 能力 | 输入与行为 |
| --- | --- |
| `workflows.list` | `{}`，返回可用名称、版本、说明和输入 schema |
| `workflows.start` | `{"workflow":"regional-report","input":{"region":"region-a"}}`，持久创建后由 Host 轮询 |
| `workflows.status` | `{"run_id":"..."}`，读取本人工作流 |
| `workflows.cancel` | `{"run_id":"..."}`，请求取消本人工作流 |

模型及普通用户通过这些接口选择已有定义。定义由宿主配置，工具节点和 Agent 节点的工具列表均禁止 `workflows.*`，限制嵌套创建范围。通过普通 Agent 发起的工作流是独立的受限 run，其内部 Agent 节点共享该工作流剩余预算。

## HTTP 与 Web SDK

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/workflows?pack_id=reports` | 授权目录 |
| POST | `/workflows/runs` | 提交并返回 202 |
| GET | `/workflows/runs/{id}` | 当前进度与输出 |
| POST | `/workflows/runs/{id}/cancel` | 请求取消并返回 202 |
| POST | `/workflows/runs/{id}/resume` | 恢复授权后重新排队，返回 202 |

创建请求示例

```json
{"pack_id":"reports","workflow":"regional-report","input":{"region":"region-a"},"request_id":"report-request-1"}
```

Web 会话票据使用对应的 `/web/v1/workflows...` 路径，并在每次请求携带 `integration_id`。frontend profile 别名默认继承其业务 pack 的定义，宿主可显式配置该别名的独立目录。身份来自当前登录 owner，请求中的未声明字段会被拒绝。

```js
const queued = await client.startWorkflow('regional-report', { region: 'region-a' }, { requestId: 'report-request-1' });
const current = await client.getWorkflow(queued.run_id);
await client.cancelWorkflow(queued.run_id);
// 权限恢复后按用户动作重新排队
await client.resumeWorkflow(queued.run_id);
```

`WorkflowView` 返回 `run_id`、名称、版本、状态、节点进度、输出与最终回答。发生阻塞时，通过 `blocked_run_id` 和 `blocked_state` 定位需要处理的原有 run。宿主展示该 run 的追问、精确审批或结果核对操作，使用既有 `/runs/{id}/input`、`approval`、`reconcile` 接口。Web 对应 `/web/v1/runs/{id}/...`，保持 owner 隔离。授权暂停使用工作流 `resume`，其他阻塞使用相应原接口。

## 边界与验收

每个 pack 最多 32 个定义，每个定义最多 64 个节点、256 KiB，单次输入最多 256 KiB。引用模板展开累计最多 1 MiB，并继续受 Host 状态、产物、调用、模型和运行时长预算约束。JSON Pointer 最多 18 段，匹配 Fact 的路径最多 16 段。工作流状态使用既有 checkpoint，数据库表结构沿用当前版本。

文件节点返回宿主现有文件或报告的受权引用、名称和 MIME 类型，文件生成与下载继续走原引擎。微信宿主可以把该引用交给框架现有 `WeChatClient` 文件发送能力。需要检查当前账号的导出权限和真实投递回执。

运行自包含示例，覆盖异步节点、Agent 子运行与 PDF 元数据节点。示例使用本地业务与模型替身，输出用于演示的文件元数据。

```sh
go run ./examples/workflow
go test -race ./internal/runtime/host ./internal/frontend/service -run '^TestWorkflow'
node --test sdk/web/workflow-client.test.js
```
