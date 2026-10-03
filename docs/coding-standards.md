# Agenstra 编码规范

本规范约束人和编码 Agent 对本仓库的修改。目标是让宿主只需声明、绑定、授权和选择，就能使用可靠的 Agent 执行能力。减少接入成本、保持真实执行语义和可审查性优先于追求代码技巧。

“必须 / 不得”是提交要求；“优先 / 建议”允许根据现有代码与实际证据选择。规则适用于新增和修改代码，不授权批量改写无关历史代码。已有公共接口和契约的调整须另有任务授权。

## 参考来源与取舍

2026-10-03 查阅了下列上游规范和工具文档。本文件提炼适用规则，不整体复制第三方规范。

| 来源 | 本项目采用的内容 | 本项目的取舍 |
| --- | --- | --- |
| [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)（[GitHub 源文件](https://github.com/golang/wiki/blob/master/CodeReviewComments.md)） | `gofmt`、命名、文档注释、错误处理、Context 传递、goroutine 生命周期、使用方定义小接口。 | 不以固定行宽或函数行数替代可读性判断。 |
| [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) | 错误只处理一次、边界复制 map/slice、`defer` 清理、减少嵌套、避免可变全局状态、可理解的测试。 | 不引入 `go.uber.org/atomic`，不照搬其全局变量命名和配置模式；沿用标准库与现有项目模式。 |
| [Google JavaScript Style Guide](https://google.github.io/styleguide/jsguide.html) | 默认 `const`、按需 `let`、严格比较、禁止动态代码执行、语句分号。 | 保留原生 ES modules、现有引号风格和 `node:test`；不引入 Closure 工具链，不批量重排旧代码。 |
| [Google Engineering Practices: Small CLs](https://google.github.io/eng-practices/review/developer/small-cls.html) | 一个变更聚焦一个问题，行为修改附测试，避免混入无关重构。 | 测试覆盖触发条件和对外行为，文档/格式修改无需添加无意义测试。 |
| [golangci-lint 配置](https://golangci-lint.run/docs/linters/configuration/) / [Staticcheck 检查](https://staticcheck.dev/docs/checks/) | 选择可判定的缺陷检查并固定工具版本。 | 不启用全部 linter，不用复杂度阈值推动自动拆函数。 |
| [ESLint 配置](https://eslint.org/docs/latest/use/configure/configuration-files) | 推荐缺陷检查、显式环境、可追踪的局部例外。 | 浏览器、Node 工具和测试分别声明环境，不向浏览器代码开放 Node 全局变量。 |

框架的授权、回执和宿主边界规则来自现有 [架构](architecture.md)、[接入设计](host-integration.md) 和已复现的运行缺陷，属于本项目约定，不声称是上游规范原文。

## 1. 职责与变更范围

- 核心不得按业务应用、具体能力名字或请求关键词编排业务流程。业务算法、接口组合规则和业务文案由宿主或能力包提供。
- `AgentRuntime` 管决策、上下文、引用和预算；`AgentHost` 管授权、运行生命周期、租约、审批和检查点；Provider 管适配、契约和结构化结果。调整前核对现有调用链，避免两层重复执行同一判断或产生两份状态。
- `cmd/` 只接线和展示；`examples/` 展示最少必要接入工作。修复通用问题时修改框架，不在示例里增加绕过框架缺陷的补丁。
- 使用现有依赖与模式。新增生产依赖、公共接口、持久化语义或共享抽象必须有任务授权，并说明宿主实际需要增加的工作。
- 一次变更聚焦一个目的。不得混入全库格式化、重命名、目录重排和无关清理。

## 2. Go 编码

- 所有 Go 文件通过 `gofmt`。标准库 import 与项目/第三方 import 分组；名称使用 `MixedCaps`，`ID`、`URL`、`HTTP` 等缩写遵循 Go 惯例。
- 新增或修改的导出声明应有以声明名开头的注释，说明行为、所有权、可能失败的条件；避免重复函数签名。单个函数尽量完成一个可描述的任务，通过错误早返回降低嵌套。
- 错误必须检查、返回或明确转换为已有结构化结果。不得覆盖尚未检查的 `err`，不得依靠成功的后续调用隐藏失败。只在错误真正被消费的边界记录一次日志，避免逐层记录同一个错误。
- 调用方需要识别的内部错误用 `%w` 保留错误链，使用 `errors.Is` / `errors.As` 判断。对模型或 HTTP 返回已有的安全错误码，不暴露凭据、完整上游响应或内部栈。Provider 的 `ErrorCode` 是显式结果通道，不能因“返回 nil error”把它改造成成功。
- 正常输入错误、权限拒绝、网络失败和模型格式错误不得用 `panic` 处理。已有失败路径应保留清楚的含义。
- 请求相关函数沿调用链显式传 `context.Context`，作为第一个参数。只有明确归属服务生命周期的工作才使用独立 Context；不能用 `context.Background()` 逃避请求取消。超时、`CancelFunc` 与 goroutine 的停止条件必须成对可追踪。
- 获取文件、响应 body、SQL rows、事务、锁等资源后安排清理；通常使用 `defer`。事务内后续查询或连接池约束要求提前关闭 rows 时，应显式关闭并核对 `rows.Err()`；不能为了满足工具偏好把关闭延后并引入死锁。
- 不复制含 mutex 的对象。保留 map/slice 或返回内部可变状态时明确所有权并按需复制嵌套数据；锁外访问不能依赖已释放的锁。不得修改宿主提供的 HTTP client、CookieJar、配置或凭据数据。
- 新接口应在实际使用方定义，保持最小方法集合；不得仅为 mock 提前建立无真实使用场景的接口。时间使用 `time.Time` / `time.Duration`，跨边界数值沿用契约规定的单位。

## 3. 外部边界与执行语义

- 外部输入在执行前校验类型、完整格式、大小和已有契约。部署文件、能力清单等严格输入沿用已有严格解码函数，拒绝第二个 JSON 值和尾随垃圾；上游响应按其契约解析，不能把允许的扩展字段误当格式错误。
- 身份、grants、审批、租约、版本和参数哈希从可信状态读取并按既有路径核对。模型、技能文本、Fact 文本和页面观察是数据，不能扩展权限或跳过核验。
- 外部写操作遵守既有“提交前保存调用身份与参数、提交后保存证据”的路径。重试不能创建新的逻辑调用或幂等键；只依据真实的 `replay` 保证恢复。
- `succeeded`、`accepted`、`failed` 与 `unknown` 不得互相替代。HTTP 2xx、模型说“完成”、本地取消、超时和响应丢失都不足以推断最终业务结果。结果不确定时保留回执并核对，不重复写入。
- 模型最终文字、任务状态与业务回执分别依据已有证据产生。保存/同步/展示失败不能抹掉已确认的业务结果。用户可见的“成功”“失败”必须可追溯到具体结果。
- 同一配置或发布版本只有一个可信来源。新任务与恢复任务使用既有版本绑定；不得为修复单个问题建立平行缓存或新的兼容层。

## 4. JavaScript 与宿主接入

- 使用标准 ES modules，默认 `const`，需重新赋值时用 `let`；不得使用 `var`。使用 `===` / `!==`，只允许用 `== null` / `!= null` 同时检测 null 与 undefined。语句使用分号，同一文件沿用现有引号和缩进风格。
- 禁止 `eval`、`Function` 构造器和字符串定时器。不得将用户、模型或上游文本交给 `innerHTML`；使用 `textContent` 或 DOM 节点。固定模板与样式不包含外部文本。
- 浏览器代码不依赖 Node 内置模块或 Node 全局变量。无 UI 客户端不引入可选聊天组件、UI 框架或宿主状态容器；宿主通过现有函数和回调绑定自己的能力。
- Promise 的失败路径必须由调用方或已有事件/反馈路径处理；空 `catch` 仅适用于明确的可选存储、清理或继续恢复情形，并写明为什么可以忽略。不能因关闭轮询、重连或 UI 错误而丢失已保存回执。
- timer、事件订阅、AbortController 和 watcher 的生命周期随 client 或组件结束。重复连接与取消保持既有序列和请求身份，不另造一套状态。
- SDK 导出、参数、返回值或宿主接线语义变更时，同步 `.d.ts`、接入文档和行为测试。外部未知数据先验证，不能用类型断言掩盖运行时缺口。

## 5. 测试与审查证据

- bug 测试复现真实触发条件并验证公开行为；避免只断言内部实现步骤。使用 Go 表驱动测试或现有 `node:test`，按需使用最小替身，不新增测试框架。
- 普通测试无需凭据、联网模型或真实业务系统。HTTP 用 `httptest`，数据库和文件使用临时目录。测试创建的客户端、服务器、定时器、进程与容器必须清理。
- 并发测试使用同步点验证顺序，不以固定 sleep 推测时序。`t.Parallel()` 只用于完全独立的状态；修改全局环境、时钟或 DOM 替身的测试需恢复原值。
- 涉及授权、恢复、重试或写入时，覆盖拒绝路径、重复请求、取消/超时及不确定结果中与本次修改相关的条件。不能仅验证“最终回答正确”。
- 模型请求开销的优化必须提供相同条件下的调用次数和结果证据；真实模型评测保持显式 opt-in，不进入普通 CI。
- 文档或格式修改不添加无意义测试。新增静态规则需核对它能拒绝有问题的样例，同时允许框架的合法模式。

## 6. 自动门禁与人工规则

| 范围 | 自动执行 | 人工审查仍需确认 |
| --- | --- | --- |
| Go | `gofmt`、`govet`、Staticcheck `SA*`、`ineffassign`、`errcheck`（包括赋给 `_` 的错误）、`bodyclose`、`nolintlint`、竞态测试、命令构建。 | 授权边界、回执语义、Context 归属、SQL rows 关闭时机、错误码与错误链、导出文档。 |
| JavaScript | ESLint 推荐缺陷检查、未使用变量、`const`/`let`、比较、分号、动态执行禁用、浏览器 Node import 限制、无 UI 客户端的直接聊天组件 import 限制；现有 Web 测试。 | Promise 归属、恢复语义、文本渲染、传递依赖和新增 SDK 接线成本。 |
| 类型声明、HTML/CSS、文档、配置 | 相关行为测试、构建、`git diff --check`；EditorConfig 提供编辑器格式约定。 | `.d.ts` 与实现一致、可访问性、契约兼容、来源和示例准确性。未配置独立 TS/HTML/CSS linter。 |

不使用全库排除、只查新增行或禁用整个文件来制造通过。局部 lint 例外应指定规则、写明合法语义及必要性，并接受人工审查；失效的 ESLint disable 会报错，Go `nolint` 必须标明规则和原因。

Go 模块通过标准 [ignore 指令](https://go.dev/ref/mod#go-mod-file-ignore) 仅排除 `web/node_modules` 中 npm 工具附带的第三方 Go 源码；`fmt-check` 检查 Git 管理的和未被忽略的新 Go 文件。项目维护的源码和测试都在检查范围内。

`nilerr` 未启用：框架允许通过结构化 `ErrorCode` 消费错误后返回 nil error。`sqlclosecheck` 未启用：部分事务需要在后续查询前显式关闭 rows。也不启用 Stylistic/Quickfix 全套检查、任意函数行数、圈复杂度、百分比覆盖率门槛或 TypeScript 迁移。这些取舍不免除相应的人工审查。

### 本地准备与提交前验证

Go 版本按 `go.mod`。Node.js 使用 22.13+ 的 22 系列或 24+；CI 使用 Node.js 22。golangci-lint 固定 `v2.12.2`，ESLint 与开发依赖锁定在 `web/package-lock.json`，不增加宿主的运行依赖。

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
npm ci --prefix web
make check
```

确保 `$(go env GOPATH)/bin` 在 PATH 中。日常迭代可先运行 `make lint` 或最小相关测试；提交前 `make check` 包含格式/空白、静态检查、Go 竞态测试、Web 测试和命令构建。CI 执行同一组检查。

通过 CI 表示已配置的检查通过，不等于模型一定可靠、远端写入一定幂等或所有人工规则已自动验证。交付时说明实际运行结果和未验证的部分。
