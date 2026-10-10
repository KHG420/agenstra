# 让现有软件接入 Agent

默认采用独立服务：原软件保留登录、业务权限与业务数据库；Agenstra 负责模型决策、上下文、任务执行、审批和恢复。宿主通过 HTTP 提交任务，Agenstra 通过 REST 或 MCP 调用已经授权的业务接口。只有页面函数需要接入的 Web 软件可以使用纯浏览器模式，无需额外提供占位 REST/MCP 能力包。Go 软件也可将运行库嵌入原进程。

## 使用开发者的 coding agent

开发者可使用 Codex、Claude Code 或 DeepSeek Harness 完成接入。本仓库提供 [agenstra-integrate-host Skill](../.agents/skills/agenstra-integrate-host/SKILL.md)，指导 agent 调查宿主的原操作、生成契约、绑定已有业务函数和身份、完成本地验收。它复用本页的 CLI/SDK，不需要启动第二个 coding agent，也不改变框架运行内核。

从选定的 Agenstra 版本复制整个 `.agents/skills/agenstra-integrate-host/` 目录（包含 `references/`）到宿主仓库：

| Coding agent | 宿主内的 Skill 目录 | 调用方式 |
| --- | --- | --- |
| Codex | `.agents/skills/agenstra-integrate-host/` | `$agenstra-integrate-host` |
| Claude Code | `.claude/skills/agenstra-integrate-host/` | `/agenstra-integrate-host` |
| DeepSeek Harness | `.agents/skills/agenstra-integrate-host/` 或 `.dsh/skills/agenstra-integrate-host/` | 在会话中明确要求使用该 Skill，并确认当前技能目录已加载 |

