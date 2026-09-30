# 能力管理：CLI、Web 与版本发布

Agenstra 的管理入口用于把已有 REST、OpenAPI、MCP 或 SDK 能力接入运行中的 Agent。管理员先生成并审查能力包，再发布不可变版本、启用版本、绑定用户连接。模型只能选择已经启用且获得授权的能力，无法通过任务文本注册新工具。

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

## 4. 绑定连接和授权

用户身份先在部署配置的 `users` 中声明；管理入口负责为已有用户绑定能力包。连接文件只保存环境变量或 `secret:` 引用，不含明文密钥：

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

Web 页面可完成相同流程：发布版本、启用或回滚、选择用户、填写变量映射与授权、检查连接、查看最近变更。页面中的管理员密钥只放在当前标签页的内存里，刷新后需重新输入；请勿在没有 HTTPS 的远程地址打开管理页面。

## 5. 运行边界与备份

- 管理 API 只在部署配置含 `management` 时注册。普通运行 API 使用用户密钥，管理 API 使用独立管理员密钥；两者不互通。
- 新能力包启用、已有用户授权和 `secret_dir` 中的新连接值可以在线生效。新增用户、修改部署配置或新增进程环境变量，仍需由部署方更新配置并重启服务。
- 旧任务固定发布版本，但授权、身份验证和连接仍实时检查。撤销权限或切换身份会暂停任务；更改连接端点可能导致指纹变化，要求核对旧任务。
- REST 能力在发布时做离线契约校验。MCP 包发布时校验清单与技能，`check` 或实际连接时验证远端工具契约；自定义 SDK 的部署和升级由调用方应用负责。
- 注册表使用单节点 SQLite WAL。备份和恢复应同时覆盖运行数据库、管理数据库和完整 `package_dir`，并保存外部密钥管理器的配置。不要只备份其中一个文件。
- 管理页面不提供模型质量、外部计算正确性或写操作幂等性的保证。发布前用目标服务测试代表性任务、审批拒绝和异常恢复。
