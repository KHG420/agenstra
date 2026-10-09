package agent

import (
	"testing"
)

func TestCompletionRequirementsUseLatestCitedFullFact(t *testing.T) {
	validator, err := RequireFactValues(FactRequirement{Capability: "job.status", Path: []any{"data", "status"}, Value: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	old := Fact{FactID: NewID(), SourceCapability: "job.status", Value: JSON{"data": JSON{"status": "succeeded"}}}
	latest := Fact{FactID: NewID(), SourceCapability: "job.status", Value: JSON{"data": JSON{"status": "running"}}}
	result := CompletionContext{Facts: []Fact{old, latest}, FactIDs: []string{old.FactID}}
	if ErrorCode(validator(t.Context(), result)) != "completion_evidence_missing" {
		t.Fatal("old citation accepted")
	}
	result.FactIDs = append(result.FactIDs, latest.FactID)
	if ErrorCode(validator(t.Context(), result)) != "completion_evidence_mismatch" {
		t.Fatal("running job accepted")
	}
	latest.Value["data"].(JSON)["status"] = "succeeded"
	if err = validator(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	result.Observations = []Observation{{CallRef: "submit", FactID: &latest.FactID}, {CallRef: "submit", ErrorCode: Strptr("operation_failed")}}
	if ErrorCode(validator(t.Context(), result)) != "completion_operation_failed" {
		t.Fatal("failed operation accepted")
	}
}
