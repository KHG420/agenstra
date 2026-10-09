package host

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// AgentHost owns run scheduling, authorization, leases and durable checkpoints.
// It must not be copied after use; its store and model are supplied by the host app.
type AgentHost struct {
	Store                  *runstore.SQLiteStore
	ProviderFactory        agentcontract.ProviderFactory
	Model                  agentcontract.DecisionModel
	CompletionValidator    agentcontract.CompletionValidator
	Reconciler             agentcontract.InvocationReconciler
	PolicyResolver         agentcontract.PolicyResolver
	ReleaseResolver        agentcontract.ReleaseResolver
	ReleaseProviderFactory agentcontract.ReleaseProviderFactory
	Settings               agentcontract.HostSettings
	Clock                  func() float64
	once                   sync.Once
	slots                  chan struct{}
}

// NewAgentHost wires host services with default limits without opening or owning their resources.
func NewAgentHost(store *runstore.SQLiteStore, factory agentcontract.ProviderFactory, model agentcontract.DecisionModel, policy agentcontract.PolicyResolver) *AgentHost {
	return &AgentHost{Store: store, ProviderFactory: factory, Model: model, PolicyResolver: policy, Settings: agentcontract.DefaultHostSettings(), Clock: runstore.UnixNow}
}

// InitializeDefaults initializes run slots once, before driving work.
func (h *AgentHost) InitializeDefaults() {
	h.once.Do(func() {
		if h.Settings == (agentcontract.HostSettings{}) {
			h.Settings = agentcontract.DefaultHostSettings()
		}
		h.slots = make(chan struct{}, max(1, h.Settings.MaxConcurrentRuns))
	})
}

// Now returns the configured owner's clock in fractional Unix seconds.
func (h *AgentHost) Now() float64 {
	if h.Clock != nil {
		return h.Clock()
	}
	return runstore.UnixNow()
}

// Policy resolves and validates current trusted authorization, optionally requiring model-data permission.
func (h *AgentHost) Policy(ctx context.Context, owner, pack string, modelData bool) (agentcontract.ExecutionPolicy, error) {
	p, err := h.PolicyResolver(ctx, owner, pack)
	if err != nil {
		var e *agentcontract.HostError
		if errors.As(err, &e) {
			return p, e
		}
		var deployment *agentcontract.DeploymentError
		if errors.As(err, &deployment) {
			return p, agentcontract.NewHostError(deployment.Code)
		}
		return p, agentcontract.NewHostError("authorization_unavailable")
	}
	if modelData && !p.AllowModelData {
		return p, agentcontract.NewHostError("model_data_not_authorized")
	}
	return p, nil
}

