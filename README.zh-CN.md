# Agenstra

语言 / Language：**简体中文** · [English](README.md)

Agenstra 是一个用 Go 实现、可独立部署的通用 Agent 框架。将已有的 REST API、OpenAPI 操作、MCP 工具或自定义 SDK 接入为**受审查的能力包**；框架负责 ReAct 决策、工具执行边界、用户授权、审批、结果证据，以及可恢复的长任务。具体计算和数据仍由所连接的服务负责。

可选的管理入口让管理员通过 CLI 或 Web 页面校验、发布、启用和回滚能力包，并为用户设置连接与授权。托管发布版本按内容哈希固定；新任务使用当前启用版本，已有托管任务继续使用创建时的版本。授权和连接身份仍会实时检查。详见[能力管理指南](docs/capability-management.md)。

可选的 Web 集成提供无 UI 的聊天/会话接口、无框架依赖的 JS SDK、会话消息排队和前端控制桥。框架统一管理 Agent 上下文和检查点恢复，宿主通过会话 ID 选择会话，自行设计聊天 UI，并绑定登录身份、接入业务 API、提供页面观察数据和注册 UI 动作。授权、审批与回执复用现有运行时。详见 [Web 接入指南](docs/web-integration.md)，或运行 `go run ./examples/web-integration` 体验无需模型密钥的本地示例。

内置定时任务支持一次性时间、固定间隔和带 IANA 时区的五字段 Cron。宿主通过公开 Go 方法或需要认证的 `/schedules` HTTP API 管理定义，并通过原有 run 接口跟踪每次执行。定义和历史持久保存，触发时重新检查权限并保留审批要求。详见[定时任务与宿主接入](docs/scheduled-tasks.md)。

内置记忆跨运行保存用户偏好、长期约束和能力包约定。明确的长期默认值立即生效；没有说“以后”，但在三次独立输入中表现出的稳定习惯，也自动成为默认偏好。宿主通过公开 Go 方法、认证 HTTP API 和 Web SDK 查看、修改、遗忘记忆及追溯来源。详见[记忆设计与宿主管理](docs/memory-management.md)。

它可以用于个人工具、团队应用或更大规模的系统。本仓库发布框架、通用测试、部署模板、教程和明确标注的本地演示，**不内置生产业务能力包、外部模型或凭据**。`deploy/deployment.example.json` 是接入模板；直接启动它不会得到一个具备实际能力的 Agent。

## 适用场景与边界

例如一个应用已有搜索、数据转换和通知三个接口。把它们分别声明为能力后，Agent 可以先搜索，从返回的 Fact 读取真实字段，再调用转换接口，最后按任务需要发送通知。框架不硬编码这条路径；接入方决定能力粒度、使用规则和授权策略。

当前持久宿主支持**单节点、持久本地磁盘和 SQLite WAL**。一个部署可以配置多个用户，每个用户有独立的能力授权和连接。分布式高可用、自动保留期清理、真实模型质量和外部计算正确性仍需另行设计或验收。

```mermaid
flowchart LR
    U[用户或调用方应用] --> H[AgentHost\n认证、授权、审批、恢复]
    H --> R[AgentRuntime\nReAct 决策循环]
    R --> B[ExecuteCall\n参数、引用、结果边界]
    P[自有能力包\n契约、技能、执行声明] --> R
    P --> B
    B --> C[CapabilityProvider\nREST / MCP / 自定义]
    C --> E[已有服务和模型]
    H <--> S[SQLiteStore\n状态、调用、Fact、事件]
```

| 组件 | 负责什么 |
| --- | --- |
| `AgentRuntime` | 看任务、能力目录、技能和真实观察；一次决定工具调用、读取技能、检查结果、追问或最终回答。没有固定场景 DAG 或预先计划模式。 |
| `AgentHost` | 按用户保存运行、调用前记录、审批、租约、超时、后台作业轮询、断线恢复及取消。 |
| `ExecuteCall` | 校验能力调用与授权；运行内核将已验证的结果保存为带来源的 Fact。 |
| `CapabilityProvider` | 统一能力、技能、调用和结果接口；内置 REST 与 MCP 连接器，也可实现自定义 Provider。 |
| 能力包 | 明确暴露哪些能力、输入输出契约、使用说明、执行性质、幂等策略和后台作业状态映射；由接入方审查和部署。 |
| `CapabilityRegistry` | 保存经过校验的不可变版本、当前启用版本、用户连接与管理审计；可选启用。 |

更完整的执行与安全边界见[架构说明](docs/architecture.md)。

