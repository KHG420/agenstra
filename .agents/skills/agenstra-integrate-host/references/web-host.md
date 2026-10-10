# Web 宿主接线

本文件补充接入时容易遗漏的接线与执行语义。完整 API、Schema 和部署字段读取所选 Agenstra 版本的文档与类型声明，避免凭记忆生成接口。

## 框架工具与宿主文件

区分 Agenstra 工具源码目录与正在修改的宿主目录。以下命令中的 `AGENSTRA_SOURCE`、`HOST_ROOT`、`HOST_VENDOR`、`HOST_OPENAPI`、`HOST_PACK`、`HOST_PROFILE` 是任务选定的路径；执行前赋为实际绝对路径。输出文件应位于本次接入范围内，现有文件先检查差异。

已有匹配版本的命令二进制时，可用同名命令替换 `go run ./cmd/...`；这些工具属于开发环境，不要求宿主改用 Go，也不增加宿主生产依赖。

### 后端能力

已有 OpenAPI 3.0/3.1 JSON 时，只选择本次操作。下面的 `records`、`records.get` 和环境变量名是格式示例，替换为实际能力与变量名称：

```sh
cd "$AGENSTRA_SOURCE"
go run ./cmd/agenstra-import-openapi \
  --spec "$HOST_OPENAPI" \
  --out "$HOST_PACK" \
  --name records \
  --base-url-env RECORDS_API_URL \
  --token-env RECORDS_API_TOKEN \
  --operation records.get
```

没有 bearer 认证时按真实接口省略 `--token-env`。复杂 OpenAPI 导入失败时读取原因，按支持范围选取/编写契约；不随意削弱原接口约束。没有 OpenAPI 的 REST 接入直接按能力包文档编写清单。

MCP 的发现入口是管理界面和 Go SDK 的 `DiscoverMCPTools`。发现执行握手与工具列表读取，不调用业务工具；stdio 连接会启动配置的进程，先核对可执行文件与环境。业务测试另行执行。工具 annotations 不能证明读写影响、授权或重放安全，须核对原实现，固定完整工具契约哈希。

有开发者授权的管理服务与管理员凭据时，复用 `agenstra-manage` 的 `validate`、`publish`、`activate`、`bind`、`check`。读取目标版本的帮助及管理文档获取参数和 revision。`validate` 和 `check` 不能替代业务调用验收；发布、启用与授权是分别执行的动作。离线清单校验可通过公开 Go SDK 的 `ValidatePackManifest`，不假定管理 CLI 能离线工作。

部署凭据保持在服务端环境或专用 secret 引用中。共享服务账户仍须让原后端可靠核对实际用户和租户；缺少身份传递与校验依据时，该能力尚未通过授权验收。

### Web SDK 与页面动作

宿主可使用所选发行版本的 Web 包；无已发布包或需要固定源码时，导出到宿主的 vendor 目录：

```sh
node "$AGENSTRA_SOURCE/sdk/web/export-client.mjs" "$HOST_VENDOR"
```

保留许可证与 `agenstra-sdk.json` 的文件 SHA-256，记录导出来源 commit。导出器会覆盖 SDK 文件，只对确定由 Agenstra 导出器维护的目标执行；宿主业务实现放在自己的文件中。

页面操作需要 frontend profile 时，再生成类型、版本与初始 handlers：

```sh
node "$AGENSTRA_SOURCE/sdk/web/export-actions.mjs" "$HOST_PROFILE" "$HOST_VENDOR"
```

该命令更新类型和 profile 版本文件，保留已存在的 `agenstra-handlers.js`。新动作不会自动追加到已有 handler：每次变更都核对 profile、生成类型和实际注册函数，绑定新增动作并审查移除动作。初始模板抛出 `handler_not_implemented`，不表示完成接入。

注册真实 `actions`，将生成的 `handlerVersion` 传给 `createAgenstraClient`，服务端使用相同 profile。页面观察只提供必要的页面、实体和业务版本信息。保留原业务的权限、锁、幂等和并发控制；浏览器桥版本不能代替业务数据版本。

仅聊天场景使用 headless client 或可选的标准聊天组件，关闭 browser 模式。页面控制按所选 profile 启用 browser bridge；纯浏览器模式无需占位 REST/MCP 包。组合接入分别配置后端 pack 与浏览器 integration 的授权和模型数据使用范围。

## 身份与授权

复用宿主登录和 CSRF/可信来源验证，再从可信服务端身份换短期 ticket。JavaScript 服务端可使用 `createAgenstraSessionHandler`；其他后端按相同 HTTP 流程实现。查阅 `agenstra-session.d.ts` 和当前示例，将 `verifyRequest`、`authenticateRequest`、`resolveAPIKey` 绑定到真实实现。

`resolveAPIKey` 可返回可信 owner 对应的服务端用户 API key；启用 `host_auth` 时也可使用宿主已验证的 bearer 凭证。动态 owner 仍需要明确绑定。浏览器自报用户/租户 ID 不建立身份，长期 key 和管理密钥不得进入浏览器。框架管理使用独立管理入口；宿主业务管理员角色不获得框架管理权限。

后端写请求继续核对原业务授权，能力包授权不能代替它。用户拒绝、撤销授权、补充输入、精确参数审批和结果不确定分别使用框架现有状态与入口。

## 生命周期与真实结果

- 应用或登录会话持有 client 时，路由组件只卸载自己的 UI。client 随路由卸载时用 `destroy({ closeSession: false })` 保留恢复身份；退出登录或切换 owner 才永久关闭对应绑定。读取当前版本文档处理 BFCache。
- 同一消息重试保留 client ID，同一任务创建重试保留 request ID；浏览器回执 ACK 丢失重传原结果，不重跑 handler。业务写入仍依赖原系统幂等与权威回执。
- handler 输出实际结果或可查询的操作 ID；仅受理的任务继续查询终态。浏览器完成证据在 `ui.command_status` 的 Fact 中，业务输出通常位于 `data.result`，以真实返回路径为准。
- 结果未知时按原幂等键/调用 ID 只读核对。业务保证缺失时如实保留限制，不宣称 exactly-once。
- 标准聊天组件使用 Shadow DOM，宿主全局快捷键需要检查 `event.composedPath()` 中的实际输入元素，避免聊天输入触发原业务快捷操作。

这些要求在所选宿主的本地运行中核验；SDK 导出、类型检查和成功换票各只证明自己的环节。
