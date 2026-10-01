# 部署与运维

本仓库提供单节点 HTTP Host 和 Docker Compose 配方。部署方提供模型网关、外部 API/MCP 服务、受审查的能力包、用户密钥和持久存储。本页说明实际运行边界；没有真实外部服务与模型时，仓库测试通过不等于目标场景验收通过。

需要在线发布和回滚能力时，按[能力管理指南](capability-management.md)启用可选管理入口。运行数据库和管理注册表使用独立 SQLite 文件；持久备份必须覆盖两者以及已发布版本目录。

## 1. 配置关系

```text
deploy/deployment.example.json     可公开的部署结构模板，不含真实能力包
deploy/deployment.env.example      环境变量名称模板，不含真实密钥
deploy/config/deployment.json      你自己的部署配置；Git 忽略
deploy/deployment.env              你自己的密钥和地址；Git 忽略
deploy/capability-packs/           你自己的能力包与技能；Git 忽略
```

`DeploymentConfig` 将 `pack_id` 映射到受信任清单路径，再为每个用户配置 `api_key_env`、连接变量、`granted_capabilities`、`approval_capabilities` 和 `allow_model_data`。配置文件相对路径以配置文件所在目录为基准；容器模板使用 `/opt/packs/records/pack.json` 绝对路径。用户的 Bearer key 只识别当前用户，服务凭据仍通过连接映射注入 Provider。

可以为连接添加可选 `identity` 配置，使用身份查询接口在执行和恢复前验证主体；字段为 `url_env`、`token_env`、`subject_path` 和可选 `expected_subject`。身份接口由部署方提供并验收。授权和身份变化会影响已有运行继续执行，不会只在创建时检查一次。

## 2. 本地服务

按[教程](tutorial.md)创建忽略提交的 `local/packs/records/pack.json` 与 `local/deployment.json`，在环境中提供模型变量、用户 API key 和外部 API 凭据，然后：

```sh
go mod download
go run ./cmd/agenstra-serve \
  --config local/deployment.json
```

默认监听 `127.0.0.1:8091`。需要暴露给其他系统时，应放在适当的 TLS、认证和网络访问控制之后。服务提供 `GET /healthz` 进程检查和 `GET /readyz` 数据库及 worker 就绪检查；运行接口需要 `Authorization: Bearer <用户 API key>`。模型适配器要求配置 `AGENT_MODEL`、`AGENT_MODEL_BASE_URL`、`AGENT_MODEL_API_KEY`，并使用兼容 `/chat/completions` 的 JSON 决策协议。

## 3. Docker Compose

Compose 模板只挂载用户自己准备的配置与包。按教程完成本地包后，可将其复制到容器挂载目录，并编辑环境变量文件：

```sh
mkdir -p deploy/config deploy/capability-packs/records
cp deploy/deployment.example.json deploy/config/deployment.json
cp deploy/deployment.env.example deploy/deployment.env
cp local/packs/records/pack.json deploy/capability-packs/records/pack.json
cp -R local/packs/records/skills deploy/capability-packs/records/
```

这里假设教程中已经创建 `skills` 目录。检查 `deploy/config/deployment.json` 的能力名称、用户授权、包路径是否与复制的包一致；编辑 `deploy/deployment.env` 填入真实值。不要把真实文件提交到 Git。容器中的 `127.0.0.1` 指向容器本身，`RECORDS_API_URL` 和模型网关地址要从容器网络实际可达。

```sh
docker compose -f deploy/compose.yaml config
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps
curl -sS http://127.0.0.1:8091/readyz
```

如果构建环境无法稳定访问默认的 Go 模块代理，可先运行 `docker compose -f deploy/compose.yaml build --build-arg GOPROXY=https://goproxy.cn`，再用 `docker compose -f deploy/compose.yaml up -d --no-build` 启动。模块版本仍由 `go.mod` 和 `go.sum` 固定。

Compose 默认把服务只映射到宿主机 `127.0.0.1:8091`，使用 `agent_data` 持久卷保存 `/data/agent.sqlite3`，并运行一个 Agent 服务实例。`deploy/Dockerfile` 构建静态 Go 服务，默认以非 root 用户运行。生产环境可改用受控的持久卷、密钥注入和入口网关，但保持单节点 SQLite 拓扑；不要仅通过增加 Compose 副本数来宣称高可用。

## 4. HTTP 运行接口

以下命令使用已配置的 `AGENT_OPERATOR_API_KEY`，`RUN_ID` 替换为创建响应的 `run_id`：

```sh
curl -sS http://127.0.0.1:8091/runs \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"pack_id":"records","instruction":"查询 R-1 并解释状态","request_id":"R-1-first"}'

curl -sS "http://127.0.0.1:8091/runs/$RUN_ID" \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY"
```

