package react

import (
	"reflect"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRejectedObservationDistinguishesMissingAndEmptyArguments(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    agentcontract.JSON
		omitted bool
	}{
		{name: "missing", omitted: true},
		{name: "explicit empty", args: agentcontract.JSON{}},
		{name: "provided", args: agentcontract.JSON{"id": 42}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &agentcontract.RuntimeState{}
			Reject(state, "call-1", "record.update", "operation_failed", tc.args, "failure-fact")
			if len(state.Observations) != 1 || len(state.ModelObservations) != 1 {
				t.Fatal("rejection lost its observation")
			}
			for _, observation := range []agentcontract.Observation{state.Observations[0], state.ModelObservations[0]} {
				if observation.ArgumentsOmitted != tc.omitted || observation.Arguments == nil {
					t.Fatal("missing arguments became a known empty input", observation)
				}
				if tc.args != nil && !reflect.DeepEqual(observation.Arguments, tc.args) {
					t.Fatal("known arguments changed", observation)
				}
				if observation.Status != "rejected" || *observation.ErrorCode != "operation_failed" || *observation.FactID != "failure-fact" || observation.CallRef != "call-1" {
					t.Fatal("rejection identity or outcome changed", observation)
				}
			}
		})
	}
}
