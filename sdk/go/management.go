package agenstra

import (
	"context"

	"github.com/KHG420/agenstra/internal/runtime/engine"
)

// DeploymentError carries a safe deployment or connection error code.
type DeploymentError = engine.DeploymentError

// IdentityConfig binds a trusted identity response to the expected subject and grants.
type IdentityConfig = engine.IdentityConfig

// ConnectionConfig declares owner-specific grants and credential references.
type ConnectionConfig = engine.ConnectionConfig

// UserConfig selects static authentication and the user's authorized connections.
type UserConfig = engine.UserConfig

// PackConfig locates a trusted capability manifest on disk.
type PackConfig = engine.PackConfig

// ManagementConfig configures the optional registry and administrator credential reference.
type ManagementConfig = engine.ManagementConfig

// DeploymentConfig contains trusted host wiring, execution limits and optional integrations.
type DeploymentConfig = engine.DeploymentConfig

// Deployment resolves trusted configuration and per-owner connections.
// Treat Config and Environment as read-only while requests are active.
type Deployment = engine.Deployment

// LoadDeployment strictly decodes configuration and snapshots the process environment.
// Registry initialization and capability connections remain explicit operations.
func LoadDeployment(path string) (*Deployment, error) { return engine.LoadDeployment(path) }

// ReconciliationRule verifies an uncertain operation using a granted read
// capability. Argument paths address {arguments, invocation_id, idempotency_key}.
// ResultPath selects the original operation's result in the query response.
type ReconciliationRule = engine.ReconciliationRule

// DiagnosticFinding explains a saved error using safe framework copy and recovery evidence.
type DiagnosticFinding = engine.DiagnosticFinding

// RunDiagnostics combines evidence-derived findings, progress and execution budgets.
type RunDiagnostics = engine.RunDiagnostics

// ExplainRunError never returns upstream error text, prompts or arguments.
func ExplainRunError(code, capability string) DiagnosticFinding {
	return engine.ExplainRunError(code, capability)
}

// CapabilityDraft shares the registry but never takes part in runtime resolution.
type CapabilityDraft = engine.CapabilityDraft

// DraftIssue identifies a validation problem at a specific manifest field.
type DraftIssue = engine.DraftIssue

// DraftEdit describes one revision-checked edit to a capability draft.
type DraftEdit = engine.DraftEdit

// EvaluationCase specifies one explicit evaluation request and evidence requirements.
type EvaluationCase = engine.EvaluationCase

// EvaluationCheck records whether a requested evidence assertion passed.
type EvaluationCheck = engine.EvaluationCheck

// EvaluationResult combines persisted run evidence, diagnostics and evaluation checks.
type EvaluationResult = engine.EvaluationResult

// EvaluateRun verifies persisted state and evidence. It neither invokes a model
// nor modifies a run. Completion alone is not sufficient when assertions exist.
func EvaluateRun(ctx context.Context, run StoredRun, c EvaluationCase) (EvaluationResult, error) {
	return engine.EvaluateRun(ctx, run, c)
}

// RegistryError carries a safe capability-management error code.
type RegistryError = engine.RegistryError

// CapabilityRegistry owns immutable package releases and live owner bindings.
// Initialize it before use and close it after all callers have stopped.
type CapabilityRegistry = engine.CapabilityRegistry

// NewCapabilityRegistry configures a registry without opening files or a database.
func NewCapabilityRegistry(databasePath, packageDir string) *CapabilityRegistry {
	return engine.NewCapabilityRegistry(databasePath, packageDir)
}
