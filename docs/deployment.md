# 部署与运维

本仓库提供单企业、单节点的 HTTP Host 和 Docker Compose 配方。需要企业自己提供模型网关、业务 API/MCP 服务、受审查的能力包、用户密钥和持久存储。本页说明实际运行边界；没有真实企业服务与模型时，仓库测试通过不等于生产验收通过。

## 1. 配置关系

```text
deploy/deployment.example.json     可公开的部署结构模板，不含真实能力包
deploy/deployment.env.example      环境变量名称模板，不含真实密钥
deploy/config/deployment.json      你自己的部署配置；Git 忽略
deploy/deployment.env              你自己的密钥和地址；Git 忽略
deploy/capability-packs/           你自己的能力包与技能；Git 忽略
```

`DeploymentConfig` 将 `pack_id` 映射到受信任清单路径，再为每个用户配置 `api_key_env`、连接变量、`granted_capabilities`、`approval_capabilities` 和 `allow_model_data`。配置文件相对路径以配置文件所在目录为基准；容器模板使用 `/opt/packs/business/pack.json` 绝对路径。用户的 Bearer key 只识别当前用户，业务凭据仍通过连接映射注入 Provider。

可以为连接添加可选 `identity` 配置，使用企业身份查询接口在执行和恢复前验证主体；字段为 `url_env`、`token_env`、`subject_path` 和可选 `expected_subject`。真实身份接口由企业提供并验收。授权和身份变化会影响已有运行继续执行，不会只在创建时检查一次。

## 2. 本地服务

按[教程](tutorial.md)创建忽略提交的 `local/packs/business/pack.json` 与 `local/deployment.json`，在环境中提供模型变量、用户 API key 和企业 API 凭据，然后：

```sh
uv sync --locked --extra server --extra mcp
uv run --locked --extra server enterprise-agent-serve \
  --config local/deployment.json
```

默认监听 `127.0.0.1:8091`。需要暴露给其他系统时，应放在企业的 TLS、认证和网络访问控制之后。服务提供 `GET /healthz` 进程检查和 `GET /readyz` 数据库及 worker 就绪检查；业务接口需要 `Authorization: Bearer <用户 API key>`。模型适配器要求配置 `AGENT_MODEL`、`AGENT_MODEL_BASE_URL`、`AGENT_MODEL_API_KEY`，并使用兼容 `/chat/completions` 的 JSON 决策协议。

## 3. Docker Compose

Compose 模板只挂载用户自己准备的配置与包。按教程完成本地包后，可将其复制到容器挂载目录，并编辑环境变量文件：

```sh
mkdir -p deploy/config deploy/capability-packs/business
cp deploy/deployment.example.json deploy/config/deployment.json
cp deploy/deployment.env.example deploy/deployment.env
cp local/packs/business/pack.json deploy/capability-packs/business/pack.json
cp -R local/packs/business/skills deploy/capability-packs/business/
```

这里假设教程中已经创建 `skills` 目录。检查 `deploy/config/deployment.json` 的能力名称、用户授权、包路径是否与复制的包一致；编辑 `deploy/deployment.env` 填入真实值。不要把真实文件提交到 Git。容器中的 `127.0.0.1` 指向容器本身，`BUSINESS_API_URL` 和模型网关地址要从容器网络实际可达。

```sh
docker compose -f deploy/compose.yaml config
docker compose -f deploy/compose.yaml up -d --build
docker compose -f deploy/compose.yaml ps
curl -sS http://127.0.0.1:8091/readyz
```

Compose 默认把服务只映射到宿主机 `127.0.0.1:8091`，使用 `agent_data` 持久卷保存 `/data/agent.sqlite3`，并运行一个 Agent 服务实例。`deploy/Dockerfile` 安装锁定依赖，默认以非 root 用户运行。生产环境可改用受控的持久卷、密钥注入和入口网关，但保持单节点 SQLite 拓扑；不要仅通过增加 Compose 副本数来宣称高可用。

## 4. HTTP 运行接口

以下命令使用已配置的 `AGENT_OPERATOR_API_KEY`，`RUN_ID` 替换为创建响应的 `run_id`：

```sh
curl -sS http://127.0.0.1:8091/runs \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"pack_id":"business","instruction":"查询 R-1 并解释状态","request_id":"R-1-first"}'

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

`revision` 必须取自**刚读取**的当前运行响应，示例中的 `3` 只是占位值。若状态是 `needs_approval`，从当前响应的 `state.runtime.pending` 找到待审批项，在业务人员看到确切能力和参数后提交：

```sh
curl -sS -X POST "http://127.0.0.1:8091/runs/$RUN_ID/approval" \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"invocation_id":"从 pending 读取","arguments_sha256":"从同一 pending 读取的 64 位哈希","revision":3,"approved":true}'
```

审批有有效期；过期后先刷新运行状态并按新的请求处理。不能只凭工具名或旧参数哈希批准。API 的 401 是 Bearer key 无效；403 是当前身份/策略拒绝；409 包括修订版本或执行状态冲突；503 可能是授权、连接或就绪问题。应用应根据返回的安全错误码处理，不猜测远端调用是否发生。

| 运行状态 | 建议处理 |
| --- | --- |
| `queued` / `running` | 继续轮询状态或事件；不要重复提交同一业务操作。 |
| `needs_input` | 显示 `input_prompt`，按当前 `revision` 补充字段。 |
| `needs_approval` | 展示待调用的参数、风险和来源，按当前调用 ID/哈希/版本作决定。 |
| `waiting` | 企业后台作业仍在进行；由 worker 按包内 `OperationBinding` 查询。 |
| `needs_authorization` | 修复企业身份或授权后，再对同一运行调用 `resume`。 |
| `needs_reconciliation` | 外部调用结果不确定或无法安全重试；先核对企业系统的真实状态，再决定新任务或取消本地任务。 |
| `completed` / `failed` / `cancelled` | 终态；读取回答、错误码、事件和必要的完整 Fact。 |

`GET /runs` 与 `GET /runs/{id}` 都按当前用户权限控制；即使用户知道别人的运行 ID 也不能据此读取其状态或 Fact。运行响应本身可能含敏感参数和观察，调用方应按企业数据级别保护。

## 5. 上线前检查

1. 用真实业务服务和模型跑代表性多接口任务，核对输入单位、结果字段、错误码、引用路径和最终回答；测试审批拒绝、权限撤销与密钥轮换。
2. 对写操作在企业服务端验证幂等行为；模拟响应丢失、Host 重启和作业等待恢复。仅有清单声明不构成幂等保证。
3. 确认模型服务可以接收被授权的数据；将 API key、企业凭据和数据库置于企业的密钥管理、加密、网络和审计策略之下。
4. 使用持久磁盘和 SQLite 在线备份机制，定期恢复演练；制定 Fact、运行事件和数据库保留期。本版本没有自动 TTL 清理。
5. 根据容量和故障目标验证单节点资源、worker 延迟、模型/接口限流。需要多节点高可用时须另行设计共享状态、调度与存储，不能直接复制当前 SQLite 部署。

框架测试、格式与类型检查命令见[README](../README.md)。
