# 能力管理：CLI、Web 与版本发布

Agenstra 的管理入口面向开发者、接入方和部署维护人员，用于配置模型及把已有 REST、OpenAPI、MCP 或 SDK 能力接入运行中的 Agent。开发者先生成并审查能力包，再发布不可变版本、启用版本、绑定用户连接。模型只能选择已经启用且获得授权的能力，无法通过任务文本注册新工具。

Web 控制台、静态资源和管理 API 由框架 HTTP 服务直接提供。开发者使用独立管理密钥访问；宿主登录和宿主业务管理员角色不授予框架管理权限。宿主仅提供用户私有功能，不复制或嵌入此控制台、不代理管理 API，也不持有管理密钥。用户的会话、任务、上下文、用量、记忆及自动化继续通过按 owner 隔离的运行接口访问。

管理入口是**可选的**。现有 `packs` / `users.*.packs` 文件式配置可以继续使用；启用管理入口后，已发布能力包和用户连接由管理注册表保存。静态配置中的用户 API key 仍负责普通运行身份认证。

## 1. 启用管理入口

在自己的部署配置（例如忽略提交的 `local/deployment.json`）中加入：

```json
{
  "database_path": "../var/runs.sqlite3",
  "users": {
    "operator": {"api_key_env": "AGENT_OPERATOR_API_KEY", "packs": {}}
  },
  "management": {
    "database_path": "../var/capabilities.sqlite3",
    "package_dir": "../var/capability-releases",
    "admin_api_key_env": "AGENSTRA_ADMIN_API_KEY",
    "secret_dir": "secrets"
  }
}
```

路径相对部署配置所在目录解析。`secret_dir` 可省略，此时连接只引用已经注入进程的环境变量。启用管理时，服务器要求管理员密钥至少 24 个字符，且不同于所有用户 API key；它不会写入配置文件。Web 页面位于 `https://<你的服务>/admin`，CLI 默认连接本机 `http://127.0.0.1:8091`。远程管理 CLI 只接受 HTTPS 地址。将服务放在 TLS 和网络访问控制之后；管理密钥拥有发布 MCP stdio 命令、变更连接和授权的高权限。

```sh
export AGENSTRA_ADMIN_API_KEY='通过密钥管理器注入的独立随机密钥'
go run ./cmd/agenstra-serve --config local/deployment.json
```

容器部署时，把管理数据库和 `package_dir` 放在 `/data` 持久卷中。若使用 `secret_dir`，由部署方将专用目录以只读方式挂载到容器；不要把真实密钥文件提交进仓库或能力包。目录内每个文件以大写变量名命名，例如 `RECORDS_API_TOKEN`。连接可以引用 `secret:RECORDS_API_TOKEN`；Agenstra 在每次建立连接时读取当前文件内容，因此预先配置好目录后，增添密钥文件无需重启服务。修改该文件的端点值可能改变连接指纹，已有任务会要求核对；轮换同一端点的 token 值不会写入指纹。

## 2. 生成和审查清单

已有 OpenAPI 3.0/3.1 JSON 时，只选取需要的操作：

```sh
go run ./cmd/agenstra-import-openapi \
  --spec local/openapi.json \
  --out local/packs/records/pack.json \
  --name records \
  --base-url-env RECORDS_API_URL \
  --token-env RECORDS_API_TOKEN \
  --operation records.get
```

Web 页面的“从 OpenAPI 生成草稿”也提供选取操作的界面；生成后可直接编辑 JSON。导入器只能生成草稿：管理员必须核对每项能力的 `effect`、输入输出、单位、错误码、幂等与审批，以及外部作业状态映射。MCP 包要固定被选工具的完整契约哈希；SDK Provider 由部署方以受信任代码安装，再按原有扩展接口暴露。

能力包可引用技能文件，清单中的路径应位于清单目录下，SHA-256 必须与文件内容一致。Web 发布时选择对应的技能目录；CLI 会从清单所在目录读取技能文件。发布请求有大小限制：清单最多 1 MB，全部技能文件合计最多 1 MB。

## 3. 校验、发布、启用

