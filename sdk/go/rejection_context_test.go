package agenstra_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestRejectedCallPresentationPreservesArgumentAvailability(t *testing.T) {
	for _, mode := range []string{"json_object", "output_tools"} {
		for _, tc := range []struct {
			name    string
			args    agenstra.JSON
			omitted bool
		}{
			{name: "missing", omitted: true},
			{name: "explicit empty", args: agenstra.JSON{}},
			{name: "provided", args: agenstra.JSON{"id": 42}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Messages []struct{ Role, Content string }
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if len(request.Messages) != 4 {
						t.Error("rejected call evidence missing", request.Messages)
						return
					}
					content := request.Messages[2].Content
					if mode == "output_tools" {
						_, content, _ = strings.Cut(content, "\n")
					}
					var prior map[string]json.RawMessage
					if err := json.Unmarshal([]byte(content), &prior); err != nil {
						t.Error(err)
						return
					}
					if tc.omitted {
						if prior["recorded_runtime_call"] == nil || prior["calls"] != nil {
							t.Error("missing rejected input became an executable empty call", content)
						}
						var recorded struct {
							CallRef          string `json:"call_ref"`
							Capability       string `json:"capability"`
							ArgumentsOmitted bool   `json:"arguments_omitted"`
						}
						if err := json.Unmarshal(prior["recorded_runtime_call"], &recorded); err != nil || recorded.CallRef != "call-1" || recorded.Capability != "record.update" || !recorded.ArgumentsOmitted {
							t.Error("recorded identity or omission changed", recorded, err)
						}
					} else {
						var calls []agenstra.ToolCall
						if err := json.Unmarshal(prior["calls"], &calls); err != nil || len(calls) != 1 {
							t.Error("known rejected input lost", content, err)
							return
						}
						raw, err := json.Marshal(calls[0].Arguments)
						if err != nil {
							t.Error(err)
						}
						want, err := json.Marshal(tc.args)
						if err != nil || string(raw) != string(want) {
							t.Error("known arguments changed", string(raw), string(want), err)
						}
					}
					message := agenstra.JSON{"content": `{"kind":"request_input","field":"choice","prompt":"Choose"}`}
					if mode == "output_tools" {
						message = agenstra.JSON{"tool_calls": []any{agenstra.JSON{"type": "function", "function": agenstra.JSON{"name": "submit_request_input", "arguments": `{"field":"choice","prompt":"Choose"}`}}}}
					}
					if err := json.NewEncoder(w).Encode(agenstra.JSON{"choices": []any{agenstra.JSON{"message": message}}}); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				model, err := agenstra.NewHTTPJSONDecisionModel("test", server.URL, "key", time.Second, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				model.DecisionOutputMode = mode
				state := &agenstra.RuntimeState{}
				agenstra.Reject(state, "call-1", "record.update", "operation_failed", tc.args, "failure-fact")
				packet := agenstra.ContextPacket{Schema: "agenstra.context.v1", Observations: state.ModelObservations}
				if _, err = model.Decide(t.Context(), packet, "Decide"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
