package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCompletionReviewsSuccessfulWriteBeforePublishingAnswer(t *testing.T) {
	capability := "session.start"
	beforeID, afterID := NewID(), NewID()
	wrong := Decision{Kind: "final", AnswerMarkdown: "A session is already active, so I could not start it.", FactIDs: []string{beforeID, afterID}}
	correct := Decision{Kind: "final", AnswerMarkdown: "I successfully started the session. It is now active.", FactIDs: []string{afterID}}
	calls, checkpoints := 0, 0
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{capability: {Name: capability, Effect: "write"}}}}
	s, callErr := r.NewState("Start a session after approval", "")
	if callErr != nil {
		t.Error(callErr)
	}
	s.Facts = []Fact{
		{FactID: beforeID, SourceCapability: "session.read", Value: JSON{"data": JSON{"revision": 0, "active_session": nil}}},
		{FactID: afterID, SourceCapability: capability, Value: JSON{"data": JSON{"revision": 1, "active_session": "S-1"}}},
	}
	s.Observations = []Observation{{CallRef: "start-1", Capability: capability, Status: "succeeded", FactID: &afterID}}
	s.ModelObservations = append([]Observation{}, s.Observations...)
	s.InvocationReceipts = []InvocationReceipt{{InvocationID: deterministicInvocationID(s.RunID, "start-1"), Capability: capability, Effect: "write", Status: "succeeded", FactID: afterID}}
	r.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, prompt string) (Decision, error) {
		calls++
		if calls == 1 {
			return wrong, nil
		}
		raw, callErr2 := CanonicalJSON(packet)
		if callErr2 != nil {
			t.Error(callErr2)
		}
		if !strings.Contains(string(raw), "completion_review") || !strings.Contains(string(raw), wrong.AnswerMarkdown) || !strings.Contains(string(raw), "action_outcomes") || !strings.Contains(prompt, "post-action") {
			t.Fatal("review lost the proposed answer or the causal execution evidence", string(raw), prompt)
		}
		if s.Status == "completed" || s.AnswerMarkdown != "" {
			t.Fatal("unreviewed answer was published")
		}
		return correct, nil
	})
	if err := r.Step(t.Context(), s, func() error { checkpoints++; return nil }); err != nil {
		t.Fatal(err)
	}
	if s.Status != "completed" || s.AnswerMarkdown != correct.AnswerMarkdown || calls != 2 || checkpoints != 2 {
		t.Fatalf("contradictory completion accepted: status=%s answer=%q calls=%d checkpoints=%d", s.Status, s.AnswerMarkdown, calls, checkpoints)
	}
	if s.ModelUsage.Requests != 2 || len(s.Decisions) != 2 || s.ModelCalls[1].Purpose != "completion_review" {
		t.Fatal("review was not accounted for", s.ModelCalls, s.Decisions)
	}
}

func consistencyFixture(t *testing.T) (*AgentRuntime, *RuntimeState) {
	t.Helper()
	r := &AgentRuntime{Provider: &coreTestProvider{caps: map[string]CapabilityDescription{"record.create": {Name: "record.create", Effect: "write"}}}}
	s, callErr3 := r.NewState("Create the record", "")
	if callErr3 != nil {
		t.Error(callErr3)
	}
	fact := Fact{FactID: NewID(), SourceCapability: "record.create", Value: JSON{"data": JSON{"id": "R-1"}}}
	s.Facts = []Fact{fact}
	s.Observations = []Observation{{CallRef: "create-1", Capability: "record.create", Status: "succeeded", FactID: &fact.FactID}}
	s.ModelObservations = append([]Observation{}, s.Observations...)
	return r, s
}