Skill 只使用通用的 `name`、`description` frontmatter 和相对引用；无需覆盖宿主原有 `AGENTS.md`、`CLAUDE.md` 或全局设置。目标目录已有同名 Skill 时先审查版本差异，保留宿主已有定制。安装后检查 agent 的技能目录或显式调用，确认加载的是所选版本；自动发现与调用受 agent 的版本和设置影响。加载路径依据 [Codex 文档](https://learn.chatgpt.com/docs/build-skills)、[Claude Code 文档](https://code.claude.com/docs/en/skills) 和 [DSH skill-filesystem 文档](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/skill/skill-filesystem/README.md)；DSH 尚处于 developer preview，使用前核对本地插件与版本。

可直接交给 agent 的任务示例：

> 使用 agenstra-integrate-host Skill，把 Agenstra 接入当前 Web 应用的指定业务操作。使用我选定的 Agenstra 源码目录和版本，复用原登录、权限、业务函数与测试设施，生成真实接入代码并完成本地验收；说明测试结果和未验证部分。

Skill 提供工作流指导；接入是否完成仍由宿主测试、真实业务证据和框架验收工具判定。MCP 可在后续需要统一工具接口时封装这些现有工具；当前 Skill 可以直接通过 agent 的 shell 使用它们。开发期指导与运行期能力包内的业务技能分开维护。

## 1. 启动服务

按[部署指南](deployment.md)准备模型网关、持久盘和 `deployment.json`，启动 `agenstra-serve --config deployment.json`。需要接入管理界面时，配置 `management`；需要聊天入口时，配置 `web_integration.chat`。页面动作另行开启 `browser_bridge`。

模型和业务凭据由部署方配置。当前运行范围是单节点和 SQLite WAL。服务端账户映射可使用静态用户 API key，也可配置 `host_auth` 验证宿主已有登录凭证并取得稳定 owner ID。动态用户通过管理绑定获得授权，无需逐个修改静态用户配置；宿主票据助手负责换票，授权仍由服务端绑定决定。

## 2. 连接与选择能力

打开 `/admin`，用管理员密钥连接，创建 REST 或 MCP 草稿。

- REST：配置服务地址、凭据的变量名称，上传 OpenAPI 3.0/3.1 JSON，勾选操作。导入器自动生成输入输出契约；没有文档的接口可手工填写。
- MCP：配置 HTTP 服务或本地 stdio 进程，点击“连接并发现工具”，勾选受支持工具后导入。发现只执行初始化和 `tools/list`，关闭发现连接，不调用工具。完整工具契约的 SHA-256 自动生成；缺少结构化输出契约的工具会显示原因。
- 根据真实业务确认读写影响、执行前确认和重复请求保证。MCP 新发现工具默认按写操作处理、需要确认、不能自动重放；远端 annotations 不会自动放宽这些设置。
- 填写用途、必要前提、单位和成功标准等业务说明。高级契约与长任务配置仍可展开编辑。

发布后，继续在向导中选择已有用户、逐项勾选授权、确认模型数据使用范围，填写凭据引用，点击“启用版本、保存授权并检查连接”。发布、启用、授权沿用现有 API 和审计记录。步骤失败时会明确显示已经完成的部分；启用版本会影响该包的新任务。

“检查连接”验证契约和连接配置能够加载；REST 尚未执行实际接口。使用下一步的代表性任务完成业务验收。能力升级时导入新版契约、检查差异并发布新版本；旧任务继续遵守原有版本固定规则。

纯浏览器接入跳过后端能力包创建，准备第 5 步的 frontend profile。在 `web_integration.integrations.<integration>` 只设置 `frontend_profile_path`，省略 `pack_id`，并开启 `browser_bridge`。同一 integration 名下的 `users.<owner>.packs.<integration>` 配置 `allow_model_data`、`granted_capabilities` 和可选 `approval_capabilities`；不需要再填写 `browser_actions`。用户身份、模型数据使用和动作授权仍须明确配置，见[纯浏览器部署示例](web-integration.md#纯浏览器宿主)。

使用动态登录或管理授权时，执行 `agenstra-manage bind <owner> <integration> browser-policy.json` 保存相同的浏览器策略，无需发布占位能力包。组合接入分别绑定后端 pack 与浏览器 integration，两边均设置 `allow_model_data: true`；动作授权按 integration 隔离。完整示例见[浏览器管理绑定](web-integration.md)。

## 3. 接入宿主登录

浏览器通过宿主的 `/api/agent-session` 换取短期 ticket。长期用户 API key 留在宿主服务端。可使用 `@agenstra/web/session` 的标准 Request/Response 适配助手：

```js
import { createAgenstraSessionHandler } from "@agenstra/web/session";

export const agentSession = createAgenstraSessionHandler({
  endpoint: "http://agenstra:8091",
  verifyRequest: request => hostCSRF.verify(request),
  authenticateRequest: async request => (await hostSessions.verify(request))?.userId ?? null,
  resolveAPIKey: userId => hostSecrets.agentKeyFor(userId)
});
```

`hostCSRF`、`hostSessions` 和 `hostSecrets` 是原软件已经存在的服务，需绑定为真实实现。`verifyRequest` 必须验证原软件的 CSRF/可信来源，`authenticateRequest` 必须验证登录会话，不能使用请求体自报的用户 ID。助手处理 POST 限制、超时、上游错误、禁止凭据随重定向转发，并只返回 `{token, expires_at}`。

非 JavaScript 宿主使用同样的 HTTP 过程：验证登录与 CSRF → 从可信服务端映射用户 API key → `POST /web/v1/token` → 只向浏览器返回短期票据。Go 嵌入式宿主也可使用 `AuthenticateRequest` 和 `MintSession`。详细说明见[Web 接入指南](web-integration.md)。

启用 `host_auth` 时，`resolveAPIKey` 可返回宿主已验证的用户 bearer 凭证；Agenstra 通过配置的可信 HTTP 身份接口验证每次换票，动态 owner 仍须有对应绑定。该接口的 `owner_path` 默认是 `owner_id`。

## 4. 装入标准聊天入口

可安装本仓库构建的 npm 包，或固定 SDK 到宿主 vendor 目录：

```sh
npm pack ./sdk/web
node sdk/web/export-client.mjs /path/to/host/vendor/agenstra
```

包支持 `@agenstra/web/client`、`@agenstra/web/chat` 和宿主服务端专用的 `@agenstra/web/session`。导出目录包含对应 JS、类型声明和 SHA-256 清单。npm 发布由项目维护者另行执行。

```js
import { createAgenstraClient } from "/agent/web/assets/agenstra-client.js";
import { mountAgenstraChat } from "/agent/web/assets/agenstra-chat.js";

const client = createAgenstraClient({
  endpoint: "/agent", integration: "orders", browser: false,
  getSession: async () => {
    const response = await fetch("/api/agent-session", {
      method: "POST", credentials: "same-origin",
      headers: hostCSRF.headers()
    });
    if (!response.ok) throw new Error("Session unavailable");
    return response.json();
  }
});
const chat = mountAgenstraChat(document.querySelector("#agent"), {
  client, title: "订单助手", locale: "zh-CN",
  subtitle: "查询和处理已授权的订单",
  emptyTitle: "需要处理什么订单？",
  emptyHint: "例如：查询本月待处理订单，说明哪些需要我确认。",
  placeholder: "输入订单问题或操作要求"
});
```

组件提供发送、进度、确认、补充输入、取消、核对、恢复权限和读取诊断。回答支持表格、有序/无序列表、加粗、行内代码和围栏代码块；这是安全的 Markdown 子集，HTML、链接、图片及其他语法保留为文本。所有内容通过 DOM 文本节点构建，模型 HTML 不会执行。文案也可通过 custom element 的 `subtitle`、`empty-title`、`empty-hint`、`placeholder` 属性设置。CSS 变量 `--agenstra-accent`、`--agenstra-height` 等可匹配宿主主题。组件使用 Shadow DOM；严格限制内联样式的宿主应配置合适的样式策略。

组件不拥有传入的 client。卸载时调用 `chat.unmount()` 解除观察。优先由应用或登录会话持有 client，让路由切换后仍可执行任务；若 client 随路由组件卸载，调用 `client.destroy({ closeSession: false })`，保留浏览器绑定供下次挂载恢复。默认 `client.destroy()` 会永久关闭绑定，用于退出登录、切换用户或明确结束使用，不能当作普通路由卸载。组件未导入时，headless client 不加载 DOM、CSS 或 UI 依赖。生命周期与存储限制见[Web 接入](web-integration.md#宿主使用无-ui-的-sdk)。

会话创建与选择使用 `client.createConversation()`、`listConversations()` 和 `selectConversation(id)`；[完整 Go 示例](../examples/web-integration/README.md)已复用正式组件，仍使用明确标注的演示数据和固定模型。

## 5. 绑定已有页面函数

只有需要操作页面时才提供 frontend profile 和 handler。同一份 profile 可生成类型和注册模板：

```sh
node sdk/web/export-client.mjs /path/to/host/vendor/agenstra
node sdk/web/export-actions.mjs frontend-profile.json /path/to/host/vendor/agenstra
```

生成器先检查版本、动作名称/重复/保留名、影响类型、超时、字段和受支持的 Schema 引用位置，发现接线错误时在写文件前报错。检查通过后生成 `agenstra-actions.d.ts`、版本文件 `agenstra-profile.js` 及配套的 `agenstra-profile.d.ts`；首次运行还会创建 `agenstra-handlers.js`。修改 handlers，把函数绑定到原系统；重复生成会更新类型和版本，保留业务 handler 文件。未绑定的模板明确返回 `handler_not_implemented`。

```js
import { actions } from "./vendor/agenstra/agenstra-handlers.js";
import { handlerVersion } from "./vendor/agenstra/agenstra-profile.js";
// 将 handlerVersion 传入 createAgenstraClient 的 options。
client.registerActions(actions);
```

生成的类型和接线检查是开发辅助，不是完整 JSON Schema 编译或业务验收；复杂 Schema 中无法精确表达的类型保留 `unknown`，服务端完整契约校验仍是运行依据。权限、业务版本、幂等保证和确定失败/不确定结果的区分由原业务函数提供。契约与 handler 语义变更时更新 profile 版本并重新发布。

## 6. 验收与诊断

管理页的“试运行与任务诊断”使用接入用户的 API key 创建真实任务、查看调用和模型预算、处理确认与补充输入。管理员密钥不代替用户权限。诊断也可通过 `GET /runs/{id}/diagnostics` 和 `client.getRunDiagnostics(id)` 读取；它不调用业务接口或模型。

诊断保留历史错误，同时用 `recovered` 和 `actionable` 区分已恢复记录与待处理问题。标准组件将明确恢复的错误标为“已恢复”，不再提示当前恢复操作。相同调用或相同参数的后续明确成功可以证明恢复；不确定写入仍须核对原命令，不能用另一条相似命令的成功掩盖。

重复验收使用 `agenstra-evaluate`。先准备以下 JSON 数组，替换能力名、任务和业务证据路径：

```json
[
  {
    "name": "查询指定订单",
    "pack_id": "orders",
    "instruction": "查询订单 1001 的状态",
    "required_capabilities": ["orders.get"],
    "forbidden_capabilities": ["orders.delete"],
    "facts": [{"capability": "orders.get", "path": ["data", "id"], "value": "1001"}]
  },
  {
    "name": "提交前需要确认",
    "pack_id": "orders",
    "instruction": "把订单 1001 提交审批",
    "expected_status": "needs_approval"
  }
]
```

```sh
go run ./cmd/agenstra-evaluate --server http://127.0.0.1:8091 \
  --key-env AGENSTRA_API_KEY --cases acceptance.json --output report.json
```

工具使用真实用户权限，顺序执行任务，记录状态、调用断言、最新被引用的业务证据和诊断中的耗时/token 预算；只有全部断言满足才退出 0，案例失败退出 1，配置错误退出 2。成本只有模型网关提供报价时可计算。它不自动审批、补充信息或核对不确定操作。需要等待用户处理时保留任务 ID；用 `run_id` 指定已有任务可只读重新验收。

新任务超时或轮询中断时发送取消请求，已有任务不会被该工具取消。取消只停止框架编排，上游操作的实际状态仍以业务系统为准。查询结果的字段断言不会证明业务系统中的所有副作用；需将权威查询接口作为业务证据接入。真实模型和目标软件的成功率由这些案例实测。

每个新任务的报告包含 `request_id`。创建请求遇到连接失败、5xx 或无效响应时，工具只用同一个 ID 重试一次，避免重复创建任务；仍无法确认时报告 `evaluation_create_outcome_unknown`。此时保留报告，将该 ID 填入同一案例的 `request_id` 后重试，保持 `pack_id`、`instruction` 不变。不要换 ID 重发可能已执行的写任务。显式 `request_id` 表示该任务由此验收案例负责；只想检查或保留已有任务时使用 `run_id`，它不会发取消请求。

## 接入范围

| 原软件已有能力 | 接入路径 | 原软件需提供 |
| --- | --- | --- |
| OpenAPI 3.0/3.1 JSON | 选择操作并生成 REST 草稿 | 地址、凭据、业务规则 |
| 普通 REST JSON API | REST 能力包 | 支持的输入输出契约、认证映射 |
| MCP HTTP / stdio | 发现、选择并固定工具契约 | 可用服务、结构化输入输出、实际业务保证 |
| Go 内部 SDK | 嵌入式 CapabilityProvider | 原函数到能力接口的绑定 |
| Web 页面已有函数 | 纯浏览器 SDK + profile，或与后端能力组合 | 原页面函数、当前页面观察数据、明确用户授权；纯浏览器无需占位后端包 |
| 只有桌面 GUI、没有 API 或绑定点 | 需专门适配 | 可调用的自动化或业务接口 |

OpenAPI 当前支持 JSON 文档、本地引用、path/query 参数和规定的序列化方式、JSON Schema 输出，以及受支持的 bearer 认证。纯对象结果保留原结构，允许 `null` 或其他类型的结果放在 `result` 字段中；复杂文档会报告导入失败，需对照[能力管理指南](capability-management.md)和导入器限制适配。框架尚未内置通用桌面 GUI 驱动或完整 OAuth 登录流程。
