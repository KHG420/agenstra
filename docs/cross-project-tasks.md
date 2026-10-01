# 跨项目任务、身份与授权

一个宿主项目拥有一个能力包，包负责声明该项目经过审查的能力、参数和返回契约、技能、使用说明、效果、重放与幂等约束、审批要求、异步操作协议。用户凭据、用户权限和用户记忆通过宿主连接管理，不能写进包。Web 前端 profile 是既有包的浏览器适配视图。

一个任务保留发起项目的 `pack_id`。宿主可以为该任务明确选入其他项目的少量能力；这形成任务的组合视图，不合并项目的能力包。框架继续使用同一套执行、Fact、审批、重试和恢复机制。

## 选定任务范围

Go 宿主使用 `AgentHost.CreateWithSources`；原来的 `Create` 只选择发起项目。HTTP `POST /runs` 示例：

```json
{
  "pack_id": "travel",
  "instruction": "查一下我所在城市今天的天气",
  "request_id": "weather-2026-10-01",
  "sources": [
    {"pack_id": "location", "capabilities": ["location.current"]},
    {"pack_id": "weather", "capabilities": ["weather.query"]}
  ]
}
```

`sources` 使用目标包的本地能力名。模型目录中的对应名称是 `location::location.current` 和 `weather::weather.query`；发起包原有名称保持不变。模型先调用位置接口，取得 Fact，再以 `$fact_value` 将其城市字段交给天气接口。目标技能也使用 `pack::skill` 名称，目标项目使用说明通过 `pack::$project` 技能按需加载，只适用于该项目的操作。

来源包 ID 使用现有管理注册表的 ID 格式；每个任务最多 8 个来源、每个来源 1–100 项能力。重复来源、重复能力、空范围、把发起包再次列为来源或提供已经带 `::` 的本地名都会被拒绝。能力和技能的模型决策名称上限为 330 字节，以容纳项目限定名。异步提交能力的轮询能力必须同时列入范围。

任务保存目标主体、范围和版本。管理注册表中的目标 release 在任务创建时固定；新版本不替换进行中的任务。静态清单沿用原有机制：首次执行保存组合指纹，后续恢复时发现清单或连接身份变化会拒绝继续；宿主应保持静态文件不变，或者使用管理发布版本。相同 `request_id` 重放会检查发起包、指令和完整来源范围，不能借重放添加、替换或移除来源。

## 张三在 B 中是谁

框架先认证发起任务的用户，再查该用户在目标包中的连接。连接由可信宿主或管理 API 配置；浏览器和模型不能传入目标账号、角色、token 或“管理员”声明。

- 使用统一 SSO 时，目标身份接口可以返回同一个稳定主体 ID。
- 项目拥有独立账号体系时，宿主建立经过验证的用户到目标账号绑定，例如框架用户 `zhangsan` 映射到 B 的 `b-user-42`，并为他提供 B 的用户凭据或受限委派凭据。
- 没有目标连接、映射不匹配、权限响应无效或身份服务不可用时，拒绝调用。不要自动使用服务管理员的连接兜底。

目标连接示例：

```json
{
  "environment": {
    "B_API_URL": "B_URL",
    "B_API_TOKEN": "ZHANGSAN_B_TOKEN"
  },
  "binding_environment": [],
  "granted_capabilities": ["weather.query"],
  "approval_capabilities": [],
  "allow_model_data": true,
  "identity": {
    "url_env": "B_IDENTITY_URL",
    "token_env": "ZHANGSAN_B_TOKEN",
    "expected_subject": "b-user-42",
    "subject_path": ["sub"],
    "capabilities_path": ["capabilities"]
  }
}
```

B 的身份/权限接口接收 `Authorization: Bearer <该用户在 B 的凭据>`，返回：

```json
{"sub": "b-user-42", "capabilities": ["weather.query"]}
```

`capabilities_path` 指向严格的字符串数组；空数组表示没有能力。返回值与本地 `granted_capabilities` 取交集。缺少这个配置的旧单包连接仍可运行，但不能作为经过验证的跨项目来源。响应错误、错误主体、缺失或畸形权限数组都会拒绝验证；身份查询不跟随重定向。

REST v2 清单的 `token_env` 必须通过 `environment` 映射到 `identity.token_env` 的**同一个凭据引用**。Streamable HTTP MCP 同样校验 `source.token_env`。因此不会出现身份查询使用张三 token，实际接口却使用管理员 token 的配置。凭据值不会进入模型目录、任务或审计。

