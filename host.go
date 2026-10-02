package agenstra

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type HostError struct{ Code string }

func (e *HostError) Error() string { return e.Code }
func hostError(code string) error  { return &HostError{Code: code} }

type ExecutionPolicy struct {
	GrantedCapabilities  map[string]bool     `json:"granted_capabilities"`
	ApprovalCapabilities map[string]bool     `json:"approval_capabilities"`
	AllowModelData       bool                `json:"allow_model_data"`
	Subject              string              `json:"subject,omitempty"`
	PermissionsVerified  bool                `json:"permissions_verified,omitempty"`
	Delegations          map[string][]string `json:"delegations,omitempty"`
}
type HostSettings struct {
	MaxConversationHistoryMessages   int           `json:"max_conversation_history_messages,omitempty"`
	MaxConversationHistoryCharacters int           `json:"max_conversation_history_characters,omitempty"`
	MaxConversationMessages          int           `json:"max_conversation_messages,omitempty"`
	ContextPolicy                    ContextPolicy `json:"context_policy"`
	ModelContextWindowTokens         int64         `json:"model_context_window_tokens,omitempty"`
	MaxModelInputTokens              int64         `json:"max_model_input_tokens,omitempty"`
	ModelOutputReserveTokens         int           `json:"model_output_reserve_tokens,omitempty"`
	ModelProtocolReserveTokens       int64         `json:"model_protocol_reserve_tokens,omitempty"`
	LeaseSeconds                     float64       `json:"lease_seconds"`
	MaxModelRounds                   int           `json:"max_model_rounds"`
	MaxToolCalls                     int           `json:"max_tool_calls"`
	MaxPollCalls                     int           `json:"max_poll_calls"`
	MaxRunSeconds                    float64       `json:"max_run_seconds"`
	MaxContextCharacters             int           `json:"max_context_characters"`
	MaxArtifactBytes                 int           `json:"max_artifact_bytes"`
	MaxActiveArtifactBytes           int           `json:"max_active_artifact_bytes"`
	MaxStateBytes                    int           `json:"max_state_bytes"`
	ModelTimeoutSeconds              float64       `json:"model_timeout_seconds"`
	InvocationTimeoutSeconds         float64       `json:"invocation_timeout_seconds"`
	MaxInvocationAttempts            int           `json:"max_invocation_attempts"`
	RetryIntervalSeconds             float64       `json:"retry_interval_seconds"`
	ApprovalSeconds                  float64       `json:"approval_seconds"`
	MaxConcurrentRuns                int           `json:"max_concurrent_runs"`
	MaxModelTokens                   int64         `json:"max_model_tokens,omitempty"`
	MaxModelOutputTokens             int           `json:"max_model_output_tokens,omitempty"`
	ModelTokenLimitField             string        `json:"model_token_limit_field,omitempty"`
	MaxStagnantRounds                int           `json:"max_stagnant_rounds,omitempty"`
	MaxConcurrentTools               int           `json:"max_concurrent_tools,omitempty"`
}

func DefaultHostSettings() HostSettings {
	return HostSettings{MaxConversationHistoryMessages: 6, MaxConversationHistoryCharacters: 1500, MaxConversationMessages: 500, LeaseSeconds: 60, MaxModelRounds: 30, MaxToolCalls: 80, MaxPollCalls: 720, MaxRunSeconds: 86400, MaxContextCharacters: 80000, MaxArtifactBytes: 8000000, MaxActiveArtifactBytes: 64000000, MaxStateBytes: 8000000, ModelTimeoutSeconds: 60, InvocationTimeoutSeconds: 300, MaxInvocationAttempts: 3, RetryIntervalSeconds: 5, ApprovalSeconds: 900, MaxConcurrentRuns: 4, MaxStagnantRounds: 8, MaxConcurrentTools: 4}
}
func (s HostSettings) Validate() error {
	if s.MaxConversationHistoryMessages < 0 || s.MaxConversationHistoryMessages > 100 || s.MaxConversationHistoryCharacters < 0 || s.MaxConversationHistoryCharacters > 12000 || s.MaxConversationMessages < 0 || s.MaxConversationMessages > 500 {
		return errors.New("host_settings_invalid")
	}
	if err := s.ContextPolicy.Validate(); err != nil {
		return err
	}
	if s.ModelContextWindowTokens < 0 || s.ModelContextWindowTokens > 100000000 || s.MaxModelInputTokens < 0 || s.MaxModelInputTokens > 100000000 || s.ModelOutputReserveTokens < 0 || s.ModelOutputReserveTokens > 1000000 || s.ModelProtocolReserveTokens < 0 || s.ModelProtocolReserveTokens > 1000000 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxConcurrentTools < 0 || s.MaxConcurrentTools > 4 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxStagnantRounds < 0 || s.MaxStagnantRounds > 1000 {
		return errors.New("host_settings_invalid")
	}
	if s.MaxModelTokens < 0 || s.MaxModelTokens > 100000000 || s.MaxModelOutputTokens < 0 || s.MaxModelOutputTokens > 1000000 || (s.ModelTokenLimitField != "" && s.ModelTokenLimitField != "max_tokens" && s.ModelTokenLimitField != "max_completion_tokens") {
		return errors.New("host_settings_invalid")
	}
	if s.LeaseSeconds < 3 || s.LeaseSeconds > 3600 || s.MaxModelRounds < 1 || s.MaxModelRounds > 1000 || s.MaxToolCalls < 1 || s.MaxToolCalls > 10000 || s.MaxPollCalls < 1 || s.MaxPollCalls > 100000 || s.MaxRunSeconds <= 0 || s.MaxContextCharacters < 1000 || s.MaxArtifactBytes < 1024 || s.MaxActiveArtifactBytes < 1024 || s.MaxStateBytes < 1024 || s.ModelTimeoutSeconds <= 0 || s.InvocationTimeoutSeconds <= 0 || s.MaxInvocationAttempts < 1 || s.MaxInvocationAttempts > 10 || s.RetryIntervalSeconds < 1 || s.ApprovalSeconds < 1 || s.ApprovalSeconds > 86400 || s.MaxConcurrentRuns < 1 || s.MaxConcurrentRuns > 64 {
		return errors.New("host_settings_invalid")
	}
	return nil
}

