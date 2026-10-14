package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

func TestLocalManifestFailureDiagnosisStopsBeforeModelAndBusiness(t *testing.T) {
	// The unsupported schema detail must stay outside persisted public findings.
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte(`{"schema":"not-a-supported-pack-private-marker"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &hostModel{}
	h := NewAgentHost(testStore(t), func(ctx context.Context, _, _ string) (agentcontract.CapabilityProvider, error) {
		return capabilitypack.OpenPack(ctx, path, nil)
	}, model, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{}, AllowModelData: true}, nil
	})
	run := createTestHostRun(t, h)
	result, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "capability_contract_invalid" || model.calls != 0 || len(state.ModelCalls) != 0 || len(state.Pending) != 0 {
		t.Fatalf("configuration error reached model/business or lost diagnosis: status=%s code=%v model_calls=%d pending=%d", result.Status, state.ErrorCode, model.calls, len(state.Pending))
	}
	diagnostics, err := h.GetDiagnostics(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.Findings) != 1 || diagnostics.Findings[0].Category != "contract" || diagnostics.Findings[0].Code != "capability_contract_invalid" {
		t.Fatalf("manifest fault misclassified: %+v", diagnostics.Findings)
	}
	for _, value := range []any{result, diagnostics} {
		raw, err := agentcontract.CanonicalJSON(value)
		if err != nil || strings.Contains(string(raw), "private-marker") {
			t.Fatalf("raw configuration detail entered public evidence: %s %v", raw, err)
		}
	}
}
