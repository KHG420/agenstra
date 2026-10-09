# Agenstra 源码拆包与重构交接

Agenstra 已完成参考 reasonix studio 目录组织的两轮重构。第一轮把根目录实现移入 `internal/` 并建立 `sdk/`；本轮进一步拆除了集中式 `internal/runtime/engine`，将契约、ReAct、Host、存储、注册表、适配器、部署装配和 HTTP/Web 服务分成独立 Go 包。公开 Go SDK 直接转发到各模块的实际所有者。

这份文档描述最终实现、验证范围和接手入口。更新时间为 **2026 年 10 月 10 日，Asia/Shanghai**。接手会话应先核对当前 Git 状态，再按用户给出的新任务继续。

## 1. 任务要求与版本

用户要求参考 reasonix 的 studio 源码目录格式重构本项目的 `main`，并明确：“只要功能不变，功能的接入形式不重要。”用户随后要求提交并 push，最后要求先完成进一步拆包，再交接。

因此本次允许调整 import、源码目录和必要的跨包接入方法；保持执行结果、授权、审批、恢复、回执、持久数据、HTTP/JSON、能力清单和 Web SDK 行为。没有新增生产依赖或数据库迁移，没有为旧根包建立兼容入口。

| 项目 | 版本或位置 |
| --- | --- |
| 本地仓库 | `/Users/aq/Documents/ChatGPT/agenstra` |
| 远端仓库 | [KHG420/agenstra](https://github.com/KHG420/agenstra) |
| 目标分支 | `main` |
| 重构前提交 | `4a03613dcf40ddae911f4b1a915b8b846f72026f` |
| 第一轮已推送提交 | [a84bb30](https://github.com/KHG420/agenstra/commit/a84bb304e639ac7e61236e04ef214f2a49b22a09) |
| 第一轮提交说明 | `refactor: organize source into kernel and SDK directories` |
| 本轮提交说明 | `refactor: split engine into owned runtime and service packages` |
| 本文所属提交 | 接手时执行 `git log -1 --format='%H %s' -- docs/refactor-handoff.md` 获取；本文与本轮代码一起提交。 |
| 参考源码 | `/Users/aq/Documents/ChatGPT/reasonix` 的 `origin/studio` |
| 参考版本 | `cdfe244aa3a09f1867562bf30627fbea3384593b`，第一轮读取的本地远端跟踪引用。 |

参考的是 `internal/` 按职责分组、`sdk/` 独立接入、命令与部署分离的组织方式。具体包边界依据 Agenstra 的真实符号依赖与状态所有权确定。

## 2. 最终源码结构

```text
agenstra/
├── cmd/                          # agenstra、serve、manage、evaluate、import-openapi
├── internal/
│   ├── assembly/deployment/      # 部署、可信身份、连接绑定、模型选择
│   ├── base/
│   │   ├── cron/                 # 日历、时区和 DST
│   │   └── jsonvalue/            # 规范 JSON、复制和严格解码
│   ├── contract/
│   │   ├── agent/                # 共享值契约、校验和决策协议
│   │   └── schema/               # JSON Schema 编译和校验
│   ├── ext/
│   │   ├── capability/           # 可信 REST/MCP 包加载及 Provider
│   │   ├── openapi/              # OpenAPI 草稿转换
│   │   └── packfiles/            # 技能文件和发布目录核验
│   ├── frontend/
│   │   ├── admin/                # 开发者控制台静态资源、JS 测试
│   │   └── service/              # HTTP、worker、聊天和浏览器桥接
│   ├── platform/
│   │   ├── mcptransport/         # stdio/HTTP/SSE 交换、取消和清理
│   │   └── modelapi/             # 模型 HTTP 协议、测量、重试和提取
│   ├── runtime/
│   │   ├── react/                # 决策、上下文、引用和调用证据
│   │   └── host/                 # 授权、审批、租约、检查点和恢复
│   └── state/
│       ├── runstore/             # 运行、回执、产物、记忆和定时任务
│       └── registry/             # 发布、启用、绑定、草稿和模型配置
├── sdk/
│   ├── go/                       # 公开 Go SDK，包名 agenstra
│   └── web/                      # client、chat、session 及类型声明
├── examples/web-integration/     # 本地宿主接入演示
├── deploy/                       # Docker 和部署模板
└── docs/                         # 接入、架构和运维文档
```

各目录包含实际实现、所属测试和包文档。旧 `internal/runtime/engine/` 已移除。完整生产依赖图及基础模块边界见[架构文档](architecture.md#源码模块与依赖方向)。

| 模块 | 主要所有权 | 生产依赖约束 |
| --- | --- | --- |
| `contract/agent` | 值契约、配置形状、纯校验与决策协议。 | 只依赖基础 JSON、日历与 Schema；不持有 Host、数据库、连接或凭据。 |
| `runtime/react` | 临时决策、上下文预算、引用、完成校验、`ExecuteCall` 和调用证据。 | 不导入 Host、Store、部署或 HTTP。 |
| `runtime/host` | 当前授权、审批、租约、checkpoint、等待/恢复、学习和派发。 | 依赖 react、runstore 和契约；不导入部署、模型适配器、注册表或前端。 |
| `state/runstore` | 既有 SQLite 运行表、fencing、调用日志、产物、记忆和定时事务。 | 不调用模型、Provider 或业务授权。 |
| `state/registry` | 不可变发布、启用版本、连接绑定、草稿、模型修订和原子审计。 | 私有数据库由所属操作访问，装配层不借用它执行 SQL。 |
| `platform/modelapi` | 模型消息、JSON/tool 输出、输入测量、重试、取消和用量。 | 不导入 Host、注册表或部署，不解析宿主凭据。 |
| `ext/capability` | REST/MCP 清单、技能、请求/响应契约和连接清理。 | 不读 Host、SQLite 或 live grants，不编排业务流程。 |
| `assembly/deployment` | 可信身份、凭据引用、模型目录、连接和业务回调装配。 | 组合实际所有者，执行由 Host 负责。 |
| `frontend/service` | HTTP、joined worker、WebStore、聊天、浏览器命令和确认。 | 调用同一 Host；WebStore 只拥有可选集成表。 |

HTTP、聊天和浏览器桥接保留在同一个 frontend service 包中，因为它们共同拥有集成身份、页面 generation、命令确认、队列和可选 Web 表。运行状态仍由 Host/runstore 持有；没有另建第二条执行路径。

## 3. 为形成真实边界所做的调整

**Host 与具体模型管理器解耦。** Host 在使用方定义小接口 `runModelSelector`，只需要 `Snapshot()` 和 `SelectModel(ModelConfiguration)`。部署层的 `ModelManager` 实现接口。Host 创建运行时保留实际使用的决策、记忆 profile；恢复读取 `effective_config` 中的冻结选择，实时模型目录修改不重定向已有任务。

**模型配置事务归注册表。** `ModelSelection` 读取持久选择；`SaveModelConfiguration` 校验配置、生成规范 JSON、核对持久 revision，并在同一事务保存配置和审计。返回的 snapshot 由保存字节独立解码，ModelManager 持有自己的配置。HTTP 通过 `Initialized` 判断连接归属，初始化失败只关闭本次打开的注册表连接。

**前置调用校验可以跨包识别。** 原私有方法改为 `InvocationValidator.ValidateInvocation`，实际实现与调用位置同步。它仍只检查前置条件；审批后执行继续重新核验 live 状态。能力适配器和跨项目/浏览器包装的原有执行、并发声明和清理路径保持一致。

**内部状态操作归实际所有者。** Host 对外组合层提供必要的授权、源绑定、恢复和核对方法；runstore 提供 owner 查询、事务、记忆、定时和 steering 操作。共享值契约没有 Host 或 SQLite 对象。模型 request metrics 的 Context key 保留在适配器私有实现中，观察回调通过契约函数传递。

**SDK 直接连接所有者。** `sdk/go` 不再统一引用一个 engine 包；值类型 alias 到契约，资源类型 alias 到其拥有包，函数转发到实际实现。第一轮的 178 个公开顶层声明全部保留。这个数量不包括方法和字段，跨包所需方法的可见性已有调整。

**测试按职责迁移。** 原 engine 的 350 个顶层测试全部保留，没有缺失或重复。混合纯校验与清单验证的投影测试拆出一项独立测试；新增 Host 模型选择接口测试，验证实时配置修改和 Host 重建后仍使用冻结选择。测试辅助替身随所属测试包保存，跨模块测试放在实际组合层。

## 4. 行为与资源边界

本次没有调整下列契约的字段、状态或存储布局：

- HTTP 路由、owner 隔离、管理认证和业务认证边界。
- REST/MCP 清单格式、输出投影、技能摘要、能力契约固定和拒绝漂移。
- 原 invocation ID、参数摘要、幂等键，以及 `safe`、`idempotent`、`never` 重放性质。
- 审批绑定、lease/fencing、等待作业、取消与不确定结果核对。
- 完整 Fact 的持久保存、有界模型预览及证据引用。
- 运行 SQLite v1、可选 Web 表、注册表和模型配置 revision。
- 记忆 owner/pack 隔离、快照失效、独立输入计数和定时派发事务。
- 模型预算、格式恢复、重试、用量证据和冻结运行设置。

`NewSQLiteStore` 仍只打开连接，调用方必须执行 `Initialize()`。持久运行的完整 Fact 通过 `artifact_ids` 指向产物；不要假设 `run.State["runtime"]` 内联包含完整 facts，也不要为方便测试改变存储表示。

`HTTPServer.Close` 仍先停止并等待 worker 和在途工作，再关闭可选集成资源。调用方提供的 run store 和部署 registry 仍由调用方管理。Provider、模型请求、MCP 进程和连接沿用既有清理路径。本轮没有启动浏览器会话或 Docker 测试容器。

## 5. 接入与构建

Go 接入路径保持第一轮确定的 SDK 入口：

```go
import agenstra "github.com/KHG420/agenstra/sdk/go"
```

旧根包 `github.com/KHG420/agenstra` 已在第一轮移除。外部宿主使用 SDK；内部包的可见性服务于框架组合，宿主无需逐包接线。

npm 包名仍为 `@agenstra/web`，client/chat/session 导出、浏览器类型声明和打包内容保持第一轮结果。Web 源码和开发命令位于 `sdk/web`，管理静态资源由 `internal/frontend/admin` 嵌入，客户端资源由 `sdk/web` 嵌入。

```sh
npm ci --prefix sdk/web
make check
```

工具版本按 `go.mod`；本机使用 Go 1.26.3、Node.js 24.15.0 和 golangci-lint v2.12.2。CI 使用仓库配置的 Go、Node.js 22 和同一 `make check`。

## 6. 验证记录与本机限制

包含当前项目源码的干净临时副本已通过原始 **`make check`**，退出码为 0。复制来源包括全部当前版本管理文件及本轮新增文件；没有修改 Makefile、CI 或检查范围。副本安装了锁定的 Web 开发依赖，并使用独立 Go / golangci-lint 缓存，避免先前已删除临时目录的源码位置缓存污染结果。

| 检查 | 实际结果 |
| --- | --- |
| `gofmt` 与 `git diff --check` | 通过。 |
| `go vet ./...` | 通过；拆包后 42 处跨包结构体位置初始化改为对应字段名。 |
| golangci-lint v2.12.2 | `0 issues.`，没有增加 lint 排除或禁用。 |
| Web / 管理资源 / 示例 ESLint | 通过。 |
| `go test -race ./...` | 所有项目 Go 包通过，包括授权、租约、审批、核对、持久恢复及公开 SDK 测试。 |
| JavaScript 测试 | 131 项全部通过，失败、取消和跳过均为 0。 |
| `go build ./cmd/...` | 五个命令构建通过。 |
| 导出与迁移核对 | SDK 的 178 个顶层导出完整保留；原 engine 的 350 个顶层测试无缺失或重复；六份原测试数据字节内容一致。 |
| 依赖人工检查 | Host 不导入部署、模型适配器、registry 或前端；react、modelapi、capability、runstore 的生产依赖符合本节包边界；codegraph 已同步。 |

完整门禁日志位于本机 `/tmp/agenstra-stage2-clean-check.log`；物理工作区失败日志位于 `/tmp/agenstra-stage2-workspace-check.log`。临时源码副本和专用缓存已清理，日志保留供复核。

常规测试使用本地替身、`httptest` 和临时 SQLite；真实模型评测保留显式 opt-in，本轮没有请求真实模型或业务系统。

本机工作区存在 Git 忽略的 `local/` 实验程序。它们仍 import 第一轮已经移除的旧根包；直接工作区 `make check` 的 `go vet ./...` 会因此失败，例如：

```text
local/sub2api-soak/continuous_users/native-rest-framework-probe.go:18:2:
no required module provides package github.com/KHG420/agenstra
```

这些用户实验文件未修改、删除或排除。完整项目门禁应在包含当前所有版本管理源码及本轮新增文件的干净临时副本中执行，初始化 Git 后安装锁定的 Web 开发依赖，再运行原始 `make check`。这是为了核验实际可提交源码；不能把它写成物理工作区检查通过。临时副本验收后清理。

## 7. 接手调查入口

先阅读 [AGENTS.md](../AGENTS.md)、[编码规范](coding-standards.md) 和[架构边界](architecture.md)。调查代码先用 codegraph；不可用时按项目指令尝试初始化并记录失败原因后回退。

| 任务 | 实现入口 | 相关验证 |
| --- | --- | --- |
| 决策、上下文、引用、完成检查 | `internal/runtime/react/runtime.go`、`context.go`、`inspection.go`、`completion_consistency.go` | react 的 runtime/context/completion/inspection 测试。 |
| 共享契约与配置校验 | `internal/contract/agent/contracts.go`、`providers.go`、`model.go`、`deployment.go` | 契约、投影、Schema 与配置测试。 |
| 授权、审批、恢复、回执 | `internal/runtime/host/host.go`、`reconciliation.go`、`projects.go` | host 的授权、核对、回执、并发与恢复测试。 |
| 冻结运行模型 | `internal/runtime/host/model_selection.go`、`internal/assembly/deployment/model_selection.go` | Host 接口边界测试及部署模型选择/重启测试。 |
| SQLite、记忆、定时与 steering | `internal/state/runstore/`，上层调用在 `internal/runtime/host/` | 存储 owner/fencing、Host 记忆/定时/steering 测试。 |
| 发布、启用、草稿、配置 revision | `internal/state/registry/registry.go`、`drafts.go` | 不可变发布、并发 revision、失败恢复和管理 HTTP 测试。 |
| REST/MCP/技能/可信包加载 | `internal/ext/capability/loader.go`、`rest.go`、`mcp.go`、`skills.go` | 契约、响应边界、协议启动及 OpenAPI 集成测试。 |
| 模型协议、输入测量、重试 | `internal/platform/modelapi/` | JSON/tool framing、预算、用量、取消和重试测试。 |
| 可信身份与连接装配 | `internal/assembly/deployment/deployment.go`、`host_auth.go`、`deployment_checks.go` | 身份、凭据引用、源权限和核对回调测试。 |
| HTTP、worker、管理与 Web | `internal/frontend/service/server.go`、`admin.go`、`web_http.go`、`web_integration.go`、`browser_bridge.go`、`chat.go` | 服务、owner、浏览器 generation、丢失确认、聊天和 worker shutdown 测试。 |
| 外部 Go 接入 | `sdk/go/` | `sdk/go/integration_test.go`，完整证据与 owner 隔离。 |

旧 engine 文件的不同声明可能分到多个拥有包，不能按旧文件名假设整份实现仍集中在一处。优先查符号与调用方；各包 `doc.go` 说明状态和资源归属。

本次结构重构已覆盖实际实现、接线、测试和文档；接手会话按用户的新任务继续，不需要重新做一轮目录搬迁或补空包。真实外部服务的容量、凭据、模型质量和远端幂等保证仍需对应部署任务的验收证据。

## 8. 可直接复制给接手会话

> 请先阅读 Agenstra 的 `docs/refactor-handoff.md` 和适用的 `AGENTS.md`。main 已完成参考 reasonix studio 的目录重构及进一步拆包，旧 `internal/runtime/engine` 已移除；契约、ReAct、Host、runstore、registry、REST/MCP、模型协议、部署装配和 HTTP/Web 服务均有实际包边界。Go 接入路径为 `github.com/KHG420/agenstra/sdk/go`。确认当前 Git 状态，不覆盖已有修改，调查先使用 codegraph，再按我接下来给出的具体任务工作。验证时区分已提交项目源码与本机 `local/` 的旧根包实验程序；不要修改数据库表示、回执或授权语义来方便测试。
