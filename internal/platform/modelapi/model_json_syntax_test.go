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

const misplacedArgumentJSON = `{"kind":"tool_batch","calls":[{"arguments":{"body":{"title":"private-invalid-output-marker"}}},"path":{"id":"fixture"}}]}`

func replyDecisionChannel(t *testing.T, w http.ResponseWriter, mode, body string) {
	t.Helper()
	message := agentcontract.JSON{"content": body}
	finish := "stop"
	if mode == "output_tools" {
		message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": body}}}}
		finish = "tool_calls"
	}
	if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": finish}}}); err != nil {
		t.Error(err)
	}
}

func TestModelJSONSyntaxErrorsPreserveSafeCodeAndLocation(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				replyDecisionChannel(t, w, mode, misplacedArgumentJSON)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			decision, err := model.Decide(context.Background(), agentcontract.ContextPacket{Schema: "agenstra.context.v1"}, "Prepare the requested notice")
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || syntax.Offset <= 0 || agentcontract.ModelErrorCode(err) != "model_decision_invalid" {
				t.Fatalf("lost original JSON syntax error or safe code: %T %v", err, err)
			}
			if strings.Contains(err.Error(), "private-invalid-output-marker") || decision.ModelCall == nil || decision.ModelCall.FormatError != "model_output_invalid_json" {
				t.Fatal("syntax error leaked output or changed existing classification")
			}
		})
	}
}

func TestRuntimeJSONSyntaxRecoveryExplainsLocationWithoutEchoingOutput(t *testing.T) {
	var raw json.RawMessage
	var syntax *json.SyntaxError
	if err := json.Unmarshal([]byte(misplacedArgumentJSON), &raw); !errors.As(err, &syntax) {
		t.Fatal("fixture must reproduce a native JSON syntax error", err)
	}
	want := fmt.Sprintf("JSON syntax error at byte %d: %s", syntax.Offset, syntax.Error())
	valid := `{"kind":"tool_batch","calls":[{"call_ref":"publish","capability":"notice.create","arguments":{},"reason":"Publish the notice"}]}`
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			corrected := valid
			if mode == "json_schema" {
				corrected = `{"schema":"agenstra.decision.v1",` + strings.TrimPrefix(valid, "{")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					replyDecisionChannel(t, w, mode, misplacedArgumentJSON)
					return
				}
				var input struct{ Messages []struct{ Content string } }
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Messages) < 2 {
					t.Error("invalid recovery request", err)
				} else if !strings.Contains(input.Messages[0].Content, want) || !strings.Contains(input.Messages[0].Content, "Keep every capability input property inside that call's single arguments object") || strings.Contains(input.Messages[0].Content, "private-invalid-output-marker") {
					t.Error("recovery must explain the syntax location without echoing rejected output")
				}
				replyDecisionChannel(t, w, mode, corrected)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			provider := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{"notice.create": {Name: "notice.create", Effect: "write", ApprovalRequired: true}}}
			runtime := &reactcore.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"notice.create": true}, MaxModelRounds: 4}
			state, err := runtime.NewState("Publish a notice", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Step(t.Context(), state, nil); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || state.RoundsUsed != 2 || state.ModelUsage.InvalidResponses != 1 || state.ModelUsage.FormatRecoveryRequests != 1 || provider.called != 0 || len(state.Pending) != 1 {
				t.Fatalf("changed recovery budget or executed an unapproved write: %+v", state)
			}
		})
	}
}
