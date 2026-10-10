package modelapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestOutputToolsPairSavedCallWithItsActualOutcome(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "unknown", "accepted"} {
		t.Run(status, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
			last := agentcontract.Observation{CallRef: "read-1", Capability: "records.read", Status: status,
				Arguments: agentcontract.JSON{"id": 23}, FactID: agentcontract.Strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}
			if status == "failed" {
				last.ErrorCode = agentcontract.Strptr("host_request_rejected")
			}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Continue the existing task",
				Observations: []agentcontract.Observation{last},
				Facts: []agentcontract.FactView{{Fact: agentcontract.Fact{FactID: *last.FactID,
					Value: agentcontract.JSON{"count": 0, "enabled": false, "missing": nil, "items": []any{}}},
					OmittedPaths: [][]any{{"hidden"}}, ReferenceAvailable: true}}}
			before, err := agentcontract.CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(before, "Return one decision")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			if len(messages) != 4 {
				t.Fatal("saved evidence pair is missing")
			}
			call, result := messages[2].(agentcontract.JSON), messages[3].(agentcontract.JSON)
			if call["role"] != "assistant" || result["role"] != "tool" {
				t.Fatal("saved calls and results were presented as new user messages")
			}
			toolCalls, ok := call["tool_calls"].([]any)
			if !ok || len(toolCalls) != 1 {
				t.Fatal("saved call is not a single paired tool call")
			}
			tc := toolCalls[0].(agentcontract.JSON)
			if tc["id"] == "" || result["tool_call_id"] != tc["id"] {
				t.Fatal("saved outcome is not paired with its call")
			}
			function := tc["function"].(agentcontract.JSON)
			if function["name"] != "submit_tool_call" {
				t.Fatal("historical call is not expressed in the active output protocol")
			}
			var arguments agentcontract.JSON
			if err := json.Unmarshal([]byte(function["arguments"].(string)), &arguments); err != nil {
				t.Fatal(err)
			}
			if arguments["capability"] != last.Capability || arguments["arguments"].(map[string]any)["id"] != float64(23) {
				t.Fatal("saved call identity or arguments changed", arguments)
			}
			content := result["content"].(string)
			for _, value := range []string{`"status":"` + status + `"`, `"count":0`, `"enabled":false`, `"missing":null`, `"items":[]`, `"omitted_paths":[["hidden"]]`, "not a new user task"} {
				if !strings.Contains(content, value) {
					t.Fatal("saved outcome changed or lost its data boundary", value)
				}
			}
			if status == "failed" && !strings.Contains(content, "host_request_rejected") {
				t.Fatal("failed outcome lost its error")
			}
			after, err := agentcontract.CanonicalJSON(packet)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("message construction mutated the caller's evidence")
			}
		})
	}
}

func TestSavedToolPairDoesNotInventCallsOrReplaceSteering(t *testing.T) {
	for _, name := range []string{"omitted_arguments", "missing_arguments", "legacy_json", "steering", "framework_decision", "memory", "deepseek_thinking", "deepseek_default"} {
		t.Run(name, func(t *testing.T) {
			model := &HTTPJSONDecisionModel{Model: "compatible-model", DecisionOutputMode: "output_tools"}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Observations: []agentcontract.Observation{{
				CallRef: "read-1", Capability: "records.read", Status: "failed", Arguments: agentcontract.JSON{},
				ErrorCode: agentcontract.Strptr("capability_input_invalid")}}}
			switch name {
			case "omitted_arguments":
				packet.Observations[0].ArgumentsOmitted = true
			case "missing_arguments":
				packet.Observations[0].Arguments = nil
			case "legacy_json":
				model.DecisionOutputMode = "json_object"
			case "steering":
				packet.Followups = []string{"steering: read a fresh context again"}
			case "framework_decision":
				packet.Observations[0].Capability = "agent.inspect_fact"
			case "memory":
				packet.Schema = "agenstra.memory_input.v1"
			case "deepseek_thinking":
				model.APIType, model.Thinking = "deepseek_chat", "enabled"
			case "deepseek_default":
				model.APIType = "deepseek_chat"
			}
			raw, err := agentcontract.CanonicalJSON(packet)
			if name == "missing_arguments" {
				// CanonicalJSON represents nil object maps as {}, which is a
				// complete zero-argument call. Exercise an explicit wire null.
				raw, err = json.Marshal(packet)
			}
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(raw, "Return one decision")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			for _, item := range messages {
				message := item.(agentcontract.JSON)
				if message["tool_calls"] != nil || message["role"] == "tool" {
					t.Fatal("an ineligible historical call was invented")
				}
			}
			if name == "steering" || name == "framework_decision" || name == "memory" {
				if len(messages) != 2 {
					t.Fatal("old call presentation overrode a newer request or another protocol")
				}
			} else if len(messages) != 4 || messages[3].(agentcontract.JSON)["role"] != "user" {
				t.Fatal("existing historical data presentation changed")
			}
		})
	}
}

func TestDeepSeekDisabledThinkingKeepsNativeSavedToolPair(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "compatible-model", APIType: "deepseek_chat", Thinking: "disabled", DecisionOutputMode: "output_tools"}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Observations: []agentcontract.Observation{{
		CallRef: "read-1", Capability: "records.read", Status: "succeeded", Arguments: agentcontract.JSON{}}}}
	raw, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.requestPayload(raw, "Return one decision")
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 4 || messages[3].(agentcontract.JSON)["role"] != "tool" || payload["thinking"].(agentcontract.JSON)["type"] != "disabled" {
		t.Fatal("explicitly disabled thinking lost its native message pairing")
	}
}

func TestSavedToolPairIsIncludedInActualInputMeasurement(t *testing.T) {
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		sent, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{
			"finish_reason": "tool_calls", "message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{
				"type": "function", "function": agentcontract.JSON{"name": "submit_final", "arguments": `{"answer_markdown":"Verified","fact_ids":[],"result_refs":[]}`},
			}}},
		}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("compatible-model", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "output_tools"
	var measured []byte
	model.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		measured = bytes.Clone(raw)
		return int64(len(raw)), nil
	}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Observations: []agentcontract.Observation{{
		CallRef: "read-1", Capability: "records.read", Status: "succeeded", Arguments: agentcontract.JSON{}}}}
	measurement, err := model.MeasureInput(packet, "Return one decision")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Decide(t.Context(), packet, "Return one decision"); err != nil {
		t.Fatal(err)
	}
	if measurement.Tokens != int64(len(sent)) || !bytes.Equal(measured, sent) || !bytes.Contains(sent, []byte(`"role":"tool"`)) {
		t.Fatal("saved tool messages escaped actual request measurement")
	}
}
