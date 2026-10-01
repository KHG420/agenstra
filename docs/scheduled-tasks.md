# 定时任务与宿主接入

Agenstra 的 Go Host 内置定时任务模块。宿主保存任务指令和时间规则后，框架按时间创建独立的 Agent run，沿用能力包、用户授权、审批、检查点、事件和 Fact。宿主负责自己的管理页面，以及向用户展示待审批或待补充输入的运行。

## 调度定义

请求包含 `name`（1–200 字符）、`pack_id`（1–128 字节）、`instruction`（1–30000 字符）和 `schedule`。三种时间规则互斥：

| `schedule.kind` | 字段 | 含义 |
| --- | --- | --- |
| `once` | `at` | 未来的 Unix 时间戳，单位为秒，可带小数。时区转换由宿主在提交前完成。 |
| `interval` | `interval_seconds` | 1–31536000 秒；首次在创建时间加间隔后执行。 |
| `cron` | `cron`、`timezone` | 五字段 Cron 与 IANA 时区；省略时区使用 `UTC`。 |

Cron 字段依次为分钟、小时、月内日期、月份、星期，支持数字、`*`、逗号列表、范围和步长，例如 `0 9 * * 1-5`、`*/15 9-17 * * *`。星期日可写 `0` 或 `7`。不支持秒字段、月份/星期名称、宏或 `L/W/#/?`。月内日期和星期同时受限时使用 OR 语义；不可能出现的日期，例如 `0 0 31 2 *`，会被拒绝。

例如，下面的定义每天北京时间 09:00 使用宿主已配置的 `records` 能力包生成日报：

```json
{
  "name": "业务日报",
  "pack_id": "records",
  "instruction": "查询今天的业务记录并生成日报，引用实际查询结果。",
  "schedule": {
    "kind": "cron",
    "cron": "0 9 * * *",
    "timezone": "Asia/Shanghai"
  }
}
```

首次执行为创建之后的下一个匹配分钟。二进制内包含时区数据库，容器不需要另装 `tzdata`。夏令时跳过的本地分钟不执行；回拨时重复的本地分钟可以触发两次，每次对应不同的绝对时间，并继续遵守重叠规则。

## HTTP 管理接口

`agenstra-serve` 启动后即可使用，复用 `Authorization: Bearer <用户 API key>`。`owner_id` 由服务端认证确定，不能从请求体指定。创建和修改定义、恢复调度时会检查该用户对能力包及模型数据的授权。查询、暂停和删除仍允许所属用户操作，因此撤销能力包权限后仍可管理已有定时任务。

| 接口 | 请求与响应 |
| --- | --- |
| `POST /schedules` | 提交完整定义；`201` 返回定时任务，初始 `revision` 为 `0`。每次 POST 创建新定义，网络重试前应核对是否已创建。 |
| `GET /schedules?limit=100` | 列出当前用户的定时任务；`limit` 范围 1–1000。 |
| `GET /schedules/{schedule_id}` | 返回定义、状态、修订号、下次时间和最近一次触发状态。 |
| `PUT /schedules/{schedule_id}` | 提交完整定义及当前 `revision`；替换定义并重新计算下次时间。暂停状态保持暂停，已完成的一次性定义可更新为新的调度。 |
| `POST /schedules/{schedule_id}/pause` | 请求体 `{"revision":N}`；暂停后续触发。 |
| `POST /schedules/{schedule_id}/resume` | 请求体 `{"revision":N}`；恢复已暂停的调度。 |
| `DELETE /schedules/{schedule_id}?revision=N` | 删除定义及触发历史，返回 `204`。已创建的 run、事件和 Fact 保留。 |
| `GET /schedules/{schedule_id}/executions?after=0&limit=100` | 按 `sequence` 升序获取触发历史；下一页使用上一页最后的 `sequence`。 |

创建示例：

```sh
curl -sS http://127.0.0.1:8091/schedules \
  -H "Authorization: Bearer $AGENT_OPERATOR_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"业务日报","pack_id":"records","instruction":"查询今天的业务记录并生成日报。","schedule":{"kind":"cron","cron":"0 9 * * *","timezone":"Asia/Shanghai"}}'
```

定时任务响应有 `schedule_id`、`owner_id`、上述定义字段、`status`、`revision`、`next_run_at`、`created_at`、`updated_at` 和可选的 `last_execution`。时间均为 Unix 秒。修改、启停及实际触发都会增加修订号；宿主遇到 `409 revision_conflict` 应重新读取状态，再提交用户仍希望执行的操作。

定义状态为 `active`、`paused` 或 `completed`。`completed` 表示一次性定义已触发，不代表所创建的 Agent run 已完成。最近触发记录和历史包含 `sequence`、`scheduled_at`、`triggered_at`、可选的 `run_id`、`status` 和可选的 `error_code`。有 run 的记录会反映运行当前的状态，包括 `needs_input`、`needs_approval`、`needs_authorization` 和 `needs_reconciliation`。

