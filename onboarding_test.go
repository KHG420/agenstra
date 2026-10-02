package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPDiscoveryPinsCompleteContractAndNeverInvokes(t *testing.T) {
	tool := JSON{"name": "orders.read", "description": "Read an order", "inputSchema": JSON{"type": "object"}, "outputSchema": JSON{"type": "object"}, "annotations": JSON{"readOnlyHint": true}}
	invalid := JSON{"name": "orders.legacy", "inputSchema": JSON{"type": "object"}}
	calls, closes := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			closes++
			w.WriteHeader(204)
			return
		}
		var request JSON
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "test-session")
		if request["method"] == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		result := JSON{"protocolVersion": "2025-03-26"}
		switch request["method"] {
		case "tools/list":
			result = JSON{"tools": []any{tool, invalid}}
		case "tools/call":
			calls++
		}
		_ = json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result})
	}))
	defer upstream.Close()
	tools, err := DiscoverMCPTools(t.Context(), MCPSource{Transport: "streamable_http", URLEnv: strptr("MCP_URL")}, map[string]string{"MCP_URL": upstream.URL})
	if err != nil || len(tools) != 2 || calls != 0 || closes != 1 {
		t.Fatal(tools, err, calls, closes)
	}
	if tools[0].Supported || tools[0].Issue == "" || !tools[1].Supported || tools[1].ContractSHA256 != MCPContractDigest(tool) {
		t.Fatal(tools)
	}
	if tools[1].Exposure.Effect != "write" || tools[1].Exposure.Replay != "never" || !tools[1].Exposure.ApprovalRequired {
		t.Fatal("untrusted annotation relaxed execution", tools[1])
	}
	r := newDraftRegistry(t)
	draft, err := r.SaveDraft("discover", intRef(0), JSON{"schema": "agenstra.mcp-pack.v1", "name": "orders", "version": "1", "guidance": "Use orders", "source": JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &Deployment{Registry: r, Config: DeploymentConfig{Management: &ManagementConfig{AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ADMIN_KEY": "distinct-admin-key-at-least-24", "UPSTREAM_URL": upstream.URL}}
	s := &HTTPServer{Deployment: d}
	request := func(key string, revision int, refs map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(JSON{"expected_revision": revision, "environment": refs})
		req := httptest.NewRequest("POST", "/admin/api/drafts/discover/discover", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.adminHTTP(w, req)
		return w
	}
	if w := request("user-key", draft.Revision, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(d.Environment["ADMIN_KEY"], draft.Revision+1, nil); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request(d.Environment["ADMIN_KEY"], draft.Revision, map[string]string{"MCP_URL": "http://injected-url"}); w.Code != 422 {
		t.Fatal(w.Code)
	}
	w := request(d.Environment["ADMIN_KEY"], draft.Revision, map[string]string{"MCP_URL": "UPSTREAM_URL"})
	if w.Code != 200 || strings.Contains(w.Body.String(), upstream.URL) || strings.Contains(w.Body.String(), d.Environment["ADMIN_KEY"]) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := r.Draft(draft.DraftID)
	if after.Revision != draft.Revision || len(after.Manifest["tools"].([]any)) != 0 || calls != 0 || closes != 2 {
		t.Fatal("discovery changed grants, draft or business state")
	}
}

func TestDeploymentCompletionChecksApplyOnlyToUsedOperations(t *testing.T) {
	h := &AgentHost{}
	d := &Deployment{Config: DeploymentConfig{CompletionChecks: map[string][]FactRequirement{"orders": {{Capability: "orders.approve", Path: []any{"approved"}, Value: true}}}}}
	if err := configureDeploymentChecks(h, d); err != nil {
		t.Fatal(err)
	}
	if err := h.CompletionValidator(t.Context(), CompletionContext{OriginPackID: "orders"}); err != nil {
		t.Fatal("greeting required a write", err)
	}
	fact := Fact{FactID: NewID(), SourceCapability: "orders.approve", Value: JSON{"approved": false}}
	result := CompletionContext{OriginPackID: "orders", FactIDs: []string{fact.FactID}, Facts: []Fact{fact}}
	if err := h.CompletionValidator(t.Context(), result); ErrorCode(err) != "completion_evidence_mismatch" {
		t.Fatal(err)
	}
	fact.Value["approved"] = true
	result.Facts = []Fact{fact}
	if err := h.CompletionValidator(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	result.Observations = []Observation{{Capability: "orders.approve", ErrorCode: strptr("operation_failed")}}
	if err := h.CompletionValidator(t.Context(), result); ErrorCode(err) != "completion_operation_failed" {
		t.Fatal(err)
	}
	result.Facts = nil
	if err := h.CompletionValidator(t.Context(), result); err == nil {
		t.Fatal("failed operation passed with no evidence")
	}
}

func TestDeclarativeReconciliationChecksOriginalIdentityAndReadPermission(t *testing.T) {
	for _, mode := range []string{"confirmed", "not_confirmed", "not_granted", "write_verifier", "large_number", "large_number_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			cap := CapabilityDescription{Name: "records.get", Version: "1", InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object", "required": []any{"confirmed"}, "properties": JSON{"confirmed": JSON{"const": true}}}, Effect: "write", Replay: "never"}
			verify := CapabilityDescription{Name: "records.verify", Version: "1", InputSchema: JSON{"type": "object"}, OutputSchema: JSON{"type": "object"}, Effect: "read", Replay: "safe"}
			if mode == "write_verifier" {
				verify.Effect = "write"
			}
			writes, reads := 0, 0
			p := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap, verify.Name: verify}, hook: func(_ context.Context, name string, args JSON, inv *InvocationContext) (CapabilityResult, error) {
				if name == cap.Name {
					writes++
					return CapabilityResult{}, errors.New("lost response")
				}
				reads++
				query, _ := args["query"].(map[string]any)
				if query["request_id"] == nil || query["request_id"] == "" {
					t.Error("original identity absent")
				}
				var found any = mode != "not_confirmed"
				if strings.HasPrefix(mode, "large_number") {
					found = json.Number("9007199254740993")
				}
				return CapabilityResult{Data: JSON{"found": found, "result": JSON{"confirmed": true}}}, nil
			}}
			h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision(cap.Name)}})
			h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
				return ExecutionPolicy{GrantedCapabilities: map[string]bool{cap.Name: true, verify.Name: mode != "not_granted"}, AllowModelData: true}, nil
			}
			rule := ReconciliationRule{VerifyCapability: verify.Name, Arguments: map[string][]any{"/query/request_id": {"idempotency_key"}}, SuccessPath: []any{"found"}, SuccessValue: true, ResultPath: []any{"result"}}
			if mode == "large_number" {
				rule.SuccessValue = json.Number("9007199254740993")
			} else if mode == "large_number_mismatch" {
				rule.SuccessValue = json.Number("9007199254740992")
			}
			d := &Deployment{Config: DeploymentConfig{ReconciliationChecks: map[string]map[string]ReconciliationRule{"records": {cap.Name: rule}}}}
			if err := configureDeploymentChecks(h, d); err != nil {
				t.Fatal(err)
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status != "needs_reconciliation" {
				t.Fatal(run.Status, err)
			}
			state, _ := h.restore(run)
			item := state.Pending[0]
			run, err = h.Reconcile(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision)
			if mode == "confirmed" || mode == "large_number" {
				if err != nil || run.Status != "queued" || reads != 1 {
					t.Fatal(run.Status, err, reads)
				}
			} else if err == nil {
				t.Fatal("unverified result accepted")
			}
			if writes != 1 {
				t.Fatal("original write replayed", writes)
			}
		})
	}
	bad := &Deployment{Config: DeploymentConfig{ReconciliationChecks: map[string]map[string]ReconciliationRule{"records": {"records.get": {VerifyCapability: "records.verify"}}}}}
	if err := configureDeploymentChecks(&AgentHost{}, bad); err == nil {
		t.Fatal("uncorrelated query accepted")
	}
}

