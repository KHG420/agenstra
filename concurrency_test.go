package agenstra

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

type parallelTestProvider struct{ *hostProvider }

func (*parallelTestProvider) ConcurrentInvocation(string) bool { return true }

func concurrentDecision() Decision {
	d := Decision{Kind: "tool_batch"}
	for i := 0; i < 4; i++ {
		d.Calls = append(d.Calls, ToolCall{CallRef: fmt.Sprintf("read-%d", i), Capability: "records.get", Arguments: JSON{"id": fmt.Sprint(i)}, Reason: "Read independent record"})
	}
	return d
}

func concurrentHost(t *testing.T, p *parallelTestProvider) *AgentHost {
	h := testHost(t, testStore(t), p.hostProvider, &hostModel{decisions: []Decision{concurrentDecision()}})
	h.ProviderFactory = func(context.Context, string, string) (CapabilityProvider, error) { return p, nil }
	h.Settings.MaxConcurrentTools = 2
	return h
}

func TestHostIndependentConcurrencyIsBoundedJournaledAndOrdered(t *testing.T) {
	started, release := make(chan string, 4), make(chan struct{})
	var active, peak atomic.Int32
	p := &parallelTestProvider{&hostProvider{}}
	p.hook = func(ctx context.Context, _ string, args JSON, _ *InvocationContext) (CapabilityResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous && !peak.CompareAndSwap(previous, n); previous = peak.Load() {
		}
		started <- args["id"].(string)
		select {
		case <-release:
			return CapabilityResult{Data: args}, nil
		case <-ctx.Done():
			return CapabilityResult{}, ctx.Err()
		}
	}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	type result struct {
		run StoredRun
		err error
	}
	done := make(chan result, 1)
	go func() { r, e := h.Drive(t.Context(), run.RunID, "alice"); done <- result{r, e} }()
	<-started
	<-started
	select {
	case <-started:
		t.Error("exceeded concurrency limit")
	default:
	}
	for _, call := range concurrentDecision().Calls {
		journal, err := h.Store.GetInvocation(run.RunID, deterministicInvocationID(run.RunID, call.CallRef), "alice")
		if err != nil || journal["status"] != "in_flight" || journal["arguments_sha256"] == "" {
			t.Error("IO started before durable journal", journal, err)
		}
	}
	close(release)
	r := <-done
	if r.err != nil || r.run.Status != "completed" || peak.Load() != 2 || active.Load() != 0 {
		t.Fatal(r.run.Status, r.err, peak.Load(), active.Load())
	}
	state, _ := h.restore(r.run)
	for i, observation := range state.Observations {
		if observation.CallRef != fmt.Sprintf("read-%d", i) {
			t.Fatal("result application reordered", state.Observations)
		}
	}
	if len(state.Facts) != 4 {
		t.Fatal(len(state.Facts))
	}
}

func TestConcurrencyCancellationJoinsWorkersAndRecoversJournal(t *testing.T) {
	started := make(chan struct{}, 4)
	var active atomic.Int32
	p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, _ JSON, _ *InvocationContext) (CapabilityResult, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return CapabilityResult{}, ctx.Err()
	}}}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := h.Drive(ctx, run.RunID, "alice"); done <- err }()
	<-started
	<-started
	cancel()
	<-done
	if active.Load() != 0 || p.calls != 2 {
		t.Fatal("workers leaked or queued IO started after cancel", active.Load(), p.calls)
	}
	p.hook = nil
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" {
		t.Fatal(run.Status, err)
	}
	state, _ := h.restore(run)
	if len(state.Facts) != 4 {
		t.Fatal("lost recovered calls", len(state.Facts))
	}
	for _, call := range concurrentDecision().Calls {
		journal, _ := h.Store.GetInvocation(run.RunID, deterministicInvocationID(run.RunID, call.CallRef), "alice")
		if journal["status"] != "succeeded" {
			t.Fatal(journal)
		}
	}
}

func TestConcurrentUnknownCallRetainsWakeAndOtherResults(t *testing.T) {
	p := &parallelTestProvider{&hostProvider{hook: func(_ context.Context, _ string, args JSON, _ *InvocationContext) (CapabilityResult, error) {
		if args["id"] == "0" {
			return CapabilityResult{}, errors.New("lost response")
		}
		return CapabilityResult{Data: args}, nil
	}}}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || run.NextWakeAt == nil {
		t.Fatal(run.Status, run.NextWakeAt, err)
	}
	state, _ := h.restore(run)
	if len(state.Facts) != 3 || state.Pending[0].Status != "unknown" {
		t.Fatal(state)
	}
	for _, item := range state.Pending[1:] {
		if item.Status != "succeeded" {
			t.Fatal(item)
		}
	}
}

func TestTransientRuntimeAlsoExecutesIndependentCallsConcurrently(t *testing.T) {
	var started atomic.Int32
	gate := make(chan struct{})
	p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, args JSON, _ *InvocationContext) (CapabilityResult, error) {
		if started.Add(1) == 2 {
			close(gate)
		}
		select {
		case <-gate:
			return CapabilityResult{Data: args}, nil
		case <-ctx.Done():
			return CapabilityResult{}, ctx.Err()
		}
	}}}
	r := &AgentRuntime{Provider: p, Model: &hostModel{decisions: []Decision{concurrentDecision()}}, Grants: map[string]bool{"records.get": true}, MaxConcurrentTools: 2}
	result, err := r.Run(t.Context(), "Read four records")
	if err != nil || result.Status != "completed" || len(result.Facts) != 4 {
		t.Fatal(result, err)
	}
	for i, obs := range result.Observations {
		if obs.CallRef != fmt.Sprintf("read-%d", i) {
			t.Fatal(result.Observations)
		}
	}
}

func TestConcurrencyExcludesWritesApprovalsJobsAndUnsafeReplay(t *testing.T) {
	policy := ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true}}
	for _, variant := range []string{"write", "approval", "job", "replay", "provider", "limit"} {
		t.Run(variant, func(t *testing.T) {
			cap := CapabilityDescription{Name: "records.get", Effect: "read", Replay: "safe"}
			if variant == "write" {
				cap.Effect = "write"
			}
			if variant == "approval" {
				cap.ApprovalRequired = true
			}
			if variant == "job" {
				cap.Operation = &OperationBinding{}
			}
			if variant == "replay" {
				cap.Replay = "never"
			}
			base := &hostProvider{caps: map[string]CapabilityDescription{cap.Name: cap}}
			var provider CapabilityProvider = &parallelTestProvider{base}
			if variant == "provider" {
				provider = base
			}
			limit := 4
			if variant == "limit" {
				limit = 1
			}
			pending := []Invocation{{Call: ToolCall{Capability: cap.Name}, Status: "prepared"}, {Call: ToolCall{Capability: cap.Name}, Status: "prepared"}}
			if independentBatch(provider, pending, policy, limit) {
				t.Fatal("unsafe parallel batch")
			}
		})
	}
}
