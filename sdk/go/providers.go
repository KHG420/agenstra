package agenstra

import (
	"context"
	"net/http"

	"github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/capability"
	"github.com/KHG420/agenstra/internal/frontend/service"
	"github.com/KHG420/agenstra/internal/runtime/react"
)

// OpenPack selects the declared provider from a trusted manifest.
// The caller owns and closes the returned connection.
func OpenPack(ctx context.Context, path string, environment map[string]string) (CapabilityProvider, error) {
	return capability.OpenPack(ctx, path, environment)
}

// ValidatePackManifest checks a complete manifest and pinned skill contents without business IO.
func ValidatePackManifest(manifest map[string]any, skillContents map[string]string) error {
	return capability.ValidatePackManifest(manifest, skillContents)
}

// MCPSource selects a trusted stdio or HTTP transport and connection references.
type MCPSource = agent.MCPSource

// MCPToolExposure pins an exposed tool's full contract and execution guarantees.
type MCPToolExposure = agent.MCPToolExposure

// MCPManifest declares pinned MCP tools, connection references and skill files.
type MCPManifest = agent.MCPManifest

// MCPPack owns an MCP transport and validated tool catalogs.
// Close it only after requests have stopped; catalogs remain read-only.
type MCPPack = capability.MCPPack

// MCPContractDigest computes the canonical digest of a JSON tool contract.
// It returns an empty string for an invalid JSON contract.
func MCPContractDigest(tool JSON) string { return capability.MCPContractDigest(tool) }

// OpenMCPPack opens and initializes a transport, rejecting any pinned tool contract drift.
// The caller must close the returned pack.
func OpenMCPPack(ctx context.Context, path string, environment map[string]string) (*MCPPack, error) {
	return capability.OpenMCPPack(ctx, path, environment)
}

// MCPDiscoveredTool describes an available tool without granting or invoking it.
// Remote annotations are not trusted as authorization or replay guarantees.
type MCPDiscoveredTool = agent.MCPDiscoveredTool

// DiscoverMCPTools initializes a connection, lists its tools and closes it. It
// never calls tools/call. Selection and confirmation of business effects remain
// explicit; the initial exposure requires approval and disallows replay.
func DiscoverMCPTools(ctx context.Context, source MCPSource, environment map[string]string) (result []MCPDiscoveredTool, resultErr error) {
	return service.DiscoverMCPTools(ctx, source, environment)
}

// ImportOpenAPI reads a complete JSON document and generates selected endpoint drafts.
// It does not infer approvals, idempotency guarantees or final job states.
func ImportOpenAPI(path, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
	return capability.ImportOpenAPI(path, name, baseURLEnv, operations, effects, tokenEnv)
}

// ImportOpenAPIDocument validates selected operations and returns a REST manifest draft for review.
func ImportOpenAPIDocument(doc JSON, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
	return capability.ImportOpenAPIDocument(doc, name, baseURLEnv, operations, effects, tokenEnv)
}

// InvocationContext carries the trusted identity and stable idempotency key of a call.
// Project routing may fill target identity fields before a provider invocation.
type InvocationContext = agent.InvocationContext

// OperationBinding declares how to read and poll an external asynchronous receipt.
type OperationBinding = agent.OperationBinding

// CapabilityDescription declares a capability's contract and execution guarantees.
// Catalog values are immutable after a provider is opened.
type CapabilityDescription = agent.CapabilityDescription

// SkillDescription identifies a pinned usage guide without its full text.
type SkillDescription = agent.SkillDescription

// Skill contains usage guidance; its content cannot expand the caller's grants.
type Skill = agent.Skill

// CapabilityResult contains provider data or a safe error code, never both.
// Error codes use 1–120 ASCII letters, digits, underscores, dots, colons or hyphens.
// An unknown external outcome must remain distinguishable from a definite failure.
type CapabilityResult = agent.CapabilityResult

// CapabilityProvider owns an opened capability connection.
// Capabilities and Skills return read-only catalogs; Invoke must respect cancellation.
// The opener closes the provider after all invocation goroutines have exited.
type CapabilityProvider = agent.CapabilityProvider