## 五分钟了解接入流程

1. 选择已有接口。REST 可以手写 `agenstra.rest-pack.v2` 清单，或从 OpenAPI 3.0/3.1 JSON 中**只导入指定的 operationId**；MCP 可以固定选定工具的契约哈希。
2. 审查每项能力的 `effect`（`read` / `compute` / `write` / `destructive`）、JSON Schema、凭据绑定、幂等机制、审批要求及长任务状态。导入器产生的是草稿，不会猜测访问权限。
3. 为 Agent 增加可选的技能文件，说明单位、前提、异常和结果解释；清单记录文件 SHA-256。技能提供语义，不授予权限。
4. 在部署配置中指定包路径、各用户的能力授权、API 凭据环境变量和是否允许把外部数据发送给模型。启动同一份框架代码即可。
5. 用代表性任务验收真实模型选择、授权、错误路径、外部 API 契约和结果质量。

从零创建一个中性的 REST 能力包、添加技能和启动服务，按[完整接入教程](docs/tutorial.md)操作。生产配置、容器部署、状态接口与运维检查见[部署与运维](docs/deployment.md)。

若要在服务运行时管理能力版本与授权，先按[能力管理指南](docs/capability-management.md)启用管理入口，再使用 `agenstra-manage` 或 `/admin`。管理密钥与普通用户 API key 分开配置。Web 和 CLI 还支持共享草稿：分步骤填写、分批导入、保存进度，再校验发布。示例见能力管理指南的“分步编辑共享草稿”。

## 安装与本地运行

需要 Go 1.26+。在仓库根目录执行：

```sh
git clone https://github.com/KHG420/agenstra.git
cd agenstra
go mod download
CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

构建后，`dist/` 中包含四个独立命令。SQLite 存储与 JSON Schema 校验使用纯 Go 库；管理页面及其静态资源嵌入服务端二进制。

| 命令 | 用途 |
| --- | --- |
| `agenstra` | 检查能力目录或执行本地任务。 |
| `agenstra-serve` | 提供 HTTP API、持久化 worker 和可选管理入口。 |
| `agenstra-manage` | 通过管理 API 校验、发布和管理能力版本与授权。 |
| `agenstra-import-openapi` | 将指定 OpenAPI 操作导入为 REST 能力包草稿。 |

REST 与 MCP 支持均包含在 Go 二进制中。按[接入教程](docs/tutorial.md)创建自己的包，并设置清单引用的环境变量后，`agenstra` 命令可先检查能力目录，不调用 LLM：

```sh
go run ./cmd/agenstra \
  --pack local/packs/records/pack.json --inspect
```

其中 `local/packs/records/pack.json` 由你按教程创建，不在仓库内。服务运行还需要：

- `AGENT_MODEL`：模型名称。
- `AGENT_MODEL_BASE_URL`：模型网关基地址，例如 `https://gateway.example/v1`；适配器请求其 `/chat/completions`。
- `AGENT_MODEL_API_KEY`：模型网关密钥。
- 部署配置中指定的每个用户 API key、外部接口地址与凭据环境变量。

默认模型适配器要求 `/chat/completions` 返回 JSON 字符串决策，使用 `response_format: {"type":"json_object"}`。模型必须实际支持这一协议；其他模型可实现 `DecisionModel` 接口接入。准备好自己的 `local/deployment.json` 后启动：

```sh
go run ./cmd/agenstra-serve \
  --config local/deployment.json
```

使用已编译服务时，执行 `./dist/agenstra-serve --config local/deployment.json`。容器构建与持久存储配置见[部署与运维](docs/deployment.md)。

默认监听 `127.0.0.1:8091`。`GET /readyz` 检查数据库和后台 worker 是否就绪。`POST /runs` 只创建任务；worker 自动执行并唤醒等待中的任务。

```sh
curl -sS http://127.0.0.1:8091/runs \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"pack_id":"records","instruction":"查询记录 R-1 并说明结果","request_id":"record-R-1-001"}'
```

同一用户提交相同 `request_id` 和内容会得到同一个运行；相同 ID 配不同内容会冲突。响应含 `run_id`、`status`、`revision` 等状态信息。使用 `GET /runs/{run_id}` 跟踪；完整结果由 `GET /runs/{run_id}/artifacts/{fact_id}` 读取。HTTP 状态和审批示例见[部署与运维](docs/deployment.md)。

## 在线管理能力（可选）

在部署配置中启用 `management` 并设置独立的 `AGENSTRA_ADMIN_API_KEY` 后，可访问 `/admin`，或使用同一管理 API 的 CLI。以下命令要求服务已启动，能力包清单已创建；CLI 默认连接 `http://127.0.0.1:8091`：

