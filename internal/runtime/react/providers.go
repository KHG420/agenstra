package react

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// BindIdempotency copies arguments before binding the existing invocation key.
// It rejects malformed paths or arguments without changing the caller's map.
func BindIdempotency(call agentcontract.ToolCall, cap agentcontract.CapabilityDescription, inv agentcontract.InvocationContext) (agentcontract.ToolCall, error) {
	if cap.IdempotencyArgument == nil {
		return call, nil
	}
	if len(cap.IdempotencyArgument) == 0 {
		return call, errors.New("idempotency_argument_invalid")
	}
	b, err := agentcontract.CanonicalJSON(call.Arguments)
	if err != nil {
		return call, errors.New("capability_input_invalid")
	}
	var args agentcontract.JSON
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return call, errors.New("capability_input_invalid")
	}
	cursor := args
	for _, key := range cap.IdempotencyArgument[:len(cap.IdempotencyArgument)-1] {
		next, ok := cursor[key]
		if !ok {
			child := agentcontract.JSON{}
			cursor[key] = child
			cursor = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return call, errors.New("capability_input_invalid")
		}
		cursor = child
	}
	cursor[cap.IdempotencyArgument[len(cap.IdempotencyArgument)-1]] = inv.IdempotencyKey
	call.Arguments = args
	return call, nil
}

// ExecuteCall checks catalog access and invokes a provider with owned arguments.
// It copies returned evidence and converts provider failures to safe outcome codes.
func ExecuteCall(ctx context.Context, provider agentcontract.CapabilityProvider, grants map[string]bool, call agentcontract.ToolCall, inv *agentcontract.InvocationContext) (agentcontract.CallOutcome, error) {
	cap, ok := provider.Capabilities()[call.Capability]
	if !ok {
		return agentcontract.CallOutcome{ErrorCode: "capability_unknown"}, nil
	}
	if !grants[cap.Name] {
		return agentcontract.CallOutcome{ErrorCode: "capability_not_granted"}, nil
	}
	if agentcontract.ValidateModelOutput(cap.ModelOutput) != nil {
		return agentcontract.CallOutcome{ErrorCode: "model_output_config_invalid"}, nil
	}
	if inv != nil {
		var err error
		call, err = BindIdempotency(call, cap, *inv)
		if err != nil {
			return agentcontract.CallOutcome{ErrorCode: err.Error()}, nil
		}
	}

	// Provider code receives its own arguments, so it cannot alter the saved
	// invocation or the caller's data after the parameter digest was checked.
	arguments, err := jsonvalue.Clone(call.Arguments)
	if err != nil || arguments == nil {
		return agentcontract.CallOutcome{ErrorCode: "capability_input_invalid"}, nil
	}
	var providerInvocation *agentcontract.InvocationContext
	if inv != nil {
		copy := *inv
		providerInvocation = &copy
	}
	projection, err := jsonvalue.Clone(cap.ModelOutput)
	if err != nil {
		return agentcontract.CallOutcome{ErrorCode: "model_output_config_invalid"}, nil
	}
	result, err := provider.Invoke(ctx, call.Capability, arguments, providerInvocation)
	if err != nil {
		return agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}, nil
	}
	if result.ErrorCode != "" || result.Data == nil {
		return agentcontract.CallOutcome{ErrorCode: firstNonempty(result.ErrorCode, "upstream_response_invalid")}, nil
	}
	data, err := jsonvalue.Clone(result.Data)
	if err != nil {
		return agentcontract.CallOutcome{ErrorCode: "upstream_response_invalid"}, nil
	}
	scope := "durable"
	if result.ReferenceScope == "connection" || cap.ReferenceScope == "connection" {
		scope = "connection"
	}
	fact := agentcontract.Fact{FactID: agentcontract.NewID(), SourceCapability: cap.Name, SourceVersion: cap.Version, Value: agentcontract.JSON{"data": data}, ModelOutput: projection, Quality: "provider_reported", ObservedAt: time.Now().UTC(), ReferenceScope: scope}
	if result.ExpiresAt != nil {
		expiresAt := *result.ExpiresAt
		fact.ExpiresAt = &expiresAt
	}
	if inv != nil {
		connectionID := inv.ConnectionID
		fact.ConnectionID = &connectionID
		fact.SourcePackID, fact.SourceRelease, fact.SourceSubject = inv.TargetPackID, inv.TargetRelease, inv.TargetSubject
	}
	return agentcontract.CallOutcome{Fact: &fact}, nil
}

func firstNonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Observe appends evidence and observations and settles the supplied invocation.
func Observe(state *agentcontract.RuntimeState, item *agentcontract.Invocation, outcome agentcontract.CallOutcome) {
	obs := agentcontract.Observation{CallRef: item.Call.CallRef, Capability: item.Call.Capability, Arguments: item.Call.Arguments}
	modelObs := obs
	modelObs.Arguments = item.OriginalArguments
	if outcome.Fact != nil {
		state.Facts = append(state.Facts, *outcome.Fact)
		item.FactID = &outcome.Fact.FactID
		obs.FactID = item.FactID
		modelObs.FactID = item.FactID
		obs.Status = "succeeded"
		modelObs.Status = "succeeded"
		item.Status = "succeeded"
		item.ErrorCode = nil
	} else {
		obs.Status = "failed"
		modelObs.Status = "failed"
		item.Status = "failed"
		item.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
		obs.ErrorCode = item.ErrorCode
		modelObs.ErrorCode = item.ErrorCode
	}
	state.Observations = append(state.Observations, obs)
	state.ModelObservations = append(state.ModelObservations, modelObs)
}

// ArgumentsDigest computes the canonical SHA-256 of a JSON capability call.
// It returns an empty string when the call cannot be encoded as JSON.
func ArgumentsDigest(call agentcontract.ToolCall) string {
	raw, err := agentcontract.CanonicalJSON(agentcontract.JSON{"capability": call.Capability, "arguments": call.Arguments})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
