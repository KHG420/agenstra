package deployment

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestReasoningRequestsMeasureAndSendSameModeAcrossOutputProtocols(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools", "native_tools"} {
		t.Run(mode, func(t *testing.T) {
			var received []byte
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				received = append(received[:0], payload...)
				requests++
				content := `{"schema":"agenstra.decision.v1","kind":"request_reasoning"}`
				message := agentcontract.JSON{"content": content}
				if mode == "output_tools" || mode == "native_tools" {
					name, args := "submit_decision", `{"kind":"request_reasoning"}`
					if mode == "native_tools" {
						name, args = "request_reasoning", `{}`
					}
					message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": name, "arguments": args}}}}
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message, "finish_reason": "stop"}}, "usage": agentcontract.JSON{"prompt_tokens": 100, "completion_tokens": 10}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			manager, err := selectionTestDeployment(t, upstream.URL).NewModel()
			if err != nil {
				t.Fatal(err)
			}
			manager.ReasoningRequests = true
			config := manager.Snapshot().Config
			profile := config.Profiles["business"]
			profile.DecisionOutputMode = mode
			config.Profiles["business"] = profile
			selected, err := manager.selectedModel(config)
			if err != nil || !selected.SupportsReasoningRequests() {
				t.Fatal("opt-in unavailable", err)
			}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: []string{"reasoning_request"}}
			measurement, err := selected.MeasureInput(packet, "Use the declared decision protocol.")
			if err != nil {
				t.Fatal(err)
			}
			decision, err := selected.Decide(t.Context(), packet, "Use the declared decision protocol.")
			if err != nil || decision.Kind != "request_reasoning" || requests != 1 || decision.ModelCall.Thinking != "disabled" || !decision.ModelCall.UsageAvailable || measurement.Tokens != int64(len(received)+128) {
				t.Fatal("upgrade protocol, fast mode or measurement failed", decision, measurement, err)
			}
			packet.ReasoningRequested = true
			packet.RuntimeFeatures = nil
			adapter := selected.decisionAdapter(packet)
			if adapter.Thinking != "enabled" || adapter.Temperature != nil || selected.decision.Thinking != profile.Thinking {
				t.Fatal("upgrade mutated frozen profile or used incorrect mode")
			}
			measurement, err = selected.MeasureInput(packet, "Continue the task.")
			if err != nil {
				t.Fatal(err)
			}
			// The fake endpoint repeats the now unavailable control. Schema and
			// tool modes reject it; plain JSON is checked by Runtime.
			decision, err = selected.Decide(t.Context(), packet, "Continue the task.")
			if mode != "json_object" {
				if agentcontract.ModelErrorCode(err) != "model_decision_invalid" {
					t.Fatal("repeated upgrade accepted by tool adapter", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if decision.ModelCall.Thinking != "enabled" || requests != 2 || measurement.Tokens != int64(len(received)+128) || decision.ModelCall.InputTokens != 100 {
				t.Fatal("reasoning request changed metering", decision.ModelCall, err)
			}
			config.Profiles["business"] = agentcontract.ModelProfile{APIType: "compatible_chat", Model: "other", BaseURL: upstream.URL, APIKeyRef: "MODEL_KEY"}
			unsupported, err := manager.selectedModel(config)
			if err != nil || unsupported.SupportsReasoningRequests() {
				t.Fatal("unsupported API opted in", err)
			}
		})
	}
}
