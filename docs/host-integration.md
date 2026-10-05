# 成熟系统接入 Agenstra

宿主负责界面、登录和业务规则；Agenstra 负责会话、上下文、任务、审批和回执。接入的主要工作是把原系统已有操作登记为明确的能力，并用 handler 调用原函数或原接口。不要为 Agent 另写一套业务规则。

框架的基本目标是让已有软件以尽量少的工作接入 Agent：宿主声明、绑定和授权原能力，选择模型与运行策略；框架完成 Agent 特有的协调，并向宿主提供可用的任务状态和执行证据。优化以减少接入代码、减少没有信息增量的模型决策、正确恢复实际执行结果为依据，使用业务断言检验效果。

## 从操作清单开始

1. 保存原项目的工作状态，在单独工作树实现。记录分支、起点和已有修改。
2. 追踪页面按钮、菜单、键盘操作、表单、文件导入、下载和后台 API。建立“原操作 → 能力 → 原执行函数 → 权限 → 验证”的表格。
3. 把读取与写入分开。名单和历史按需分页读取，页面观察只提供当前页面、筛选、实体 ID、数量和业务版本。
4. 在 frontend profile 声明动作输入、输出、effect、审批和 timeout。原后端接口可直接用 REST pack；必须依赖当前浏览器登录、原页面状态或本机下载时，用浏览器 handler 调用原接口和函数。
5. 服务端建立用户到 Agenstra owner 的可信映射。宿主验证原登录身份和同源请求，服务端以长期 API key 换短期 web ticket。长期 key 不进入浏览器。
6. 配置 deployment 的 integration、pack、frontend profile 和用户能力。保持查看用户与管理员原权限差别，写请求还须由原后端核验。
7. 导出 SDK、注册 handler，再接宿主 UI。UI 只显示框架 snapshot、发送用户消息和选择会话 ID，不提供 history/context，也不决定运行检查点。
8. 展示补充输入、精确审批、拒绝、停止和不确定结果。按顺序渲染消息的 `input_history` 中的追问和用户补充，完成或重新进入会话后也保留；尚未回答的追问使用活跃 run 的 `input_prompt`。普通问候和讨论可以直接答复并结束本轮。消息重试保留 client ID；写操作不因超时自动重发。
9. 用虚构数据验证全部操作，包括拒绝、过期版本、抽奖中锁定或对应业务锁定、刷新恢复、文件校验和并发条件。再用真实模型验证自然语言，分别记录结果。
10. 关闭本任务启动的服务、测试容器和浏览器页面；保留用户原有资源。提交源码、能力契约、完整操作映射及可复现命令，排除密钥与数据库。

## SDK 的最小宿主职责

| 宿主需要实现 | 框架已经提供 |
| --- | --- |
| 已验证登录身份到 owner 的映射，票据交换或认证 hook | 票据签名、有效期和扩展路由权限 |
| profile、动作输入与原业务函数的对应 | 能力目录、Schema 校验、审批与命令投递 |
| `getPageObservation` 和 `registerActions` | 页面版本、generation、认领、回执及恢复 |
| 原系统自己的聊天 UI | 聊天/会话接口、队列、消息历史、运行检查点 |
| 文件选择、业务幂等与原后端校验 | 稳定 client/command ID、不重跑已认领 handler |

SDK 在 `web/agenstra-client.js`，导出命令和完整配置见 [Web integration guide](web-integration.md)。headless client 不加载 DOM 或样式；宿主也可单独导入标准聊天组件和服务端票据助手。同一个 SDK 可供不同系统使用，实际页面行为仍由宿主绑定。

## 浏览器 SDK 的接线与恢复

- SDK 可以无 npm 依赖导出 JS 与 TypeScript 声明，并附 SHA-256；宿主构建使用固定字节，升级时明确重新导出。
- SDK 心跳同步手动页面变化，读取或 handler 未改变观察时不增加版本。
- 同一任务多步控制页面时，可反复读取两个内置动态观察能力。业务写操作的重复限制和总预算仍保留。
- 内置观察不要求前置页面观察；一次读取成功后继续实际动作或 Schema 检查，避免递归读取同一页面。
- `AgenstraActionError` 区分已证实的未提交失败；未知写结果仍保持 unknown。
- 没有 `randomUUID` 的 HTTP 页面使用 Web Crypto 生成稳定 ID。
- 聊天消息保留原 run 的 `error_code`，让宿主能解释终止任务的实际原因。