| 端点 | 用途 |
| --- | --- |
| `POST /runs` / `GET /runs` | 创建任务、列出当前用户有权访问的任务。相同用户、相同 `request_id` 和内容返回同一任务。 |
| `GET /runs/{run_id}` | 读取状态、`revision`、追问或待审批调用。 |
| `POST /runs/{run_id}/input` | 提交被请求的 `field`、`text` 和当前 `revision`。 |
| `POST /runs/{run_id}/approval` | 以当前 `revision`、`invocation_id`、`arguments_sha256` 和 `approved` 批准/拒绝确切调用。 |
| `POST /runs/{run_id}/resume` | 在授权恢复或需要主动推进时执行到下一暂停点；可能较久。后台 worker 通常会推进 `queued` / `waiting`。 |
| `POST /runs/{run_id}/cancel` | 请求取消本地编排；不会证明远端作业已撤销。 |
| `GET /runs/{run_id}/events?after=0&limit=100` | 分页获取运行事件。 |
| `GET /runs/{run_id}/artifacts/{fact_id}` | 读取完整、带来源的 Fact。ID 可从运行状态中的 `artifact_ids` 获取。 |

如果状态是 `needs_input`，读取响应中 `state.runtime.input_field` 和 `input_prompt`，再提交：

```sh
curl -sS -X POST "http://127.0.0.1:8091/runs/$RUN_ID/input" \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"field":"record_id","text":"R-1","revision":3}'
```

`revision` 必须取自**刚读取**的当前运行响应，示例中的 `3` 只是占位值。若状态是 `needs_approval`，从当前响应的 `state.runtime.pending` 找到待审批项，在审批者看到确切能力和参数后提交：

```sh
curl -sS -X POST "http://127.0.0.1:8091/runs/$RUN_ID/approval" \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"invocation_id":"从 pending 读取","arguments_sha256":"从同一 pending 读取的 64 位哈希","revision":3,"approved":true}'
```

审批有有效期；过期后先刷新运行状态并按新的请求处理。不能只凭工具名或旧参数哈希批准。API 的 401 是 Bearer key 无效；403 是当前身份/策略拒绝；409 包括修订版本或执行状态冲突；503 可能是授权、连接或就绪问题。应用应根据返回的安全错误码处理，不猜测远端调用是否发生。

| 运行状态 | 建议处理 |
| --- | --- |
| `queued` / `running` | 继续轮询状态或事件；不要重复提交同一外部操作。 |
| `needs_input` | 显示 `input_prompt`，按当前 `revision` 补充字段。 |
| `needs_approval` | 展示待调用的参数、风险和来源，按当前调用 ID/哈希/版本作决定。 |
| `waiting` | 外部后台作业仍在进行；由 worker 按包内 `OperationBinding` 查询。 |
| `needs_authorization` | 修复身份或授权后，再对同一运行调用 `resume`。 |
| `needs_reconciliation` | 外部调用结果不确定或无法安全重试；先核对外部系统的真实状态，再决定新任务或取消本地任务。 |
| `completed` / `failed` / `cancelled` | 终态；读取回答、错误码、事件和必要的完整 Fact。 |

`GET /runs` 与 `GET /runs/{id}` 都按当前用户权限控制；即使用户知道别人的运行 ID 也不能据此读取其状态或 Fact。运行响应本身可能含敏感参数和观察，调用方应按数据敏感级别保护。

定时任务通过同样的用户 Bearer key 访问 `/schedules`。后台 worker 自动创建到期任务的 run，再沿用上述状态和审批接口推进执行；一次性、间隔、Cron 时区、启停、历史、重启补执行及重叠规则见[定时任务与宿主接入](scheduled-tasks.md)。运行数据库备份同时包含定时定义和触发历史。

## 5. 上线前检查

1. 用真实外部服务和模型跑代表性多接口任务，核对输入单位、结果字段、错误码、引用路径和最终回答；测试审批拒绝、权限撤销与密钥轮换。
2. 对写操作在外部服务端验证幂等行为；模拟响应丢失、Host 重启和作业等待恢复。仅有清单声明不构成幂等保证。
3. 确认模型服务可以接收被授权的数据；将 API key、服务凭据和数据库置于适用的密钥管理、加密、网络和审计策略之下。
4. 使用持久磁盘和 SQLite 在线备份机制，定期恢复演练；制定 Fact、运行事件和数据库保留期。本版本没有自动 TTL 清理。
5. 根据容量和故障目标验证单节点资源、worker 延迟、模型/接口限流。需要多节点高可用时须另行设计共享状态、调度与存储，不能直接复制当前 SQLite 部署。

## 6. 从早期版本升级

Go 版本继续使用 v1 SQLite 表结构、现有部署 JSON 和静态能力包配置；仓库测试包含从旧数据库恢复运行的样例。升级前备份运行数据库以及管理注册表和发布目录，并在目标环境验证未完成运行。启用管理注册表后，新任务固定启用版本；管理 API 不提供删除发布版本的操作。v0.3 及更早版本清单仍需更新后重新审查，未完成运行没有自动跨版本迁移机制。

框架测试、格式与类型检查命令见[中文版 README](../README.zh-CN.md)。