`agenstra-manage` 和 Web 页面使用同一管理 API。CLI 从 `AGENSTRA_ADMIN_API_KEY` 读取密钥；也可用 `--admin-key-env` 指定另一个环境变量名。

```sh
go run ./cmd/agenstra-manage validate local/packs/records/pack.json
go run ./cmd/agenstra-manage publish local/packs/records/pack.json
go run ./cmd/agenstra-manage list
```

`validate` 会检查清单、JSON Schema、技能哈希等，但不调用外部业务操作。发布按清单和技能内容生成 SHA-256 版本 ID。同一 `pack_id` 与版本号只能对应一份内容；重复发布相同内容是幂等操作。发布后**不会自动启用，也不会自动授权**。

从 `list` 输出读取完整 `digest` 和当前 `revision`，再启用：

```sh
go run ./cmd/agenstra-manage activate records <digest> --revision 0
```

`--revision` 防止两位管理员相互覆盖；首次启用为 `0`，每次切换递增。启用旧版本就是回滚。启用只改变**新任务**所用版本，已经创建的任务继续使用自己保存的版本。已发布版本不会由管理 API 删除，以保证旧任务可以恢复。

发布过程中若文件已落盘而数据库提交失败，可重试相同发布内容。框架在同一个 SQLite 写事务下核对不可变版本，并验证残留目录的清单、技能哈希、文件类型及完整内容摘要后补齐发布记录；内容不同、额外文件或符号链接会被拒绝，不会删除或覆盖已有目录。

启用或加载已发布版本前，框架核对包文件和注册表中的清单完整性。清单必须是单个完整 JSON 文档，可以带尾随空白；拼接文档或尾随垃圾返回 `release_tampered`，拒绝启用并保留原启用状态。

## 4. 绑定连接和授权

用户身份可在部署配置的 `users` 中声明，也可由 `host_auth` 从宿主登录系统验证并取得动态 owner ID。管理入口为指定 owner 绑定能力包；动态 owner 必须明确绑定，不能继承静态用户授权。连接文件只保存环境变量或 `secret:` 引用，不含明文密钥：

```json
{
  "environment": {
    "RECORDS_API_URL": "secret:RECORDS_API_URL",
    "RECORDS_API_TOKEN": "secret:RECORDS_API_TOKEN"
  },
  "granted_capabilities": ["records.get"],
  "approval_capabilities": [],
  "allow_model_data": true
}
```

左边是清单所需的变量名，右边是部署进程已有的环境变量名或专用密钥目录中的 `secret:NAME`。管理 API 会检查引用当前可读取，且授权能力存在于启用版本中。能力本身声明 `approval_required` 时，即使这里未列出，也仍需逐次审批。

```sh
go run ./cmd/agenstra-manage bind operator records local/records-connection.json
go run ./cmd/agenstra-manage check operator records
go run ./cmd/agenstra-manage audit
```

`check` 建立连接并读取实际能力目录，不发起业务工具调用；对 MCP 会进行握手和契约哈希校验。停用连接会立即撤销运行访问，包括已有任务：

```sh
go run ./cmd/agenstra-manage disable operator records
```

浏览器 integration 使用相同的 `bind`、`check`、`disable` 命令和管理 API，将 pack 参数替换为 integration ID。其策略只包含 `granted_capabilities`、`approval_capabilities` 和 `allow_model_data`；服务端 frontend profile 提供可信能力目录，无需后端发布版本。组合接入分别管理后端连接与浏览器策略，后端凭据仍放在后端绑定。管理浏览器绑定优先于静态 `browser_actions` 和 Go hook，停用后不会继承它们的授权。详见[Web 接入指南](web-integration.md)。

Web 页面可完成相同流程：发布版本、启用或回滚、选择用户、填写变量映射与授权、检查连接、查看最近变更。页面中的管理员密钥只放在当前标签页的内存里，刷新后需重新输入；请勿在没有 HTTPS 的远程地址打开管理页面。

## 5. 运行边界与备份

