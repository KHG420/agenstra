// Package engine owns Agenstra's shared execution and persistence state.
//
// AgentRuntime owns decisions, context, budgets and fact references. AgentHost
// owns authorization, approvals, leases, checkpoints and invocation recovery.
// SQLiteStore persists that state; providers adapt trusted REST and MCP packs.
// The HTTP server, deployment registry, browser bridge and conversation APIs
// use the same owners rather than constructing a second execution path.
//
// These components remain in one package because their existing private methods
// and state transitions form a coupled dependency graph. Directory grouping
// does not grant other packages access to authorization or persistent state.
// Pure JSON, schema, calendar, pack-file, protocol and asset code lives in its
// own package and does not import this engine. Public Go integration belongs in
// github.com/KHG420/agenstra/sdk/go; commands and examples use that SDK.
package engine
