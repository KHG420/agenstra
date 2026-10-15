package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func batchBoundsFixture(t *testing.T, count int, schema bool) string {
	t.Helper()
	calls := make([]any, count)
	for i := range calls {
		calls[i] = agentcontract.JSON{"call_ref": fmt.Sprintf("read-%d", i), "capability": "notice.create",
			"arguments": agentcontract.JSON{"marker": "private-overflow-marker"}, "reason": "Perform the requested operation"}
	}
	value := agentcontract.JSON{"kind": "tool_batch", "calls": calls}
	if schema {
		value["schema"] = "agenstra.decision.v1"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestModelBatchBoundsSurviveEveryOutputChannel(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		for _, count := range []int{4, 5, 11} {
			t.Run(fmt.Sprintf("%s/%d", mode, count), func(t *testing.T) {
				body := batchBoundsFixture(t, count, mode != "output_tools")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
				defer server.Close()
				model, err := NewHTTPJSONDecisionModel("compatible", server.URL, "test", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = mode
				decision, err := model.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Execute independent requested operations")
				if count == 4 {
					if err != nil || len(decision.Calls) != 4 {
						t.Fatal("valid boundary batch rejected", decision, err)
					}
					return
				}
				var bounds agentcontract.DecisionTooManyCallsError
				if !errors.As(err, &bounds) || agentcontract.ModelErrorCode(err) != "model_decision_invalid" {
					t.Fatalf("lost fixed batch constraint in %s: %T %v", mode, err, err)
				}
				if decision.ModelCall == nil || decision.ModelCall.FormatError != "model_decision_schema_invalid" || strings.Contains(err.Error(), "private-overflow-marker") {
					t.Fatal("batch rejection lost telemetry or leaked arguments", decision.ModelCall, err)
				}
			})
		}
	}
}

func TestNativeBatchBoundRecoveryRemainsBeforeApprovalAndExecution(t *testing.T) {
	var requests atomic.Int32
	oversized, corrected := batchBoundsFixture(t, 11, true), batchBoundsFixture(t, 1, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		var payload struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Messages) != 2 {
			t.Error("invalid fixture request", err)
			return
		}
		prompt := payload.Messages[0].Content
		if n == 1 {
			if !strings.Contains(prompt, "tool_batch contains at most 4 calls") {
				t.Error("initial decision omitted the batch limit")
			}
			memoryModelReply(t, w, oversized)
			return
		}
		if !strings.Contains(prompt, "Your previous decision was invalid: tool_batch.calls has at most 4 items") || strings.Contains(prompt, "private-overflow-marker") {
			t.Error("recovery omitted the fixed limit or echoed rejected arguments")
		}
		memoryModelReply(t, w, corrected)
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("compatible", server.URL, "test", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "json_schema"
	provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"notice.create": {
		Name: "notice.create", Effect: "write", ApprovalRequired: true,
	}}}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"notice.create": true}, MaxModelRounds: 4}
	state, err := runtime.NewState("Perform eleven independent authorized operations", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || state.RoundsUsed != 2 || len(state.ModelCalls) != 2 || !state.ModelCalls[1].FormatRecovery || provider.called != 0 || len(state.Pending) != 1 {
		t.Fatal("correction changed the recovery budget, approval or execution boundary", requests.Load(), state, provider.called)
	}
}
