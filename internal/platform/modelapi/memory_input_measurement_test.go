package modelapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestMemoryInputMeasurementMatchesSentEnvelopeWithoutIO(t *testing.T) {
	for _, mode := range []string{"json_object", "json_schema", "output_tools"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			var sent []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var err error
				sent, err = io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				memoryModelReply(t, w, `{"proposals":[]}`)
			}))
			defer server.Close()
			model, err := NewHTTPJSONDecisionModel("memory-model", server.URL, "test-key", time.Second, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = mode
			model.MaxOutputTokens = 8192
			request := agentcontract.MemoryExtractionRequest{Text: "请用中文。", MaxOutputTokens: 1024}
			measurement, err := model.MeasureMemoryInput(request)
			if err != nil || requests != 0 || measurement.Source != "utf8_bytes_estimate" {
				t.Fatalf("measurement=%+v requests=%d error=%v", measurement, requests, err)
			}
			_, metrics, err := model.ExtractMemoriesMeasured(t.Context(), request)
			if err != nil || requests != 1 || measurement.Tokens != int64(len(sent)+128) || metrics.EstimatedInputTokens != measurement.Tokens {
				t.Fatalf("measured=%d sent bytes=%d metrics=%+v requests=%d error=%v", measurement.Tokens, len(sent), metrics, requests, err)
			}
			if model.MaxOutputTokens != 8192 || request.MaxOutputTokens != 1024 {
				t.Fatal("measurement or extraction mutated caller configuration")
			}
		})
	}
}