type ProviderFactory func(context.Context, string, string) (CapabilityProvider, error)
type PolicyResolver func(context.Context, string, string) (ExecutionPolicy, error)
type ReleaseResolver func(context.Context, string, string) (string, error)
type ReleaseProviderFactory func(context.Context, string, string, string) (CapabilityProvider, error)
type AgentHost struct {
	Store                  *SQLiteStore
	ProviderFactory        ProviderFactory
	Model                  DecisionModel
	CompletionValidator    CompletionValidator
	Reconciler             InvocationReconciler
	PolicyResolver         PolicyResolver
	ReleaseResolver        ReleaseResolver
	ReleaseProviderFactory ReleaseProviderFactory
	Settings               HostSettings
	Clock                  func() float64
	once                   sync.Once
	slots                  chan struct{}
}

func NewAgentHost(store *SQLiteStore, factory ProviderFactory, model DecisionModel, policy PolicyResolver) *AgentHost {
	return &AgentHost{Store: store, ProviderFactory: factory, Model: model, PolicyResolver: policy, Settings: DefaultHostSettings(), Clock: unixNow}
}
func (h *AgentHost) InitializeDefaults() {
	h.once.Do(func() {
		if h.Settings == (HostSettings{}) {
			h.Settings = DefaultHostSettings()
		}
		h.slots = make(chan struct{}, max(1, h.Settings.MaxConcurrentRuns))
	})
}
func (h *AgentHost) now() float64 {
	if h.Clock != nil {
		return h.Clock()
	}
	return unixNow()
}
func (h *AgentHost) policy(ctx context.Context, owner, pack string, modelData bool) (ExecutionPolicy, error) {
	p, err := h.PolicyResolver(ctx, owner, pack)
	if err != nil {
		var e *HostError
		if errors.As(err, &e) {
			return p, e
		}
		var deployment *DeploymentError
		if errors.As(err, &deployment) {
			return p, hostError(deployment.Code)
		}
		return p, hostError("authorization_unavailable")
	}
	if modelData && !p.AllowModelData {
		return p, hostError("model_data_not_authorized")
	}
	return p, nil
}
func terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}
func objectOf(v any) (map[string]any, error) {
	b, e := CanonicalJSON(v)
	if e != nil {
		return nil, e
	}
	return decodeObject(string(b))
}
func requestRunID(owner, request string) string {
	if request == "" {
		return NewID()
	}
	b, _ := CanonicalJSON([]string{owner, request})
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
		return nil, hostError("instruction_invalid")
	}
	if _, e := h.policy(ctx, owner, pack, true); e != nil {
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
	state, e := NewState(instruction, id)
	if e != nil {
		return nil, e
	}
	runtime, e := objectOf(state)
	if e != nil {
		return nil, e
	}
	delete(runtime, "facts")
	settings := normalizedRunSettings(h.Settings)
	info := modelInfo(h.Model)
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
	envelope := map[string]any{"runtime": runtime, "artifact_ids": []string{}, "pack_fingerprint": nil, "pack_release": release, "effective_config": EffectiveRunConfig{Version: 1, Source: "run_snapshot", Settings: settings, Model: modelInfo(h.Model)}}
	setMemoryInput(envelope, id+":instruction", instruction)
	raw, err := CanonicalJSON(envelope)
	if err != nil {
		return nil, err
	}
	if len(raw) > settings.MaxStateBytes {
		return nil, hostError("run_state_too_large")
	}
	return envelope, nil
}
func (h *AgentHost) Create(ctx context.Context, owner, pack, instruction, requestID string) (StoredRun, error) {
	return h.createWithMemoryInput(ctx, owner, pack, instruction, requestID, instruction)
}
func (h *AgentHost) createWithMemoryInput(ctx context.Context, owner, pack, instruction, requestID, input string) (StoredRun, error) {
	return h.createWithSources(ctx, owner, pack, instruction, requestID, input, nil)
}
func (h *AgentHost) Get(ctx context.Context, id, owner string) (StoredRun, error) {
	r, e := h.Store.GetRun(id, owner)
	if e != nil {
		return r, e
	}
	_, e = h.policy(ctx, owner, r.PackID, false)
	return r, e
}
func (h *AgentHost) restore(run StoredRun) (*RuntimeState, error) {
	if _, err := h.effectiveRunConfig(run); err != nil {
		return nil, err
	}
	runtime, ok := run.State["runtime"].(map[string]any)
	if !ok {
		return nil, hostError("run_state_invalid")
	}
	copy, e := objectOf(runtime)
	if e != nil {
		return nil, e
	}
	facts := []any{}
	ids, ok := run.State["artifact_ids"].([]any)
	if !ok {
		return nil, hostError("run_state_invalid")
	}
	for _, v := range ids {
		id, ok := v.(string)
		if !ok {
			return nil, hostError("run_state_invalid")
		}
		f, e := h.Store.GetArtifact(run.RunID, id, run.OwnerID)
		if e != nil {
			return nil, e
		}
		facts = append(facts, f)
	}
	copy["facts"] = facts
	b, e := CanonicalJSON(copy)
	if e != nil {
		return nil, e
	}
	var state RuntimeState
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e = d.Decode(&state); e != nil || state.SchemaVersion != 1 || state.RunID != run.RunID {
		return nil, hostError("run_state_invalid")
	}
	return &state, nil
}
func (h *AgentHost) save(run StoredRun, state *RuntimeState, fingerprint string, wake *float64, event map[string]any) (StoredRun, error) {
	h.recordExecution(state, event)
	runtime, e := objectOf(state)
	if e != nil {
		return run, e
	}
	delete(runtime, "facts")
	ids := []string{}
	artifacts := map[string]map[string]any{}
	size := 0
	for _, fact := range state.Facts {
		ids = append(ids, fact.FactID)
		b, e := CanonicalJSON(fact)
		if e != nil {
			return run, e
		}
		size += len(b)
		if _, e = h.Store.GetArtifact(run.RunID, fact.FactID, run.OwnerID); errors.Is(e, ErrRunNotFound) {
			a, e := decodeObject(string(b))
			if e != nil {
				return run, e
			}
			artifacts[fact.FactID] = a
		} else if e != nil {
			return run, e
		}
	}
	if size > h.runSettings(run).MaxActiveArtifactBytes {
		return run, hostError("run_artifacts_too_large")
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
	b, e := CanonicalJSON(envelope)
	if e != nil {
		return run, e
	}
	if len(b) > h.runSettings(run).MaxStateBytes {
		return run, hostError("run_state_too_large")
	}
	invs := []map[string]any{}
	for _, item := range state.Pending {
		v, e := objectOf(item)
		if e != nil {
			return run, e
		}
		invs = append(invs, v)
	}
	events := []map[string]any{}
	if event != nil {
		if progress := runProgress(state, h.runSettings(run).MaxStagnantRounds); progress != nil {
			event["progress"] = progress
		}
		event["status"] = state.Status
		if strings.HasPrefix(fmt.Sprint(event["kind"]), "model_") || strings.HasPrefix(fmt.Sprint(event["kind"]), "memory_") {
			view := run
			view.State = envelope
			view.Status = state.Status
			view.Revision++
			if telemetry, err := h.telemetry(view); err == nil {
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
func (h *AgentHost) SupplyInput(ctx context.Context, id, owner, field, text string, revision int) (StoredRun, error) {
	current, e := h.Get(ctx, id, owner)
	if e != nil {
		return current, e
	}
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 30000 {
		return current, hostError("input_invalid")
	}
	run, e := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	defer h.Store.Release(id, owner, run.LeaseToken)
	state, e := h.restore(run)
	if e != nil {
		return run, e
	}
	if run.Revision != revision || current.Revision != revision {
		return run, hostError("revision_conflict")
	}
	if state.Status != "needs_input" || state.InputField == nil || *state.InputField != field {
		return run, hostError("input_not_requested")
	}
	inputs := []memoryInput{}
	if raw, err := CanonicalJSON(run.State["memory_inputs"]); err == nil && run.State["memory_inputs"] != nil {
		if err := strictUnmarshal(raw, &inputs); err != nil {
			return run, hostError("run_state_invalid")
		}
	}
	run.State["memory_inputs"] = append(inputs, memoryInput{ID: fmt.Sprintf("%s:input:%d", id, revision), Text: text})
	state.Followups = append(state.Followups, field+": "+text)
	state.InputField = nil
	state.InputPrompt = nil
	state.Status = "queued"
	state.ErrorCode = nil
	return h.save(run, state, "", nil, map[string]any{"kind": "input_received", "field": field})
}
func (h *AgentHost) Approve(ctx context.Context, id, owner, invocationID, argsSHA string, revision int, approved bool) (StoredRun, error) {
	if _, e := h.Get(ctx, id, owner); e != nil {
		return StoredRun{}, e
	}
	run, e := h.Store.Claim(id, owner, h.Settings.LeaseSeconds)
	if e != nil {
		return run, e
	}
	defer h.Store.Release(id, owner, run.LeaseToken)
	state, e := h.restore(run)
	if e != nil {
		return run, e
	}
	if run.Revision != revision {
		return run, hostError("revision_conflict")
	}
	var item *Invocation
	for i := range state.Pending {
		if state.Pending[i].InvocationID == invocationID {
			item = &state.Pending[i]
			break
		}
	}
	if state.Status != "needs_approval" || item == nil || item.Status != "needs_approval" {
		return run, hostError("approval_not_requested")
	}
	if item.ArgumentsSHA256 != argsSHA {
		return run, hostError("approval_arguments_changed")
	}
	if item.ApprovalExpiresAt == nil || h.now() >= *item.ApprovalExpiresAt {
		return run, hostError("approval_expired")
	}
	kind := "approval_denied"
	if approved {
		policy, err := h.projectPolicy(ctx, run)
		if err != nil {
			return run, err
		}
		if !policy.GrantedCapabilities[item.Call.Capability] {
			return run, hostError("capability_not_granted")
		}
		item.ApprovedHash = &argsSHA
		item.ApprovedUntil = item.ApprovalExpiresAt
		item.Status = "prepared"
		kind = "approval_granted"
	} else {
		Observe(state, item, CallOutcome{ErrorCode: "approval_denied"})
	}
	state.Status = "queued"
	return h.save(run, state, "", nil, map[string]any{"kind": kind, "invocation_id": invocationID, "arguments_sha256": argsSHA})
}
func (h *AgentHost) Cancel(ctx context.Context, id, owner string) (StoredRun, error) {
	r, e := h.Get(ctx, id, owner)
	if e != nil || terminal(r.Status) {
		return r, e
	}
	return h.Store.RequestCancel(id, owner)
}
func fingerprint(provider CapabilityProvider) string {
	skills := map[string]string{}
	for name, s := range provider.Skills() {
		skills[name] = s.Content
	}
	binding := ""
	if p, ok := provider.(interface{ BindingID() string }); ok {
		binding = p.BindingID()
	}
	b, _ := CanonicalJSON(map[string]any{"capabilities": provider.Capabilities(), "skills": skills, "prompt": provider.SystemPrompt(), "binding": binding})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func invocationContext(run StoredRun, item *Invocation, connectionID string) InvocationContext {
	return InvocationContext{RunID: run.RunID, OwnerID: run.OwnerID, InvocationID: item.InvocationID, IdempotencyKey: item.InvocationID, ConnectionID: connectionID}
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
func unknownOutcome(code string) bool {
	return code == "provider_outcome_unknown" || code == "upstream_response_invalid" || code == "upstream_unavailable"
}
func pollAccess(provider CapabilityProvider, policy ExecutionPolicy, binding OperationBinding) error {
	cap, ok := provider.Capabilities()[binding.PollCapability]
	if !ok || cap.Effect != "read" {
		return hostError("operation_poll_must_be_read")
	}
	if !policy.GrantedCapabilities[cap.Name] {
		return hostError("capability_not_granted")
	}
	if cap.ApprovalRequired || policy.ApprovalCapabilities[cap.Name] {
		return hostError("operation_poll_requires_unattended_access")
	}
	return nil
}
func (h *AgentHost) execute(ctx context.Context, run StoredRun, state *RuntimeState, item *Invocation, provider CapabilityProvider, runtime *AgentRuntime) (StoredRun, error) {
	run, prepared, err := h.prepareInvocation(ctx, run, state, item, provider, runtime)
	if err != nil || prepared == nil {
		return run, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(h.runSettings(run).InvocationTimeoutSeconds*1e9))
	outcome, err := ExecuteCall(callCtx, provider, prepared.grants, item.Call, &prepared.inv)
	cancel()
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	if err != nil {
		outcome = CallOutcome{ErrorCode: "provider_outcome_unknown"}
	}
	return h.settleInvocation(run, state, item, *prepared, outcome)
}
func (h *AgentHost) prepareInvocation(ctx context.Context, run StoredRun, state *RuntimeState, item *Invocation, provider CapabilityProvider, runtime *AgentRuntime) (StoredRun, *preparedInvocation, error) {
	cap, ok := provider.Capabilities()[item.Call.Capability]
	policy, e := h.projectPolicy(ctx, run)
	if e != nil {
		return run, nil, e
	}
	if !ok {
		Observe(state, item, CallOutcome{ErrorCode: "capability_unknown"})
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
		state.ErrorCode = strptr("capability_not_granted")
		saved, err := h.save(run, state, "", nil, map[string]any{"kind": "call_denied", "invocation_id": item.InvocationID})
		return saved, nil, err
	}
	inv := invocationContext(run, item, runtime.ConnectionID)
	identifyInvocation(&inv, run, provider, item.Call.Capability, policy)
	facts := map[string]Fact{}
	for _, f := range state.Facts {
		facts[f.FactID] = f
	}
	resolved := JSON{}
	for key, value := range item.OriginalArguments {
		v, err := ResolveArgument(value, facts, runtime.ConnectionID, true)
		if err != nil {
			code := ErrorCode(err)
			if item.Attempts > 0 {
				item.Status = "unknown"
				item.ErrorCode = strptr(code)
				state.Status = "needs_reconciliation"
				state.ErrorCode = strptr(code)
			} else {
				Observe(state, item, CallOutcome{ErrorCode: code})
			}
			saved, err := h.save(run, state, "", nil, map[string]any{"kind": "reference_unavailable", "invocation_id": item.InvocationID})
			return saved, nil, err
		}
		resolved[key] = v
	}
	normalized := item.Call
	normalized.Arguments = resolved
	normalized, e = BindIdempotency(normalized, cap, inv)
	if e != nil {
		Observe(state, item, CallOutcome{ErrorCode: "capability_input_invalid"})
		saved, err := h.save(run, state, "", nil, nil)
		return saved, nil, err
	}
	digest := ArgumentsDigest(normalized)
	if item.ArgumentsSHA256 != "" && item.ArgumentsSHA256 != digest {
		return run, nil, hostError("invocation_arguments_changed")
	}
	item.Call = normalized
	item.ArgumentsSHA256 = digest
	if item.Status == "in_flight" || item.Status == "unknown" {
		if cap.Replay == "never" || item.Attempts >= h.runSettings(run).MaxInvocationAttempts {
			item.Status = "unknown"
			state.Status = "needs_reconciliation"
			state.ErrorCode = strptr("provider_outcome_unknown")
			saved, err := h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
			return saved, nil, err
		}
		item.Status = "prepared"
	}
	validApproval := item.ApprovedHash != nil && *item.ApprovedHash == digest && item.ApprovedUntil != nil && h.now() < *item.ApprovedUntil
	if (cap.ApprovalRequired || policy.ApprovalCapabilities[cap.Name]) && !validApproval {
		item.Status = "needs_approval"
		expires := h.now() + h.runSettings(run).ApprovalSeconds
		item.ApprovalExpiresAt = &expires
		item.ApprovedHash = nil
		item.ApprovedUntil = nil
		state.Status = "needs_approval"
		saved, err := h.save(run, state, "", nil, map[string]any{"kind": "approval_requested", "invocation_id": item.InvocationID, "arguments_sha256": digest})
		return saved, nil, err
	}
	item.Status = "in_flight"
	item.Attempts++
	audit := invocationAudit("call_started", inv)
	audit["capability"], audit["arguments_sha256"] = cap.Name, digest
	run, e = h.save(run, state, "", nil, audit)
	if e != nil {
		return run, nil, e
	}
	return run, &preparedInvocation{cap: cap, inv: inv, grants: policy.GrantedCapabilities}, nil
}
func (h *AgentHost) settleInvocation(run StoredRun, state *RuntimeState, item *Invocation, prepared preparedInvocation, outcome CallOutcome) (StoredRun, error) {
	cap, inv := prepared.cap, prepared.inv
	var e error
	if definiteAuth(outcome.ErrorCode) {
		item.Status = "prepared"
		item.ErrorCode = strptr(outcome.ErrorCode)
		state.Status = "needs_authorization"
		state.ErrorCode = strptr(outcome.ErrorCode)
		return h.save(run, state, "", nil, map[string]any{"kind": "authorization_required"})
	}
	if unknownOutcome(outcome.ErrorCode) && (cap.Effect != "read" || outcome.ErrorCode == "provider_outcome_unknown") {
		item.Status = "unknown"
		item.ErrorCode = strptr(outcome.ErrorCode)
		state.Status = "needs_reconciliation"
		var wake *float64
		if cap.Replay != "never" && item.Attempts < h.runSettings(run).MaxInvocationAttempts {
			state.Status = "waiting"
			v := h.now() + h.runSettings(run).RetryIntervalSeconds
			wake = &v
		}
		state.ErrorCode = strptr(outcome.ErrorCode)
		return h.save(run, state, "", wake, map[string]any{"kind": "call_outcome_unknown", "invocation_id": item.InvocationID})
	}
	if outcome.Fact != nil {
		b, e := CanonicalJSON(outcome.Fact)
		if e != nil {
			return run, e
		}
		code := ""
		if len(b) > h.runSettings(run).MaxArtifactBytes {
			code = "result_too_large"
		} else {
			size := len(b)
			for _, f := range state.Facts {
				b, _ := CanonicalJSON(f)
				size += len(b)
			}
			if size > h.runSettings(run).MaxActiveArtifactBytes {
				code = "run_artifacts_too_large"
			}
		}
		if code != "" {
			outcome = CallOutcome{ErrorCode: code}
			state.Status = "failed"
			state.ErrorCode = strptr(code)
		}
	}
	Observe(state, item, outcome)
	if outcome.Fact != nil && cap.Operation != nil {
		if e = h.operation(state, item, *outcome.Fact, *cap.Operation); e != nil {
			return run, e
		}
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
	audit["fact_id"], audit["error_code"] = factID, strptr(outcome.ErrorCode)
	return h.save(run, state, "", nil, audit)
}
func operationValue(value any, path []any) (any, error) {
	for _, p := range path {
		switch v := value.(type) {
		case map[string]any:
			key, ok := p.(string)
			if !ok {
				return nil, hostError("operation_contract_invalid")
			}
			var exists bool
			value, exists = v[key]
			if !exists {
				return nil, hostError("operation_contract_invalid")
			}
		case []any:
			var index int
			switch x := p.(type) {
			case int:
				index = x
			case float64:
				if x != float64(int(x)) {
					return nil, hostError("operation_contract_invalid")
				}
				index = int(x)
			case json.Number:
				i, e := x.Int64()
				if e != nil {
					return nil, hostError("operation_contract_invalid")
				}
				index = int(i)
			default:
				return nil, hostError("operation_contract_invalid")
			}
			if index < 0 || index >= len(v) {
				return nil, hostError("operation_contract_invalid")
			}
			value = v[index]
		default:
			return nil, hostError("operation_contract_invalid")
		}
	}
	return value, nil
}
func containsString(items []string, value string) bool {
	for _, v := range items {
		if v == value {
			return true
		}
	}
	return false
}
func (h *AgentHost) operation(state *RuntimeState, item *Invocation, fact Fact, binding OperationBinding) error {
	status, e := operationValue(fact.Value["data"], binding.StatusPath)
	if e != nil {
		return e
	}
	id, e := operationValue(fact.Value["data"], binding.IDPath)
	if e != nil {
		return e
	}
	s, ok := status.(string)
	if !ok {
		return hostError("operation_contract_invalid")
	}
	var operationID string
	switch v := id.(type) {
	case string:
		operationID = v
	case json.Number:
		if _, e := v.Int64(); e != nil {
			return hostError("operation_contract_invalid")
		}
		operationID = v.String()
	case int:
		operationID = fmt.Sprint(v)
	case float64:
		if v != float64(int64(v)) {
			return hostError("operation_contract_invalid")
		}
		operationID = fmt.Sprintf("%.0f", v)
	default:
		return hostError("operation_contract_invalid")
	}
	if item.Operation != nil && item.Operation.OperationID != operationID {
		return hostError("operation_identity_changed")
	}
	switch {
	case containsString(binding.PendingStates, s):
		if item.Operation == nil {
			args := JSON{}
			cursor := args
			if len(binding.PollArgument) == 0 {
				return hostError("operation_contract_invalid")
			}
			for _, key := range binding.PollArgument[:len(binding.PollArgument)-1] {
				child := JSON{}
				cursor[key] = child
				cursor = child
			}
			cursor[binding.PollArgument[len(binding.PollArgument)-1]] = id
			item.Operation = &OperationReceipt{OperationID: operationID, Binding: binding, PollArguments: args, Deadline: h.now() + binding.TimeoutSeconds}
		}
		item.Operation.NextPollAt = h.now() + binding.IntervalSeconds
		item.Status = "waiting"
	case containsString(binding.SuccessStates, s):
		item.Status = "succeeded"
	case containsString(binding.FailureStates, s):
		item.Status = "failed"
		item.ErrorCode = strptr("operation_failed")
		Reject(state, item.Call.CallRef, item.Call.Capability, "operation_failed", nil, "")
	case containsString(binding.ReconciliationStates, s):
		if item.Operation == nil {
			args := JSON{}
			cursor := args
			if len(binding.PollArgument) == 0 {
				return hostError("operation_contract_invalid")
			}
			for _, key := range binding.PollArgument[:len(binding.PollArgument)-1] {
				child := JSON{}
				cursor[key] = child
				cursor = child
			}
			cursor[binding.PollArgument[len(binding.PollArgument)-1]] = id
			item.Operation = &OperationReceipt{OperationID: operationID, Binding: binding, PollArguments: args, Deadline: h.now() + binding.TimeoutSeconds}
		}
		item.Status = "unknown"
		item.ErrorCode = strptr("operation_outcome_unknown")
		state.Status = "needs_reconciliation"
		state.ErrorCode = item.ErrorCode
	default:
		return hostError("operation_state_unknown")
	}
	return nil
}
func (h *AgentHost) poll(ctx context.Context, run StoredRun, state *RuntimeState, item *Invocation, provider CapabilityProvider, runtime *AgentRuntime) (StoredRun, error) {
	receipt := item.Operation
	var prior *Fact
	if item.FactID != nil {
		for i := range state.Facts {
			if state.Facts[i].FactID == *item.FactID {
				prior = &state.Facts[i]
				break
			}
		}
	}
	if prior == nil || !ReferenceAvailable(*prior, runtime.ConnectionID) {
		state.Status = "needs_reconciliation"
		state.ErrorCode = strptr("operation_reference_unavailable")
		return h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
	}
	if h.now() >= receipt.Deadline {
		if receipt.Binding.ReconcileOnTimeout {
			item.Status = "unknown"
			item.ErrorCode = strptr("operation_outcome_unknown")
			state.Status = "needs_reconciliation"
			state.ErrorCode = item.ErrorCode
			return h.save(run, state, "", nil, map[string]any{"kind": "reconciliation_required", "invocation_id": item.InvocationID})
		}
		item.Status = "failed"
		item.ErrorCode = strptr("operation_deadline_exceeded")
		Reject(state, item.Call.CallRef, item.Call.Capability, "operation_deadline_exceeded", nil, "")
		return h.save(run, state, "", nil, map[string]any{"kind": "operation_timed_out"})
	}
	if h.now() < receipt.NextPollAt && !item.PollInFlight {
		return run, nil
	}
	if state.PollCallsUsed >= h.runSettings(run).MaxPollCalls {
		state.Status = "failed"
		state.ErrorCode = strptr("poll_budget_exhausted")
		return h.save(run, state, "", nil, nil)
	}
	policy, e := h.projectPolicy(ctx, run)
	if e != nil {
		return run, e
	}
	if e = pollAccess(provider, policy, receipt.Binding); e != nil {
		return run, e
	}
	if !item.PollInFlight {
		receipt.Polls++
	}
	item.PollInFlight = true
	state.PollCallsUsed++
	call := ToolCall{CallRef: fmt.Sprintf("poll-%s-%d", item.InvocationID, receipt.Polls), Capability: receipt.Binding.PollCapability, Arguments: receipt.PollArguments, Reason: "Read persisted operation status"}
	inv := invocationContext(run, item, runtime.ConnectionID)
	identifyInvocation(&inv, run, provider, call.Capability, policy)
	inv.InvocationID = fmt.Sprintf("%s:poll:%d", item.InvocationID, receipt.Polls)
	inv.IdempotencyKey = inv.InvocationID
	audit := invocationAudit("operation_poll_started", inv)
	audit["poll"] = receipt.Polls
	run, e = h.save(run, state, "", nil, audit)
	if e != nil {
		return run, e
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(h.runSettings(run).InvocationTimeoutSeconds*1e9))
	outcome, e := ExecuteCall(callCtx, provider, policy.GrantedCapabilities, call, &inv)
	cancel()
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	if e != nil {
		outcome = CallOutcome{ErrorCode: "provider_outcome_unknown"}
	}
	item.PollInFlight = false
	if definiteAuth(outcome.ErrorCode) {
		state.Status = "needs_authorization"
		state.ErrorCode = strptr(outcome.ErrorCode)
	} else if outcome.Fact == nil {
		receipt.NextPollAt = h.now() + receipt.Binding.IntervalSeconds
		item.ErrorCode = strptr(outcome.ErrorCode)
	} else {
		b, e := CanonicalJSON(outcome.Fact)
		if e != nil {
			return run, e
		}
		if len(b) > h.runSettings(run).MaxArtifactBytes {
			return run, hostError("result_too_large")
		}
		size := len(b)
		facts := []Fact{}
		for _, f := range state.Facts {
			if f.FactID != *item.FactID {
				b, _ := CanonicalJSON(f)
				size += len(b)
				facts = append(facts, f)
			}
		}
		if size > h.runSettings(run).MaxActiveArtifactBytes {
			return run, hostError("run_artifacts_too_large")
		}
		state.Facts = append(facts, *outcome.Fact)
		item.FactID = strptr(outcome.Fact.FactID)
		item.ErrorCode = nil
		if e = h.operation(state, item, *outcome.Fact, receipt.Binding); e != nil {
			return run, e
		}
		observation := Observation{CallRef: call.CallRef, Capability: call.Capability, Status: "succeeded", FactID: item.FactID, Arguments: call.Arguments}
		prefix := "poll-" + item.InvocationID + "-"
		filter := func(items []Observation) []Observation {
			result := []Observation{}
			for _, o := range items {
				if !strings.HasPrefix(o.CallRef, prefix) {
					result = append(result, o)
				}
			}
			return append(result, observation)
		}
		state.Observations = filter(state.Observations)
		state.ModelObservations = filter(state.ModelObservations)
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "operation_polled", "invocation_id": item.InvocationID, "poll": receipt.Polls, "fact_id": item.FactID, "error_code": strptr(outcome.ErrorCode)})
}
func (h *AgentHost) work(ctx context.Context, run StoredRun) (result StoredRun, err error) {
	claimedToken := run.LeaseToken
	state, e := h.restore(run)
	if e != nil {
		return run, e
	}
	defer func() {
		if err == nil || errors.Is(err, ErrLeaseLost) || ctx.Err() != nil {
			return
		}
		code := "connection_or_execution_failed"
		var he *HostError
		if errors.As(err, &he) {
			code = he.Code
		}
		var deployment *DeploymentError
		if errors.As(err, &deployment) {
			code = deployment.Code
		}
		if code == "run_state_too_large" {
			if latest, e := h.Store.GetRun(run.RunID, run.OwnerID); e == nil {
				if latest.LeaseToken != claimedToken {
					result, err = latest, ErrLeaseLost
					return
				}
				if restored, e := h.restore(latest); e == nil {
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
			if item.Status == "in_flight" || item.Status == "unknown" {
				if state.Status != "needs_authorization" {
					state.Status = "needs_reconciliation"
				}
				if item.Status == "in_flight" {
					item.Status = "unknown"
				}
			}
		}
		state.ErrorCode = strptr(code)
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
	if terminal(state.Status) || state.Status == "needs_input" || state.Status == "needs_reconciliation" {
		return run, nil
	}
	if state.Status == "waiting" && run.NextWakeAt != nil && h.now() < *run.NextWakeAt {
		return run, nil
	}
	policy, e := h.projectPolicy(ctx, run)
	if e != nil {
		return run, e
	}
	config, configErr := h.effectiveRunConfig(run)
	if configErr != nil {
		return run, configErr
	}
	currentModel := modelInfo(h.Model)
	if config.Source == "run_snapshot" && config.Model.Name != "" && config.Model.Name != currentModel.Name {
		return run, hostError("model_changed")
	}
	provider, e := h.openRunProvider(ctx, run)
	if e != nil {
		return run, e
	}
	defer provider.Close()
	fp := fingerprint(provider)
	if previous, ok := run.State["pack_fingerprint"].(string); ok && previous != fp {
		return run, hostError("pack_changed")
	}
	runtime := &AgentRuntime{Provider: provider, Model: h.Model, Grants: policy.GrantedCapabilities, OriginPackID: run.PackID, ConnectionID: NewID(), Durable: true, MaxModelRounds: h.runSettings(run).MaxModelRounds, MaxToolCalls: h.runSettings(run).MaxToolCalls, MaxRepeatedCall: 2, MaxContextCharacters: h.runSettings(run).MaxContextCharacters}
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
	run, e = h.prepareMemories(ctx, run)
	if e != nil {
		return run, e
	}
	state, e = h.restore(run)
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
			state.Status = "cancelled"
			state.ErrorCode = strptr("cancel_requested")
			break
		}
		if h.now()-run.CreatedAt >= h.runSettings(run).MaxRunSeconds {
			state.Status = "failed"
			state.ErrorCode = strptr("run_deadline_exceeded")
			for i := range state.Pending {
				item := &state.Pending[i]
				if item.Status == "in_flight" || item.Status == "unknown" {
					state.Status = "needs_reconciliation"
					item.Status = "unknown"
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
			state.Pending = []Invocation{}
			run, e = h.save(run, state, "", nil, nil)
			if e != nil {
				return run, e
			}
			continue
		}
		policy, e = h.projectPolicy(ctx, run)
		if e != nil {
			return run, e
		}
		runtime.Grants = policy.GrantedCapabilities
		runtime.Memories, e = h.runMemories(run)
		if e != nil {
			return run, e
		}
		visible := []MemoryView{}
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
		modelCtx = WithModelRequestObserver(modelCtx, func(progress ModelRequestProgress) error {
			var err error
			run, err = h.save(run, state, "", nil, JSON{"kind": progress.Kind, "attempt": progress.Attempt, "error_code": progress.ErrorCode, "retry_at": progress.RetryAt})
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
			state.ErrorCode = strptr("model_timeout")
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
			if !errors.Is(e, errSteeringPending) {
				break
			}
		}
		if e != nil {
			return run, e
		}
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "run_stopped", "status": state.Status})
}
func (h *AgentHost) markCancelled(run StoredRun, state *RuntimeState) (StoredRun, error) {
	state.Status = "cancelled"
	state.ErrorCode = strptr("cancel_requested")
	for i := range state.Pending {
		if state.Pending[i].Status == "in_flight" {
			state.Pending[i].Status = "unknown"
			state.Pending[i].ErrorCode = strptr("provider_outcome_unknown")
		}
	}
	return h.save(run, state, "", nil, map[string]any{"kind": "run_cancelled"})
}
func (h *AgentHost) Drive(ctx context.Context, id, owner string) (StoredRun, error) {
	h.InitializeDefaults()
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return StoredRun{}, ctx.Err()
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
		e = ErrLeaseLost
	default:
		if workCtx.Err() != nil && e != nil {
			if current, err := h.Store.GetRun(id, owner); err == nil && current.CancelRequested {
				if current.LeaseToken != run.LeaseToken {
					e = ErrLeaseLost
				} else if state, err := h.restore(current); err == nil {
					current.LeaseToken = run.LeaseToken
					result, e = h.markCancelled(current, state)
				}
			}
		}
	}
	releaseErr := h.Store.Release(id, owner, run.LeaseToken)
	if e == nil && releaseErr != nil && !errors.Is(releaseErr, ErrLeaseLost) {
		e = releaseErr
	}
	return result, e
}
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
			if err != nil && !errors.Is(err, ErrStoreConflict) && !errors.Is(err, ErrLeaseLost) {
				var he *HostError
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
