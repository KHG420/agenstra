package agenstra

import (
	"context"
	"encoding/json"
	"strings"
)

type DiagnosticFinding struct {
	Category   string `json:"category"`
	Code       string `json:"code"`
	Capability string `json:"capability,omitempty"`
	Message    string `json:"message"`
	NextAction string `json:"next_action"`
	Recovered  bool   `json:"recovered"`
	Actionable bool   `json:"actionable"`
}
type RunDiagnostics struct {
	Schema    string              `json:"schema"`
	RunID     string              `json:"run_id"`
	Status    string              `json:"status"`
	Revision  int                 `json:"revision"`
	ElapsedMS int64               `json:"elapsed_ms"`
	Findings  []DiagnosticFinding `json:"findings"`
	Progress  *RunProgress        `json:"progress"`
	Budget    RunBudget           `json:"budget"`
}

// ExplainRunError never returns upstream error text, prompts or arguments.
func ExplainRunError(code, capability string) DiagnosticFinding {
	f := DiagnosticFinding{Actionable: true, Category: "business", Code: code, Capability: capability, Message: "业务操作未完成。", NextAction: "核对业务接口的返回状态与实际数据。"}
	switch {
	case code == "browser_context_required" || code == "browser_context_changed":
		f.Category, f.Message, f.NextAction = "browser", "页面观察数据尚未读取或已发生改变。", "先读取 ui.get_context 获取当前页面状态，再决定是否重新提交页面操作；活动写入还需核对宿主业务版本。"
	case strings.Contains(code, "authorization") || strings.Contains(code, "unauthorized") || strings.Contains(code, "forbidden") || strings.Contains(code, "not_granted") || strings.Contains(code, "identity") || strings.Contains(code, "access_denied"):
		f.Category, f.Message, f.NextAction = "authorization", "用户身份或业务权限未满足执行条件。", "检查宿主身份映射、连接凭据和能力授权，恢复权限后继续任务。"
	case strings.Contains(code, "input_invalid") || strings.Contains(code, "arguments") || strings.Contains(code, "schema") || strings.Contains(code, "contract") || strings.Contains(code, "pack_changed") || strings.Contains(code, "capability_unknown"):
		f.Category, f.Message, f.NextAction = "contract", "接口契约或调用参数与当前能力不匹配。", "检查参数类型和接口版本；契约变化时重新导入并发布新版本。"
	case strings.Contains(code, "budget") || strings.Contains(code, "limit") || strings.Contains(code, "deadline"):
		f.Category, f.Message, f.NextAction = "budget", "任务达到运行时间或资源限制。", "检查任务范围和调用次数，再决定是否调整预算。"
	case strings.Contains(code, "model") || strings.Contains(code, "decision") || strings.Contains(code, "stagnant") || strings.Contains(code, "no_progress"):
		f.Category, f.Message, f.NextAction = "model", "模型请求或任务决策未能完成。", "检查模型服务、结构化决策兼容性、任务描述和运行预算。"
	case strings.Contains(code, "unknown") || strings.Contains(code, "reconciliation"):
		f.Category, f.Message, f.NextAction = "reconciliation", "操作可能已执行，但结果尚未确认。", "用原请求的业务证据核对结果，再提交核对；保持任务暂停直至结果明确。"
	case strings.Contains(code, "connection") || strings.Contains(code, "transport") || strings.Contains(code, "timeout") || strings.Contains(code, "unavailable") || strings.Contains(code, "http"):
		f.Category, f.Message, f.NextAction = "connection", "外部服务连接未完成。", "检查服务地址、凭据引用、网络和上游服务状态。"
	case strings.Contains(code, "completion") || code == "final_fact_citations_invalid" || code == "final_result_refs_invalid":
		f.Category, f.Message, f.NextAction = "completion", "任务结果没有满足业务成功标准。", "检查最新业务证据、成功字段和最终回答引用。"
	}
	return f
}

