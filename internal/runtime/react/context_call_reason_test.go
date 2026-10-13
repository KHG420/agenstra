package react

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func callReasonFixture(t *testing.T) (*AgentRuntime, *agentcontract.RuntimeState) {
	t.Helper()
	cap := agentcontract.CapabilityDescription{Name: "screen.read", Effect: "read", Replay: "safe"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}}
	s, err := r.NewState("Read context, then read record 2", "")
	if err != nil {
		t.Fatal(err)
	}
	call := agentcontract.ToolCall{CallRef: "context-2", Capability: cap.Name, Arguments: agentcontract.JSON{}, Reason: "Read the context before the unfinished record 2 lookup."}
	recordDecision(s, agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{call}})
	item := agentcontract.Invocation{Call: call, OriginalArguments: call.Arguments}
	Observe(s, &item, agentcontract.CallOutcome{Fact: &agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: cap.Name, ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"revision": 2}}}})
	return r, s
}

func TestContextRetainsActualLastCallReasonAfterRestore(t *testing.T) {
	r, state := callReasonFixture(t)
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*agentcontract.RuntimeState{state, &restored} {
		packet, err := agentcontract.ObjectOf(r.Context(s))
		if err != nil {
			t.Fatal(err)
		}
		if packet["last_call_reason"] != "Read the context before the unfinished record 2 lookup." {
			t.Fatal("context lost the actual purpose of the last completed call")
		}
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("call-purpose projection changed saved execution state")
	}
}

func TestContextCallReasonRequiresMatchingVisibleCall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*AgentRuntime, *agentcontract.RuntimeState)
	}{
		{"missing history", func(_ *AgentRuntime, s *agentcontract.RuntimeState) { s.Decisions = nil }},
		{"revoked grant", func(r *AgentRuntime, _ *agentcontract.RuntimeState) { r.Grants["screen.read"] = false }},
		{"different reference", func(_ *AgentRuntime, s *agentcontract.RuntimeState) { s.ModelObservations[0].CallRef = "another" }},
		{"different capability", func(_ *AgentRuntime, s *agentcontract.RuntimeState) { s.ModelObservations[0].Capability = "other.read" }},
		{"different arguments", func(_ *AgentRuntime, s *agentcontract.RuntimeState) {
			s.ModelObservations[0].Arguments = agentcontract.JSON{"id": 3}
		}},
		{"omitted arguments", func(_ *AgentRuntime, s *agentcontract.RuntimeState) { s.ModelObservations[0].ArgumentsOmitted = true }},
		{"user steering", func(_ *AgentRuntime, s *agentcontract.RuntimeState) {
			s.Followups = []string{"steering: Read record 3 instead"}
		}},
		{"oversized reason", func(_ *AgentRuntime, s *agentcontract.RuntimeState) {
			call := agentcontract.ToolCall{CallRef: "context-2", Capability: "screen.read", Arguments: agentcontract.JSON{}, Reason: strings.Repeat("界", 2001)}
			s.Decisions = nil
			recordDecision(s, agentcontract.Decision{Kind: "tool_batch", Calls: []agentcontract.ToolCall{call}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, state := callReasonFixture(t)
			tc.change(r, state)
			packet, err := agentcontract.ObjectOf(r.Context(state))
			if err != nil {
				t.Fatal(err)
			}
			if packet["last_call_reason"] != nil {
				t.Fatal("context invented or reassociated a call purpose")
			}
		})
	}
}

func TestContextCallReasonDoesNotDisplaceTaskOrEvidence(t *testing.T) {
	r, state := callReasonFixture(t)
	packet := r.contextCandidate(state)
	without := packet
	without.LastCallReason = ""
	limit := contextCharacters(without)
	projected := budgetContext(packet, state, limit)
	actual, err := agentcontract.CanonicalJSON(projected)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := agentcontract.CanonicalJSON(without)
	if err != nil || !bytes.Equal(actual, expected) || contextCharacters(projected) > limit {
		t.Fatal("optional call purpose displaced original task, arguments or result evidence")
	}
	if packet.LastCallReason == "" {
		t.Fatal("budget projection changed the caller's packet")
	}
}
