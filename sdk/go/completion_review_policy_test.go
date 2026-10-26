package agenstra_test

import (
	"context"
	"path/filepath"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKCompletionReviewPolicyUsesDurableEvidence(t *testing.T) {
	store, err := agenstra.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	provider := &recordProvider{}
	host := agenstra.NewAgentHost(store,
		func(context.Context, string, string) (agenstra.CapabilityProvider, error) { return provider, nil },
		recordModel{},
		func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
			return agenstra.ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true}, AllowModelData: true}, nil
		})
	policyCalls, validationCalls := 0, 0
	var policy agenstra.CompletionReviewPolicy = func(ctx context.Context, c agenstra.CompletionContext) bool {
		policyCalls++
		if ctx.Err() != nil || c.OriginPackID != "records" || c.Instruction != "Read record R-1" || len(c.Facts) != 1 || len(c.Observations) != 1 || c.Observations[0].Status != "succeeded" || c.Observations[0].FactID == nil || *c.Observations[0].FactID != c.Facts[0].FactID || c.Facts[0].SourceCapability != "records.get" {
			t.Fatal("embedded policy lost successful current-run evidence", c)
		}
		return true
	}
	host.CompletionReviewPolicy = policy
	host.CompletionValidator = func(context.Context, agenstra.CompletionContext) error {
		validationCalls++
		return nil
	}
	run, err := host.Create(t.Context(), "alice", "records", "Read record R-1", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = host.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || provider.calls != 1 || policyCalls != 1 || validationCalls != 1 {
		t.Fatal("embedded review replayed a read or bypassed final validation", run.Status, provider.calls, policyCalls, validationCalls, err)
	}
	telemetry, err := host.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || telemetry.Budget.UsageByPurpose["decision"].Requests != 2 || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
		t.Fatal("optional review was not accounted separately", err, telemetry.Budget)
	}
}