// Terminal reports whether a run has reached a terminal execution state.
func Terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func requestRunID(owner, request string) string {
	if request == "" {
		return agentcontract.NewID()
	}
	b, _ := agentcontract.CanonicalJSON([]string{owner, request}) //nolint:errcheck // A slice of strings always encodes as JSON.

	// UUIDv5, URL namespace; preserves request-id replay across Python and Go.
	namespace := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	sum := sha1.Sum(append(namespace, b...))
	sum[6] = (sum[6] & 15) | 0x50
	sum[8] = (sum[8] & 63) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// RequestRunID returns the stable run identity used by Create. Integrations can
// persist a binding before a queued run becomes visible to the worker.
// A nonempty request ID is required for a stable identity.
func RequestRunID(owner, request string) string { return requestRunID(owner, request) }

func (h *AgentHost) prepareRun(ctx context.Context, owner, pack, instruction, id string) (map[string]any, error) {
	if strings.TrimSpace(instruction) == "" || len([]rune(instruction)) > 30000 {
		return nil, agentcontract.NewHostError("instruction_invalid")
	}
	if _, e := h.Policy(ctx, owner, pack, true); e != nil {
		return nil, e
	}
	var release any
	if h.ReleaseResolver != nil {
		r, e := h.ReleaseResolver(ctx, owner, pack)
		if e != nil {
			return nil, e
		}
		if r != "" {
			release = r
		}
	}
	state, e := reactcore.NewState(instruction, id)
	if e != nil {
		return nil, e
	}
	runtime, e := agentcontract.ObjectOf(state)
	if e != nil {
		return nil, e
	}
	delete(runtime, "facts")
	settings := NormalizedRunSettings(h.Settings)
	runModel := h.Model
	var selection *agentcontract.ModelSelectionSnapshot
	if manager, ok := h.Model.(runModelSelector); ok {
		snapshot := manager.Snapshot()
		profiles := map[string]agentcontract.ModelProfile{}
		for _, id := range []string{snapshot.Config.DefaultProfile, snapshot.Config.ProfileID("decision"), snapshot.Config.ProfileID("memory_extraction")} {
			profiles[id] = snapshot.Config.Profiles[id]
		}
		snapshot.Config.Profiles = profiles
		selected, err := manager.SelectModel(snapshot.Config)
		if err != nil {
			return nil, err
		}
		runModel, selection = selected, &snapshot
	}
	info := agentcontract.DescribeModel(runModel)
	settings.ModelProtocolReserveTokens = max(settings.ModelProtocolReserveTokens, info.ProtocolReserveTokens)
	if info.ContextWindowTokens != nil && (settings.ModelContextWindowTokens == 0 || *info.ContextWindowTokens < settings.ModelContextWindowTokens) {
		settings.ModelContextWindowTokens = *info.ContextWindowTokens
	}
	if info.MaxInputTokens != nil && (settings.MaxModelInputTokens == 0 || *info.MaxInputTokens < settings.MaxModelInputTokens) {
		settings.MaxModelInputTokens = *info.MaxInputTokens
	}
	if info.MaxOutputTokens != nil && (settings.MaxModelOutputTokens == 0 || *info.MaxOutputTokens < int64(settings.MaxModelOutputTokens)) {
		settings.MaxModelOutputTokens = int(*info.MaxOutputTokens)
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	envelope := map[string]any{"runtime": runtime, "artifact_ids": []string{}, "pack_fingerprint": nil, "pack_release": release, "effective_config": agentcontract.EffectiveRunConfig{Version: 1, Source: "run_snapshot", Settings: settings, Model: info, ModelSelection: selection}}
	setMemoryInput(envelope, id+":instruction", instruction)
	raw, err := agentcontract.CanonicalJSON(envelope)
	if err != nil {
		return nil, err
	}
	if len(raw) > settings.MaxStateBytes {
		return nil, agentcontract.NewHostError("run_state_too_large")
	}
	return envelope, nil
}

// Create authorizes and persists a run, reusing the owner's supplied request identity.
func (h *AgentHost) Create(ctx context.Context, owner, pack, instruction, requestID string) (agentcontract.StoredRun, error) {
	return h.createWithMemoryInput(ctx, owner, pack, instruction, requestID, instruction)
}

func (h *AgentHost) createWithMemoryInput(ctx context.Context, owner, pack, instruction, requestID, input string) (agentcontract.StoredRun, error) {
	return h.CreateScopedRun(ctx, owner, pack, instruction, requestID, input, nil)
}

// Get returns an owner-scoped run after checking current access.
func (h *AgentHost) Get(ctx context.Context, id, owner string) (agentcontract.StoredRun, error) {
	r, e := h.Store.GetRun(id, owner)
	if e != nil {
		return r, e
	}
	_, e = h.Policy(ctx, owner, r.PackID, false)
	return r, e
}

// Restore loads complete persisted facts and validates the runtime checkpoint. The returned state belongs to the driver.
func (h *AgentHost) Restore(run agentcontract.StoredRun) (*agentcontract.RuntimeState, error) {
	if _, err := h.effectiveRunConfig(run); err != nil {
		return nil, err
	}
	runtime, ok := run.State["runtime"].(map[string]any)
	if !ok {
		return nil, agentcontract.NewHostError("run_state_invalid")
	}
	copy, e := agentcontract.ObjectOf(runtime)
	if e != nil {
		return nil, e
	}
	facts := []any{}
	ids, ok := run.State["artifact_ids"].([]any)
	if !ok {
		return nil, agentcontract.NewHostError("run_state_invalid")
	}
	for _, v := range ids {
		id, ok := v.(string)
		if !ok {
			return nil, agentcontract.NewHostError("run_state_invalid")
		}
		f, e := h.Store.GetArtifact(run.RunID, id, run.OwnerID)
		if e != nil {
			return nil, e
		}
		facts = append(facts, f)
	}
	copy["facts"] = facts
	b, e := agentcontract.CanonicalJSON(copy)
	if e != nil {
		return nil, e
	}
	var state agentcontract.RuntimeState
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e = d.Decode(&state); e != nil || state.SchemaVersion != 1 || state.RunID != run.RunID {
		return nil, agentcontract.NewHostError("run_state_invalid")
	}
	return &state, nil
}

func (h *AgentHost) save(run agentcontract.StoredRun, state *agentcontract.RuntimeState, fingerprint string, wake *float64, event map[string]any) (agentcontract.StoredRun, error) {
	checkpointInvocationReceipts(state)
	h.recordExecution(state, event)
	runtime, e := agentcontract.ObjectOf(state)
	if e != nil {
		return run, e
	}
	delete(runtime, "facts")
	ids := []string{}
	artifacts := map[string]map[string]any{}
	size := 0
	for _, fact := range state.Facts {
		ids = append(ids, fact.FactID)
		b, e := agentcontract.CanonicalJSON(fact)
		if e != nil {
			return run, e
		}
		size += len(b)
		if _, e = h.Store.GetArtifact(run.RunID, fact.FactID, run.OwnerID); errors.Is(e, runstore.ErrRunNotFound) {
			a, e := agentcontract.DecodeObject(string(b))
			if e != nil {
				return run, e
			}
			artifacts[fact.FactID] = a
		} else if e != nil {
			return run, e
		}
	}
	if size > h.runSettings(run).MaxActiveArtifactBytes {
		return run, agentcontract.NewHostError("run_artifacts_too_large")
	}
	var fp any = run.State["pack_fingerprint"]
	if fingerprint != "" {
		fp = fingerprint
	}
	envelope := map[string]any{"runtime": runtime, "artifact_ids": ids, "pack_fingerprint": fp, "pack_release": run.State["pack_release"]}
	for _, key := range []string{"memory_inputs", "memory_snapshot", "memory_errors", "project_sources", "effective_config"} {
		if value, exists := run.State[key]; exists {
			envelope[key] = value
		}
	}
	b, e := agentcontract.CanonicalJSON(envelope)
	if e != nil {
		return run, e
	}
	if len(b) > h.runSettings(run).MaxStateBytes {
		return run, agentcontract.NewHostError("run_state_too_large")
	}
	invs := []map[string]any{}
	for _, item := range state.Pending {
		v, e := agentcontract.ObjectOf(item)
		if e != nil {
			return run, e
		}
		invs = append(invs, v)
	}
	events := []map[string]any{}
	if event != nil {
		if progress := reactcore.RunProgress(state, h.runSettings(run).MaxStagnantRounds); progress != nil {
			event["progress"] = progress
		}
		event["status"] = state.Status
		if strings.HasPrefix(fmt.Sprint(event["kind"]), "model_") || strings.HasPrefix(fmt.Sprint(event["kind"]), "memory_") {
			view := run
			view.State = envelope
			view.Status = state.Status
			view.Revision++
			if telemetry, err := h.Telemetry(view); err == nil {
				event["budget"] = telemetry.Budget
				event["context"] = telemetry.Context
			}
		}
		events = append(events, event)
	}
	saved, err := h.Store.Checkpoint(run.RunID, run.OwnerID, run.LeaseToken, envelope, state.Status, wake, invs, artifacts, events)
	if err != nil {
		return run, err
	}
	return saved, nil
}

// SupplyInput validates a requested answer and revision before queuing continuation.
func (h *AgentHost) SupplyInput(ctx context.Context, id, owner, field, text string, revision int) (agentcontract.StoredRun, error) {
	current, e := h.Get(ctx, id, owner)
	if e != nil {
		return current, e
	}
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 30000 {
		return current, agentcontract.NewHostError("input_invalid")
	}
	run, e := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	defer func() {

		// The lease expires if release fails; keep the committed run outcome.
		if err := h.Store.Release(id, owner, run.LeaseToken); err != nil {
			log.Print("run lease release failed")
		}
	}()
	state, e := h.Restore(run)
	if e != nil {
		return run, e
	}
	if run.Revision != revision || current.Revision != revision {
		return run, agentcontract.NewHostError("revision_conflict")
	}
	if state.Status != "needs_input" || state.InputField == nil || *state.InputField != field {
		return run, agentcontract.NewHostError("input_not_requested")
	}
	if e := ValidateRequestedInput(state.InputSchema, text); e != nil {
		return run, e
	}
	inputs := []runstore.MemoryInput{}
	if raw, err := agentcontract.CanonicalJSON(run.State["memory_inputs"]); err == nil && run.State["memory_inputs"] != nil {
		if err := jsonvalue.DecodeStrict(raw, &inputs); err != nil {
			return run, agentcontract.NewHostError("run_state_invalid")
		}
	}
	run.State["memory_inputs"] = append(inputs, runstore.MemoryInput{ID: fmt.Sprintf("%s:input:%d", id, revision), Text: text})
	state.Followups = append(state.Followups, field+": "+text)
	state.InputField = nil
	state.InputPrompt = nil
	state.InputSchema = nil
	state.Status = "queued"
	state.ErrorCode = nil
	return h.save(run, state, "", nil, map[string]any{"kind": "input_received", "field": field})
}

// Approve binds an approval or rejection to the saved invocation, argument hash and revision.
func (h *AgentHost) Approve(ctx context.Context, id, owner, invocationID, argsSHA string, revision int, approved bool) (agentcontract.StoredRun, error) {
	if _, e := h.Get(ctx, id, owner); e != nil {
		return agentcontract.StoredRun{}, e
	}
	run, e := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	defer func() {

		// The lease expires if release fails; keep the committed run outcome.
		if err := h.Store.Release(id, owner, run.LeaseToken); err != nil {
			log.Print("run lease release failed")
		}
	}()
	state, e := h.Restore(run)
	if e != nil {
		return run, e
	}
	if run.Revision != revision {
		return run, agentcontract.NewHostError("revision_conflict")
	}
	var item *agentcontract.Invocation
	for i := range state.Pending {
		if state.Pending[i].InvocationID == invocationID {
			item = &state.Pending[i]
			break
		}
	}
	if state.Status != "needs_approval" || item == nil || item.Status != "needs_approval" {
		return run, agentcontract.NewHostError("approval_not_requested")
	}
	if item.ArgumentsSHA256 != argsSHA {
		return run, agentcontract.NewHostError("approval_arguments_changed")
	}
	if item.ApprovalExpiresAt == nil || h.Now() >= *item.ApprovalExpiresAt {
		return run, agentcontract.NewHostError("approval_expired")
	}
	kind := "approval_denied"
	if approved {
		policy, err := h.ProjectPolicy(ctx, run)
		if err != nil {
			return run, err
		}
		if !policy.GrantedCapabilities[item.Call.Capability] {
			return run, agentcontract.NewHostError("capability_not_granted")
		}
		item.ApprovedHash = &argsSHA
		item.ApprovedUntil = item.ApprovalExpiresAt
		item.Status = "prepared"
		kind = "approval_granted"
	} else {
		reactcore.Observe(state, item, agentcontract.CallOutcome{ErrorCode: "approval_denied"})
	}
	state.Status = "queued"
	return h.save(run, state, "", nil, map[string]any{"kind": kind, "invocation_id": invocationID, "arguments_sha256": argsSHA})
}

// Cancel requests local cancellation without claiming that external work was undone.
func (h *AgentHost) Cancel(ctx context.Context, id, owner string) (agentcontract.StoredRun, error) {
	r, e := h.Get(ctx, id, owner)
	if e != nil || Terminal(r.Status) {
		return r, e
	}
	return h.Store.RequestCancel(id, owner)
}

// Fingerprint reads the provider's optional pinned-contract fingerprint.
func Fingerprint(provider agentcontract.CapabilityProvider) string {
	skills := map[string]string{}
	for name, s := range provider.Skills() {
		skills[name] = s.Content
	}
	binding := ""
	if p, ok := provider.(interface{ BindingID() string }); ok {
		binding = p.BindingID()
	}
	b, err := agentcontract.CanonicalJSON(map[string]any{"capabilities": provider.Capabilities(), "skills": skills, "prompt": provider.SystemPrompt(), "binding": binding})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func invocationContext(run agentcontract.StoredRun, item *agentcontract.Invocation, connectionID string) agentcontract.InvocationContext {
	return agentcontract.InvocationContext{RunID: run.RunID, OwnerID: run.OwnerID, InvocationID: item.InvocationID, IdempotencyKey: item.InvocationID, ConnectionID: connectionID}
}

func pauseAuth(code string) bool {
	switch code {
	case "product_api_unauthorized", "product_api_forbidden", "upstream_http_401", "upstream_http_403", "unauthorized", "forbidden", "identity_unverified", "authorization_unavailable", "model_data_not_authorized", "access_denied", "connection_unavailable", "capability_not_granted", "operation_poll_requires_unattended_access":
		return true
	}
	return false
}

func definiteAuth(code string) bool {
	switch code {
	case "product_api_unauthorized", "product_api_forbidden", "upstream_http_401", "upstream_http_403", "unauthorized", "forbidden", "identity_unverified", "access_denied", "capability_not_granted", "model_data_not_authorized", "authorization_unavailable", "connection_unavailable":
		return true
	}
	return false
}

func pollAccess(provider agentcontract.CapabilityProvider, policy agentcontract.ExecutionPolicy, binding agentcontract.OperationBinding) error {
	cap, ok := provider.Capabilities()[binding.PollCapability]
	if !ok || cap.Effect != "read" {
		return agentcontract.NewHostError("operation_poll_must_be_read")
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return agentcontract.NewHostError("capability_not_granted")
	}
	if cap.ApprovalRequired || policy.ApprovalCapabilities[cap.Name] {
		return agentcontract.NewHostError("operation_poll_requires_unattended_access")
	}
	return nil
}

func (h *AgentHost) execute(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, item *agentcontract.Invocation, provider agentcontract.CapabilityProvider, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	run, prepared, err := h.prepareInvocation(ctx, run, state, item, provider, runtime)
	if err != nil || prepared == nil {
		return run, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(h.runSettings(run).InvocationTimeoutSeconds*1e9))
	outcome, err := reactcore.ExecuteCall(callCtx, provider, prepared.grants, item.Call, &prepared.inv)
	cancel()
	if ctx.Err() != nil && outcome.Fact == nil {
		return run, ctx.Err()
	}
	if err != nil {
		outcome = agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}
	}
	return h.settleInvocation(run, state, item, *prepared, outcome)
}

func (h *AgentHost) prepareInvocation(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, item *agentcontract.Invocation, provider agentcontract.CapabilityProvider, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, *preparedInvocation, error) {
	cap, ok := provider.Capabilities()[item.Call.Capability]
	policy, e := h.ProjectPolicy(ctx, run)
	if e != nil {
		return run, nil, e
	}
	if !ok {
		reactcore.Observe(state, item, agentcontract.CallOutcome{ErrorCode: "capability_unknown"})
		saved, err := h.save(run, state, "", nil, nil)
		return saved, nil, err
	}
	if cap.Operation != nil {
		if e = pollAccess(provider, policy, *cap.Operation); e != nil {
			return run, nil, e
		}
	}
	if !policy.GrantedCapabilities[cap.Name] {
		state.Status = "needs_authorization"
		state.ErrorCode = agentcontract.Strptr("capability_not_granted")
		saved, err := h.save(run, state, "", nil, map[string]any{"kind": "call_denied", "invocation_id": item.InvocationID})
		return saved, nil, err
	}
	inv := invocationContext(run, item, runtime.ConnectionID)
	IdentifyInvocation(&inv, run, provider, item.Call.Capability, policy)
	facts := map[string]agentcontract.Fact{}
	for _, f := range state.Facts {
		facts[f.FactID] = f
	}
	resolved := agentcontract.JSON{}
	for key, value := range item.OriginalArguments {
		v, err := reactcore.ResolveArgument(value, facts, runtime.ConnectionID, true)
		if err != nil {
			code := agentcontract.ErrorCode(err)
			if item.Attempts > 0 {
				item.Status = "unknown"
				item.ErrorCode = agentcontract.Strptr(code)
				state.Status = "needs_reconciliation"
				state.ErrorCode = agentcontract.Strptr(code)
			} else {
				reactcore.Observe(state, item, agentcontract.CallOutcome{ErrorCode: code})
			}
			saved, err := h.save(run, state, "", nil, map[string]any{"kind": "reference_unavailable", "invocation_id": item.InvocationID})
			return saved, nil, err
		}
		resolved[key] = v
	}
	normalized := item.Call
	normalized.Arguments = resolved
	normalized, e = reactcore.BindIdempotency(normalized, cap, inv)
	if e != nil {
		reactcore.Observe(state, item, agentcontract.CallOutcome{ErrorCode: "capability_input_invalid"})
		saved, err := h.save(run, state, "", nil, nil)
		return saved, nil, err
	}
	digest := reactcore.ArgumentsDigest(normalized)
	if item.ArgumentsSHA256 != "" && item.ArgumentsSHA256 != digest {
		return run, nil, agentcontract.NewHostError("invocation_arguments_changed")
	}
	item.Call = normalized
	item.ArgumentsSHA256 = digest
	validApproval := item.ApprovedHash != nil && *item.ApprovedHash == digest && item.ApprovedUntil != nil && h.Now() < *item.ApprovedUntil
	if item.Attempts == 0 {

		// Reject invalid inputs before asking a user to approve them. Providers
		// retain their execution checks; approval cannot freeze live page state.
		if cap.InputSchema != nil {
			schema, err := agentcontract.ValidateLocalSchema(cap.InputSchema, false)
			if err != nil || agentcontract.ValidateSchema(schema, normalized.Arguments) != nil {
				e = agentcontract.NewHostError("capability_input_invalid")
			}
		}
		if e == nil && !validApproval {
			if validator, ok := provider.(agentcontract.InvocationValidator); ok {
				e = validator.ValidateInvocation(ctx, cap.Name, inv)
			}
		}
		if e != nil {
			outcome := agentcontract.CallOutcome{ErrorCode: agentcontract.ErrorCode(e)}
			reactcore.CaptureInvocationReceipt(item, cap, outcome, nil)
			reactcore.Observe(state, item, outcome)
			saved, err := h.save(run, state, "", nil, nil)
			return saved, nil, err
		}
	}
	if item.Status == "in_flight" || item.Status == "unknown" {
		if cap.Replay == "never" || item.Attempts >= h.runSettings(run).MaxInvocationAttempts {
			item.Status = "unknown"
			state.Status = "needs_reconciliation"
			state.ErrorCode = agentcontract.Strptr("provider_outcome_unknown")
			saved, err := h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
			return saved, nil, err
		}
		item.Status = "prepared"
	}
	if (cap.ApprovalRequired || policy.ApprovalCapabilities[cap.Name]) && !validApproval {
		item.Status = "needs_approval"
		expires := h.Now() + h.runSettings(run).ApprovalSeconds
		item.ApprovalExpiresAt = &expires
		item.ApprovedHash = nil
		item.ApprovedUntil = nil
		state.Status = "needs_approval"
		saved, err := h.save(run, state, "", nil, map[string]any{"kind": "approval_requested", "invocation_id": item.InvocationID, "arguments_sha256": digest})
		return saved, nil, err
	}
	item.Status = "in_flight"
	item.Attempts++
	reactcore.BeginInvocationReceipt(item, cap)
	audit := invocationAudit("call_started", inv)
	audit["capability"], audit["arguments_sha256"] = cap.Name, digest
	run, e = h.save(run, state, "", nil, audit)
	if e != nil {
		return run, nil, e
	}
	return run, &preparedInvocation{cap: cap, inv: inv, grants: policy.GrantedCapabilities}, nil
}

func (h *AgentHost) settleInvocation(run agentcontract.StoredRun, state *agentcontract.RuntimeState, item *agentcontract.Invocation, prepared preparedInvocation, outcome agentcontract.CallOutcome) (agentcontract.StoredRun, error) {
	cap, inv := prepared.cap, prepared.inv
	var e error
	stoppedStatus, stoppedError := state.Status, state.ErrorCode
	if outcome.Fact == nil {
		reactcore.CaptureInvocationReceipt(item, cap, outcome, nil)
	}
	if definiteAuth(outcome.ErrorCode) {
		item.Status = "prepared"
		item.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
		state.Status = "needs_authorization"
		state.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
		return h.save(run, state, "", nil, map[string]any{"kind": "authorization_required"})
	}
	if reactcore.UnknownOutcome(outcome.ErrorCode) && (cap.Effect != "read" || outcome.ErrorCode == "provider_outcome_unknown") {
		item.Status = "unknown"
		item.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
		state.Status = "needs_reconciliation"
		var wake *float64
		if cap.Replay != "never" && item.Attempts < h.runSettings(run).MaxInvocationAttempts {
			state.Status = "waiting"
			v := h.Now() + h.runSettings(run).RetryIntervalSeconds
			wake = &v
		}
		state.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
		return h.save(run, state, "", wake, map[string]any{"kind": "call_outcome_unknown", "invocation_id": item.InvocationID})
	}
	if outcome.Fact != nil {
		b, e := agentcontract.CanonicalJSON(outcome.Fact)
		if e != nil {
			return run, e
		}
		reactcore.CaptureInvocationReceipt(item, cap, outcome, b)
		code := ""
		if len(b) > h.runSettings(run).MaxArtifactBytes {
			code = "result_too_large"
		} else {
			size := len(b)
			for _, f := range state.Facts {
				b, err := agentcontract.CanonicalJSON(f)
				if err != nil {
					return run, agentcontract.NewHostError("run_state_invalid")
				}
				size += len(b)
			}
			if size > h.runSettings(run).MaxActiveArtifactBytes {
				code = "run_artifacts_too_large"
			}
		}
		if code != "" {
			if item.Receipt != nil {
				item.Receipt.FactID = ""
				item.Receipt.ResultErrorCode = code
			}
			outcome = agentcontract.CallOutcome{ErrorCode: code}
			state.Status = "failed"
			state.ErrorCode = agentcontract.Strptr(code)
		}
	}
	reactcore.Observe(state, item, outcome)
	if item.Receipt != nil && item.Receipt.Status == "succeeded" && item.Receipt.ResultErrorCode != "" {
		item.Status, item.ErrorCode = "succeeded", nil
	} else if item.Receipt != nil && item.Receipt.ResultErrorCode != "" && (item.Receipt.Status == "accepted" || item.Receipt.Status == "unknown") {
		item.Status, item.ErrorCode = "unknown", agentcontract.Strptr("operation_outcome_unknown")
	}
	if outcome.Fact != nil && cap.Operation != nil {
		if e = h.operation(state, item, *outcome.Fact, *cap.Operation); e != nil {
			return run, e
		}
	}
	if stoppedStatus == "cancelled" || stoppedStatus == "failed" {
		state.Status, state.ErrorCode = stoppedStatus, stoppedError
	}
	var factID any
	if outcome.Fact != nil {
		factID = outcome.Fact.FactID
	}
	kind := "call_finished"
	if item.Reconciled {
		kind = "invocation_reconciled"
	}
	audit := invocationAudit(kind, inv)
	audit["fact_id"], audit["error_code"] = factID, agentcontract.Strptr(outcome.ErrorCode)
	return h.save(run, state, "", nil, audit)
}

func (h *AgentHost) operation(state *agentcontract.RuntimeState, item *agentcontract.Invocation, fact agentcontract.Fact, binding agentcontract.OperationBinding) error {
	status, e := reactcore.OperationValue(fact.Value["data"], binding.StatusPath)
	if e != nil {
		return e
	}
	id, e := reactcore.OperationValue(fact.Value["data"], binding.IDPath)
	if e != nil {
		return e
	}
	s, ok := status.(string)
	if !ok {
		return agentcontract.NewHostError("operation_contract_invalid")
	}
	var operationID string
	switch v := id.(type) {
	case string:
		operationID = v
	case json.Number:
		if _, e := v.Int64(); e != nil {
			return agentcontract.NewHostError("operation_contract_invalid")
		}
		operationID = v.String()
	case int:
		operationID = fmt.Sprint(v)
	case float64:
		if v != float64(int64(v)) {
			return agentcontract.NewHostError("operation_contract_invalid")
		}
		operationID = fmt.Sprintf("%.0f", v)
	default:
		return agentcontract.NewHostError("operation_contract_invalid")
	}
	if item.Operation != nil && item.Operation.OperationID != operationID {
		return agentcontract.NewHostError("operation_identity_changed")
	}
	switch {
	case agentcontract.ContainsString(binding.PendingStates, s):
		if item.Operation == nil {
			args := agentcontract.JSON{}
			cursor := args
			if len(binding.PollArgument) == 0 {
				return agentcontract.NewHostError("operation_contract_invalid")
			}
			for _, key := range binding.PollArgument[:len(binding.PollArgument)-1] {
				child := agentcontract.JSON{}
				cursor[key] = child
				cursor = child
			}
			cursor[binding.PollArgument[len(binding.PollArgument)-1]] = id
			item.Operation = &agentcontract.OperationReceipt{OperationID: operationID, Binding: binding, PollArguments: args, Deadline: h.Now() + binding.TimeoutSeconds}
		}
		item.Operation.NextPollAt = h.Now() + binding.IntervalSeconds
		item.Status = "waiting"
	case agentcontract.ContainsString(binding.SuccessStates, s):
		item.Status = "succeeded"
	case agentcontract.ContainsString(binding.FailureStates, s):
		item.Status = "failed"
		item.ErrorCode = agentcontract.Strptr("operation_failed")
		reactcore.Reject(state, item.Call.CallRef, item.Call.Capability, "operation_failed", nil, fact.FactID)
	case agentcontract.ContainsString(binding.ReconciliationStates, s):
		if item.Operation == nil {
			args := agentcontract.JSON{}
			cursor := args
			if len(binding.PollArgument) == 0 {
				return agentcontract.NewHostError("operation_contract_invalid")
			}
			for _, key := range binding.PollArgument[:len(binding.PollArgument)-1] {
				child := agentcontract.JSON{}
				cursor[key] = child
				cursor = child
			}
			cursor[binding.PollArgument[len(binding.PollArgument)-1]] = id
			item.Operation = &agentcontract.OperationReceipt{OperationID: operationID, Binding: binding, PollArguments: args, Deadline: h.Now() + binding.TimeoutSeconds}
		}
		item.Status = "unknown"
		item.ErrorCode = agentcontract.Strptr("operation_outcome_unknown")
		state.Status = "needs_reconciliation"
		state.ErrorCode = item.ErrorCode
	default:
		return agentcontract.NewHostError("operation_state_unknown")
	}
	return nil
}

func (h *AgentHost) poll(ctx context.Context, run agentcontract.StoredRun, state *agentcontract.RuntimeState, item *agentcontract.Invocation, provider agentcontract.CapabilityProvider, runtime *reactcore.AgentRuntime) (agentcontract.StoredRun, error) {
	receipt := item.Operation
	var prior *agentcontract.Fact
	if item.FactID != nil {
		for i := range state.Facts {
			if state.Facts[i].FactID == *item.FactID {
				prior = &state.Facts[i]
				break
			}
		}
	}
	if prior == nil || !reactcore.ReferenceAvailable(*prior, runtime.ConnectionID) {
		state.Status = "needs_reconciliation"
		state.ErrorCode = agentcontract.Strptr("operation_reference_unavailable")
		return h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
	}
	expired := h.Now() >= receipt.Deadline
	if expired && !receipt.Binding.ReconcileOnTimeout {
		item.Status = "failed"
		item.ErrorCode = agentcontract.Strptr("operation_deadline_exceeded")
		reactcore.Reject(state, item.Call.CallRef, item.Call.Capability, "operation_deadline_exceeded", nil, "")
		return h.save(run, state, "", nil, map[string]any{"kind": "operation_timed_out"})
	}
	if !expired && h.Now() < receipt.NextPollAt && !item.PollInFlight {
		return run, nil
	}
	if state.PollCallsUsed >= h.runSettings(run).MaxPollCalls && !item.PollInFlight {
		item.Status = "unknown"
		item.ErrorCode = agentcontract.Strptr("operation_outcome_unknown")
		state.Status = "needs_reconciliation"
		state.ErrorCode = item.ErrorCode
		return h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
	}
	policy, e := h.ProjectPolicy(ctx, run)
	if e != nil {
		return run, e
	}
	if e = pollAccess(provider, policy, receipt.Binding); e != nil {
		return run, e
	}
	if !item.PollInFlight {
		receipt.Polls++
		state.PollCallsUsed++
	}
	item.PollInFlight = true
	call := agentcontract.ToolCall{CallRef: fmt.Sprintf("poll-%s-%d", item.InvocationID, receipt.Polls), Capability: receipt.Binding.PollCapability, Arguments: receipt.PollArguments, Reason: "Read persisted operation status"}
	inv := invocationContext(run, item, runtime.ConnectionID)
	IdentifyInvocation(&inv, run, provider, call.Capability, policy)
	inv.InvocationID = fmt.Sprintf("%s:poll:%d", item.InvocationID, receipt.Polls)
	inv.IdempotencyKey = inv.InvocationID
	audit := invocationAudit("operation_poll_started", inv)
	audit["poll"] = receipt.Polls
	run, e = h.save(run, state, "", nil, audit)
	if e != nil {
		return run, e
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(h.runSettings(run).InvocationTimeoutSeconds*1e9))
	outcome, e := reactcore.ExecuteCall(callCtx, provider, policy.GrantedCapabilities, call, &inv)
	cancel()
	if ctx.Err() != nil && outcome.Fact == nil {
		return run, ctx.Err()
	}
	if e != nil {
		outcome = agentcontract.CallOutcome{ErrorCode: "provider_outcome_unknown"}
	}
	item.PollInFlight = false
	if definiteAuth(outcome.ErrorCode) {
		state.Status = "needs_authorization"
		state.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
	} else if outcome.Fact == nil {
		receipt.NextPollAt = h.Now() + receipt.Binding.IntervalSeconds
		item.ErrorCode = agentcontract.Strptr(outcome.ErrorCode)
	} else {

		// Check the persisted operation before replacing any of its evidence.
		if e = h.operation(state, item, *outcome.Fact, receipt.Binding); e != nil {
			item.Status, item.ErrorCode = "unknown", agentcontract.Strptr(agentcontract.ErrorCode(e))
			state.Status, state.ErrorCode = "needs_reconciliation", item.ErrorCode
			return h.save(run, state, "", nil, agentcontract.JSON{"kind": "reconciliation_required", "invocation_id": item.InvocationID, "code": agentcontract.ErrorCode(e)})
		}
		if item.Status == "succeeded" || item.Status == "waiting" {
			item.ErrorCode = nil
		}
		b, e := agentcontract.CanonicalJSON(outcome.Fact)
		if e != nil {
			return run, e
		}
		cap := provider.Capabilities()[item.Call.Capability]
		reactcore.CaptureInvocationReceipt(item, cap, outcome, b)
		if len(b) > h.runSettings(run).MaxArtifactBytes {
			return h.failOperationResultStorage(run, state, item, "result_too_large")
		}
		size := len(b)
		facts := []agentcontract.Fact{}
		for _, f := range state.Facts {
			if f.FactID != *item.FactID {
				b, err := agentcontract.CanonicalJSON(f)
				if err != nil {
					return run, agentcontract.NewHostError("run_state_invalid")
				}
				size += len(b)
				facts = append(facts, f)
			}
		}
		if size > h.runSettings(run).MaxActiveArtifactBytes {
			return h.failOperationResultStorage(run, state, item, "run_artifacts_too_large")
		}
		state.Facts = append(facts, *outcome.Fact)
		item.FactID = agentcontract.Strptr(outcome.Fact.FactID)
		observation := agentcontract.Observation{CallRef: call.CallRef, Capability: call.Capability, Status: "succeeded", FactID: item.FactID, Arguments: call.Arguments}
		prefix := "poll-" + item.InvocationID + "-"
		filter := func(items []agentcontract.Observation, latest agentcontract.Observation) []agentcontract.Observation {
			result := []agentcontract.Observation{}
			for _, o := range items {
				if !strings.HasPrefix(o.CallRef, prefix) {
					result = append(result, o)
				}
			}
			return append(result, latest)
		}
		state.Observations = filter(state.Observations, observation)

		// Poll arguments are generated from the full result and can contain
		// fields deliberately excluded by model_output.
		observation.Arguments, observation.ArgumentsOmitted = agentcontract.JSON{}, true
		state.ModelObservations = filter(state.ModelObservations, observation)
	}
	if expired && state.Status == "running" && item.Status == "waiting" {
		item.Status = "unknown"
		item.ErrorCode = agentcontract.Strptr("operation_outcome_unknown")
		state.Status = "needs_reconciliation"
		state.ErrorCode = item.ErrorCode
		return h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "operation_polled", "invocation_id": item.InvocationID, "poll": receipt.Polls, "fact_id": item.FactID, "error_code": agentcontract.Strptr(outcome.ErrorCode)})
}

func (h *AgentHost) work(ctx context.Context, run agentcontract.StoredRun) (result agentcontract.StoredRun, err error) {
	claimedToken := run.LeaseToken
	state, e := h.Restore(run)
	if e != nil {
		return run, e
	}
	defer func() {
		if err == nil || errors.Is(err, runstore.ErrLeaseLost) || ctx.Err() != nil {
			return
		}
		code := "connection_or_execution_failed"
		var he *agentcontract.HostError
		if errors.As(err, &he) {
			code = he.Code
		}
		var deployment *agentcontract.DeploymentError
		if errors.As(err, &deployment) {
			code = deployment.Code
		}
		if code == "run_state_too_large" {
			if latest, e := h.Store.GetRun(run.RunID, run.OwnerID); e == nil {
				if latest.LeaseToken != claimedToken {
					result, err = latest, runstore.ErrLeaseLost
					return
				}
				if restored, e := h.Restore(latest); e == nil {
					run = latest
					run.LeaseToken = claimedToken
					state = restored
				}
			}
		}
		state.Status = "failed"
		if pauseAuth(code) {
			state.Status = "needs_authorization"
		}
		for i := range state.Pending {
			item := &state.Pending[i]
			if unsettledInvocation(*item) {
				if state.Status != "needs_authorization" {
					state.Status = "needs_reconciliation"
				}
				item.Status, item.PollInFlight = "unknown", false
			}
		}
		state.ErrorCode = agentcontract.Strptr(code)
		saved, e := h.save(run, state, "", nil, map[string]any{"kind": "run_blocked", "code": code})
		if e != nil {
			result = run
			err = e
			return
		}
		result = saved
		if !pauseAuth(code) {
			err = nil
		}
	}()
	if run.CancelRequested {
		return h.markCancelled(run, state)
	}
	if Terminal(state.Status) || state.Status == "needs_input" || state.Status == "needs_reconciliation" {
		return run, nil
	}
	if state.Status == "waiting" && run.NextWakeAt != nil && h.Now() < *run.NextWakeAt {
		return run, nil
	}
	policy, e := h.ProjectPolicy(ctx, run)
	if e != nil {
		return run, e
	}
	config, configErr := h.effectiveRunConfig(run)
	if configErr != nil {
		return run, configErr
	}
	runModel, modelErr := h.modelForRun(run)
	if modelErr != nil {
		return run, modelErr
	}
	currentModel := agentcontract.DescribeModel(runModel)
	if config.Source == "run_snapshot" && config.Model.Name != "" && config.Model.Name != currentModel.Name {
		return run, agentcontract.NewHostError("model_changed")
	}
	provider, e := h.OpenRunProvider(ctx, run)
	if e != nil {
		return run, e
	}
	defer func() {

		// Closing the connection does not change an already observed business outcome.
		if err := provider.Close(); err != nil {
			log.Print("provider cleanup failed")
		}
	}()
	fp := Fingerprint(provider)
	if fp == "" {
		return run, agentcontract.NewHostError("pack_changed")
	}
	if previous, ok := run.State["pack_fingerprint"].(string); ok && previous != fp {
		return run, agentcontract.NewHostError("pack_changed")
	}
	runtime := &reactcore.AgentRuntime{Provider: provider, Model: runModel, Grants: policy.GrantedCapabilities, OriginPackID: run.PackID, ConnectionID: agentcontract.NewID(), Durable: true, MaxModelRounds: h.runSettings(run).MaxModelRounds, MaxToolCalls: h.runSettings(run).MaxToolCalls, MaxRepeatedCall: 2, MaxContextCharacters: h.runSettings(run).MaxContextCharacters}
	runtime.CompletionValidator = h.CompletionValidator
	runtime.ContextPolicy = h.runSettings(run).ContextPolicy
	runtime.ModelContextWindowTokens = h.runSettings(run).ModelContextWindowTokens
	runtime.MaxModelInputTokens = h.runSettings(run).MaxModelInputTokens
	runtime.ModelOutputReserveTokens = h.runSettings(run).ModelOutputReserveTokens
	runtime.ModelProtocolReserveTokens = h.runSettings(run).ModelProtocolReserveTokens
	runtime.MaxModelTokens = h.runSettings(run).MaxModelTokens
	runtime.MaxModelOutputTokens = h.runSettings(run).MaxModelOutputTokens
	runtime.MaxStagnantRounds = h.runSettings(run).MaxStagnantRounds
	runtime.MaxConcurrentTools = h.runSettings(run).MaxConcurrentTools
	runtime.MaxContextCapabilities = h.runSettings(run).MaxContextCapabilities
	run, e = h.prepareMemories(ctx, run)
	if e != nil {
		return run, e
	}
	state, e = h.Restore(run)
	if e != nil {
		return run, e
	}
	state.Status = "running"
	state.ErrorCode = nil
	run, e = h.save(run, state, fp, nil, map[string]any{"kind": "run_resumed"})
	if e != nil {
		return run, e
	}
	for state.Status == "running" {
		if e = ctx.Err(); e != nil {
			return run, e
		}
		latest, e := h.Store.GetRun(run.RunID, run.OwnerID)
		if e != nil {
			return run, e
		}
		if latest.CancelRequested {
			return h.markCancelled(run, state)
		}
		if h.Now()-run.CreatedAt >= h.runSettings(run).MaxRunSeconds {
			state.Status = "failed"
			state.ErrorCode = agentcontract.Strptr("run_deadline_exceeded")
			for i := range state.Pending {
				item := &state.Pending[i]
				if unsettledInvocation(*item) {
					item.Status, item.PollInFlight = "unknown", false
				}
			}
			break
		}
		run, _, e = h.applySteering(run, state)
		if e == nil {
			run, e = h.applyContextPolicy(run, state)
		}
		if e != nil {
			return run, e
		}
		if len(state.Pending) > 0 {
			parallel, err := h.parallelBatch(ctx, run, state, provider)
			if err != nil {
				return run, err
			}
			if parallel {
				run, e = h.executeParallel(ctx, run, state, provider, runtime)
				if e != nil || state.Status != "running" {
					return run, e
				}
				continue
			}
			steered := false
			for i := range state.Pending {
				item := &state.Pending[i]
				if item.Status == "succeeded" || item.Status == "failed" {
					continue
				}
				if item.Status == "waiting" && item.Operation != nil {
					run, e = h.poll(ctx, run, state, item, provider, runtime)
				} else {
					run, e = h.execute(ctx, run, state, item, provider, runtime)
				}
				if e != nil {
					return run, e
				}
				if state.Status != "running" {
					return run, nil
				}
				run, steered, e = h.applySteering(run, state)
				if e != nil {
					return run, e
				}
				if steered {
					break
				}
			}
			if steered {
				continue
			}
			var wake *float64
			for _, item := range state.Pending {
				if item.Status == "waiting" && item.Operation != nil {
					v := item.Operation.NextPollAt
					if wake == nil || v < *wake {
						wake = &v
					}
				}
			}
			if wake != nil {
				state.Status = "waiting"
				return h.save(run, state, "", wake, map[string]any{"kind": "run_waiting"})
			}
			state.Pending = []agentcontract.Invocation{}
			run, e = h.save(run, state, "", nil, nil)
			if e != nil {
				return run, e
			}
			continue
		}
		policy, e = h.ProjectPolicy(ctx, run)
		if e != nil {
			return run, e
		}
		runtime.Grants = policy.GrantedCapabilities
		runtime.Memories, e = h.runMemories(run)
		if e != nil {
			return run, e
		}
		visible := []agentcontract.MemoryView{}
		for _, m := range runtime.Memories {
			allowed := m.PackID == "" || m.PackID == run.PackID
			if !allowed {
				for name, grant := range policy.GrantedCapabilities {
					if grant && strings.HasPrefix(name, m.PackID+"::") {
						allowed = true
						break
					}
				}
			}
			if allowed {
				visible = append(visible, m)
			}
		}
		runtime.Memories = visible
		before := func() error {
			var e error
			run, e = h.save(run, state, "", nil, map[string]any{"kind": "model_requested", "round": state.RoundsUsed})
			return e
		}
		modelCtx, cancel := context.WithTimeout(ctx, time.Duration(h.runSettings(run).ModelTimeoutSeconds*1e9))
		modelCtx = agentcontract.WithModelRequestObserver(modelCtx, func(progress agentcontract.ModelRequestProgress) error {
			var err error
			run, err = h.save(run, state, "", nil, agentcontract.JSON{"kind": progress.Kind, "attempt": progress.Attempt, "error_code": progress.ErrorCode, "retry_at": progress.RetryAt})
			return err
		})
		e = runtime.Step(modelCtx, state, before)
		expired := errors.Is(modelCtx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if expired {
			state.Status = "failed"
			state.ErrorCode = agentcontract.Strptr("model_timeout")
		} else if e != nil {
			return run, e
		}
		event := map[string]any{"kind": "model_decided", "round": state.RoundsUsed}
		if len(state.ModelCalls) > 0 {
			event["metrics"] = state.ModelCalls[len(state.ModelCalls)-1]
		}
		if len(state.Decisions) > 0 {
			event["decision_kind"] = state.Decisions[len(state.Decisions)-1]["kind"]
		}

		// Enqueue and completion are serialized by the store. A steering request
		// accepted while the model finishes must reach the next decision.
		for {
			run, _, e = h.applySteering(run, state)
			if e == nil {
				run, e = h.applyContextPolicy(run, state)
			}
			if e != nil {
				return run, e
			}
			run, e = h.save(run, state, "", nil, event)
			if !errors.Is(e, runstore.ErrSteeringPending) {
				break
			}
		}
		if e != nil {
			return run, e
		}
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "run_stopped", "status": state.Status})
}

func (h *AgentHost) markCancelled(run agentcontract.StoredRun, state *agentcontract.RuntimeState) (agentcontract.StoredRun, error) {
	state.Status = "cancelled"
	state.ErrorCode = agentcontract.Strptr("cancel_requested")
	for i := range state.Pending {
		if unsettledInvocation(state.Pending[i]) {
			state.Pending[i].Status = "unknown"
			state.Pending[i].ErrorCode = agentcontract.Strptr("provider_outcome_unknown")
			if state.Pending[i].Receipt != nil {
				if state.Pending[i].Receipt.Status != "accepted" {
					state.Pending[i].Receipt.Status = "unknown"
				}
				state.Pending[i].Receipt.ErrorCode = "provider_outcome_unknown"
			}
		}
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "run_cancelled"})
}