func TestCompletionReviewCannotExecuteToolsOrPublishOnError(t *testing.T) {
	for _, failure := range []string{"tools", "gateway", "rounds", "tokens", "context"} {
		t.Run(failure, func(t *testing.T) {
			r, s := consistencyFixture(t)
			calls := 0
			r.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
				calls++
				if packet.CompletionReview != nil {
					if failure == "gateway" {
						return Decision{}, ModelDecisionError{"model_unavailable"}
					}
					return callDecision("record.create"), nil
				}
				answer := "The record could not be created."
				if failure == "context" {
					answer = strings.Repeat("x", 29000)
				}
				return Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{s.Facts[0].FactID}, ModelCall: &ModelCallMetrics{Attempts: 1, UsageAvailable: true, InputTokens: 100, OutputTokens: 20}}, nil
			})
			wantCalls, wantCode := 1, "model_round_budget_exhausted"
			switch failure {
			case "tools":
				wantCalls, wantCode = 3, "model_decision_invalid"
			case "gateway":
				wantCalls, wantCode = 2, "model_unavailable"
			case "rounds":
				r.MaxModelRounds = 1
			case "tokens":
				r.MaxModelTokens, wantCode = 120, "model_token_budget_exhausted"
			case "context":
				r.MaxContextCharacters, wantCode = 12000, "context_too_large"
			}
			if err := r.Step(t.Context(), s, nil); err != nil {
				t.Fatal(err)
			}
			if s.Status != "failed" || s.AnswerMarkdown != "" || len(s.Pending) != 0 || calls != wantCalls || s.ErrorCode == nil || *s.ErrorCode != wantCode {
				t.Fatalf("unsafe review failure: status=%s answer=%q pending=%v calls=%d error=%v", s.Status, s.AnswerMarkdown, s.Pending, calls, s.ErrorCode)
			}
			if s.ModelUsage.Requests != wantCalls || len(s.Facts) != 1 {
				t.Fatal("failure lost accounting or execution evidence", s.ModelUsage, s.Facts)
			}
		})
	}
}

