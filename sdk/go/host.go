package agenstra

import (
	"github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/frontend/service"
	"github.com/KHG420/agenstra/internal/runtime/host"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

// HostError carries a stable run lifecycle or authorization error code.
type HostError = agent.HostError

// ExecutionPolicy is the live authorization resolved from trusted host state.
// Its maps must not be changed while a caller is using the policy.
type ExecutionPolicy = agent.ExecutionPolicy

// HostSettings bounds execution, persistence and model context.
// Configure settings before the host begins serving concurrent runs.
type HostSettings = agent.HostSettings

// DefaultHostSettings returns independent default execution limits.
func DefaultHostSettings() HostSettings { return agent.DefaultHostSettings() }

// ProviderFactory opens an owner-specific capability connection for a pack.
// The host closes each successfully opened provider.
type ProviderFactory = agent.ProviderFactory

// PolicyResolver reads current authorization; cached model data cannot replace it.
type PolicyResolver = agent.PolicyResolver

// ReleaseResolver selects the immutable pack release bound to a new run.
type ReleaseResolver = agent.ReleaseResolver

// ReleaseProviderFactory opens the pinned release for an existing run.
// The host closes each successfully opened provider.
type ReleaseProviderFactory = agent.ReleaseProviderFactory

// AgentHost owns run scheduling, authorization, leases and durable checkpoints.
// It must not be copied after use; its store and model are supplied by the host app.
type AgentHost = host.AgentHost

// NewAgentHost wires host services with default limits without opening or owning their resources.
func NewAgentHost(store *SQLiteStore, factory ProviderFactory, model DecisionModel, policy PolicyResolver) *AgentHost {
	return host.NewAgentHost(store, factory, model, policy)
}

// RequestRunID returns the stable run identity used by Create. Integrations can
// persist a binding before a queued run becomes visible to the worker.
// A nonempty request ID is required for a stable identity.
func RequestRunID(owner, request string) string { return host.RequestRunID(owner, request) }

// HostAuthConfig delegates bearer-token validation to a trusted host endpoint.
// The endpoint returns a stable owner ID; grants still require a managed binding.
type HostAuthConfig = agent.HostAuthConfig

// Memory is a user-owned default or project convention. Pack scope is the first
// version's project boundary; scope and ownership are assigned by the Host.
type Memory = agent.Memory

// MemoryView is a bounded active preference projection, never an authorization source.
type MemoryView = agent.MemoryView

// MemoryUpdate supplies a host edit with an optional expected revision.
type MemoryUpdate = agent.MemoryUpdate

// MemoryEvidence records an independent input supporting a learned preference.
type MemoryEvidence = agent.MemoryEvidence

// MemoryHistory contains retained revisions and supporting evidence for an owner-scoped entry.
type MemoryHistory = agent.MemoryHistory

// MemoryProposal is untrusted extracted input that the host validates before learning.
type MemoryProposal = agent.MemoryProposal

// MemoryExtractionRequest contains the current input and bounded existing preferences.
type MemoryExtractionRequest = agent.MemoryExtractionRequest

// MemoryExtractor is optional for custom models. HTTPJSONDecisionModel implements
// it; models without it can still use and manage manually saved memories.
type MemoryExtractor = agent.MemoryExtractor

// MeasuredMemoryExtractor is optional; existing custom extractors still work.
type MeasuredMemoryExtractor = agent.MeasuredMemoryExtractor

// ReconciliationContext gives a verifier a detached copy of the original uncertain invocation and trusted identity.
type ReconciliationContext = agent.ReconciliationContext

// InvocationReconciler must verify the original invocation using authoritative
// business evidence (for example its idempotency key). It must not replay it.
// Return only a settled result; a verification failure leaves the run paused.
type InvocationReconciler = agent.InvocationReconciler

// ScheduleSpec defines either an absolute Unix timestamp, an interval in
// seconds, or a numeric five-field cron expression in an explicit IANA zone.
type ScheduleSpec = agent.ScheduleSpec

// ScheduleRequest is the complete editable definition of a scheduled task.
// Updates replace this definition and require the current revision.
type ScheduleRequest = agent.ScheduleRequest

// ScheduledTask retains an owner's schedule, revision and next dispatch time.
type ScheduledTask = agent.ScheduledTask

// ScheduleExecution records one dispatch attempt. For attempts with a run,
// Status and ErrorCode reflect its current durable state. Fetch that run through
// AgentHost.Get (or /runs/{id}) to read results under the current authorization.
type ScheduleExecution = agent.ScheduleExecution

// ErrStoreConflict reports that a run cannot be claimed in its current state.
var ErrStoreConflict = runstore.ErrStoreConflict

// ErrRunNotFound reports that no run belongs to the requested owner and identity.
var ErrRunNotFound = runstore.ErrRunNotFound

// ErrLeaseLost reports that a worker no longer owns the live fencing token.
var ErrLeaseLost = runstore.ErrLeaseLost

// StoredRun is an owner-scoped snapshot with revision and lease fencing metadata.
// Timestamps and lease deadlines use Unix seconds.
type StoredRun = agent.StoredRun

// SQLiteStore retains the v1 database schema used by the original host.
// IMMEDIATE transactions serialize claims and checkpoint writes; fencing tokens
// prevent an expired worker from committing state.
type SQLiteStore = runstore.SQLiteStore

// NewSQLiteStore opens a single-connection disk store; Initialize sets up WAL and tables.
// The caller owns the store and must close it.
func NewSQLiteStore(path string) (*SQLiteStore, error) { return runstore.NewSQLiteStore(path) }

// CounterBudget reports use and remaining allowance for a bounded execution counter.
type CounterBudget = agent.CounterBudget

// TokenBudget reports token use while retaining unknown provider usage as unknown.
type TokenBudget = agent.TokenBudget

// RunBudget combines the run's frozen limits with measured execution usage.
type RunBudget = agent.RunBudget

// RunTelemetry is an owner-scoped evidence view of configuration, budgets and context projection.
type RunTelemetry = agent.RunTelemetry

// WebIntegrationConfig enables optional chat and browser integration with trusted session settings.
type WebIntegrationConfig = agent.WebIntegrationConfig

// WebProfileConfig binds an integration alias to a pack and optional frontend profile.
type WebProfileConfig = agent.WebProfileConfig

// FrontendAction declares a host browser handler's contract and execution properties.
type FrontendAction = agent.FrontendAction

// FrontendProfile pins browser actions, context schema and a handler version.
type FrontendProfile = agent.FrontendProfile

// WebIntegration can be attached to a custom Host as well as agenstra-serve.
// AuthenticateRequest is an optional trusted application-side identity hook.
// It is only used to mint web session tickets; tickets never authorize /runs or /admin.
type WebIntegration = service.WebIntegration

// NewWebIntegration validates profiles, opens its store and installs host provider wiring.
// Close the integration after callers stop; it leaves the host's run store caller-owned.
func NewWebIntegration(h *AgentHost, d *Deployment, c WebIntegrationConfig) (*WebIntegration, error) {
	return service.NewWebIntegration(h, d, c)
}

// WebStore owns only optional integration data, never the v1 run schema.
type WebStore = service.WebStore

// NewWebStore opens and initializes the optional integration database.
// The caller must close it after all integration requests have stopped.
func NewWebStore(path string) (*WebStore, error) { return service.NewWebStore(path) }

// BrowserSession is the persisted owner-bound browser generation and confirmed page snapshot.
type BrowserSession = agent.BrowserSession

// BrowserCommand retains an action's exact invocation and client-reported execution receipt.
type BrowserCommand = agent.BrowserCommand

// WebRunBinding links an authorized run to its integration and browser session.
type WebRunBinding = agent.WebRunBinding

// ChatConversation retains an owner-scoped integration and active message identity.
type ChatConversation = agent.ChatConversation

// ChatInput is an accepted response to a task's request for missing information.
// Its prompt and text are projected from the framework-owned run checkpoint.
type ChatInput = agent.ChatInput

// ChatMessage retains queued user text and the framework's persisted result projection.
type ChatMessage = agent.ChatMessage
