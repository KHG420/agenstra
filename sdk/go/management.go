package agenstra

import (
	"context"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	"github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/runtime/host"
	"github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/registry"
)

// DeploymentError carries a safe deployment or connection error code.
type DeploymentError = agent.DeploymentError

// IdentityConfig binds a trusted identity response to the expected subject and grants.
type IdentityConfig = agent.IdentityConfig

// ConnectionConfig declares owner-specific grants and credential references.
type ConnectionConfig = agent.ConnectionConfig

// UserConfig selects static authentication and the user's authorized connections.
type UserConfig = agent.UserConfig

// PackConfig locates a trusted capability manifest on disk.
type PackConfig = agent.PackConfig

// ManagementConfig configures the optional registry and administrator credential reference.
type ManagementConfig = agent.ManagementConfig

// DeploymentConfig contains trusted host wiring, execution limits and optional integrations.
type DeploymentConfig = agent.DeploymentConfig

// Deployment resolves trusted configuration and per-owner connections.
// Treat Config and Environment as read-only while requests are active.
type Deployment = deployassembly.Deployment

// LoadDeployment strictly decodes configuration and snapshots the process environment.
// Registry initialization and capability connections remain explicit operations.
func LoadDeployment(path string) (*Deployment, error) { return deployassembly.LoadDeployment(path) }

// ReconciliationRule verifies an uncertain operation using a granted read
// capability. Argument paths address {arguments, invocation_id, idempotency_key}.
// ResultPath selects the original operation's result in the query response.
type ReconciliationRule = agent.ReconciliationRule

// DiagnosticFinding explains a saved error using safe framework copy and recovery evidence.
type DiagnosticFinding = agent.DiagnosticFinding

// RunDiagnostics combines evidence-derived findings, progress and execution budgets.
type RunDiagnostics = agent.RunDiagnostics

// ExplainRunError never returns upstream error text, prompts or arguments.
func ExplainRunError(code, capability string) DiagnosticFinding {
	return host.ExplainRunError(code, capability)
}

// CapabilityDraft shares the registry but never takes part in runtime resolution.
type CapabilityDraft = agent.CapabilityDraft

// DraftIssue identifies a validation problem at a specific manifest field.
type DraftIssue = agent.DraftIssue

// DraftEdit describes one revision-checked edit to a capability draft.
type DraftEdit = agent.DraftEdit

// EvaluationCase specifies one explicit evaluation request and evidence requirements.
type EvaluationCase = agent.EvaluationCase

// EvaluationCheck records whether a requested evidence assertion passed.
type EvaluationCheck = agent.EvaluationCheck

// EvaluationResult combines persisted run evidence, diagnostics and evaluation checks.
type EvaluationResult = agent.EvaluationResult

// EvaluateRun verifies persisted state and evidence. It neither invokes a model
// nor modifies a run. Completion alone is not sufficient when assertions exist.
func EvaluateRun(ctx context.Context, run StoredRun, c EvaluationCase) (EvaluationResult, error) {
	return react.EvaluateRun(ctx, run, c)
}

// RegistryError carries a safe capability-management error code.
type RegistryError = agent.RegistryError

// CapabilityRegistry owns immutable package releases and live owner bindings.
// Initialize it before use and close it after all callers have stopped.
type CapabilityRegistry = registry.CapabilityRegistry

// NewCapabilityRegistry configures a registry without opening files or a database.
func NewCapabilityRegistry(databasePath, packageDir string) *CapabilityRegistry {
	return registry.NewCapabilityRegistry(databasePath, packageDir)
}