func TestCompletionReviewCheckpointFailureDoesNotPublishProposal(t *testing.T) {
	r, s := consistencyFixture(t)
	r.Model = &hostModel{}
	checkpoints := 0
	want := errors.New("checkpoint unavailable")
	err := r.Step(t.Context(), s, func() error {
		checkpoints++
		if checkpoints == 2 {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) || s.AnswerMarkdown != "" || s.Status == "completed" || s.ModelUsage.Requests != 1 || len(s.Decisions) != 1 {
		t.Fatal("checkpoint failure published proposal or charged unsent review", err, s)
	}
}

func TestCompletionReviewStillEnforcesReferencesAndHostValidator(t *testing.T) {
	r, s := consistencyFixture(t)
	validated := ""
	r.CompletionValidator = func(_ context.Context, c CompletionContext) error { validated = c.AnswerMarkdown; return nil }
	r.Model = decisionModelFunc(func(_ context.Context, p ContextPacket, _ string) (Decision, error) {
		if p.CompletionReview == nil {
			return Decision{Kind: "final", AnswerMarkdown: "Wrong", FactIDs: []string{s.Facts[0].FactID}}, nil
		}
		return Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{s.Facts[0].FactID}, ResultRefs: []ResultRefRequest{{FactID: s.Facts[0].FactID, Path: []any{"data", "id"}}}}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil || validated != "Created" || len(s.ResultRefs) != 1 || s.ResultRefs[0].ID != "R-1" {
		t.Fatal("review bypassed ordinary final checks", err, validated, s.ResultRefs)
	}
	r, s = consistencyFixture(t)
	r.Model = decisionModelFunc(func(_ context.Context, p ContextPacket, _ string) (Decision, error) {
		id := s.Facts[0].FactID
		if p.CompletionReview != nil {
			id = NewID()
		}
		return Decision{Kind: "final", AnswerMarkdown: "Created", FactIDs: []string{id}}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil || s.Status == "completed" || *s.Observations[len(s.Observations)-1].ErrorCode != "final_fact_citations_invalid" {
		t.Fatal("invented review evidence accepted", err, s)
	}
}

func TestCompletionReviewDoesNotAddCallsToReadOnlyOrComputeTasks(t *testing.T) {
	for _, effect := range []string{"read", "compute"} {
		t.Run(effect, func(t *testing.T) {
			r, s := consistencyFixture(t)
			r.Provider.(*coreTestProvider).caps["record.create"] = CapabilityDescription{Name: "record.create", Effect: effect}
			r.Model = &hostModel{}
			if err := r.Step(t.Context(), s, nil); err != nil || s.Status != "completed" || s.ModelUsage.Requests != 1 {
				t.Fatal("read/compute gained a review call", err, s.ModelCalls)
			}
		})
	}
}

func TestActionOutcomesUseCurrentReceiptWithoutLeakingPrivateBindings(t *testing.T) {
	r, s := consistencyFixture(t)
	id := deterministicInvocationID(s.RunID, "create-1")
	receipt := InvocationReceipt{InvocationID: id, Capability: "record.create", Effect: "write", Status: "accepted", FactID: "discarded", OperationID: "private-operation-token", OperationStatus: "private-provider-status", ResultSHA256: "private-result-hash", ArgumentsSHA256: "private-argument-hash"}
	s.InvocationReceipts = []InvocationReceipt{receipt}
	receipt.Status, receipt.FactID = "succeeded", s.Facts[0].FactID
	s.Pending = []Invocation{{InvocationID: id, Call: ToolCall{CallRef: "create-1"}, Status: "succeeded", Receipt: &receipt}}
	s.Facts[0].ModelOutput = &ModelOutput{Paths: [][]string{}}
	for i := 0; i < 20; i++ {
		s.ModelObservations = append(s.ModelObservations, Observation{CallRef: "read", Capability: "record.read", Arguments: JSON{"large": strings.Repeat("x", 2000)}})
	}
	before, callErr4 := CanonicalJSON(s)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	packet := budgetContext(r.contextCandidate(s), s, 7000)
	if len(packet.ActionOutcomes) != 1 || packet.ActionOutcomes[0].Status != "succeeded" || packet.ActionOutcomes[0].FactID != s.Facts[0].FactID || packet.ActionOutcomes[0].CallRef != "create-1" {
		t.Fatal("projection lost the action/result relation", packet.ActionOutcomes)
	}
	raw, callErr5 := CanonicalJSON(packet)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "R-1") {
		t.Fatal("receipts bypassed model visibility", string(raw))
	}
	after, callErr6 := CanonicalJSON(s)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("projection mutated the checkpoint")
	}
}

func TestActionOutcomesPreserveDistinctAttemptsAndFailureSemantics(t *testing.T) {
	r, s := consistencyFixture(t)
	binding := &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}}
	r.Provider.(*coreTestProvider).caps["record.create"] = CapabilityDescription{Name: "record.create", Effect: "write", Operation: binding}
	s.Facts[0].Value = JSON{"data": JSON{"id": "private-token", "status": "running"}}
	if got := actionOutcomes(s, r.Provider.Capabilities()); len(got) != 1 || got[0].Status != "accepted" {
		t.Fatal("accepted job reported as complete", got)
	}
	Reject(s, "create-1", "record.create", "operation_failed", nil, s.Facts[0].FactID)
	s.Observations = append(s.Observations,
		Observation{CallRef: "denied-1", Capability: "record.create", Status: "failed", ErrorCode: strptr("approval_denied")},
		Observation{CallRef: "unknown-1", Capability: "record.create", Status: "failed", ErrorCode: strptr("provider_outcome_unknown")})
	got := actionOutcomes(s, r.Provider.Capabilities())
	if len(got) != 3 || got[0].Status != "failed" || got[1].Status != "not_executed" || got[2].Status != "unknown" {
		t.Fatal("failure, denied approval or unknown outcome lost", got)
	}
}
func TestDestructiveOutcomesIncludeFailedReceiptsAndRejectedInputs(t *testing.T) {
	state := &RuntimeState{RunID: "run", InvocationReceipts: []InvocationReceipt{
		{InvocationID: "write", Capability: "records.remove", Effect: "destructive", Status: "failed", ErrorCode: "operation_failed"},
	}, Observations: []Observation{{CallRef: "invalid", Capability: "records.remove", Status: "failed", ErrorCode: strptr("capability_input_invalid")}}}
	got := actionOutcomes(state, map[string]CapabilityDescription{"records.remove": {Name: "records.remove", Effect: "destructive", ApprovalRequired: true}})
	if len(got) != 2 || got[0].Status != "failed" || got[1].ErrorCode != "capability_input_invalid" || !got[0].ApprovalRequired {
		t.Fatal("destructive failures hidden from final review", got)
	}
}

func TestHostCompletionReviewAfterApprovalDoesNotReplayWrite(t *testing.T) {
	p := &hostProvider{caps: map[string]CapabilityDescription{"records.get": {Name: "records.get", Effect: "write", Replay: "never", ApprovalRequired: true, InputSchema: JSON{"type": "object"}}}}
	h := testHost(t, testStore(t), p, &hostModel{decisions: []Decision{callDecision("records.get")}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_approval" || p.calls != 0 {
		t.Fatal("write ran before approval", err, run.Status)
	}
	s, callErr7 := h.restore(run)
	if callErr7 != nil {
		t.Error(callErr7)
	}
	item := s.Pending[0]
	run, err = h.Approve(t.Context(), run.RunID, "alice", item.InvocationID, item.ArgumentsSHA256, run.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	h.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
		answer := "Could not create"
		if packet.CompletionReview != nil {
			answer = "Created R-1"
		}
		return Decision{Kind: "final", AnswerMarkdown: answer, FactIDs: []string{packet.Facts[0].FactID}}, nil
	})
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	s, restoreErr := h.restore(run)
	if err != nil || restoreErr != nil || run.Status != "completed" || s.AnswerMarkdown != "Created R-1" || p.calls != 1 || len(s.InvocationReceipts) != 1 || s.InvocationReceipts[0].Status != "succeeded" {
		t.Fatal("approval/review did not preserve exactly one write", err, restoreErr, run.Status, s, p.calls)
	}
	telemetry, err := h.GetTelemetry(t.Context(), run.RunID, "alice")
	if err != nil || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
		t.Fatal("review missing from persisted telemetry", err, telemetry.Budget)
	}
	if got := actionOutcomes(s, p.Capabilities()); len(got) != 1 || !got[0].ApprovalRequired {
		t.Fatal("completed approved action lost its enforced approval requirement", got)
	}
}

