# 年会抽奖从零接入：缺陷与运行验收

日期：2026-10-02。框架基线 `main@18b4d7e`，抽奖软件基线 `ff75c68`（把原目录现有未提交功能保存到新工作树，不含 Agent 接入）。本次未复制历史 Agent 试验代码，未访问生产活动数据，未提交密钥或数据库。

两个独立工作树：

- Agenstra：`/Users/aq/.codex/worktrees/lottery-agent-integration-7056/agenstra`，`codex/lottery-integration-findings`。
- 抽奖软件：`/Users/aq/.codex/worktrees/lottery-agent-fresh-20261002/lottery-ui`，`codex/agent-fresh-integration`。

## 实际接线与职责

宿主使用标准 SDK 和标准聊天组件，定义 16 个具体页面动作；动作绑定原 `LotteryClient` 的读写、选奖、开始、揭晓和导出函数。所有持久写仍是原 `/api/activity` → `applyCommand` → D1 CAS，保留原登录、活动 owner、revision、进行中锁定、奖品人数限制与服务端随机结果。没有写另一套抽奖业务、模型调用、聊天历史或 Agent 循环。

助手默认只读人数、奖项和最近轮次概览。人员/中奖资料按需分页，每次最多 50 人。业务写附带当前 `expectedRevision`，由 Agenstra 请求用户审批。开始只冻结候选，揭晓才产生中奖者。导入仍由用户在原后台选 CSV/XLSX 并预览；不开放任意本机文件读取、通用 command 透传、指定中奖者或一键清空记录。

## 已复现并修复的框架缺陷

| 编号 | 触发与证据 | 根因 | 修复与验证 |
| --- | --- | --- | --- |
| F1：Worker 换票据失败 | 本机 Cloudflare Worker 宿主 `/api/agent-session` 返回 503；同一 API key 直接调用 Agent `/web/v1/token` 返回 200。可信服务端临时诊断显示 `Invalid redirect value ... error won't be implemented ... use manual` | `web/agenstra-session.js` 使用 `redirect: "error"`；Worker fetch 只支持 manual/follow，助手 catch 又将异常统一为不可用 | 改为 manual；仍拒绝全部非 2xx，不向重定向目的地转发 key。Worker 契约成功测试、307 拒绝测试及真实 Worker 换票据通过。诊断临时代码已移除 |
| F2：写入后重新查询被去重拦截 | 真实模型“停止揭晓并核对”任务 `a0b14157-cf79-5d92-8d9b-08875fdf7077` 出现 `ui.read_activity / repeated_equivalent_call`。确定性浏览器测试连续 3 次同参数只读，修复前第 3 次未分发而直接 completed | runtime 只允许保留的 `ui.get_context` / `ui.command_status` 重读；宿主声明的 read 动作也受重复参数限制，错误地把动态业务查询当固定结果 | 对声明为 read 且使用 `ui.command_status` 操作绑定的浏览器动作允许重新读取。业务写仍保留重复保护，轮次/工具/停滞预算继续生效。新回归测试及全部 Go 测试通过 |
| F3：遥测轮询不断替换聊天控件 | Chrome 审批视图内容未变，原审批按钮节点已失效；确定性 DOM 测试只改变 telemetry 的 observed_at / seconds_remaining，旧组件仍替换整条消息及按钮 | render signature 序列化整个 snapshot，包含每次查询都变化的遥测时间字段 | 签名只保留遥测中实际显示的工具调用数。时间变化保持节点与文本选择；调用数变化仍刷新。测试先失败后通过，安全文本呈现测试继续通过 |
| F4：页面前置条件被诊断成业务接口故障 | 已完成的只读任务曾先漏读 `ui.get_context`，诊断给 `browser_context_required` 的建议是“核对业务接口返回状态”，把排查方向指向 D1/业务接口 | `ExplainRunError` 没有页面状态错误分类 | 两个 context 错误归为 browser，明确提示先获取页面观察再判断操作，并区分业务 revision。unknown 写仍归为 reconciliation。专门回归测试通过 |

