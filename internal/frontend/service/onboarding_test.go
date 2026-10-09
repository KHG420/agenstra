package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestMCPDiscoveryPinsCompleteContractAndNeverInvokes(t *testing.T) {
	tool := agentcontract.JSON{"name": "orders.read", "description": "Read an order", "inputSchema": agentcontract.JSON{"type": "object"}, "outputSchema": agentcontract.JSON{"type": "object"}, "annotations": agentcontract.JSON{"readOnlyHint": true}}
	invalid := agentcontract.JSON{"name": "orders.legacy", "inputSchema": agentcontract.JSON{"type": "object"}}
	calls, closes := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			closes++
			w.WriteHeader(204)
			return
		}
		var request agentcontract.JSON
		if callErr := json.NewDecoder(r.Body).Decode(&request); callErr != nil {
			t.Error(callErr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "test-session")
		if request["method"] == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		result := agentcontract.JSON{"protocolVersion": "2025-03-26"}
		switch request["method"] {
		case "tools/list":
			result = agentcontract.JSON{"tools": []any{tool, invalid}}
		case "tools/call":
			calls++
		}
		if callErr2 := json.NewEncoder(w).Encode(agentcontract.JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer upstream.Close()
	tools, err := DiscoverMCPTools(t.Context(), agentcontract.MCPSource{Transport: "streamable_http", URLEnv: agentcontract.Strptr("MCP_URL")}, map[string]string{"MCP_URL": upstream.URL})
	if err != nil || len(tools) != 2 || calls != 0 || closes != 1 {
		t.Fatal(tools, err, calls, closes)
	}
	if tools[0].Supported || tools[0].Issue == "" || !tools[1].Supported || tools[1].ContractSHA256 != capabilitypack.MCPContractDigest(tool) {
		t.Fatal(tools)
	}
	if tools[1].Exposure.Effect != "write" || tools[1].Exposure.Replay != "never" || !tools[1].Exposure.ApprovalRequired {
		t.Fatal("untrusted annotation relaxed execution", tools[1])
	}
	r := newDraftRegistry(t)
	draft, err := r.SaveDraft("discover", intRef(0), agentcontract.JSON{"schema": "agenstra.mcp-pack.v1", "name": "orders", "version": "1", "guidance": "Use orders", "source": agentcontract.JSON{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := &deployassembly.Deployment{Registry: r, Config: agentcontract.DeploymentConfig{Management: &agentcontract.ManagementConfig{AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ADMIN_KEY": "distinct-admin-key-at-least-24", "UPSTREAM_URL": upstream.URL}}
	s := &HTTPServer{Deployment: d}
	request := func(key string, revision int, refs map[string]string) *httptest.ResponseRecorder {
		raw, callErr3 := json.Marshal(agentcontract.JSON{"expected_revision": revision, "environment": refs})
		if callErr3 != nil {
			t.Error(callErr3)
		}
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
	after, callErr4 := r.Draft(draft.DraftID)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if after.Revision != draft.Revision || len(after.Manifest["tools"].([]any)) != 0 || calls != 0 || closes != 2 {
		t.Fatal("discovery changed grants, draft or business state")
	}
}

func TestRunDiagnosticsReadOnlyAndOwnerScoped(t *testing.T) {
	h, p, run, _ := uncertainRun(t)
	before, callErr6 := h.Store.GetRun(run.RunID, "alice")
	if callErr6 != nil {
		t.Error(callErr6)
	}
	report, err := h.GetDiagnostics(t.Context(), run.RunID, "alice")
	if err != nil || report.Status != "needs_reconciliation" || len(report.Findings) == 0 {
		t.Fatal(report, err)
	}
	if _, err = h.GetDiagnostics(t.Context(), run.RunID, "bob"); !errors.Is(err, runstore.ErrRunNotFound) {
		t.Fatal(err)
	}
	s := &HTTPServer{Host: h}
	req := httptest.NewRequest("GET", "/runs/"+run.RunID+"/diagnostics", nil)
	out := httptest.NewRecorder()
	s.runHTTP(out, req, "alice")
	if out.Code != 200 || strings.Contains(out.Body.String(), "R-1") || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
	after, callErr7 := h.Store.GetRun(run.RunID, "alice")
	if callErr7 != nil {
		t.Error(callErr7)
	}
	if before.Revision != after.Revision || p.calls != 1 {
		t.Fatal("diagnostic read invoked a tool or mutated a run")
	}
}
