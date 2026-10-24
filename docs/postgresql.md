# PostgreSQL 存储选项

静态能力包部署可将运行与 Web 数据直接存入 PostgreSQL，不创建 SQLite 文件。原有 `database_path` 配置和 SQLite v1 数据库继续受支持。两种后端使用相同的任务、审批、租约、回执和 HTTP/Web 契约。

## 配置

在已有 deployment 中删除顶层 `database_path` 和 `web_integration.database_path`，添加：

```json
{
  "database": {
    "driver": "postgres",
    "dsn_env": "AGENSTRA_DATABASE_DSN",
    "schema": "agenstra_run",
    "web_schema": "agenstra_web"
  }
}
```

这是需要合并到 deployment 的存储片段；`users`、能力包、模型和 Web integration 仍按原接入配置提供。`AGENSTRA_DATABASE_DSN` 由服务端私密环境注入，使用 PostgreSQL URI 或 pgx 支持的连接参数。不要将真实 DSN 放进 Git、浏览器或日志。TLS 模式由连接配置明确指定；服务不会自动降级 TLS。

- `schema` 保存运行、调用记录、Fact、事件、审批检查点、记忆、定时定义和执行记录。
- `web_schema` 保存会话、消息、浏览器连接、指令与回执、固定能力版本映射；启用 Web integration 时必填，必须与运行 schema 不同。
- schema 只能使用小写字母开头的字母、数字和下划线，最长 63 字符；不允许 `public`、`pg_*` 或 `information_schema`。
- 数据库和角色由部署方预先提供。启动只在指定 schema 建表并检查版本；不会创建数据库、角色或修改宿主业务表。首次启动需能创建指定 schema；后续运行需有表的读写与序列权限。
- 当前 PostgreSQL 模式支持静态能力包、静态用户授权和 Web 接入。动态 `management` 注册表与依赖它的 `host_auth` 暂只支持 SQLite；混合配置会明确拒绝，避免声称“不使用 SQLite”却隐式创建文件。

服务启动命令不变：

```sh
agenstra-serve --config /etc/agent/deployment.json --host 127.0.0.1 --port 8091
```

Go 嵌入式宿主可用 `agenstra.NewPostgresStore(dsn, schema)`，然后调用 `Initialize()`；也可用已读取的 `deployment.OpenRunStore()`。两者都由调用方关闭。`NewPostgresWebStore` 用于单独打开 Web schema；通常 `NewHTTPServer` 根据 deployment 自动完成 Web 接线。旧 `NewSQLiteStore` 和 `SQLiteStore` 类型保持兼容。

## 事务、恢复和运维

PostgreSQL 的写事务按数据库和 schema 获取事务级 advisory lock，对应 SQLite 的 IMMEDIATE 写入序列；事务内仍检查 owner、租约、版本与 fencing token。外部模型和工具调用不在数据库事务中执行。时间戳使用双精度，事件与 Web 载荷使用 JSONB，证据原文与哈希依据继续保留。运行库与 Web 库各自有独立事务边界，沿用原有跨库幂等和恢复机制。

采用 PostgreSQL 不等于已经支持分布式高可用。当前继续按单个 Agent 服务部署；多个业务环境使用不同 schema。`/readyz` 会查询真实运行数据库。持久性、备份和保留策略使用 PostgreSQL 的运维机制。

此选项不会自动迁移已有 SQLite 历史。首次切换会使用 PostgreSQL 中对应 schema 的历史；需要保留旧任务时，应先停止旧服务、备份原文件并另行执行经过验收的数据迁移，不要让两个后端同时继续同一任务。

## 验证

普通 `make check` 保留完整 SQLite 回归。下面的显式测试需要专用测试 PostgreSQL，创建随机 `test_runs_*` / `test_web_*` schema 并在结束时删除：

```sh
# 在私密环境预先设置 AGENSTRA_TEST_POSTGRES_DSN。
go test -race ./internal/state/runstore ./internal/frontend/service -run TestPostgres -count=1
```

测试复用原合同断言，覆盖 owner 隔离、重新打开、独立连接竞争租约、检查点原子性、持久取消、记忆遗忘、定时执行、聊天排队与历史恢复、审批撤销、浏览器断线回执核对和代际隔离。未提供测试 DSN 时明确跳过，不代表 PostgreSQL 验收通过。
