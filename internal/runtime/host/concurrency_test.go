package host

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
)

type parallelTestProvider struct{ *hostProvider }

func (*parallelTestProvider) ConcurrentInvocation(string) bool { return true }

func concurrentDecision() agentcontract.Decision {
	d := agentcontract.Decision{Kind: "tool_batch"}
	for i := 0; i < 4; i++ {
		d.Calls = append(d.Calls, agentcontract.ToolCall{CallRef: fmt.Sprintf("read-%d", i), Capability: "records.get", Arguments: agentcontract.JSON{"id": fmt.Sprint(i)}, Reason: "Read independent record"})
	}
	return d
}

func concurrentHost(t *testing.T, p *parallelTestProvider) *AgentHost {
	h := testHost(t, testStore(t), p.hostProvider, &hostModel{decisions: []agentcontract.Decision{concurrentDecision()}})
	h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return p, nil }
	h.Settings.MaxConcurrentTools = 2
	return h
}

func TestHostIndependentConcurrencyIsBoundedJournaledAndOrdered(t *testing.T) {
	started, release := make(chan string, 4), make(chan struct{})
	var active, peak atomic.Int32
	p := &parallelTestProvider{&hostProvider{}}
	p.hook = func(ctx context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous && !peak.CompareAndSwap(previous, n); previous = peak.Load() {
		}
		started <- args["id"].(string)
		select {
		case <-release:
			return agentcontract.CapabilityResult{Data: args}, nil
		case <-ctx.Done():
			return agentcontract.CapabilityResult{}, ctx.Err()
		}
	}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	type result struct {
		run agentcontract.StoredRun
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
		journal, err := h.Store.GetInvocation(run.RunID, reactcore.DeterministicInvocationID(run.RunID, call.CallRef), "alice")
		if err != nil || journal["status"] != "in_flight" || journal["arguments_sha256"] == "" {
			t.Error("IO started before durable journal", journal, err)
		}
	}
	close(release)
	r := <-done
	if r.err != nil || r.run.Status != "completed" || peak.Load() != 2 || active.Load() != 0 {
		t.Fatal(r.run.Status, r.err, peak.Load(), active.Load())
	}
	state, callErr := h.Restore(r.run)
	if callErr != nil {
		t.Error(callErr)
	}
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
	p := &parallelTestProvider{&hostProvider{hook: func(ctx context.Context, _ string, _ agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return agentcontract.CapabilityResult{}, ctx.Err()
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
	state, callErr2 := h.Restore(run)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if len(state.Facts) != 4 {
		t.Fatal("lost recovered calls", len(state.Facts))
	}
	for _, call := range concurrentDecision().Calls {
		journal, callErr3 := h.Store.GetInvocation(run.RunID, reactcore.DeterministicInvocationID(run.RunID, call.CallRef), "alice")
		if callErr3 != nil {
			t.Error(callErr3)
		}
		if journal["status"] != "succeeded" {
			t.Fatal(journal)
		}
	}
}

func TestConcurrencyCancellationRetainsKnownResponses(t *testing.T) {
	started := make(chan struct{}, 4)
	p := &parallelTestProvider{&hostProvider{caps: map[string]agentcontract.CapabilityDescription{"records.get": {Name: "records.get", Version: "1", InputSchema: agentcontract.JSON{"type": "object"}, Effect: "compute", Replay: "safe", ReferenceScope: "durable"}}, hook: func(ctx context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return agentcontract.CapabilityResult{Data: args}, nil
	}}}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := h.Drive(ctx, run.RunID, "alice"); done <- err }()
	<-started
	<-started
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	run, err := h.Get(t.Context(), run.RunID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.Restore(run)
	if err != nil || len(state.Facts) != 2 {
		t.Fatal("known parallel results lost on cancellation", state, err)
	}
	settled := 0
	for _, receipt := range state.InvocationReceipts {
		if receipt.Status == "succeeded" {
			settled++
		}
	}
	if settled != 2 {
		t.Fatal("known parallel receipts lost", state.InvocationReceipts)
	}
	p.hook = nil
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || p.calls != 4 {
		t.Fatal("known operations were replayed", run.Status, err, p.calls)
	}
}

func TestConcurrentUnknownCallRetainsWakeAndOtherResults(t *testing.T) {
	p := &parallelTestProvider{&hostProvider{hook: func(_ context.Context, _ string, args agentcontract.JSON, _ *agentcontract.InvocationContext) (agentcontract.CapabilityResult, error) {
		if args["id"] == "0" {
			return agentcontract.CapabilityResult{}, errors.New("lost response")
		}
		return agentcontract.CapabilityResult{Data: args}, nil
	}}}
	h := concurrentHost(t, p)
	run := createTestHostRun(t, h)
	run, err := h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "waiting" || run.NextWakeAt == nil {
		t.Fatal(run.Status, run.NextWakeAt, err)
	}
	state, callErr4 := h.Restore(run)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if len(state.Facts) != 3 || state.Pending[0].Status != "unknown" {
		t.Fatal(state)
	}
	for _, item := range state.Pending[1:] {
		if item.Status != "succeeded" {
			t.Fatal(item)
		}
	}
}