- 管理 API 只在部署配置含 `management` 时注册。普通运行 API 使用用户密钥，管理 API 使用独立管理员密钥；两者不互通。
- 新能力包启用、已有用户授权和 `secret_dir` 中的新连接值可以在线生效。新增用户、修改部署配置或新增进程环境变量，仍需由部署方更新配置并重启服务。
- 旧任务固定发布版本，但授权、身份验证和连接仍实时检查。撤销权限或切换身份会暂停任务；更改连接端点可能导致指纹变化，要求核对旧任务。
- REST 能力在发布时做离线契约校验。MCP 包发布时校验清单与技能，`check` 或实际连接时验证远端工具契约；自定义 SDK 的部署和升级由调用方应用负责。
- 注册表使用单节点 SQLite WAL。备份和恢复应同时覆盖运行数据库、管理数据库和完整 `package_dir`，并保存外部密钥管理器的配置。不要只备份其中一个文件。
- 管理页面不提供模型质量、外部计算正确性或写操作幂等性的保证。发布前用目标服务测试代表性任务、审批拒绝和异常恢复。

## 6. 分步编辑共享草稿

管理入口现在支持可保存的 REST v2 / MCP v1 草稿。草稿存放在管理数据库的 `drafts` 表中，Web 和 CLI 读取同一份内容；草稿可以不完整，保存不需要连接外部服务或模型。正式版本仍经过现有完整校验并按内容哈希固定，编辑草稿不会影响已发布版本或已有运行。

Web 的“草稿编辑”按六步组织：基本信息、服务连接、能力与契约、执行规则、使用说明、检查与发布。可随时保存，重新连接管理服务后从列表继续。OpenAPI 提供操作选择，MCP 提供“发现工具”与逐项选择；不支持的工具显示原因。每项能力单独编辑，技能正文保存时计算 SHA-256。输入输出 Schema、响应映射和长任务 `operation` 等复杂规则保留高级 JSON 编辑入口。完整清单也可以在最后一步查看或编辑。

发布成功后，同一流程显示启用版本、选择已有用户、逐项能力授权、环境引用与模型数据许可，再运行连接检查。每一步使用已有管理 API；部分成功后明确显示已完成的步骤，不把发布、启用和授权当作一个事务。管理页另提供“试运行与任务诊断”，必须使用用户 API key；接口契约检查不能代替目标业务系统中的任务验收。

分批导入只合并能力和技能，保留草稿的名称、版本、总体原则和连接配置。OpenAPI 导入使用草稿已有的 REST 连接声明，只追加所选 operationId。导入默认 `error`：任一同名项冲突时整批不保存；`keep` 保留已有同名项并追加新项；`replace` 替换同名项并保留其他项。替换会使用导入项的整份契约，请先审查导入预览。Web 导入前会先保存当前表单，导入失败也不会丢掉这些修改。

CLI 示例（服务和管理密钥配置同前文）：

```sh
# 创建草稿；不要求一次填完。MCP 可加 --type mcp。
go run ./cmd/agenstra-manage draft create records-work --name records --version 1.0.0

# 只改一个配置部分；JSON 文件可只包含该部分的待更新字段。
go run ./cmd/agenstra-manage draft set records-work basic local/basic.json
go run ./cmd/agenstra-manage draft set records-work connection local/connection.json

# 添加单项能力或一批能力：文件内容是一个能力对象或对象数组。
go run ./cmd/agenstra-manage draft add records-work local/read-capability.json

# 增量修改一项已有能力：只提供要修改的字段，其余字段保留。
go run ./cmd/agenstra-manage draft update records-work records.get local/execution-rules.json

# 分批导入完整包中的能力/技能，或选中的 OpenAPI 操作。
go run ./cmd/agenstra-manage draft import records-work local/packs/records/pack.json --conflict keep
go run ./cmd/agenstra-manage draft openapi records-work local/openapi.json --operation records.get --operation records.list --conflict error

# MCP 草稿保存 source 后发现工具；可提供变量到环境/secret 引用的 JSON 映射。
go run ./cmd/agenstra-manage draft discover mcp-work local/mcp-environment-refs.json

# 添加技能，自动计算哈希；需要在能力的 skills 数组中关联名称。
go run ./cmd/agenstra-manage draft skill records-work record-rules local/SKILL.md --description '记录状态解释'

# 查看、校验、移除以及直接通过 EDITOR 编辑完整清单。
go run ./cmd/agenstra-manage draft list
go run ./cmd/agenstra-manage draft show records-work
go run ./cmd/agenstra-manage draft validate records-work
go run ./cmd/agenstra-manage draft remove records-work capability records.list
EDITOR=vi go run ./cmd/agenstra-manage draft edit records-work

# 导出为一个新目录，包含 pack.json 和技能文件；拒绝覆盖已有目录。
go run ./cmd/agenstra-manage draft export records-work local/records-export

# 完整校验通过后发布；仍需另外 activate 和 bind。
go run ./cmd/agenstra-manage draft publish records-work
```