```sh
go run ./cmd/agenstra-manage validate local/packs/records/pack.json
go run ./cmd/agenstra-manage publish local/packs/records/pack.json
go run ./cmd/agenstra-manage list
```

发布不会自动启用或授权。从 `list` 结果读取完整内容哈希和当前修订版本，明确启用后，再为用户绑定连接与能力授权；启用旧版本即回滚。新版本只影响新任务，撤销授权则会立即影响已有任务。连接记录保存环境变量或 `secret:NAME` 引用，不保存明文密钥。配置示例、启用与回滚命令、连接检查及备份要求见[能力管理指南](docs/capability-management.md)。

## 能力包支持范围

| 来源 | 已提供的接入方式 | 导入后仍需审查 |
| --- | --- | --- |
| REST JSON | GET/POST/PUT/PATCH/DELETE；路径、查询、请求头、请求体绑定；嵌套 JSON Schema、状态码和服务错误码。 | 端点、认证、`effect`、重试/幂等、结果含义。 |
| OpenAPI 3.0/3.1 JSON | 选择 `operationId`，转成 REST v2 草稿；支持本地 schema 引用及显式 bearer 环境变量。 | 生成的契约、未支持的序列化/认证、技能、审批、后台作业映射。 |
| MCP | stdio 与 streamable HTTP；分页发现、选定工具、输入输出校验、已审查契约哈希。 | 运行命令/端点、工具作用、引用有效期、幂等与授权。 |
| 自定义 SDK | 实现 `CapabilityProvider` 协议，由应用提供用户级连接工厂。 | SDK 的身份隔离、输入输出验证和执行保证。 |

OpenAPI 导入命令示意（把路径和 operationId 换成自己的）：

```sh
go run ./cmd/agenstra-import-openapi \
  --spec local/openapi.json \
  --out local/packs/records/pack.json \
  --name records \
  --base-url-env RECORDS_API_URL \
  --token-env RECORDS_API_TOKEN \
  --operation records.get
```

导入命令**不会**据接口名字推断审批、幂等性或后台任务。写操作和长期任务的声明请按[接入教程](docs/tutorial.md)补充和验证。

## 运行与数据保证

- Host 在外部调用前保存调用 ID、确切参数和哈希。对于失败后结果不确定的调用，只有声明为安全或真正可幂等重放的能力才能按同一 ID 恢复；其他情况进入 `needs_reconciliation`，由接入方核对真实外部状态。
- 被批准的是特定用户、特定调用和参数哈希；提交前会重新检查身份、权限及租约。技能文本不能跳过这些检查。
- 完整工具结果作为 Fact 单独持久化；上下文预算保留任务约束和 Fact 身份，优先呈现近期证据，并明确标记遗漏内容供按路径检查。工具传参仍可通过 Fact 引用获取完整原值，详见[上下文管理设计](docs/context-management.md)。Fact ID 是框架本地证据 ID，不等于外部资源 ID。
- 对声明了 `OperationBinding` 的后台作业，Host 保存外部作业回执并轮询状态接口；HTTP 请求成功或返回 `queued` 不代表任务完成。
- 运行、事件、Fact 和续接接口按 `owner_id` 隔离；一个部署可配置多个用户及不同能力集合。

取消只停止本地编排，不承诺撤销已提交到外部系统的作业。SQLite WAL 适合当前单节点范围；请使用持久磁盘并制定备份与数据保留策略。

## 从 Python 版本切换

Go 版本保留部署 JSON 格式、能力包清单、HTTP 路由与响应格式，以及 SQLite v1 运行数据库结构。兼容性测试覆盖读取并继续执行 Python 版本创建的运行，包括已保存的 Fact、请求 ID 和 REST 契约指纹。

先备份数据库并停止 Python worker，再使用相同部署配置与持久数据库路径启动 Go 服务；启动命令改为上面的 Go 命令。自定义 Python `CapabilityProvider` 和 `DecisionModel` 实现需要移植到对应的 Go 接口。

## 开发验证

```sh
go test -race ./...
go vet ./...
go build ./cmd/...
```

测试使用临时生成的 REST/MCP 契约、模型替身和 SQLite；不需要场景能力包或真实外部服务。发布前仍应在目标环境验收真实身份、模型决策、接口契约、长任务以及运维条件。当前实现的取舍和上线前检查见[架构说明](docs/architecture.md)与[部署与运维](docs/deployment.md)。
