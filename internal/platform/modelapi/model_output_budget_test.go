package modelapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestModelEmptyOutputAtEffectiveTokenLimit(t *testing.T) {
	for _, tc := range []struct {
		name, content, finish, want string
		configured, packetLimit     int
		remaining, output           int64
		omitUsage                   bool
	}{
		{name: "reasoning consumed limit", configured: 4096, output: 4096, finish: "stop", want: "model_output_truncated"},
		{name: "whitespace at limit", configured: 4096, output: 4096, content: " \n\t", finish: "stop", want: "model_output_truncated"},
		{name: "packet limit", configured: 4096, packetLimit: 256, output: 256, finish: "stop", want: "model_output_truncated"},
		{name: "remaining run budget", configured: 4096, remaining: 100, output: 90, finish: "stop", want: "model_output_truncated"},
		{name: "empty below limit", configured: 4096, output: 4095, finish: "stop", want: "model_decision_invalid"},
		{name: "unknown limit", output: 4096, finish: "stop", want: "model_decision_invalid"},
		{name: "missing usage", configured: 4096, omitUsage: true, finish: "stop", want: "model_decision_invalid"},
		{name: "invalid usage", configured: 4096, output: 1000000001, finish: "stop", want: "model_decision_invalid"},
		{name: "provider reports truncation", configured: 4096, output: 20, finish: "length", content: `{"kind":"final","answer_markdown":"partial"}`, want: "model_output_truncated"},
		{name: "complete output at limit", configured: 4096, output: 4096, finish: "stop", content: `{"kind":"final","answer_markdown":"done","fact_ids":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request agentcontract.JSON
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if tc.remaining > 0 && request["max_tokens"] != float64(tc.remaining-10) {
					t.Errorf("remaining budget not applied: %v", request["max_tokens"])
				}
				reply := agentcontract.JSON{"choices": []any{agentcontract.JSON{"finish_reason": tc.finish, "message": agentcontract.JSON{"content": tc.content}}}}
				if !tc.omitUsage {
					reply["usage"] = agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": tc.output, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": tc.output}}
				}
				if err := json.NewEncoder(w).Encode(reply); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.MaxOutputTokens = tc.configured
			model.CountInputTokens = func(string, []byte) (int64, error) { return 10, nil }
			decision, err := model.Decide(t.Context(), agentcontract.ContextPacket{MaxModelOutputTokens: tc.packetLimit, ModelTokensRemaining: tc.remaining}, "system")
			if agentcontract.ErrorCode(err) != tc.want || requests != 1 {
				t.Fatalf("error=%v, requests=%d, want=%s", err, requests, tc.want)
			}
			if tc.want == "model_output_truncated" && (decision.ModelCall.FormatError != "" || decision.ModelCall.FinishReason != tc.finish || decision.ModelCall.OutputTokens != tc.output) {
				t.Fatalf("truncation lost usage or became a format error: %+v", decision.ModelCall)
			}
		})
	}
}

func TestMemoryOutputExhaustionRetainsUsageWithoutRecovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["max_tokens"] != float64(8192) {
			t.Errorf("unexpected memory output limit: %v", request["max_tokens"])
		}
		reply := agentcontract.JSON{"choices": []any{agentcontract.JSON{"finish_reason": "stop", "message": agentcontract.JSON{"content": ""}}}, "usage": agentcontract.JSON{"prompt_tokens": 10, "completion_tokens": 8192, "completion_tokens_details": agentcontract.JSON{"reasoning_tokens": 8192}}}
		if err := json.NewEncoder(w).Encode(reply); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := NewHTTPJSONDecisionModel("test", server.URL, "test-key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.MaxOutputTokens = 16384
	proposals, metrics, err := model.ExtractMemoriesMeasured(t.Context(), agentcontract.MemoryExtractionRequest{Text: "Use Chinese in reports.", MaxOutputTokens: 8192})
	if agentcontract.ErrorCode(err) != "model_output_truncated" || len(proposals) != 0 || requests != 1 || metrics.Attempts != 1 || metrics.OutputTokens != 8192 || metrics.ReasoningOutputTokens == nil || *metrics.ReasoningOutputTokens != 8192 || metrics.FormatError != "" || metrics.FormatRecovery {
		t.Fatalf("memory exhaustion: error=%v proposals=%v requests=%d metrics=%+v", err, proposals, requests, metrics)
	}
}