其中 `basic.json` 可为 `{"guidance":"仅使用真实记录数据，不推测缺失字段。"}`；REST 的 `connection.json` 可为 `{"base_url_env":"RECORDS_API_URL","token_env":"RECORDS_API_TOKEN"}`；`execution-rules.json` 可为 `{"approval_required":true}`。`set` / `update` 中的 `null` 删除相应可选字段。MCP 的连接片段使用 `source` 对象，例如 `{"source":{"transport":"streamable_http","url_env":"MCP_URL"}}`。MCP 工具清单固定远端契约哈希，连接时仍要求输入输出 Schema 和契约匹配。

`discover` 只进行握手与 `tools/list`，返回完整契约 SHA-256、可接入的 exposure 和不支持原因；结束时关闭连接，不执行 `tools/call`，不修改草稿或用户权限。新 exposure 默认按写操作、需要审批、不可重放处理，管理员根据真实业务语义审查。环境映射例如 `{"MCP_URL":"secret:MCP_URL"}`，响应不包含引用对应的凭据。CLI 发现后用 `draft add` 导入审查过的 exposure；Web 可直接勾选并导入。

Web 导出的是包含 `manifest` 与 `skills` 正文的 JSON 文件，可从 Web 的清单合并入口导回；CLI 导出的是标准文件目录，可直接用于文件式部署和原有发布命令。

草稿的每次保存都会增加 `revision`。Web 保存和 CLI 的读取后更新都提交读取时的修订号；若另一处先保存，返回 HTTP 409，不覆盖其修改。CLI `edit` 也使用打开编辑器前的修订号；解析或保存失败时保留编辑后的临时文件并输出路径，便于核对和恢复。Web 会保留发生冲突时的本地编辑，可先导出，再重新读取并合并。CLI 片段文件保留在本地，重新查看后可重试；不要未经核对覆盖其他管理员的内容。

草稿 API（均要求管理员密钥）：

| 请求 | 行为 |
| --- | --- |
| `GET /admin/api/drafts` | 列出草稿摘要。 |
| `GET /admin/api/drafts/{id}` | 读取清单、技能正文、修订号和待完成问题。 |
| `PUT /admin/api/drafts/{id}` | 创建或保存完整草稿；请求包含 `expected_revision`、`manifest`、`skills`。创建用修订号 `0`。 |
| `PATCH /admin/api/drafts/{id}` | 修改一个部分或导入一批内容；必须提供 `expected_revision`。 |
| `POST /admin/api/drafts/{id}/validate` | 校验指定修订的草稿，返回按步骤定位的 `issues`；有问题仍允许保存。 |
| `POST /admin/api/drafts/{id}/discover` | 用 `expected_revision` 和可选 `environment` 引用发现 MCP 工具；不修改草稿，不执行工具。 |
| `POST /admin/api/drafts/{id}/publish` | 发布指定修订，完整校验失败返回 422 与 `issues`；发布成功不启用、不授权。 |

`PATCH` 的 `section` 支持 `basic`、`connection`（通过 `value` 对象更新字段），`capabilities`、`skills`（通过 `items` 数组和 `conflict` 合并，技能正文放在 `skills` 对象中），`import`（`value` 为同类型清单），`openapi`（`value` 含 `spec`、`operations` 和可选 `effects`），以及 `remove_capability` / `remove_skill`（通过 `name` 指定）。导入和合并在一次修订更新中完成。备份管理数据库时也会备份草稿；仍需同时备份运行数据库、发布目录和密钥管理配置。
