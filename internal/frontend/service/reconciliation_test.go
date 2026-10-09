package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
)

func uncertainRun(t *testing.T) (*durablehost.AgentHost, *hostProvider, agentcontract.StoredRun, agentcontract.Invocation) {
	t.Helper()
	cap := agentcontract.CapabilityDescription{Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, OutputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"confirmed": agentcontract.JSON{"const": true}}, "required": []any{"confirmed"}}, Effect: "write", Replay: "never"}
	p := &hostProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}, hook: func(context.Context, string, agentcontract.JSON, *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{}, errors.New("lost response after possible commit")
	}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []agentcontract.Decision{callDecision(cap.Name)}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_reconciliation" {
		t.Fatal(run.Status, err)
	}
	state, callErr := h.Restore(run)
	if callErr != nil {
		t.Error(callErr)
	}
	return h, p, run, state.Pending[0]
}

func TestReconciliationHTTPDoesNotTrustClientOutcome(t *testing.T) {
	h, _, run, item := uncertainRun(t)
	s := &HTTPServer{Host: h}
	body := agentcontract.JSON{"invocation_id": item.InvocationID, "arguments_sha256": item.ArgumentsSHA256, "revision": run.Revision}
	request := func(payload agentcontract.JSON) *httptest.ResponseRecorder {
		raw, callErr11 := agentcontract.CanonicalJSON(payload)
		if callErr11 != nil {
			t.Error(callErr11)
		}
		req := httptest.NewRequest("POST", "/runs/"+run.RunID+"/reconcile", strings.NewReader(string(raw)))
		out := httptest.NewRecorder()
		s.runHTTP(out, req, "alice")
		return out
	}
	if out := request(body); out.Code != 503 {
		t.Fatal(out.Code, out.Body.String())
	}
	body["data"] = agentcontract.JSON{"confirmed": true}
	if out := request(body); out.Code != 422 {
		t.Fatal("client result accepted", out.Code, out.Body.String())
	}
	delete(body, "data")
	h.Reconciler = func(context.Context, agentcontract.ReconciliationContext) (agentcontract.CapabilityResult, error) {
		return agentcontract.CapabilityResult{Data: agentcontract.JSON{"confirmed": true}}, nil
	}
	if out := request(body); out.Code != 200 || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
}
