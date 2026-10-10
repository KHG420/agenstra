package service

import (
	"context"
	"errors"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type validationTestProvider struct {
	*hostProvider
	validate func(context.Context, string, agentcontract.InvocationContext) error
}

func (p *validationTestProvider) ValidateInvocation(ctx context.Context, name string, inv agentcontract.InvocationContext) error {
	return p.validate(ctx, name, inv)
}

func validationWrappers() map[string]func(agentcontract.CapabilityProvider) agentcontract.CapabilityProvider {
	return map[string]func(agentcontract.CapabilityProvider) agentcontract.CapabilityProvider{
		"deployment": func(p agentcontract.CapabilityProvider) agentcontract.CapabilityProvider {
			return deployassembly.BindProvider(p, "binding", "subject")
		},
		"browser": func(p agentcontract.CapabilityProvider) agentcontract.CapabilityProvider {
			return &browserProvider{base: p, caps: p.Capabilities(), profile: &compiledFrontend{digest: "test-profile"}}
		},
		"combined": func(p agentcontract.CapabilityProvider) agentcontract.CapabilityProvider {
			return deployassembly.BindProvider(&browserProvider{base: deployassembly.BindProvider(p, "binding", "subject"), caps: p.Capabilities(), profile: &compiledFrontend{digest: "test-profile"}}, "outer-binding", "subject")
		},
	}
}

func TestProviderWrappersPreserveInvocationValidation(t *testing.T) {
	for name, wrap := range validationWrappers() {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inv := agentcontract.InvocationContext{RunID: "run", InvocationID: "invocation", IdempotencyKey: "stable", OwnerID: "alice", ConnectionID: "connection", OriginPackID: "origin", TargetPackID: "target", TargetSubject: "subject", TargetRelease: "release"}
			calls := 0
			failure := agentcontract.NewHostError("fixture_prerequisite_missing")
			p := &validationTestProvider{hostProvider: &hostProvider{}, validate: func(got context.Context, capability string, identity agentcontract.InvocationContext) error {
				calls++
				if got != ctx || capability != "records.get" || identity != inv {
					t.Fatal("validation lost request context or trusted invocation", capability, identity)
				}
				if got.Err() != nil {
					return got.Err()
				}
				return failure
			}}
			validator, ok := wrap(p).(agentcontract.InvocationValidator)
			if !ok {
				t.Fatal("wrapper hid invocation validator")
			}
			if err := validator.ValidateInvocation(ctx, "records.get", inv); !errors.Is(err, failure) || calls != 1 {
				t.Fatal("wrapper lost prerequisite rejection", err, calls)
			}
			cancel()
			if err := validator.ValidateInvocation(ctx, "records.get", inv); !errors.Is(err, context.Canceled) || calls != 2 {
				t.Fatal("wrapper lost cancellation", err, calls)
			}
			if p.calls != 0 {
				t.Fatal("preflight executed business capability", p.calls)
			}
		})
	}
}

func TestWrappedPrerequisiteRejectionPrecedesApproval(t *testing.T) {
	for name, wrap := range validationWrappers() {
		t.Run(name, func(t *testing.T) {
			checks := 0
			p := &validationTestProvider{hostProvider: &hostProvider{}, validate: func(_ context.Context, capability string, inv agentcontract.InvocationContext) error {
				checks++
				if capability != "records.get" || inv.OwnerID != "alice" || inv.RunID == "" || inv.InvocationID == "" || inv.TargetPackID != "records" {
					t.Fatal("missing trusted preflight identity", capability, inv)
				}
				return agentcontract.NewHostError("fixture_prerequisite_missing")
			}}
			h := testHost(t, testStore(t), p.hostProvider, &hostModel{decisions: []agentcontract.Decision{callDecision("records.get")}})
			h.ProviderFactory = func(context.Context, string, string) (agentcontract.CapabilityProvider, error) { return wrap(p), nil }
			h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
				return agentcontract.ExecutionPolicy{GrantedCapabilities: map[string]bool{"records.get": true}, ApprovalCapabilities: map[string]bool{"records.get": true}, AllowModelData: true}, nil
			}
			run := createTestHostRun(t, h)
			run, err := h.Drive(t.Context(), run.RunID, "alice")
			if err != nil || run.Status == "needs_approval" || checks != 1 || p.calls != 0 {
				t.Fatal("unfulfillable call requested approval or executed", run.Status, err, checks, p.calls)
			}
			state, err := h.Restore(run)
			if err != nil || len(state.Observations) != 1 || state.Observations[0].ErrorCode == nil || *state.Observations[0].ErrorCode != "fixture_prerequisite_missing" {
				t.Fatal("preflight rejection missing from observations", state, err)
			}
		})
	}
}

func TestProviderWrappersKeepValidationOptionalAndBrowserLocal(t *testing.T) {
	for name, wrap := range validationWrappers() {
		t.Run(name, func(t *testing.T) {
			p := &hostProvider{}
			validator, ok := wrap(p).(agentcontract.InvocationValidator)
			if !ok {
				t.Fatal("wrapper validation interface unavailable")
			}
			if err := validator.ValidateInvocation(t.Context(), "records.get", agentcontract.InvocationContext{}); err != nil {
				t.Fatal("provider without optional validator became unusable", err)
			}
		})
	}
	base := &validationTestProvider{hostProvider: &hostProvider{}, validate: func(context.Context, string, agentcontract.InvocationContext) error {
		t.Fatal("server-side browser observation forwarded to business validator")
		return nil
	}}
	wrapped := deployassembly.BindProvider(&browserProvider{base: base}, "binding", "subject")
	validator, ok := wrapped.(agentcontract.InvocationValidator)
	if !ok {
		t.Fatal("wrapper hid browser validation")
	}
	for _, name := range []string{"ui.get_context", "ui.command_status"} {
		if err := validator.ValidateInvocation(t.Context(), name, agentcontract.InvocationContext{}); err != nil {
			t.Fatal(name, err)
		}
	}
}
