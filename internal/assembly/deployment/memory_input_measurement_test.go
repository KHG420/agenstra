package deployment

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestMemoryMeasurementUsesPinnedProfileAndActualEnvelope(t *testing.T) {
	requests := 0
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var err error
		sent, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if _, err = w.Write([]byte(`{"choices":[{"message":{"content":"{\"proposals\":[]}"}}]}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	deployment := selectionTestDeployment(t, server.URL)
	manager, err := deployment.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	before := manager.Snapshot()
	frozen, err := manager.SelectModel(before.Config)
	if err != nil {
		t.Fatal(err)
	}
	request := agentcontract.MemoryExtractionRequest{Text: "Use Chinese.", MaxOutputTokens: 1024}
	pinned := frozen.(agentcontract.MemoryInputMeasurer)
	measurement, err := pinned.MeasureMemoryInput(request)
	if err != nil || requests != 0 {
		t.Fatalf("measurement=%+v requests=%d error=%v", measurement, requests, err)
	}
	profile := before.Config.Profiles["memory"]
	profile.Model = "memory-new-with-a-longer-name"
	before.Config.Profiles["memory"] = profile
	if _, err = manager.Configure(before.Config, before.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := manager.MeasureMemoryInput(request)
	if err != nil || current.Tokens == measurement.Tokens || requests != 0 {
		t.Fatalf("memory selection was not measured independently: current=%+v old=%+v requests=%d error=%v", current, measurement, requests, err)
	}
	_, metrics, err := frozen.(agentcontract.MeasuredMemoryExtractor).ExtractMemoriesMeasured(t.Context(), request)
	if err != nil || requests != 1 || measurement.Tokens != int64(len(sent)+128) || metrics.EstimatedInputTokens != measurement.Tokens {
		t.Fatalf("frozen measurement differs from actual request: measured=%+v metrics=%+v bytes=%d requests=%d error=%v", measurement, metrics, len(sent), requests, err)
	}
}