// ConcurrentCapabilityProvider explicitly opts into simultaneous Invoke calls.
// Catalogs and request validation must be safe for concurrent reads.
type ConcurrentCapabilityProvider = agent.ConcurrentCapabilityProvider

// DecisionModel chooses one validated action from a bounded context.
// Implementations must respect cancellation and return errors without exposing secrets.
// Errors with Code() string deliberately expose that safe code; unknown uncoded errors become model_unavailable.
type DecisionModel = agent.DecisionModel

// CallOutcome carries retained evidence or a structured execution error code.
type CallOutcome = agent.CallOutcome

// BindIdempotency copies arguments before binding the existing invocation key.
// It rejects malformed paths or arguments without changing the caller's map.
func BindIdempotency(call ToolCall, cap CapabilityDescription, inv InvocationContext) (ToolCall, error) {
	return react.BindIdempotency(call, cap, inv)
}

// ExecuteCall checks catalog access and declared input before invoking a provider with owned arguments.
// It copies and validates declared output before retaining evidence and converts provider failures to safe outcome codes.
func ExecuteCall(ctx context.Context, provider CapabilityProvider, grants map[string]bool, call ToolCall, inv *InvocationContext) (CallOutcome, error) {
	return react.ExecuteCall(ctx, provider, grants, call, inv)
}

// Observe appends evidence and observations and settles the supplied invocation.
func Observe(state *RuntimeState, item *Invocation, outcome CallOutcome) {
	react.Observe(state, item, outcome)
}

// ArgumentsDigest computes the canonical SHA-256 of a JSON capability call.
// It returns an empty string when the call cannot be encoded as JSON.
func ArgumentsDigest(call ToolCall) string { return react.ArgumentsDigest(call) }

// AgentPrompt combines host usage guidance with the framework decision protocol.
func AgentPrompt(guidance string) string { return capability.AgentPrompt(guidance) }

// RestEndpoint fixes an HTTP operation's input, output and execution contract.
type RestEndpoint = agent.RestEndpoint

// RestBusinessCheck treats a successful HTTP status as a business failure when
// the response field differs from Value. ErrorCode is deliberately namespaced
// so a business response cannot impersonate an authorization or unknown outcome.
type RestBusinessCheck = agent.RestBusinessCheck

// RestManifest declares trusted REST endpoints and credential references.
type RestManifest = agent.RestManifest

// RestPack is an opened REST provider with compiled request and response contracts.
// Catalogs and configuration are read-only during invocation; the HTTP client remains caller-owned.
type RestPack = capability.RestPack

// LoadRestPack validates a manifest and resolves its configured connection references.
func LoadRestPack(path string, environment map[string]string, client *http.Client) (*RestPack, error) {
	return capability.LoadRestPack(path, environment, client)
}

// RestField describes a primitive field in the legacy REST contract.
type RestField = agent.RestField

// LegacyRestCapability declares a fixed endpoint and flat input/output fields.
type LegacyRestCapability = agent.LegacyRestCapability

// LegacyPackManifest is the original flat REST capability pack format.
type LegacyPackManifest = agent.LegacyPackManifest

// LegacyRestPack adapts a validated legacy pack to the provider contract.
// Its configuration and catalogs must remain read-only during invocation.
type LegacyRestPack = capability.LegacyRestPack

// LoadLegacyPack validates and opens a legacy pack without taking ownership of a supplied HTTP client.
func LoadLegacyPack(path string, environment map[string]string, client *http.Client) (*LegacyRestPack, error) {
	return capability.LoadLegacyPack(path, environment, client)
}

// SkillFile pins a usage guide's path, metadata and SHA-256 digest.
type SkillFile = agent.SkillFile

// LoadSkillFiles verifies pinned guides beneath the package directory and returns their contents.
func LoadSkillFiles(entries []SkillFile, directory string) (map[string]Skill, error) {
	return capability.LoadSkillFiles(entries, directory)
}