func TestRunDiagnosticsReadOnlyAndOwnerScoped(t *testing.T) {
	h, p, run, _ := uncertainRun(t)
	before, _ := h.Store.GetRun(run.RunID, "alice")
	report, err := h.GetDiagnostics(t.Context(), run.RunID, "alice")
	if err != nil || report.Status != "needs_reconciliation" || len(report.Findings) == 0 {
		t.Fatal(report, err)
	}
	if _, err = h.GetDiagnostics(t.Context(), run.RunID, "bob"); !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	s := &HTTPServer{Host: h}
	req := httptest.NewRequest("GET", "/runs/"+run.RunID+"/diagnostics", nil)
	out := httptest.NewRecorder()
	s.runHTTP(out, req, "alice")
	if out.Code != 200 || strings.Contains(out.Body.String(), "R-1") || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
	after, _ := h.Store.GetRun(run.RunID, "alice")
	if before.Revision != after.Revision || p.calls != 1 {
		t.Fatal("diagnostic read invoked a tool or mutated a run")
	}
}

func TestEvaluateRunRequiresLatestCitedBusinessEvidence(t *testing.T) {
	id := NewID()
	fact := Fact{FactID: NewID(), SourceCapability: "orders.approve", Value: JSON{"approved": true}}
	state := RuntimeState{RunID: id, Status: "completed", Facts: []Fact{fact}, Observations: []Observation{{Capability: fact.SourceCapability, Status: "succeeded"}}, Decisions: []JSON{{"kind": "final", "fact_ids": []string{fact.FactID}}}}
	run := StoredRun{RunID: id, PackID: "orders", Status: "completed", State: JSON{"runtime": state}}
	c := EvaluationCase{Name: "approve", PackID: "orders", Instruction: "Approve", RequiredCapabilities: []string{fact.SourceCapability}, ForbiddenCapabilities: []string{"orders.delete"}, Facts: []FactRequirement{{Capability: fact.SourceCapability, Path: []any{"approved"}, Value: true}}}
	result, err := EvaluateRun(t.Context(), run, c)
	if err != nil || !result.Passed {
		t.Fatal(result, err)
	}
	state.Facts = append(state.Facts, Fact{FactID: NewID(), SourceCapability: fact.SourceCapability, Value: JSON{"approved": false}})
	run.State["runtime"] = state
	result, err = EvaluateRun(t.Context(), run, c)
	if err != nil || result.Passed {
		t.Fatal("older evidence passed", result, err)
	}
	state.Facts = []Fact{fact}
	state.Pending = []Invocation{{Call: ToolCall{Capability: "orders.delete"}}}
	run.State["runtime"] = state
	result, err = EvaluateRun(t.Context(), run, c)
	if err != nil || result.Passed {
		t.Fatal("forbidden operation passed", result, err)
	}
}