宿主使用 `run_id` 调用现有 `GET /runs/{run_id}` 获取状态和回答，读取 `/runs/{run_id}/artifacts/{fact_id}` 获取完整 Fact，并使用原有 input、approval、resume、cancel 接口处理执行。结果接口继续检查当时有效的权限；定时历史仅提供状态和关联 ID。

未认证返回 `401`，访问其他用户的 ID 返回 `404`，权限拒绝返回 `403`，非法定义或参数返回 `422`，修订号冲突或恢复已完成的一次性定义返回 `409`。未知接口返回 `404`，不支持的 HTTP 方法返回 `405`。

## Go 嵌入式调用

先按现有方式配置 `AgentHost` 的存储、模型、用户策略和能力连接，并调用 `host.Store.Initialize()`。使用同一 Host 管理定时任务：

```go
task, err := host.CreateSchedule(ctx, ownerID, agenstra.ScheduleRequest{
    Name: "业务日报",
    PackID: "records",
    Instruction: "查询今天的业务记录并生成日报。",
    Schedule: agenstra.ScheduleSpec{
        Kind: "cron", Cron: "0 9 * * *", Timezone: "Asia/Shanghai",
    },
})
if err != nil {
    return err
}
_, err = host.PauseSchedule(ctx, task.ScheduleID, ownerID, task.Revision)
```

公开方法为 `CreateSchedule`、`GetSchedule`、`ListSchedules`、`UpdateSchedule`、`PauseSchedule`、`ResumeSchedule`、`DeleteSchedule`、`ListScheduleExecutions` 和 `DispatchDueSchedules`。`ScheduleRequest`、`ScheduleSpec`、`ScheduledTask` 和 `ScheduleExecution` 是公开 Go 类型；定义更新使用完整 `ScheduleRequest` 与当前修订号。

嵌入式宿主在自己的、受 context 生命周期控制的周期循环内调用调度和执行：

```go
_, scheduleErr := host.DispatchDueSchedules(ctx, 100)
_, runErr := host.WakeDue(ctx, 100)
return errors.Join(scheduleErr, runErr)
```

`DispatchDueSchedules` 仅创建持久化的 queued run，返回本轮已提交的触发记录数（包含跳过重叠和权限暂停）。`WakeDue` 推进运行到完成或暂停点。HTTP server 的原有 worker 已执行这两个步骤；关闭 worker 时不会自动触发定时任务。HTTP 用户密钥应保留在宿主后端，浏览器通过宿主自己的受认证代理访问；现有 Web ticket 不授权 `/schedules`。

## 重启、重叠和权限语义

- 定义及触发历史保存在现有运行数据库的新增表中。原 v1 run 表结构和 `PRAGMA user_version=1` 保持兼容，旧数据库初始化时自动添加新表；备份运行数据库即可同时保存定时任务。
- 一次触发的 run 创建、事件写入、触发历史和下次时间更新在同一 SQLite 事务内完成。事务失败不留下半次触发；并发扫描以定义修订号防止重复创建，修改、暂停或删除期间的旧扫描也不能提交。
- 停机期间错过的周期在恢复后合并为一次触发，记录最早尚未处理的 `scheduled_at`。间隔保持原始节奏，下一次为当前时间之后的周期点；Cron 取当前时间之后的匹配分钟。过期的一次性任务在恢复后触发一次。
- 同一定义已有非终态 run 时，新的触发记录为 `skipped_overlap`，并推进下次时间。等待输入、审批、授权或核对结果都属于非终态。修改定义也不会允许它与此前创建的运行重叠。
- 暂停和删除只控制后续触发，已有 run 继续存在。要停止已创建运行，宿主调用原 cancel 接口。周期任务恢复时跳过暂停期间的周期，并从原始节奏的下一个未来时间继续；一次性任务恢复时保留原到期时间。
- 触发时重新检查用户授权，并为本次 run 固定当时启用的能力包版本。明确的权限拒绝写入 `needs_authorization` 记录并暂停定义；恢复权限后由宿主提交 resume。临时授权/连接/发布服务故障保留待触发时间，后续 worker 重试，`/readyz` 在故障持续期间反映不可用；其他可处理的定义和 run 继续推进。
- 定时触发遵守与普通 run 相同的能力授权、模型数据策略和精确审批。浏览器页面动作仍需要现有浏览器桥的服务端绑定；调度不会自动建立会话或页面绑定。无人值守任务应选用可在后台执行的 REST/MCP/SDK 能力包。

当前调度与既有框架一样面向单节点 SQLite 持久盘部署。调度精度受 worker 间隔和运行耗时影响，不保证实时触发；触发历史没有自动清理。外部写操作的幂等与不确定结果继续按现有能力契约处理。
