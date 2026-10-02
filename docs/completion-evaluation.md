# 完成校验与模型评测

`completed` 表示当前运行的回答已结束。业务动作是否完成，还需要可验证的结果契约；Fact 引用检查只保证引用存在，无法证明自然语言中的每句话正确。

嵌入式宿主可配置 `AgentHost.CompletionValidator`；临时运行可配置 `AgentRuntime.CompletionValidator`。回调在引用检查通过后接收完整 Fact、观察、任务、补充输入及拟返回的回答，应保持只读并响应取消。返回 nil 才会完成；返回 `CompletionValidationError` 会把安全的错误码和明确反馈加入观察，让模型在原轮次预算内修正。其他错误仅暴露 `completion_validation_failed`，不会把内部错误详情发给模型。重启后的宿主应使用相同业务校验配置。

`RequireFactValues` 可检查指定能力的最新 Fact、引用和字段值，例如：

```go
check, err := agenstra.RequireFactValues(agenstra.FactRequirement{
    Capability: "job.status",
    Path: []any{"data", "status"},
    Value: "succeeded",
})
if err != nil { return err }
host.CompletionValidator = check
```

不同任务应选择对应的校验规则；不要把业务动作完成规则应用于问候、讨论和解释。需要验证报告中的数字、单位或正式产物时，可组合业务专用回答校验。通用框架不会用字符串匹配替代业务语义判断，也不会因有效的 Fact ID 就保证回答正确。

回归测试覆盖错误回答修正、最新证据、异步状态、失败操作、完整数据及错误脱敏。真实模型评测使用合成工具，不访问业务系统；需显式选择并提供自己的模型网关：

```sh
AGENSTRA_LIVE_EVAL=1 go test -run TestLiveAgentCompletionEvaluation -v
```

环境变量使用 `AGENT_MODEL`、`AGENT_MODEL_BASE_URL` 和 `AGENT_MODEL_API_KEY`。评测打印每个任务的结果、工具调用、模型决策数量和耗时；默认 CI 跳过付费模型调用。测试通过代表这些案例通过，不能替代目标场景的成功率、事实一致性和延迟评测。