func TestCompletionReviewsHistoricalLotteryDraft(t *testing.T) {
	var fixture struct {
		RuntimeState
		Proposal Decision `json:"proposal"`
	}
	raw, err := os.ReadFile("testdata/completion-start-round.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture unavailable", err)
	}
	p := &coreTestProvider{caps: map[string]CapabilityDescription{"ui.start_round": {Name: "ui.start_round", Effect: "write", ApprovalRequired: true}}}
	r := &AgentRuntime{Provider: p}
	s, callErr8 := r.NewState(fixture.Instruction, fixture.RunID)
	if callErr8 != nil {
		t.Error(callErr8)
	}
	s.Facts, s.Observations, s.ModelObservations, s.InvocationReceipts = fixture.Facts, fixture.Observations, fixture.ModelObservations, fixture.InvocationReceipts
	r.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
		if packet.CompletionReview == nil {
			return fixture.Proposal, nil
		}
		if len(packet.ActionOutcomes) != 1 || packet.ActionOutcomes[0].Status != "succeeded" || !packet.ActionOutcomes[0].ApprovalRequired || packet.ActionOutcomes[0].FactID != "05269c82-0d71-4e32-8440-30c749d48182" {
			t.Fatal("historical operation lost its result/approval evidence", packet.ActionOutcomes)
		}
		return Decision{Kind: "final", AnswerMarkdown: "已在你批准后成功开始一轮三等奖，人数为 3。当前轮次进行中，尚未揭晓。", FactIDs: []string{packet.ActionOutcomes[0].FactID}}, nil
	})
	if err := r.Step(t.Context(), s, nil); err != nil || s.Status != "completed" || s.AnswerMarkdown == fixture.Proposal.AnswerMarkdown || p.called != 0 {
		t.Fatal("historical draft published or write replayed", err, s.AnswerMarkdown, p.called)
	}
}

func TestAsyncCompletionReviewUsesTerminalPollReceipt(t *testing.T) {
	h, p, now := asyncReceiptHost(t, func(JSON) JSON { return JSON{"id": "private-job-token", "status": "succeeded"} }, &ModelOutput{Paths: [][]string{{"status"}}})
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" {
		t.Fatal(run.Status, err)
	}
	initial, callErr9 := h.restore(run)
	if callErr9 != nil {
		t.Error(callErr9)
	}
	initialFact := initial.Facts[0].FactID
	*now += 2
	h.Model = decisionModelFunc(func(_ context.Context, packet ContextPacket, _ string) (Decision, error) {
		outcomes := packet.ActionOutcomes
		if len(outcomes) != 1 || outcomes[0].Status != "succeeded" || outcomes[0].FactID == initialFact || outcomes[0].FactID != packet.Facts[0].FactID {
			t.Fatal("review used submission receipt instead of final poll", outcomes, packet.Facts)
		}
		raw, callErr10 := CanonicalJSON(packet)
		if callErr10 != nil {
			t.Error(callErr10)
		}
		if strings.Contains(string(raw), "private-job-token") {
			t.Fatal("poll binding leaked through completion review")
		}
		return Decision{Kind: "final", AnswerMarkdown: "Operation completed", FactIDs: []string{outcomes[0].FactID}}, nil
	})
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 2 {
		t.Fatal("async review replayed an action or lost completion", err, run.Status, p.calls)
	}
}
