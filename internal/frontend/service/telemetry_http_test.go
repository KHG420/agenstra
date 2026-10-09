package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestTelemetryHTTPReadAndWebBinding(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	run := f.run(t)
	s := &HTTPServer{Host: f.h, Web: f.w, Deployment: f.d}
	token, callErr := f.w.MintSession("alice")
	if callErr != nil {
		t.Error(callErr)
	}
	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/web/v1/runs/"+id+"/telemetry", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		s.webHTTP(out, req)
		return out
	}
	out := get(run.RunID)
	var telemetry agentcontract.RunTelemetry
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &telemetry) != nil || telemetry.Context == nil || telemetry.RunRevision != run.Revision {
		t.Fatal(out.Code, out.Body.String())
	}
	if strings.Contains(out.Body.String(), "alice-key") || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal("private metadata exposed")
	}
	unbound := createTestHostRun(t, f.h)
	if out = get(unbound.RunID); out.Code != 404 {
		t.Fatal(out.Code)
	}
	before, callErr2 := f.h.Store.GetRun(run.RunID, "alice")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	_ = get(run.RunID)
	after, callErr3 := f.h.Store.GetRun(run.RunID, "alice")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if before.Revision != after.Revision {
		t.Fatal("read mutated run")
	}
	request := httptest.NewRequest("GET", "/runs/"+run.RunID, nil)
	out = httptest.NewRecorder()
	s.runHTTP(out, request, "alice")
	var view agentcontract.JSON
	if callErr4 := json.Unmarshal(out.Body.Bytes(), &view); callErr4 != nil {
		t.Error(callErr4)
	}
	if view["telemetry"] == nil {
		t.Fatal("run snapshot omitted telemetry")
	}
}
