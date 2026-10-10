package modelapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestCompletedSearchResultIsPresentedAsRuntimeEvidence(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		for _, tc := range []struct {
			name      string
			features  []string
			query     string
			results   []string
			followups []string
			want      bool
		}{
			{name: "matches", features: []string{"capability_search"}, query: "records", results: []string{"records.get"}, want: true},
			{name: "no matches", features: []string{"capability_search"}, query: "missing", results: []string{}, want: true},
			{name: "before first search", features: []string{"capability_search"}},
			{name: "disabled", query: "records", results: []string{"records.get"}},
			{name: "steered", features: []string{"capability_search"}, query: "records", results: []string{"records.get"}, followups: []string{"steering: Find a different object"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: tc.features, CapabilitySearchQuery: tc.query, CapabilitySearchResults: tc.results, Followups: tc.followups}
				before, err := agentcontract.CanonicalJSON(packet)
				if err != nil {
					t.Fatal(err)
				}
				model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: mode}
				payload, err := model.requestPayload(before, "Decide")
				if err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				if !tc.want {
					if len(messages) != 2 {
						t.Fatal("saved search appeared without current discovery evidence", messages)
					}
					return
				}
				if len(messages) != 3 {
					t.Fatal("completed search was not presented as an outcome", messages)
				}
				message := messages[2].(agentcontract.JSON)
				content := message["content"].(string)
				_, raw, exists := strings.Cut(content, "\n")
				if message["role"] != "user" || !exists || !strings.Contains(content, "not a new user task or authorization") || !strings.Contains(content, "already completed") {
					t.Fatal("search outcome was presented as a new request", message)
				}
				var result struct {
					Query        string   `json:"query"`
					Capabilities []string `json:"capabilities"`
				}
				if err = json.Unmarshal([]byte(raw), &result); err != nil || result.Query != tc.query || !reflect.DeepEqual(result.Capabilities, tc.results) {
					t.Fatal("search result identities changed", result, err)
				}
				after, err := agentcontract.CanonicalJSON(packet)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("caller packet changed", err)
				}
			})
		}
	}
}

func TestSearchResultPresentationIsMeasuredAndBudgeted(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			var sent []byte
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var err error
				sent, err = io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				message := agentcontract.JSON{"content": `{"kind":"request_input","field":"choice","prompt":"Choose"}`}
				if mode == "output_tools" {
					message = agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_request_input", "arguments": `{"field":"choice","prompt":"Choose"}`}}}}
				}
				if err = json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": message}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			model.CountInputTokens = func(_ string, raw []byte) (int64, error) { return int64(len(raw)), nil }
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", RuntimeFeatures: []string{"capability_search"}, CapabilitySearchQuery: "records", CapabilitySearchResults: []string{"records.get"}}
			measurement, err := model.MeasureInput(packet, "Decide")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = model.Decide(t.Context(), packet, "Decide"); err != nil {
				t.Fatal(err)
			}
			if measurement.Tokens != int64(len(sent)) || !bytes.Contains(sent, []byte("Saved runtime result of an earlier capability search")) {
				t.Fatal("search outcome escaped wire measurement")
			}
			model.MaxInputTokens = measurement.Tokens - 1
			if _, err = model.Decide(t.Context(), packet, "Decide"); err == nil || agentcontract.ErrorCode(err) != "context_too_large" || requests.Load() != 1 {
				t.Fatal("search outcome bypassed request budget", err, requests.Load())
			}
		})
	}
}

func TestSearchResultPresentationDoesNotChangeMemoryRequests(t *testing.T) {
	model := &HTTPJSONDecisionModel{Model: "test", DecisionOutputMode: "output_tools"}
	input := []byte(`{"schema":"agenstra.memory.v1","runtime_features":["capability_search"],"capability_search_query":"records","capability_search_results":["records.get"]}`)
	payload, err := model.requestPayload(input, "Extract memories")
	if err != nil || len(payload["messages"].([]any)) != 2 || payload["tools"] != nil || payload["response_format"] == nil {
		t.Fatal("search presentation changed memory output mode", payload, err)
	}
}