诊断仍保留历史失败证据；后续恢复成功并不删除旧错误。当前 findings 没有“已恢复/仍待处理”的单独标识，这是剩余的诊断体验限制，不能把历史 finding 等同于整个任务失败。

## 宿主接线中修复的问题

**H1：聊天输入空格会抽奖。** 原软件通过 `event.target.closest('input,textarea,...')` 排除输入框；标准聊天组件使用 Shadow DOM，事件在 document 上被重定向为 `agenstra-chat`，因此输入空格触发舞台 SPACE 快捷键。真实 Chrome 中已复现：结果页输入空格后立刻“正在准备”，保存了一轮 active，但没有产生中奖名单。该测试轮次随后通过原“取消本轮”按钮取消。

宿主快捷键改为检查 `event.composedPath()` 中的真实输入元素，保留舞台原快捷键；最终构建在真实聊天输入框复测空格，前后均为 revision 8、active=null、history.length=3，未启动或揭晓抽奖。此项属于宿主事件处理，不计作框架随机抽奖或审批逻辑缺陷。框架接入文档补充了 Shadow DOM 的快捷键接线注意事项。

宿主适配还明确区分拒绝与不确定：权限/revision/忙碌前置失败及 HTTP 4xx 可以证明没有写入；网络/5xx 保留 unknown。start 使用命令 ID 作稳定轮次 ID，finish 使用已读取 roundId，并沿用原接口丢响应后的业务核对逻辑。

## 尚未解决的框架接入成本

| 观察 | 已核对的依据与本次处理 | 建议优先级 |
| --- | --- | --- |
| 纯浏览器宿主仍必须提供后端能力包 | `NewWebIntegration` 的 profile 必须指向 base pack；`ValidatePackManifest` / `NewRestProvider` 禁止空 capabilities。抽奖原 API 不向独立服务提供登录凭据，因此本次增加只返回静态规则的 `/api/agent-info` 作有效 base 能力。这是多余的宿主接线成本，未引入新 pack 架构来绕开 | 高：提供正式 browser-only 模式，仍保留业务说明、版本固定和授权 |
| 标准换票据不足以完成服务部署和用户映射 | 宿主仍需设置可信 owner → 服务端 key；本机示例固定 local_seedy。动态 host_auth 需要 management 及显式 owner binding，独立服务 browser grants 仍来自静态配置或 Go 回调。当前示例不是多用户自动开通方案 | 高：宿主身份、能力授权、页面动作授权一起生成/校验，错误归因需留在可信服务端 |
| profile/handler 接线量与业务语义仍需手工完成 | export-actions 能生成契约类型及模板，但无法推断原函数的权限、revision、失败是否写入、候选冻结与幂等性。本次保存和抽奖函数保持共用，并为每个动作填写影响与审批 | 高：提供业务接口/页面函数的具体适配范式及契约验收工具；不能凭函数名称自动判定副作用 |
| 文件与下载缺少统一宿主体验 | 当前 RunSource 是能力来源选择，不是文件上传。标准聊天组件没有 CSV/XLSX 附件入口；导出回执不能证明文件实际落盘。本次仍走原软件文件预览，下载只报告 downloadRequested | 中：引入明确宿主负责的上传/预览/下载结果契约，再做通用 UI |
| 聊天组件领域文案和排版有限 | 初始提示固定为“查询本月订单”，不适合抽奖；title/locale 可配，业务示例不能配。回答中的 Markdown 表格/加粗显示为文本，只对代码块提供格式；真实查询回答还主动输出 revision/phase 等内部字段 | 中：允许宿主提供领域空状态/示例，安全 Markdown 子集。当前用业务 guidance 约束面向主持人的回答，未扩大组件公共 API |
| 简单任务的模型调用仍有明显开销 | 较早验收 4 场景使用 6 / 6 / 18 / 6 次模型请求；最终版本使用 10 / 6 / 26 / 6 次。最终揭晓任务多次重读，并出现 operation_failed、final_fact_citations_invalid、final_result_refs_invalid 后恢复完成，业务结果正确，但性能没有改善。尚未把这些协议错误逐一归因到模型或框架 | 高：用代表任务持续测量 rounds/tokens/耗时，清晰表达浏览器前置条件和异步结果；进一步追踪事实与结果引用的失败原因 |
| 运行依赖页面生命周期 | 浏览器桥需要在线且能执行 handler 的页面；关闭/崩溃后写结果可能 unknown，动画等待还受页面可见性影响。收起聊天面板本次保持 client 连接。未声称离线页面也能继续操作 | 按场景：服务端适合数据操作，浏览器只负责必要的可见页面动作 |

