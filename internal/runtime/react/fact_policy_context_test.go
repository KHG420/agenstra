package react

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestAppliedFactVisibilityPolicyDoesNotDisplaceVisibleReceipt(t *testing.T) {
	for _, variant := range []string{"visible fields", "opaque result"} {
		t.Run(variant, func(t *testing.T) {
			paths := [][]string{{"result", "count"}, {"result", "enabled"}, {"result", "missing"}, {"result", "items"}, {"result", "label"}}
			for i := 0; i < 200; i++ {
				paths = append(paths, []string{"result", fmt.Sprintf("optional_declared_property_with_long_name_%03d", i)})
			}
			if variant == "opaque result" {
				paths = [][]string{}
			}
			fact := agentcontract.Fact{FactID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SourceCapability: "records.read", SourceVersion: "1.0.0",
				SourcePackID: "records", SourceRelease: "pinned-release", SourceSubject: "owner-23", Quality: "observed", ReferenceScope: "durable", ObservedAt: time.Now().UTC(),
				ModelOutput: &agentcontract.ModelOutput{Paths: paths}, Value: agentcontract.JSON{"data": agentcontract.JSON{
					"result": agentcontract.JSON{"count": 0, "enabled": false, "missing": nil, "items": []any{}, "label": "Saved receipt"}, "credential": "private-sentinel"}}}
			state := &agentcontract.RuntimeState{Facts: []agentcontract.Fact{fact}}
			before, err := agentcontract.CanonicalJSON(state)
			if err != nil {
				t.Fatal(err)
			}
			view := factView(fact, 6000)
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "Return the authorized saved receipt.", Facts: []agentcontract.FactView{view}}
			projected := budgetContext(packet, state, 1600)
			if contextCharacters(projected) > 1600 || len(projected.Facts[0].OmittedPaths) != 0 {
				t.Fatal("already applied visibility policy displaced a small authorized receipt", contextCharacters(projected), projected.Facts[0].OmittedPaths)
			}
			expected, err := agentcontract.CanonicalJSON(agentcontract.ModelFactValue(fact))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := agentcontract.CanonicalJSON(projected.Facts[0].Value)
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatal("visibility policy removal changed actual authorized values", err)
			}
			wire, err := agentcontract.CanonicalJSON(projected)
			if err != nil || bytes.Contains(wire, []byte("private-sentinel")) || bytes.Contains(wire, []byte("model_output")) {
				t.Fatal("model projection contains private data or redundant execution policy", err)
			}
			if view.SourcePackID != fact.SourcePackID || view.SourceRelease != fact.SourceRelease || view.SourceSubject != fact.SourceSubject || view.FactID != fact.FactID || view.SourceCapability != fact.SourceCapability || !view.ObservedAt.Equal(fact.ObservedAt) || !view.ReferenceAvailable {
				t.Fatal("applied visibility policy removal lost evidence identity or provenance")
			}
			_, err = ResolveArgument(agentcontract.JSON{"$fact_value": agentcontract.JSON{"fact_id": fact.FactID, "path": []any{"data", "credential"}}}, map[string]agentcontract.Fact{fact.FactID: fact}, "", true)
			if err == nil {
				t.Fatal("original evidence policy no longer rejects private references")
			}
			after, err := agentcontract.CanonicalJSON(state)
			if err != nil || !bytes.Equal(before, after) || fact.ModelOutput == nil {
				t.Fatal("model projection mutated the original evidence or visibility policy", err)
			}
		})
	}
}
