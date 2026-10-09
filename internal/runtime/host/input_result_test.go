package host

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/modelapi"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

func TestRequestedInputPromptRecoversObservedSchemaMismatch(t *testing.T) {

	// The real model used standard JSON Schema's string+enum form twice. On
	// recovery it must have an executable example of this protocol's enum form.
	invalid := `{"kind":"request_input","field":"choice","prompt":"Which option?","input_schema":{"type":"string","enum":["first","second"]}}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempt := requests.Add(1)
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload.Messages) == 0 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		response := invalid
		if attempt > 1 {
			for _, line := range strings.Split(payload.Messages[0].Content, "\n") {
				if example, ok := strings.CutPrefix(line, "Input enum schema: "); ok {
					var corrected agentcontract.JSON
					if err := json.Unmarshal([]byte(invalid), &corrected); err != nil {
						t.Error(err)
						return
					}
					corrected["input_schema"] = json.RawMessage(example)
					raw, err := json.Marshal(corrected)
					if err != nil {
						t.Error(err)
						return
					}
					response = string(raw)
				}
			}
		}
		if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": response}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	model, err := modelapi.NewHTTPJSONDecisionModel("fake", server.URL, "key", time.Second, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r := &reactcore.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: model}
	state, err := r.NewState("Ask for a missing choice", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "needs_input" || requests.Load() != 2 || state.InputSchema == nil || state.InputSchema.Type != "enum" {
		t.Fatalf("schema recovery failed: status=%s requests=%d schema=%+v", state.Status, requests.Load(), state.InputSchema)
	}
	if state.ModelUsage.InvalidResponses != 1 || state.ModelUsage.FormatRecoveryRequests != 1 {
		t.Fatalf("recovery usage missing: %+v", state.ModelUsage)
	}
	if ValidateRequestedInput(state.InputSchema, state.InputSchema.Enum[0]) != nil || ValidateRequestedInput(state.InputSchema, "invalid") == nil {
		t.Fatal("prompted enum did not retain strict input validation")
	}
}