// Drive claims a run, renews its lease and waits for execution workers to stop before release.
func (h *AgentHost) Drive(ctx context.Context, id, owner string) (agentcontract.StoredRun, error) {
	h.InitializeDefaults()
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return agentcontract.StoredRun{}, ctx.Err()
	}
	defer func() { <-h.slots }()
	run, e := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Duration(h.Settings.LeaseSeconds / 3 * 1e9))
		defer ticker.Stop()

		// Cancellation is persisted independently of the lease. Check it
		// promptly without increasing the frequency of lease-renewal writes.
		cancelTicker := time.NewTicker(time.Second)
		defer cancelTicker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if e := h.Store.Renew(id, owner, run.LeaseToken, h.Settings.LeaseSeconds); e != nil {
					heartbeatErr <- e
					cancel()
					return
				}
			case <-cancelTicker.C:
				current, e := h.Store.GetRun(id, owner)
				if e != nil {
					heartbeatErr <- e
					cancel()
					return
				}
				if current.CancelRequested {
					cancel()
					return
				}
			}
		}
	}()
	result, e := h.work(workCtx, run)
	cancel()
	<-done
	select {
	case <-heartbeatErr:
		e = runstore.ErrLeaseLost
	default:
		if workCtx.Err() != nil {
			if current, err := h.Store.GetRun(id, owner); err == nil && current.CancelRequested {
				if current.LeaseToken != run.LeaseToken {
					e = runstore.ErrLeaseLost
				} else if state, err := h.Restore(current); err == nil {
					current.LeaseToken = run.LeaseToken
					result, e = h.markCancelled(current, state)
				}
			}
		}
	}
	releaseErr := h.Store.Release(id, owner, run.LeaseToken)
	if e == nil && releaseErr != nil && !errors.Is(releaseErr, runstore.ErrLeaseLost) {
		e = releaseErr
	}
	return result, e
}

// WakeDue drives a bounded set of due runs and waits for all started workers.
func (h *AgentHost) WakeDue(ctx context.Context, limit int) (int, error) {
	runs, e := h.Store.DueRuns(min(limit, h.Settings.MaxConcurrentRuns))
	if e != nil {
		return 0, e
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, run := range runs {
		wg.Go(func() {
			_, err := h.Drive(ctx, run.RunID, run.OwnerID)
			if err != nil && !errors.Is(err, runstore.ErrStoreConflict) && !errors.Is(err, runstore.ErrLeaseLost) {
				var he *agentcontract.HostError
				if !errors.As(err, &he) {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	return len(runs), firstErr
}

// runModelSelector freezes a model selection for a run without coupling the host
// to deployment configuration, credentials, or a concrete model adapter.
type runModelSelector interface {
	Snapshot() agentcontract.ModelSelectionSnapshot
	SelectModel(agentcontract.ModelConfiguration) (agentcontract.DecisionModel, error)
}
