package agenstra

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelFollowupContextKeepsLatestOutcomeAfterSnapshot(t *testing.T) {
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		sent, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"finish_reason": "stop", "message": JSON{"content": `{"schema":"agenstra.decision.v1","kind":"final","answer_markdown":"Verified","fact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"]}`}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var measured [][]byte
	model.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		measured = append(measured, bytes.Clone(raw))
		return int64(len(raw)), nil
	}
	packet := ContextPacket{
		Schema: "agenstra.context.v1", Instruction: "Read after the supplied label",
		Followups:    []string{"label: received"},
		Observations: []Observation{{CallRef: "read-1", Capability: "record.read", Status: "succeeded", Arguments: JSON{"id": 23}, FactID: strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}},
		Facts:        []FactView{{Fact: Fact{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Value: JSON{"id": 23, "content": "actual result"}}, ReferenceAvailable: true}},
	}
	before, err := CanonicalJSON(packet)
	if err != nil {
		t.Fatal(err)
	}
	measurement, err := model.MeasureInput(packet, "Return JSON")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Decide(t.Context(), packet, "Return JSON"); err != nil {
		t.Fatal(err)
	}
	var payload JSON
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatal(err)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("latest call and result were not retained after the snapshot: %v", payload["messages"])
	}
	if messages[2].(map[string]any)["role"] != "assistant" || messages[3].(map[string]any)["role"] != "user" {
		t.Fatal("execution messages are out of order")
	}
	if !strings.Contains(messages[3].(map[string]any)["content"].(string), "actual result") {
		t.Fatal("latest result is missing")
	}
	if len(measured) < 2 || measurement.Tokens != int64(len(sent)) || !bytes.Equal(measured[0], sent) || !bytes.Equal(measured[len(measured)-1], sent) {
		t.Fatal("execution messages escaped input measurement")
	}
	after, err := CanonicalJSON(packet)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("request construction changed the caller's packet")
	}
}

func TestModelRuntimeContextKeepsCompletedReadWhenFollowupsAreEmpty(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "fake"}
	packet := ContextPacket{
		Schema:       "agenstra.context.v1",
		Instruction:  "Current user request: read a new snapshot once. Earlier conversation is historical data.",
		Observations: []Observation{{CallRef: "current-read", Capability: "records.snapshot", Status: "succeeded", Arguments: JSON{"include_details": true}, FactID: strptr("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}},
		Facts:        []FactView{{Fact: Fact{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Value: JSON{"data": JSON{"items": []any{}, "count": 0, "stream": false}}}, ReferenceAvailable: true}},
	}
	for _, omitted := range []bool{false, true} {
		name := "complete_arguments"
		if omitted {
			name = "omitted_arguments"
		}
		t.Run(name, func(t *testing.T) {
			packet.Observations[0].ArgumentsOmitted = omitted
			if omitted {
				packet.Observations[0].Arguments = JSON{}
			}
			input, err := CanonicalJSON(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := model.requestPayload(input, "Return a JSON decision")
			if err != nil {
				t.Fatal(err)
			}
			messages := payload["messages"].([]any)
			if len(messages) != 4 {
				t.Fatal("a completed read without request_input was left only inside the cumulative snapshot")
			}
			call := messages[2].(JSON)["content"].(string)
			if omitted && (strings.Contains(call, `"kind":"tool_batch"`) || !strings.Contains(call, `"arguments_omitted":true`)) {
				t.Fatal("omitted arguments were presented as a complete executable call", call)
			}
			content := messages[3].(JSON)["content"].(string)
			for _, value := range []string{`"items":[]`, `"count":0`, `"stream":false`, `"reference_available":true`} {
				if !strings.Contains(content, value) {
					t.Fatal("current read evidence lost empty, zero, false, or reference status", content)
				}
			}
		})
	}
}

func TestModelContextWindowReservesOutputAndCountsSerializedInput(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload JSON
		if callErr := json.NewDecoder(r.Body).Decode(&payload); callErr != nil {
			t.Error(callErr)
		}
		if payload["max_tokens"] != float64(128) {
			t.Fatal(payload)
		}
		if callErr2 := json.NewEncoder(w).Encode(JSON{"choices": []any{JSON{"message": JSON{"content": `{"kind":"request_input","field":"confirm","prompt":"confirm"}`}}}}); callErr2 != nil {
			t.Error(callErr2)
		}
	}))
	defer server.Close()
	m, callErr3 := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if callErr3 != nil {
		t.Error(callErr3)
	}
	m.CountInputTokens = func(_ string, raw []byte) (int64, error) {
		var payload JSON
		if json.Unmarshal(raw, &payload) != nil || payload["messages"] == nil {
			t.Fatal("not full payload")
		}
		return int64(len(raw) / 4), nil
	}
	r, state, _ := contextBudgetRuntime(t, 30000)
	r.Model = m
	r.ModelContextWindowTokens = 2500
	r.ModelOutputReserveTokens = 128
	r.ModelProtocolReserveTokens = 64
	for i := 0; i < 12; i++ {
		state.Facts = append(state.Facts, contextBudgetFact(JSON{"body": strings.Repeat("x", 1500)}))
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	c := state.ContextTelemetry
	if requests != 1 || c.TokenMeasurementSource != "tokenizer" || c.InputTokens == nil || *c.InputTokens > 2308 || *c.EffectiveInputTokenLimit != 2308 || *c.ReservedOutputTokens != 128 || c.TokensRemaining == nil {
		t.Fatalf("%+v requests=%d", c, requests)
	}
	var callErr4 error
	state, callErr4 = r.NewState(strings.Repeat("required", 3000), "")
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if callErr5 := r.Step(t.Context(), state, nil); callErr5 != nil {
		t.Error(callErr5)
	}
	if requests != 1 || state.Status != "failed" || !state.ContextTelemetry.OverLimit {
		t.Fatalf("%+v requests=%d", state, requests)
	}
}

func TestUnknownModelWindowIsNotReportedAsZero(t *testing.T) {
	r, state, _ := contextBudgetRuntime(t, 9000)
	r.Model = &coreTestModel{decisions: []Decision{{Kind: "request_input", Field: "x", Prompt: "x"}}}
	if callErr6 := r.Step(t.Context(), state, nil); callErr6 != nil {
		t.Error(callErr6)
	}
	c := state.ContextTelemetry
	if c.ModelContextWindowTokens != nil || c.EffectiveInputTokenLimit != nil || c.TokensRemaining != nil || c.InputTokens == nil {
		t.Fatalf("%+v", c)
	}
}
