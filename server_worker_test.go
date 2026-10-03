package agenstra

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type independentWorkerModel struct {
	started chan struct{}
	once    sync.Once
}

func (m *independentWorkerModel) Decide(ctx context.Context, packet ContextPacket, _ string) (Decision, error) {
	if packet.Instruction == "block model" {
		m.once.Do(func() { close(m.started) })
		<-ctx.Done()
		return Decision{}, ctx.Err()
	}
	if len(packet.Facts) == 0 {
		return callDecision("job.submit"), nil
	}
	ids := make([]string, 0, len(packet.Facts))
	for _, fact := range packet.Facts {
		ids = append(ids, fact.FactID)
	}
	return Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Done", FactIDs: ids}, nil
}

func TestHTTPWorkerPollsWhileAnotherRunWaitsForModel(t *testing.T) {
	store := testStore(t)
	model := &independentWorkerModel{started: make(chan struct{})}
	polled := make(chan struct{})
	var pollOnce sync.Once
	var submits atomic.Int32
	binding := &OperationBinding{IDPath: []any{"id"}, StatusPath: []any{"status"}, PollCapability: "job.status", PollArgument: []string{"id"}, IntervalSeconds: 1, TimeoutSeconds: 30, PendingStates: []string{"running"}, SuccessStates: []string{"succeeded"}, FailureStates: []string{"failed"}, ReconcileOnTimeout: true}
	provider := &hostProvider{caps: map[string]CapabilityDescription{
		"job.submit": {Name: "job.submit", Version: "1", Effect: "write", Replay: "never", ReferenceScope: "durable", InputSchema: JSON{"type": "object"}, Operation: binding},
		"job.status": {Name: "job.status", Version: "1", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: JSON{"type": "object"}},
	}}
	provider.hook = func(_ context.Context, name string, _ JSON, _ *InvocationContext) (CapabilityResult, error) {
		status := "running"
		if name == "job.submit" {
			submits.Add(1)
		} else {
			status = "succeeded"
			pollOnce.Do(func() { close(polled) })
		}
		return CapabilityResult{Data: JSON{"id": "task-job", "status": status}, ReferenceScope: "durable"}, nil
	}
	host := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) {
		return provider, nil
	}, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{"job.submit": true, "job.status": true}, AllowModelData: true}, nil
	})
	host.Settings.MaxConcurrentRuns = 2
	run, err := host.Create(t.Context(), "alice", "records", "poll job", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err = host.Drive(t.Context(), run.RunID, run.OwnerID)
	if err != nil || run.Status != "waiting" {
		t.Fatalf("prepare asynchronous job: status=%s err=%v", run.Status, err)
	}
	if _, err = host.Create(t.Context(), "alice", "records", "block model", ""); err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPServer(host, &Deployment{Config: DeploymentConfig{}}, true, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking model was not started")
	}
	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("a blocked model prevented an independent operation status query")
	}
	if submits.Load() != 1 {
		t.Fatalf("external operation replayed: %d submits", submits.Load())
	}
}

type joinedWorkerModel struct {
	started chan struct{}
	exited  chan struct{}
}

func (m *joinedWorkerModel) Decide(ctx context.Context, _ ContextPacket, _ string) (Decision, error) {
	m.started <- struct{}{}
	<-ctx.Done()
	m.exited <- struct{}{}
	return Decision{}, ctx.Err()
}

func TestHTTPWorkerShutdownJoinsEveryStartedRun(t *testing.T) {
	store := testStore(t)
	model := &joinedWorkerModel{started: make(chan struct{}, 2), exited: make(chan struct{}, 2)}
	host := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) {
		return &hostProvider{}, nil
	}, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{AllowModelData: true}, nil
	})
	host.Settings.MaxConcurrentRuns = 2
	for range 2 {
		if _, err := host.Create(t.Context(), "alice", "records", "block model", ""); err != nil {
			t.Fatal(err)
		}
	}
	server, err := NewHTTPServer(host, &Deployment{Config: DeploymentConfig{}}, true, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		select {
		case <-model.started:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not start both independent runs")
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker shutdown did not join all started runs")
	}
	if len(model.exited) != 2 {
		t.Fatalf("Close returned with an active model: %d of 2 exited", len(model.exited))
	}
}
