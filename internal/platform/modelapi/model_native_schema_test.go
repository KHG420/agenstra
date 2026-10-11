package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestNativeDecisionSchemaMatchesStageAndMeasuresWholeRequest(t *testing.T) {
	for _, review := range []bool{false, true} {
		packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
		if review {
			packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}
		}
		input, err := agentcontract.CanonicalJSON(packet)
		if err != nil {
			t.Fatal(err)
		}
		model := &HTTPJSONDecisionModel{Model: "any-compatible-model", DecisionOutputMode: "json_schema"}
		payload, err := model.requestPayload(input, "Keep the host evidence rules.")
		if err != nil {
			t.Fatal(err)
		}
		format := payload["response_format"].(agentcontract.JSON)
		descriptor := format["json_schema"].(agentcontract.JSON)
		if format["type"] != "json_schema" || descriptor["strict"] != false || payload["tools"] != nil || payload["tool_choice"] != nil {
			t.Fatal("native output mixed transports or claimed strict-provider support")
		}
		schema := descriptor["schema"].(agentcontract.JSON)
		compiled, err := agentcontract.ValidateLocalSchema(schema, false)
		if err != nil {
			t.Fatal(err)
		}
		final := agentcontract.JSON{"schema": "agenstra.decision.v1", "kind": "final", "answer_markdown": "ok", "fact_ids": []any{}}
		if review {
			final["completion_feedback"] = ""
		}
		if err := agentcontract.ValidateSchema(compiled, final); err != nil {
			t.Fatal("declared final rejected", err)
		}
		call := agentcontract.JSON{"call_ref": "read-1", "capability": "records.read", "arguments": agentcontract.JSON{"enabled": false, "count": 0, "optional": nil}, "reason": "Read the record"}
		batch := agentcontract.JSON{"schema": "agenstra.decision.v1", "kind": "tool_batch", "calls": []any{call}}
		if err := agentcontract.ValidateSchema(compiled, batch); (err == nil) == review {
			t.Fatal("native schema does not match stage availability", err)
		}
		delete(call, "call_ref")
		if err := agentcontract.ValidateSchema(compiled, batch); err == nil {
			t.Fatal("native JSON call has no runtime reference")
		}
		for _, kind := range []string{"read_skill", "search_capabilities"} {
			value := agentcontract.JSON{"schema": "agenstra.decision.v1", "kind": kind}
			if kind == "read_skill" {
				value["name"] = "undeclared"
			} else {
				value["query"] = "undeclared"
			}
			if err := agentcontract.ValidateSchema(compiled, value); err == nil {
				t.Fatal("native schema offers an unavailable decision", kind)
			}
		}
		serialized, err := agentcontract.CanonicalJSON(payload)
		if err != nil {
			t.Fatal(err)
		}
		measurement, err := model.MeasureInput(packet, "Keep the host evidence rules.")
		if err != nil || measurement.Tokens != int64(len(serialized)+128) {
			t.Fatal("native schema excluded from input measurement", measurement, err)
		}
		after, err := agentcontract.CanonicalJSON(packet)
		if err != nil || string(after) != string(input) {
			t.Fatal("native schema generation changed caller evidence")
		}
	}
	packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Skills: []agentcontract.JSON{{"name": "host.guide"}}, RuntimeFeatures: []string{"capability_search"}}
	input, err := agentcontract.CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	model := &HTTPJSONDecisionModel{Model: "another-compatible-model", DecisionOutputMode: "json_schema"}
	payload, err := model.requestPayload(input, "Keep the host rules.")
	if err != nil {
		t.Fatal(err)
	}
	schema := payload["response_format"].(agentcontract.JSON)["json_schema"].(agentcontract.JSON)["schema"].(agentcontract.JSON)
	compiled, err := agentcontract.ValidateLocalSchema(schema, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []agentcontract.JSON{
		{"schema": "agenstra.decision.v1", "kind": "read_skill", "name": "host.guide"},
		{"schema": "agenstra.decision.v1", "kind": "search_capabilities", "query": "record"},
	} {
		if err := agentcontract.ValidateSchema(compiled, value); err != nil {
			t.Fatal("native schema removed an available decision", err)
		}
	}
}

func TestNativeDecisionOutputRetainsLocalValidationAndNoTransportFallback(t *testing.T) {
	const final = `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"{\"result\":null}","fact_ids":[]}`
	for _, tc := range []struct {
		name, content, want string
		review, tool        bool
		status              int
	}{
		{name: "valid final", content: final},
		{name: "ordinary business JSON rejected", content: `{"result":null}`, want: "model_decision_invalid"},
		{name: "flat call cannot replace declared batch", content: `{"schema":"agenstra.decision.v1","kind":"tool_batch","call_ref":"update-1","capability":"records.update","arguments":{"enabled":true},"reason":"Update the record"}`, want: "model_decision_invalid"},
		{name: "missing schema rejected", content: `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`, want: "model_decision_invalid"},
		{name: "duplicate key rejected", content: `{"schema":"agenstra.decision.v1","kind":"final","kind":"final","answer_markdown":"ok","fact_ids":[]}`, want: "model_decision_invalid"},
		{name: "unavailable skill rejected", content: `{"schema":"agenstra.decision.v1","kind":"read_skill","name":"missing"}`, want: "model_decision_invalid"},
		{name: "review missing feedback rejected", content: final, review: true, want: "model_decision_invalid"},
		{name: "review execution rejected", content: `{"schema":"agenstra.decision.v1","kind":"inspect_fact","fact_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","path":[]}`, review: true, want: "model_decision_invalid"},
		{name: "tool output rejected", content: final, tool: true, want: "model_decision_invalid"},
		{name: "gateway unsupported without fallback", status: http.StatusBadRequest, want: "model_http_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var payload agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["response_format"].(map[string]any)["type"] != "json_schema" || payload["tools"] != nil {
					t.Error("native request silently changed output transport")
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				message := agentcontract.JSON{"content": tc.content}
				if tc.tool {
					message["tool_calls"] = []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": "{}"}}}
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 20}, "choices": []any{agentcontract.JSON{"message": message, "finish_reason": "stop"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("any-model", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "json_schema"
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1"}
			if tc.review {
				packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "draft"}
			}
			decision, err := model.Decide(t.Context(), packet, "Return one decision.")
			if agentcontract.ErrorCode(err) != tc.want || requests != 1 || decision.ModelCall == nil || decision.ModelCall.Attempts != 1 {
				t.Fatalf("decision=%+v err=%v requests=%d", decision, err, requests)
			}
			if tc.want == "" && (decision.Kind != "final" || decision.AnswerMarkdown != `{"result":null}`) {
				t.Fatal("native output changed business content")
			}
		})
	}
}

func TestNativeDecisionModeLeavesMemoryProtocolUnchanged(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "any-model", DecisionOutputMode: "json_schema"}
	input := []byte(`{"text":"remember my preference","existing":[]}`)
	payload, err := model.requestPayload(input, "Memory extraction instructions.")
	if err != nil {
		t.Fatal(err)
	}
	if payload["response_format"].(agentcontract.JSON)["type"] != "json_object" || payload["tools"] != nil {
		t.Fatal("native decision configuration changed memory extraction")
	}
	if strings.Contains(payload["messages"].([]any)[0].(agentcontract.JSON)["content"].(string), "agenstra.decision") {
		t.Fatal("decision schema leaked into memory instructions")
	}
}
