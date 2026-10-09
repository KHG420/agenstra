package host

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostPersistsModelMetricsAndEvent(t *testing.T) {
	h := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil || len(state.ModelCalls) != 1 || state.ModelUsage.Requests != 1 {
		t.Fatalf("%+v %v", state, err)
	}
	events, err := h.Store.ListEvents(run.RunID, "alice", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		payload := event["event"].(agentcontract.JSON)
		if payload["kind"] == "model_decided" {
			_, found = payload["metrics"]
		}
	}
	if !found {
		t.Fatal("model metrics missing from event")
	}
}
