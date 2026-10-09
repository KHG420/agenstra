package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestSteeringHTTPContract(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	s := &HTTPServer{Host: h}
	body := fmtSteeringBody(t, agentcontract.NewID(), run.Revision)
	req := httptest.NewRequest("POST", "/runs/"+run.RunID+"/steer", strings.NewReader(body))
	out := httptest.NewRecorder()
	s.runHTTP(out, req, "alice")
	if out.Code != 202 || strings.Contains(out.Body.String(), "lease_token") {
		t.Fatal(out.Code, out.Body.String())
	}
}

func fmtSteeringBody(t *testing.T, id string, revision int) string {
	raw, callErr13 := agentcontract.CanonicalJSON(agentcontract.JSON{"request_id": id, "text": "Explain first", "revision": revision})
	if callErr13 != nil {
		t.Error(callErr13)
	}
	return string(raw)
}