// GetDiagnostics reads authorized persisted evidence without calling a model
// or opening a business connection.
func (h *AgentHost) GetDiagnostics(ctx context.Context, id, owner string) (RunDiagnostics, error) {
	run, err := h.Get(ctx, id, owner)
	if err != nil {
		return RunDiagnostics{}, err
	}
	telemetry, err := h.telemetry(run)
	if err != nil {
		return RunDiagnostics{}, err
	}
	raw, err := CanonicalJSON(run.State["runtime"])
	if err != nil {
		return RunDiagnostics{}, err
	}
	var state RuntimeState
	if err := json.Unmarshal(raw, &state); err != nil {
		return RunDiagnostics{}, hostError("run_state_invalid")
	}
	end := h.now()
	if run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled" {
		end = run.UpdatedAt
	}
	result := RunDiagnostics{Schema: "agenstra.run-diagnostics.v1", RunID: id, Status: run.Status, Revision: run.Revision, ElapsedMS: int64(max(0, end-run.CreatedAt) * 1000), Findings: []DiagnosticFinding{}, Progress: runProgress(&state, h.runSettings(run).MaxStagnantRounds), Budget: telemetry.Budget}
	seen := map[string]int{}
	add := func(f DiagnosticFinding) {
		key := f.Code + ":" + f.Capability
		if index, exists := seen[key]; exists {
			// Any unresolved occurrence takes precedence over recovered history.
			if !f.Recovered {
				result.Findings[index] = f
			}
		} else {
			seen[key] = len(result.Findings)
			result.Findings = append(result.Findings, f)
		}
	}
	if state.ErrorCode != nil {
		add(ExplainRunError(*state.ErrorCode, ""))
	}
	latest := map[string]int{}
	unresolved := map[string]bool{}
	for _, item := range state.Pending {
		if item.Status != "succeeded" && item.Status != "failed" {
			unresolved[item.Call.CallRef] = true
		}
	}
	for i, observation := range state.Observations {
		latest[observation.CallRef] = i
	}
	for i, observation := range state.Observations {
		if observation.ErrorCode == nil {
			continue
		}
		finding := ExplainRunError(*observation.ErrorCode, observation.Capability)
		// Only explicit later success is recovery evidence. An uncertain write
		// cannot be cleared by another command with similar arguments.
		if observation.Capability == "agent.final" && run.Status == "completed" {
			finding.Recovered = true
		} else {
			for j := i + 1; j < len(state.Observations); j++ {
				next := state.Observations[j]
				if latest[next.CallRef] != j || unresolved[next.CallRef] || next.Status != "succeeded" || next.ErrorCode != nil {
					continue
				}
				sameCall := next.CallRef == observation.CallRef
				sameArguments := next.Capability == observation.Capability && ArgumentsDigest(ToolCall{Capability: next.Capability, Arguments: next.Arguments}) == ArgumentsDigest(ToolCall{Capability: observation.Capability, Arguments: observation.Arguments})
				if sameCall || (sameArguments && finding.Category != "reconciliation" && !unresolved[observation.CallRef]) {
					finding.Recovered = true
					break
				}
			}
		}
		finding.Actionable = !finding.Recovered
		add(finding)
	}
	for _, item := range state.Pending {
		if item.ErrorCode != nil && item.Status != "failed" && item.Status != "succeeded" {
			add(ExplainRunError(*item.ErrorCode, item.Call.Capability))
		}
	}
	switch run.Status {
	case "needs_input":
		add(DiagnosticFinding{Actionable: true, Category: "input", Code: "needs_input", Message: "任务需要补充业务信息。", NextAction: "根据任务追问提供信息后继续。"})
	case "needs_approval":
		add(DiagnosticFinding{Actionable: true, Category: "approval", Code: "needs_approval", Message: "业务操作等待用户确认。", NextAction: "核对操作和精确参数，再批准或拒绝。"})
	case "needs_authorization":
		add(ExplainRunError("authorization_required", ""))
	case "needs_reconciliation":
		add(ExplainRunError("reconciliation_required", ""))
	}
	return result, nil
}
