// This executable uses local business and model substitutes. Replace the
// provider methods with the host's existing APIs and supply its live policy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type demoProvider struct{}

func (*demoProvider) Capabilities() map[string]agenstra.CapabilityDescription {
	caps := map[string]agenstra.CapabilityDescription{}
	for _, name := range []string{"reports.start", "reports.status", "reports.file"} {
		caps[name] = agenstra.CapabilityDescription{Name: name, Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: agenstra.JSON{"type": "object"}, OutputSchema: agenstra.JSON{"type": "object"}}
	}
	start := caps["reports.start"]
	start.Effect, start.Replay = "compute", "idempotent"
	start.Operation = &agenstra.OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "reports.status", PollArgument: []string{"id"}, PendingStates: []string{"running"}, SuccessStates: []string{"completed"}, FailureStates: []string{"failed"}, IntervalSeconds: 1, TimeoutSeconds: 30}
	caps[start.Name] = start
	return caps
}
func (*demoProvider) Skills() map[string]agenstra.Skill { return map[string]agenstra.Skill{} }
func (*demoProvider) SystemPrompt() string              { return "Use verified report evidence." }
func (*demoProvider) Close() error                      { return nil }
func (*demoProvider) Invoke(_ context.Context, name string, _ agenstra.JSON, _ *agenstra.InvocationContext) (agenstra.CapabilityResult, error) {
	data := agenstra.JSON{"id": "report-1", "status": "completed", "value": 7}
	if name == "reports.start" {
		data["status"] = "running"
	}
	if name == "reports.file" {
		data = agenstra.JSON{"report_id": "report-1", "content_type": "application/pdf", "name": "Demo report.pdf"}
	}
	return agenstra.CapabilityResult{Data: data, ReferenceScope: "durable"}, nil
}

type demoModel struct{}

func (demoModel) Decide(context.Context, agenstra.ContextPacket, string) (agenstra.Decision, error) {
	return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Demo analysis completed", FactIDs: []string{}}, nil
}

func ref(pointer string) agenstra.JSON { return agenstra.JSON{"$workflow": pointer} }

func run(ctx context.Context) (resultErr error) {
	dir, err := os.MkdirTemp("", "agenstra-workflow-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(dir)) }()
	store, err := agenstra.NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	if err := store.Initialize(); err != nil {
		return err
	}
	h := agenstra.NewAgentHost(store, func(context.Context, string, string) (agenstra.CapabilityProvider, error) {
		return &demoProvider{}, nil
	}, demoModel{}, func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
		return agenstra.ExecutionPolicy{AllowModelData: true, GrantedCapabilities: map[string]bool{"reports.start": true, "reports.status": true, "reports.file": true, "workflows.list": true, "workflows.start": true, "workflows.status": true, "workflows.cancel": true}}, nil
	})
	h.Workflows = map[string][]agenstra.WorkflowDefinition{"reports": {{
		Name: "report", Version: "1", Description: "Generate and analyze a report",
		InputSchema: agenstra.JSON{"type": "object"},
		Steps: []agenstra.WorkflowStep{
			{ID: "collect", Capability: "reports.start", Arguments: agenstra.JSON{"region": ref("/input/region")}},
			{ID: "analyze", DependsOn: []string{"collect"}, Agent: &agenstra.WorkflowAgent{Instruction: "Analyze the supplied report", Capabilities: []string{}}, Arguments: agenstra.JSON{"value": ref("/steps/collect/data/value")}},
			{ID: "file", DependsOn: []string{"analyze"}, Capability: "reports.file", Arguments: agenstra.JSON{"id": ref("/steps/collect/data/id"), "analysis": ref("/steps/analyze/answer_markdown")}},
		},
		Output: agenstra.JSON{"file": ref("/steps/file/data")}, Answer: ref("/steps/analyze/answer_markdown"),
	}}}
	run, err := h.CreateWorkflow(ctx, "demo-user", "reports", "report", agenstra.JSON{"region": "demo-region"}, "demo-request")
	if err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := h.WakeDue(ctx, 1); err != nil {
			return err
		}
		view, err := h.GetWorkflow(ctx, run.RunID, "demo-user")
		if err != nil {
			return err
		}
		if view.Status == "completed" {
			return json.NewEncoder(os.Stdout).Encode(view)
		}
		if view.Status == "failed" || view.Status == "cancelled" {
			return fmt.Errorf("workflow stopped: %s", view.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