第一版内置验证支持 REST v2 Bearer 和 Streamable HTTP MCP Bearer。REST 来源同时配置 `headers_env` 时，框架无法证明额外静态头不会切换身份，因此不接受这类来源；这类集成可由可信自定义 Provider 负责验证绑定。stdio MCP、旧 REST 和无法证明主体绑定的其他鉴权方式不能作为跨项目来源。嵌入式自定义 Provider 可实现 `BoundSubject() string`；可信工厂必须保证它是实际调用凭据的主体，并且与策略返回的主体一致。

## 普通任务不会继承管理员所有能力

发起项目为用户配置委派上限，例如 travel 连接中：

```json
{"delegations": {"location": ["location.current"], "weather": ["weather.query"]}}
```

跨项目有效授权是四者交集：

```text
发起连接的 delegations
∩ 目标连接的 granted_capabilities
∩ 目标身份接口返回的当前能力权限
∩ 本次任务 sources 的明确范围
```

即使张三在 B 本身是管理员，普通天气任务也只暴露和调用上述范围中的查询能力。读取、计算、写入、删除及轮询全部需要逐项授权，`effect: read` 不再隐含授权。未授权能力从模型目录和能力检查中隐藏；执行入口也再次拒绝，所以模型直接猜名称不能绕过限制。

每次模型请求、工具执行、重试、审批和轮询都检查当前策略；组合 Provider 在实际转发前再检查一次。目标主体必须继续等于任务冻结的主体。撤销委派、目标权限、模型数据授权或账号绑定后，任务进入 `needs_authorization`，不继续调用。审批只同意一个确切调用和参数，不能补授本来没有的权限。

B 的业务后端仍须按实际凭据执行行、资源和租户权限检查。框架负责正确主体、任务能力范围与凭据绑定；接口是否允许访问某条订单由 B 决定。权限接口应由 B 的权威服务提供，并与业务接口一致。已经完成的调用和已获取的审计证据不会因撤权被回滚；并发撤权后的最终拒绝也由 B 在处理请求时执行。

## 记忆与上下文

记忆继续按认证用户隔离。张三在 A 中形成的项目报告语言偏好属于 `scope: pack`、A 的 `pack_id`，李四不会读到它。用户明确表达的个人默认偏好可以使用 `scope: user`，仍只属于该用户。

任务使用发起项目的个人默认和项目约定；加入其他项目时，只读取同一用户在这些来源项目中的 active 项目约定，每条投影携带 `pack_id`。来源约定只指导该项目的接口操作，发起项目决定整体报告风格；不能通过来源约定改变权限。来源权限全部被撤销后，后续模型请求也移除该来源的记忆和已加载技能。

首次执行冻结记忆；后续调用过滤已更正或遗忘的版本。自动学习只读当前用户的原始消息，写入发起项目，工具返回、来源技能和会话历史不变成新的用户习惯。明确长期规则立即生效，多次稳定习惯在 3 次独立输入后形成默认；单次临时要求不写入长期记忆。

宿主管理接口见[记忆管理](memory-management.md)：Go CRUD、`/memories`、Web ticket 下的 `/web/v1/memories` 和 SDK CRUD/历史接口都保留用户认证、项目边界和修订检查。

## 现有入口

- Go：`CreateWithSources`、`SubmitMessageWithSources`、`CreateBrowserRunWithSources`；旧方法等价于无来源。
- HTTP：`POST /runs`、`POST /chat/v1/conversations/{id}/messages`、`POST /browser/v1/runs` 均接受可选 `sources`。
- SDK：`client.send(text, {clientId, sources})`、`client.run(instruction, {requestId, sources})`。省略来源时保留既有请求格式。
- 定时任务：`ScheduleRequest.sources` 在创建、更新、恢复和每次触发时检查；每次触发固定当时发布版本。权限被撤销时暂停定义并记录原因。
- 管理 API/CLI：连接 JSON 增加 `delegations` 和 `identity.capabilities_path`；管理页面的高级连接选项也支持配置。

执行审计记录 `owner_id`、`origin_pack_id`、`target_pack_id`、`target_subject`、`target_release`。Fact 记录 `source_pack_id`、`source_subject`、`source_release`，保持跨项目结果来源可追溯。

## 接入与验证

已有部署应逐项补齐读取和异步轮询的授权；浏览器适配器显式授予当前项目的 `ui.get_context` 与 `ui.command_status`，其宿主动作仍由配置的 BrowserActions 授权。包自身和技能都不能授予权限。

回归测试覆盖：读取管理员能力拒绝与隐藏、三层委派与目标权限交集、错误/缺失主体映射、远程权限故障、用户凭据隔离、凭据引用不一致、运行中撤权、审批/重试/轮询、来源版本和请求重放、项目记忆隔离与遗忘、HTTP/聊天/浏览器/定时入口，以及真实 `httptest` REST 的“位置 → Fact → 天气”链路。测试使用本地替身，不调用真实用户服务或付费模型。
