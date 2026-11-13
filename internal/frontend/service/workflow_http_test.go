package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

func serviceWorkflow() agentcontract.WorkflowDefinition {
	return agentcontract.WorkflowDefinition{
		Name: "report", Version: "1", Description: "Create report",
		InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false},
		Steps:       []agentcontract.WorkflowStep{{ID: "read", Capability: "records.get", Arguments: agentcontract.JSON{"id": agentcontract.JSON{"$workflow": "/input/id"}}}},
		Output:      agentcontract.JSON{"id": agentcontract.JSON{"$workflow": "/steps/read/data/id"}}, Answer: "Report complete",
	}
}

func workflowServiceHost(t *testing.T, model *hostModel) (*durablehost.AgentHost, *hostProvider) {
	t.Helper()
	p := &hostProvider{}
	h := testHost(t, testStore(t), p, model)
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{AllowModelData: true, GrantedCapabilities: map[string]bool{"records.get": true, "workflows.list": true, "workflows.start": true, "workflows.status": true, "workflows.cancel": true}}, nil
	}
	h.Workflows = map[string][]agentcontract.WorkflowDefinition{"records": {serviceWorkflow()}}
	return h, p
}

func TestWorkflowHTTPQueuesAndIsolatesOwners(t *testing.T) {
	h, p := workflowServiceHost(t, &hostModel{})
	d := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Users: map[string]agentcontract.UserConfig{"alice": {APIKeyEnv: "ALICE"}, "bob": {APIKeyEnv: "BOB"}}}, Environment: map[string]string{"ALICE": "alice-secret", "BOB": "bob-secret"}}
	s, err := NewHTTPServer(h, d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	scheduleHTTP(t, s, "GET", "/workflows?pack_id=records", "", "", 401)
	catalog := scheduleHTTP(t, s, "GET", "/workflows?pack_id=records", "alice", "", 200)
	if !strings.Contains(catalog.Body.String(), `"name":"report"`) || strings.Contains(catalog.Body.String(), `"steps"`) {
		t.Fatal(catalog.Body.String())
	}
	for _, body := range []string{`{}`, `null`, `{} {}`, `{"pack_id":"records","workflow":"report","input":{},"request_id":"bad"}`, `{"pack_id":"records","workflow":"report","input":{"id":"R-1"},"request_id":"bad","owner_id":"bob"}`} {
		scheduleHTTP(t, s, "POST", "/workflows/runs", "alice", body, 422)
	}
	body := `{"pack_id":"records","workflow":"report","input":{"id":"R-1"},"request_id":"report-1"}`
	response := scheduleHTTP(t, s, "POST", "/workflows/runs", "alice", body, 202)
	var view agentcontract.WorkflowView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.Status != "queued" || p.calls != 0 {
		t.Fatal(view, p.calls, err)
	}
	path := "/workflows/runs/" + view.RunID
	scheduleHTTP(t, s, "GET", path, "bob", "", 404)
	scheduleHTTP(t, s, "POST", path+"/cancel", "bob", "", 404)
	duplicate := scheduleHTTP(t, s, "POST", "/workflows/runs", "alice", body, 202)
	if !strings.Contains(duplicate.Body.String(), view.RunID) {
		t.Fatal("duplicate submission created a new run")
	}
	scheduleHTTP(t, s, "POST", "/workflows/runs", "alice", strings.Replace(body, "R-1", "R-2", 1), 409)
	run, err := h.Drive(t.Context(), view.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
	scheduleHTTP(t, s, "GET", path, "alice", "", 200)
	public := scheduleHTTP(t, s, "GET", "/runs/"+view.RunID, "alice", "", 200)
	if strings.Contains(public.Body.String(), "workflow_catalog") || strings.Contains(public.Body.String(), `"definition"`) {
		t.Fatal("internal definitions exposed by generic run API")
	}
}

func TestWorkflowWebAliasAndExistingApprovalAPI(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	h, _ := workflowServiceHost(t, &hostModel{})
	h.Store = f.h.Store
	definitions := h.Workflows
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "write", Replay: "never", ApprovalRequired: true, ReferenceScope: "durable"}}}
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return p, nil }
	s, err := NewHTTPServer(h, f.d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, mutated := definitions["records-web"]; mutated {
		t.Fatal("constructor modified caller-owned catalog")
	}
	mint := memoryHTTP(t, s, "POST", "/web/v1/token", "alice-key", "", "", 200)
	var token struct{ Token string }
	if err := json.Unmarshal(mint.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	base := "/web/v1/workflows"
	memoryHTTP(t, s, "GET", base+"?integration_id=records-web", token.Token, "", "", 200)
	memoryHTTP(t, s, "POST", base+"/runs?integration_id=records-web", token.Token, "", `{"pack_id":"other","workflow":"report","input":{"id":"R-1"},"request_id":"web"}`, 403)
	response := memoryHTTP(t, s, "POST", base+"/runs?integration_id=records-web", token.Token, "", `{"workflow":"report","input":{"id":"R-1"},"request_id":"web"}`, 202)
	var view agentcontract.WorkflowView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	run, err := h.Drive(t.Context(), view.RunID, "alice")
	if err != nil || run.Status != "needs_approval" {
		t.Fatal(run.Status, err)
	}
	blocked := memoryHTTP(t, s, "GET", base+"/runs/"+run.RunID+"?integration_id=records-web", token.Token, "", "", 200)
	if err := json.Unmarshal(blocked.Body.Bytes(), &view); err != nil || view.BlockedRunID != run.RunID || view.Steps[0].Status != "needs_approval" {
		t.Fatal(view, err)
	}
	memoryHTTP(t, s, "GET", "/web/v1/runs/"+run.RunID, token.Token, "", "", 200)
	state, err := h.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	item := state.Pending[0]
	body := fmt.Sprintf(`{"invocation_id":%q,"arguments_sha256":%q,"revision":%d,"approved":true}`, item.InvocationID, item.ArgumentsSHA256, run.Revision)
	memoryHTTP(t, s, "POST", "/web/v1/runs/"+run.RunID+"/approval", token.Token, "", body, 200)
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 1 {
		t.Fatal(run.Status, p.calls, err)
	}
}

func TestWorkflowWorkerOneSlotExecutesAgentStartedWorkflowAndChild(t *testing.T) {
	decision := callDecision("workflows.start")
	decision.Calls[0].Arguments = agentcontract.JSON{"workflow": "report", "input": agentcontract.JSON{"id": "R-1"}}
	model := &hostModel{decisions: []agentcontract.Decision{decision}}
	h, p := workflowServiceHost(t, model)
	h.Settings.MaxConcurrentRuns = 1
	definition := serviceWorkflow()
	definition.Steps = append(definition.Steps, agentcontract.WorkflowStep{ID: "analyze", DependsOn: []string{"read"}, Agent: &agentcontract.WorkflowAgent{Instruction: "Analyze supplied report", Capabilities: []string{}}, Arguments: agentcontract.JSON{"id": agentcontract.JSON{"$workflow": "/steps/read/data/id"}}})
	definition.Answer = agentcontract.JSON{"$workflow": "/steps/analyze/answer_markdown"}
	h.Workflows["records"] = []agentcontract.WorkflowDefinition{definition}
	run := createTestHostRun(t, h)
	s, err := NewHTTPServer(h, &deployassembly.Deployment{}, true, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("single-slot worker did not complete workflow and Agent child")
		case <-ticker.C:
			current, err := h.Store.GetRun(run.RunID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if !durablehost.Terminal(current.Status) {
				continue
			}
			if current.Status != "completed" {
				t.Fatal(current.Status, current.State["runtime"])
			}
			p.mu.Lock()
			calls := p.calls
			p.mu.Unlock()
			if calls != 1 {
				t.Fatal("business step replayed", calls)
			}
			return
		}
	}
}