业务版本与浏览器版本须分开命名。例如，宿主用 `activityRevision` 表示原业务 API 的版本，写操作传 `expectedRevision`；`ui.get_context` 外层 `revision` 是浏览器桥版本。文件导入使用真实 preview ID、导入模式与业务版本绑定审批，避免批准后被替换文件。

文件上传仍需要用户选择文件；Agent 可以执行后续检查、模式选择、审批和导入。下载 handler 确认“下载已发起”，不能假定用户电脑的保存位置。聊天 UI 和动作 handler 都应等待可核对的结果后再显示完成。

跨项目任务可通过可选 `sources` 明确选择目标能力，并按发起项目委派、目标验证权限和任务范围取交集。目标身份、实际凭据、版本固定、项目记忆和各入口的完整接入说明见[跨项目任务、身份与授权](cross-project-tasks.md)。所有读取与轮询也必须明确授权。

## 运行进度与运行中补充指令

`GET /runs/{id}/events?after=SEQUENCE&limit=100` 按递增 sequence 返回持久事件。模型决定事件包含 decision_kind 和模型指标；调用结束、等待和停止等事件包含当前状态以及有界 progress。Web 集成沿用 `/web/v1/runs/{id}/events`，保持原有 owner 与 integration 校验。

`POST /runs/{id}/steer` 接收 `{ "request_id": "UUID", "revision": 12, "text": "先完成当前写操作，然后只汇报结果" }`，成功返回 202。request_id 支持响应丢失后的原请求重试，重复 ID 改写文本会冲突；revision 必须是提交时的当前版本。队列最多保留 32 条未处理指令，每条最多 30,000 字符。

指令存入现有事件日志，在安全边界一次性加入 followups。已发出的写操作先记录实际结果；未发出且尚无尝试的调用会标记 steering_superseded，已有审批失效，模型依据新输入重新决定。异步任务继续跟踪，结果不明确的操作继续要求对账。模型返回 final 时的事务检查保证已接受的指令不会被完成检查点覆盖。排队、运行、等待和待审批支持补充；needs_input 使用原有 input 接口，needs_reconciliation 先完成对账。取消与预算仍优先。

## 通用业务调用对账与原运行恢复

响应丢失或不允许重放的调用进入 needs_reconciliation 后，宿主可配置 `AgentHost.Reconciler`（`InvocationReconciler`）。回调收到原 Invocation 的隔离副本、owner、来源项目、目标身份、原幂等键与 capability 契约；应用应通过原幂等键、业务回执或审计记录，只读核验原调用的真实结果。回调返回匹配原输出 schema 的 CapabilityResult，或业务系统已明确证实的失败；查询失败/证据不足应返回 error，不执行原操作。回调应遵守 context 的超时，核验期限为 invocation timeout 与租约一半的较小值。未注册核验器时返回 reconciliation_unavailable（503），运行继续暂停。

`POST /runs/{id}/reconcile` 请求体为：

```json
{
  "invocation_id": "原调用 UUID",
  "arguments_sha256": "原调用日志中的 64 位参数摘要",
  "revision": 12
}
```

客户端不提交成功标志、Fact 或结果数据。Host 核对当前 owner/授权、运行版本、原调用摘要、Pack 指纹与租约，核验前后重新检查权限，再校验结果与异步操作终态/身份。运行中的任务、结果不明的错误和不同操作 ID 均不能解除暂停。浏览器 ui.command_status 绑定继续使用原有已验证 receipt 对账入口。

已核验结果（Fact quality 为 verified_reconciliation）、调用 reconciled 标记、审计事件与原 run 的 queued 状态在一个检查点提交。还有其他不确定调用时仍为 needs_reconciliation；全部解决后，worker 或原有 resume/Drive 入口继续同一个 run，不重放已核验动作。取消始终阻止继续执行。响应丢失可用同一 invocation_id 和摘要重试，即使原 run 已继续执行或完成，也从持久调用日志返回结果。

Web 集成对应 `/web/v1/runs/{id}/reconcile` 与 `client.reconcileInvocation(runId, invocation, revision)`，保留 owner/integration 校验。浏览器动作仍使用 `client.reconcile(commandId, revision)`。对账可靠性取决于宿主接入的权威业务核验；框架提供校验、持久化和恢复机制，不将模型判断或客户端声明当作业务证据。

## 快速接入与诊断

完整接入套件见[快速接入指南](quick-integration.md)。`GET /runs/{id}/diagnostics` 返回归因、恢复建议、调用进度、耗时和模型预算，保持 owner 校验且不触发模型或业务接口。可选部署 `completion_checks` 和 `reconciliation_checks` 见[业务校验配置](completion-evaluation.md)。
