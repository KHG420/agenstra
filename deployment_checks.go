package agenstra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ReconciliationRule verifies an uncertain operation using a granted read
// capability. Argument paths address {arguments, invocation_id, idempotency_key}.
// ResultPath selects the original operation's result in the query response.
type ReconciliationRule struct {
	VerifyCapability string           `json:"verify_capability"`
	Arguments        map[string][]any `json:"arguments"`
	SuccessPath      []any            `json:"success_path"`
	SuccessValue     any              `json:"success_value"`
	ResultPath       []any            `json:"result_path"`
}

// A plain argument name addresses a top-level MCP field; a JSON pointer such
// as /query/request_id builds the grouped input required by a REST capability.
func reconciliationArgumentPath(name string) ([]string, error) {
	if name == "" {
		return nil, fmt.Errorf("reconciliation argument required")
	}
	if !strings.HasPrefix(name, "/") {
		return []string{name}, nil
	}
	parts := strings.Split(name[1:], "/")
	if len(parts) > 16 {
		return nil, fmt.Errorf("reconciliation argument path too long")
	}
	for i, part := range parts {
		if part == "" || strings.Contains(strings.ReplaceAll(strings.ReplaceAll(part, "~0", ""), "~1", ""), "~") {
			return nil, fmt.Errorf("reconciliation argument path invalid")
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func setReconciliationArgument(args JSON, name string, value any) error {
	path, err := reconciliationArgumentPath(name)
	if err != nil {
		return err
	}
	for _, part := range path[:len(path)-1] {
		if args[part] == nil {
			args[part] = JSON{}
		}
		child, ok := args[part].(map[string]any)
		if !ok {
			return fmt.Errorf("reconciliation argument conflict")
		}
		args = child
	}
	key := path[len(path)-1]
	if _, exists := args[key]; exists {
		return fmt.Errorf("reconciliation argument conflict")
	}
	args[key] = value
	return nil
}

func validCheckPath(path []any) bool {
	if len(path) > 16 {
		return false
	}
	for _, part := range path {
		if _, ok := part.(string); !ok {
			if index, ok := pathIndex(part); !ok || index < 0 {
				return false
			}
		}
	}
	return true
}

// configureDeploymentChecks compiles optional business checks before workers
// start. Explicit Go callbacks take precedence over deployment configuration.
func configureDeploymentChecks(h *AgentHost, d *Deployment) error {
	compiledCompletion, compiledReconciler := h.CompletionValidator, h.Reconciler
	checks := map[string]map[string][]CompletionValidator{}
	required := map[string]map[string]bool{}
	for pack, requirements := range d.Config.CompletionChecks {
		if pack == "" {
			return fmt.Errorf("completion_checks pack required")
		}
		checks[pack] = map[string][]CompletionValidator{}
		required[pack] = map[string]bool{}
		for _, requirement := range requirements {
			validator, err := RequireFactValues(requirement)
			if err != nil {
				return err
			}
			checks[pack][requirement.Capability] = append(checks[pack][requirement.Capability], validator)
			if requirement.Required {
				required[pack][requirement.Capability] = true
			}
		}
	}
	if h.CompletionValidator == nil && len(checks) > 0 {
		compiledCompletion = func(ctx context.Context, result CompletionContext) error {
			used := map[string]bool{}
			latest := map[string]Observation{}
			for _, observation := range result.Observations {
				used[observation.Capability] = true
				latest[observation.Capability] = observation
			}
			for _, fact := range result.Facts {
				used[fact.SourceCapability] = true
			}
			for name := range required[result.OriginPackID] {
				if !used[name] {
					return CompletionValidationError{"completion_capability_required", "Complete the required business operation before reporting completion."}
				}
			}
			for name := range used {
				for _, check := range checks[result.OriginPackID][name] {
					if observation, ok := latest[name]; ok && observation.ErrorCode != nil {
						return CompletionValidationError{"completion_operation_failed", "Verify the latest operation result before reporting completion."}
					}
					if err := check(ctx, result); err != nil {
						return err
					}
				}
			}
			return ctx.Err()
		}
	}
	// Copy and compile rules so later edits to configuration maps cannot change
	// callback behavior. Paths and values are validated before serving traffic.
	raw, err := json.Marshal(d.Config.ReconciliationChecks)
	if err != nil {
		return err
	}
	var rules map[string]map[string]ReconciliationRule
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&rules); err != nil {
		return err
	}
	for pack, entries := range rules {
		for capability, rule := range entries {
			if pack == "" || capability == "" || rule.VerifyCapability == "" || !validCheckPath(rule.SuccessPath) || !validCheckPath(rule.ResultPath) {
				return fmt.Errorf("reconciliation_checks invalid")
			}
			if _, err := validateLocalSchema(JSON{"const": rule.SuccessValue}, false); err != nil {
				return err
			}
			correlated := false
			argumentPaths := [][]string{}
			for name, path := range rule.Arguments {
				if len(path) == 1 && (path[0] == "idempotency_key" || path[0] == "invocation_id") {
					correlated = true
				}
				if name == "" || len(path) == 0 || !validCheckPath(path) {
					return fmt.Errorf("reconciliation_checks argument invalid")
				}
				argumentPath, err := reconciliationArgumentPath(name)
				if err != nil {
					return err
				}
				for _, previous := range argumentPaths {
					overlap := true
					for i := 0; i < min(len(previous), len(argumentPath)); i++ {
						overlap = overlap && previous[i] == argumentPath[i]
					}
					if overlap {
						return fmt.Errorf("reconciliation_checks argument paths overlap")
					}
				}
				argumentPaths = append(argumentPaths, argumentPath)
			}
			if !correlated {
				return fmt.Errorf("reconciliation_checks must query the original invocation identity")
			}
		}
	}
	if h.Reconciler == nil && len(rules) > 0 {
		compiledReconciler = func(ctx context.Context, verification ReconciliationContext) (CapabilityResult, error) {
			rule, ok := rules[verification.OriginPackID][verification.Capability.Name]
			if !ok {
				return CapabilityResult{}, hostError("reconciliation_unavailable")
			}
			run, err := h.Get(ctx, verification.RunID, verification.OwnerID)
			if err != nil {
				return CapabilityResult{}, err
			}
			provider, err := h.openRunProvider(ctx, run)
			if err != nil {
				return CapabilityResult{}, err
			}
			defer provider.Close()
			if previous, ok := run.State["pack_fingerprint"].(string); ok && previous != fingerprint(provider) {
				return CapabilityResult{}, hostError("pack_changed")
			}
			cap, ok := provider.Capabilities()[rule.VerifyCapability]
			if !ok || cap.Effect != "read" {
				return CapabilityResult{}, hostError("reconciliation_verifier_not_read_only")
			}
			policy, err := h.projectPolicy(ctx, run)
			if err != nil {
				return CapabilityResult{}, err
			}
			if !policy.GrantedCapabilities[cap.Name] || policy.ApprovalCapabilities[cap.Name] || cap.ApprovalRequired {
				return CapabilityResult{}, hostError("reconciliation_verifier_not_granted")
			}
			input := JSON{"arguments": verification.Invocation.Call.Arguments, "invocation_id": verification.Invocation.InvocationID, "idempotency_key": verification.Identity.IdempotencyKey}
			args := JSON{}
			for name, path := range rule.Arguments {
				value, err := valueAt(input, path)
				if err != nil {
					return CapabilityResult{}, hostError("reconciliation_argument_missing")
				}
				if err := setReconciliationArgument(args, name, value); err != nil {
					return CapabilityResult{}, hostError("reconciliation_argument_invalid")
				}
			}
			result, err := provider.Invoke(ctx, cap.Name, args, nil)
			if err != nil || result.ErrorCode != "" {
				return CapabilityResult{}, hostError("reconciliation_verification_failed")
			}
			value, err := valueAt(result.Data, rule.SuccessPath)
			schema, _ := validateLocalSchema(JSON{"const": rule.SuccessValue}, false)
			if err != nil || validateSchema(schema, value) != nil {
				return CapabilityResult{}, hostError("reconciliation_verification_failed")
			}
			value, err = valueAt(result.Data, rule.ResultPath)
			data, ok := value.(map[string]any)
			if err != nil || !ok {
				return CapabilityResult{}, hostError("reconciliation_result_invalid")
			}
			return CapabilityResult{Data: data}, nil
		}
	}
	h.CompletionValidator, h.Reconciler = compiledCompletion, compiledReconciler
	return nil
}
