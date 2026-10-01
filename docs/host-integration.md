# 成熟系统接入 Agenstra：实施顺序与年会抽奖参考

宿主负责界面、登录和业务规则；Agenstra 负责会话、上下文、任务、审批和回执。接入的主要工作是把原系统已有操作登记为明确的能力，并用 handler 调用原函数或原接口。不要为 Agent 另写一套业务规则。

## 从操作清单开始

1. 保存原项目的工作状态，在单独工作树实现。记录分支、起点和已有修改。
2. 追踪页面按钮、菜单、键盘操作、表单、文件导入、下载和后台 API。建立“原操作 → 能力 → 原执行函数 → 权限 → 验证”的表格。
3. 把读取与写入分开。名单和历史按需分页读取，页面观察只提供当前页面、筛选、实体 ID、数量和业务版本。
4. 在 frontend profile 声明动作输入、输出、effect、审批和 timeout。原后端接口可直接用 REST pack；必须依赖当前浏览器登录、原页面状态或本机下载时，用浏览器 handler 调用原接口和函数。
5. 服务端建立用户到 Agenstra owner 的可信映射。宿主验证原登录身份和同源请求，服务端以长期 API key 换短期 web ticket。长期 key 不进入浏览器。
6. 配置 deployment 的 integration、pack、frontend profile 和用户能力。保持查看用户与管理员原权限差别，写请求还须由原后端核验。
7. 导出 SDK、注册 handler，再接宿主 UI。UI 只显示框架 snapshot、发送用户消息和选择会话 ID，不提供 history/context，也不决定运行检查点。
8. 展示补充输入、精确审批、拒绝、停止和不确定结果。消息重试保留 client ID；写操作不因超时自动重发。
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

SDK 在 `web/agenstra-client.js`，导出命令和完整配置见 [Web integration guide](web-integration.md)。Agenstra 不带聊天 DOM、样式、React/Vue 组件或业务页面驱动器；同一个 SDK 可供不同系统使用。

## 年会抽奖的实际接法

这次在 Agenstra 的 `codex/lottery-agent-integration` 与抽奖项目的 `codex/agent-integration` 工作树完成接入。框架起点为 main `e89023a`；抽奖起点为 `5210c90`，已有用户工作保存为 `ca73831`。抽奖实际应用位于内层 `lottery-ui` 仓库，原工作区保留。

抽奖宿主使用 React/Vinext、Cloudflare Worker 和 D1。已有业务执行链为 `LotteryClient → /api/activity → applyCommand`。浏览器 handler 复用此链，因此仍用当前浏览器身份，服务端仍校验管理员、活动版本、抽奖锁定、人员/奖项配额和中奖身份。Agenstra 服务独立运行，通过宿主受限同源代理访问。pack 只提供服务健康读取与模型指导，29 个宿主动作覆盖业务及页面操作。

抽奖项目的 `agent/contracts.mjs` 是能力源，脚本导出 frontend、pack 和本机 deployment；`lib/agent-actions.ts` 对应原业务及 UI port；`components/lottery-agent.tsx` 是该宿主自己的界面。完整步骤、全部操作映射和启动命令保存在抽奖仓库的 `docs/agent-integration.md`。这些宿主文件不进入框架核心。

## 本次反哺框架的通用修正

- SDK 可以无 npm 依赖导出 JS 与 TypeScript 声明，并附 SHA-256；宿主构建使用固定字节，升级时明确重新导出。
- SDK 心跳同步手动页面变化，读取或 handler 未改变观察时不增加版本。
- 同一任务多步控制页面时，可反复读取两个内置动态观察能力。业务写操作的重复限制和总预算仍保留。
- 内置观察不再被提示为需要前置页面观察的宿主动作；一次读取成功后继续实际动作或 Schema 检查，避免递归读取同一页面。
- `AgenstraActionError` 区分已证实的未提交失败；未知写结果仍保持 unknown。
- 没有 `randomUUID` 的 HTTP 页面使用 Web Crypto 生成稳定 ID。
- 聊天消息保留原 run 的 `error_code`，让宿主能解释终止任务的实际原因。

业务版本与浏览器版本须分开命名。抽奖用 `activityRevision` 表示 `/api/activity` 的版本，写操作传 `expectedRevision`；`ui.get_context` 外层 `revision` 是浏览器桥版本。文件导入使用真实 preview ID、导入模式与业务版本绑定审批，避免批准后被替换文件。

文件上传仍需要用户选择文件；Agent 可以执行后续检查、模式选择、审批和导入。下载 handler 确认“下载已发起”，不能假定用户电脑的保存位置。聊天 UI 和动作 handler 都应等待可核对的结果后再显示完成。
