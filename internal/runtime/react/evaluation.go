package react

import (
	"bytes"
	"context"
	"encoding/json"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// EvaluateRun verifies persisted state and evidence. It neither invokes a model
// nor modifies a run. Completion alone is not sufficient when assertions exist.
func EvaluateRun(ctx context.Context, run agentcontract.StoredRun, c agentcontract.EvaluationCase) (agentcontract.EvaluationResult, error) {
	if err := c.Validate(); err != nil {
		return agentcontract.EvaluationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return agentcontract.EvaluationResult{}, err
	}
	raw, err := agentcontract.CanonicalJSON(run.State["runtime"])
	if err != nil {
		return agentcontract.EvaluationResult{}, err
	}
	var state agentcontract.RuntimeState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&state); err != nil || state.RunID != run.RunID {
		return agentcontract.EvaluationResult{}, agentcontract.NewHostError("run_state_invalid")
	}
	result := agentcontract.EvaluationResult{Name: c.Name, RunID: run.RunID, Status: run.Status, Passed: true, Checks: []agentcontract.EvaluationCheck{}}
	add := func(kind, target string, passed bool, code string) {
		if passed {
			code = ""
		}
		result.Checks = append(result.Checks, agentcontract.EvaluationCheck{Kind: kind, Target: target, Passed: passed, Code: code})
		result.Passed = result.Passed && passed
	}
	status := c.ExpectedStatus
	if status == "" {
		status = "completed"
	}
	add("status", status, run.Status == status, "unexpected_status")
	if c.PackID != "" {
		add("pack", c.PackID, run.PackID == c.PackID, "unexpected_pack")
	}
	succeeded, attempted := map[string]bool{}, map[string]bool{}
	latest := map[string]agentcontract.Observation{}
	for _, observation := range state.Observations {
		attempted[observation.Capability] = true
		latest[observation.Capability] = observation
	}
	for name, observation := range latest {
		succeeded[name] = observation.Status == "succeeded" && observation.ErrorCode == nil
	}
	for _, pending := range state.Pending {
		attempted[pending.Call.Capability] = true
	}
	for _, name := range c.RequiredCapabilities {
		add("capability", name, succeeded[name], "required_capability_not_succeeded")
	}
	for _, name := range c.ForbiddenCapabilities {
		add("forbidden_capability", name, !attempted[name], "forbidden_capability_attempted")
	}
	var citations []string
	if len(state.Decisions) > 0 {
		last, err := json.Marshal(state.Decisions[len(state.Decisions)-1])
		if err != nil {
			return result, agentcontract.NewHostError("run_state_invalid")
		}
		var decision agentcontract.Decision
		if json.Unmarshal(last, &decision) == nil && decision.Kind == "final" {
			citations = decision.FactIDs
		}
	}
	for _, requirement := range c.Facts {
		check, err := agentcontract.RequireFactValues(requirement)
		if err != nil {
			return result, err
		}
		err = check(ctx, agentcontract.CompletionContext{RunID: run.RunID, OriginPackID: run.PackID, Instruction: state.Instruction, AnswerMarkdown: state.AnswerMarkdown, FactIDs: citations, Facts: state.Facts, Observations: state.Observations})
		code := ""
		if err != nil {
			code = agentcontract.ErrorCode(err)
		}
		add("fact", requirement.Capability, run.Status == "completed" && err == nil, code)
	}
	return result, nil
}
