package react

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRepeatedReadFeedbackReachesModelBeforeStagnation(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "records.read", Effect: "read", Replay: "safe"}
	p := &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}
	modelCalls := 0
	warned := false
	r := &AgentRuntime{Provider: p, Grants: map[string]bool{cap.Name: true}, MaxStagnantRounds: 3}
	r.Model = decisionModelFunc(func(_ context.Context, packet agentcontract.ContextPacket, prompt string) (agentcontract.Decision, error) {
		modelCalls++
		if strings.Contains(prompt, "Repeated completed read detected") {
			warned = true
			if packet.Progress.NoProgressRounds != 1 || packet.Progress.StagnationWarning {
				t.Fatalf("feedback did not precede the termination warning: %+v", packet.Progress)
			}
			return agentcontract.Decision{Kind: "final", AnswerMarkdown: "The read returned 42.", FactIDs: []string{packet.Facts[len(packet.Facts)-1].FactID}}, nil
		}
		decision := callDecision(cap.Name)
		decision.Calls[0].CallRef = fmt.Sprintf("read-%d", modelCalls)
		return decision, nil
	})
	result, err := r.Run(t.Context(), "Read the current value")
	if err != nil || result.Status != "completed" || p.called != 2 || !warned {
		t.Fatalf("completed reads did not receive early feedback: status=%s reads=%d warned=%t err=%v", result.Status, p.called, warned, err)
	}
}

func repeatedReadFixture(t *testing.T) (*AgentRuntime, *agentcontract.RuntimeState) {
	t.Helper()
	cap := agentcontract.CapabilityDescription{Name: "records.read", Effect: "read", Replay: "safe"}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}}
	state, err := r.NewState("Read the current record", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: cap.Name, ReferenceScope: "durable", Value: agentcontract.JSON{"data": agentcontract.JSON{"value": "private-result-text"}}}
		state.Facts = append(state.Facts, fact)
		observation := agentcontract.Observation{CallRef: fmt.Sprintf("read-%d", i), Capability: cap.Name, Arguments: agentcontract.JSON{"id": 1}, Status: "succeeded", FactID: agentcontract.Strptr(fact.FactID)}
		state.Observations = append(state.Observations, observation)
		state.ModelObservations = append(state.ModelObservations, observation)
		updateProgress(state, 8, r.Provider.Capabilities())
	}
	return r, state
}

func TestRepeatedReadFeedbackIsBoundedAndSurvivesRestore(t *testing.T) {
	r, state := repeatedReadFixture(t)
	before, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentcontract.RuntimeState
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*agentcontract.RuntimeState{state, &restored} {
		prompt := r.repeatedReadPrompt(value, r.Context(value))
		if !strings.Contains(prompt, `capability "records.read" has succeeded 2 consecutive times`) || !strings.Contains(prompt, "explicitly requested read after a write") || len(prompt) > 1000 {
			t.Fatal("missing bounded, task-aware correction", prompt)
		}
		if strings.Contains(prompt, "private-result-text") || strings.Contains(prompt, state.Facts[1].FactID) {
			t.Fatal("feedback exposed retained data or internal references")
		}
	}
	after, err := agentcontract.CanonicalJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("feedback changed execution evidence or progress")
	}
}

func TestRepeatedReadFeedbackSkipsRefreshAndUnverifiedOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		change func(*AgentRuntime, *agentcontract.RuntimeState, *agentcontract.ContextPacket)
	}{
		{"first read", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations = s.Observations[:1]
		}},
		{"new evidence or input", func(_ *AgentRuntime, _ *agentcontract.RuntimeState, p *agentcontract.ContextPacket) {
			p.Progress.NoProgressRounds = 0
		}},
		{"changed result outside preview", func(_ *AgentRuntime, s *agentcontract.RuntimeState, p *agentcontract.ContextPacket) {
			s.Facts[1].Value = agentcontract.JSON{"data": agentcontract.JSON{"value": "changed-result"}}
			p.Facts[1].Value = p.Facts[0].Value
		}},
		{"changed arguments", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].Arguments = agentcontract.JSON{"id": 2}
		}},
		{"read after write", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations = append(s.Observations[:1], agentcontract.Observation{CallRef: "write-1", Capability: "records.write", Status: "succeeded"}, s.Observations[1])
		}},
		{"read after another action", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations = append(s.Observations[:1], agentcontract.Observation{CallRef: "other-1", Capability: "other.read", Status: "succeeded"}, s.Observations[1])
		}},
		{"waiting operation", func(_ *AgentRuntime, _ *agentcontract.RuntimeState, p *agentcontract.ContextPacket) {
			p.Progress.Pending = []agentcontract.ProgressItem{{Status: "waiting"}}
		}},
		{"failed read", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].Status = "failed"
		}},
		{"accepted read", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].Status = "accepted"
		}},
		{"context rejection", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].ErrorCode = agentcontract.Strptr("browser_context_stale")
		}},
		{"missing retained fact", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Facts = s.Facts[1:]
		}},
		{"omitted arguments", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].ArgumentsOmitted = true
		}},
		{"unavailable model fact", func(_ *AgentRuntime, _ *agentcontract.RuntimeState, p *agentcontract.ContextPacket) {
			p.Facts[1].ReferenceAvailable = false
		}},
		{"revoked grant", func(r *AgentRuntime, _ *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			r.Grants["records.read"] = false
		}},
		{"write effect", func(r *AgentRuntime, _ *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			r.Provider.(*coreTestProvider).caps["records.read"] = agentcontract.CapabilityDescription{Name: "records.read", Effect: "write"}
		}},
		{"awaiting input", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.InputField = agentcontract.Strptr("requested_sample")
		}},
		{"supplied input", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Followups = []string{"requested_sample: read again"}
		}},
		{"steering", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.SteeringCursor = 1
		}},
		{"same invocation", func(_ *AgentRuntime, s *agentcontract.RuntimeState, _ *agentcontract.ContextPacket) {
			s.Observations[1].CallRef = s.Observations[0].CallRef
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, state := repeatedReadFixture(t)
			packet := r.Context(state)
			tc.change(r, state, &packet)
			if prompt := r.repeatedReadPrompt(state, packet); prompt != "" {
				t.Fatal("normal refresh or unverified result mislabeled as a repeated completed read", prompt)
			}
		})
	}
}

func TestRepeatedReadFeedbackUsesCompletedBrowserResults(t *testing.T) {
	cap := agentcontract.CapabilityDescription{Name: "ui.read_activity", Effect: "read", Operation: &agentcontract.OperationBinding{PollCapability: "ui.command_status"}}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{cap.Name: cap}}, Grants: map[string]bool{cap.Name: true}}
	state, err := r.NewState("Read the current activity", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		appendBrowserProgressResult(state, cap.Name, fmt.Sprintf("read-%d", i), agentcontract.JSON{"count": 156})
		updateProgress(state, 8, r.Provider.Capabilities())
	}
	if prompt := r.repeatedReadPrompt(state, r.Context(state)); !strings.Contains(prompt, "has succeeded 2 consecutive times") {
		t.Fatal("new command receipts disguised the same completed browser result", prompt)
	}
	appendBrowserProgressResult(state, cap.Name, "read-2", agentcontract.JSON{"count": 157})
	updateProgress(state, 8, r.Provider.Capabilities())
	if prompt := r.repeatedReadPrompt(state, r.Context(state)); prompt != "" {
		t.Fatal("changed browser data was called an unchanged read", prompt)
	}
}
