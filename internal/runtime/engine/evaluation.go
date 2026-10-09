package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// EvaluationCase specifies one explicit evaluation request and evidence requirements.
type EvaluationCase struct {
	Name                  string            `json:"name"`
	PackID                string            `json:"pack_id"`
	Instruction           string            `json:"instruction"`
	RunID                 string            `json:"run_id,omitempty"`
	RequestID             string            `json:"request_id,omitempty"`
	ExpectedStatus        string            `json:"expected_status,omitempty"`
	RequiredCapabilities  []string          `json:"required_capabilities,omitempty"`
	ForbiddenCapabilities []string          `json:"forbidden_capabilities,omitempty"`
	Facts                 []FactRequirement `json:"facts,omitempty"`
}

// EvaluationCheck records whether a requested evidence assertion passed.
type EvaluationCheck struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Passed bool   `json:"passed"`
	Code   string `json:"code,omitempty"`
}

// EvaluationResult combines persisted run evidence, diagnostics and evaluation checks.
type EvaluationResult struct {
	Name        string            `json:"name"`
	Iteration   int               `json:"iteration,omitempty"`
	ElapsedMS   int64             `json:"elapsed_ms,omitempty"`
	RunID       string            `json:"run_id,omitempty"`
	RequestID   string            `json:"request_id,omitempty"`
	Status      string            `json:"status,omitempty"`
	Passed      bool              `json:"passed"`
	Checks      []EvaluationCheck `json:"checks"`
	ErrorCode   string            `json:"error_code,omitempty"`
	Diagnostics *RunDiagnostics   `json:"diagnostics,omitempty"`
}

// Validate rejects unsupported evaluation scope and requirements before execution.
func (c EvaluationCase) Validate() error {
	if len(c.RequestID) > 128 {
		return fmt.Errorf("evaluation request_id too long")
	}
	if c.Name == "" || (c.RunID == "" && (c.PackID == "" || c.Instruction == "")) || (c.RunID != "" && !validUUID(c.RunID)) {
		return fmt.Errorf("evaluation case identity invalid")
	}
	status := c.ExpectedStatus
	if status == "" {
		status = "completed"
	}
	switch status {
	case "completed", "failed", "cancelled", "needs_input", "needs_approval", "needs_authorization", "needs_reconciliation":
	default:
		return fmt.Errorf("evaluation expected_status invalid")
	}
	if len(c.Facts) > 0 && status != "completed" {
		return fmt.Errorf("fact checks require completed status")
	}
	if _, err := RequireFactValues(c.Facts...); err != nil {
		return err
	}
	for _, names := range [][]string{c.RequiredCapabilities, c.ForbiddenCapabilities} {
		for _, name := range names {
			if name == "" {
				return fmt.Errorf("evaluation capability required")
			}
		}
	}
	return nil
}

// EvaluateRun verifies persisted state and evidence. It neither invokes a model
// nor modifies a run. Completion alone is not sufficient when assertions exist.
func EvaluateRun(ctx context.Context, run StoredRun, c EvaluationCase) (EvaluationResult, error) {
	if err := c.Validate(); err != nil {
		return EvaluationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return EvaluationResult{}, err
	}
	raw, err := CanonicalJSON(run.State["runtime"])
	if err != nil {
		return EvaluationResult{}, err
	}
	var state RuntimeState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&state); err != nil || state.RunID != run.RunID {
		return EvaluationResult{}, hostError("run_state_invalid")
	}
	result := EvaluationResult{Name: c.Name, RunID: run.RunID, Status: run.Status, Passed: true, Checks: []EvaluationCheck{}}
	add := func(kind, target string, passed bool, code string) {
		if passed {
			code = ""
		}
		result.Checks = append(result.Checks, EvaluationCheck{kind, target, passed, code})
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
	latest := map[string]Observation{}
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
			return result, hostError("run_state_invalid")
		}
		var decision Decision
		if json.Unmarshal(last, &decision) == nil && decision.Kind == "final" {
			citations = decision.FactIDs
		}
	}
	for _, requirement := range c.Facts {
		check, err := RequireFactValues(requirement)
		if err != nil {
			return result, err
		}
		err = check(ctx, CompletionContext{RunID: run.RunID, OriginPackID: run.PackID, Instruction: state.Instruction, AnswerMarkdown: state.AnswerMarkdown, FactIDs: citations, Facts: state.Facts, Observations: state.Observations})
		code := ""
		if err != nil {
			code = ErrorCode(err)
		}
		add("fact", requirement.Capability, run.Status == "completed" && err == nil, code)
	}
	return result, nil
}