## 验收证据与边界

使用本机虚构 156 人名单，工作树自己的 D1、Agenstra run/web SQLite，以及已有模型网关。本例 deployment 配置了 30 个模型轮次和 600 秒任务预算；这些结果不能直接证明默认预算也能完成同样任务。真实模型协议验收脚本连接真实 Worker API 和持久库，使用同一个宿主适配器；浏览器动画、聊天、人工审批和 Shadow DOM 键盘行为单独在既有 Chrome 会话中验收。

- 自动场景：只读概览；审批后开始、无历史写入；审批后揭晓并查询名单；重复 finish 返回同一名单且 revision 不变；页面导航与主题。
- 真实 Chrome：聊天查询、展开精确审批参数、批准开始后舞台 rolling、批准揭晓后结果卡牌与保存名单一致、刷新恢复聊天/活动、空格输入回归。
- 宿主单元测试：旧 revision、只读身份、忙碌阶段阻止写入；stable roundId；winner query 与实际 domain 结果一致；人员分页；4xx/5xx 区分；同源身份换票据与代理路由限制。
- 框架验证：`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/...`、Web SDK 全部测试和差异空白检查。
- 宿主验证：33 个测试、ESLint、TypeScript、Vinext 正式构建、Impeccable detector；浏览器验收截图保存到宿主忽略目录。

完整本机模型报告与截图留在宿主 `agent/data`，密钥和数据库保持忽略。验收不是生产成功率统计；没有用真实员工资料测试，没有证明完整多用户管理、网关长期可用性、所有 16 个动作在真实模型下的稳定成功率或大规模并发运行。

最终自动验收使用修复后的框架与最新宿主构建，四个任务均 completed，终态 errorCode 均为空。开始与揭晓各审批一次；第三轮保存 3 人后，重复 finish 的业务 revision 不变、中奖人员 ID 完全一致。最终活动为 revision 8、3 轮、9 名中奖者、无进行中轮次。

最后在 Chrome 使用最终版本重新查询第三轮，回答中的周沐晴、林可欣、陈安然与舞台卡牌、原 API 保存名单一致；查询后没有业务版本变化。截图为这个最终状态，聊天 Markdown 仍以文本显示。

| 场景 | Run ID | 模型请求数 | 累计输入 token | 累计输出 token |
| --- | --- | ---: | ---: | ---: |
| 只读概览 | 872f4df4-7e11-57b4-922a-4f11332bdfd0 | 10 | 65,688 | 8,265 |
| 审批开始 | fe869d27-42c4-59fe-8c93-ee3a837f6469 | 6 | 36,114 | 3,789 |
| 审批揭晓并核对 | a47b6411-6041-5b13-9e2e-c61c24d562d7 | 26 | 268,561 | 30,792 |
| 页面导航与主题 | d16c64a0-a222-5896-b973-aa21f39b672c | 6 | 36,277 | 1,931 |

这些是报告中的累计模型用量，不代表单次上下文大小或成功率。前三个场景仍先漏读页面观察并产生 browser_context_required，随后恢复；诊断历史保留该证据。揭晓最终答复引用问题也在完成前恢复，不能据此宣称该问题已经修复。
