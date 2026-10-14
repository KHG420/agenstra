package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelInspectionRangeBoundsSurviveEveryOutputChannel(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := `{"kind":"inspect_fact","fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":["data","rows"],"offset":32,"limit":126}`
				message := agentcontract.JSON{"content": body}
				finish := "stop"
				if mode == "output_tools" {
					message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": body}}}}
					finish = "tool_calls"
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": finish}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			model, err := NewHTTPJSONDecisionModel("compatible", upstream.URL, "test", time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			decision, err := model.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Inspect the saved next page")
			var bounds agentcontract.DecisionInspectionRangeError
			if !errors.As(err, &bounds) || agentcontract.ModelErrorCode(err) != "model_decision_invalid" {
				t.Fatalf("lost fixed field constraint in %s: %T %v", mode, err, err)
			}
			if decision.ModelCall == nil || decision.ModelCall.FormatError != "model_decision_schema_invalid" {
				t.Fatal("rejected native range lost original telemetry")
			}
		})
	}
}
