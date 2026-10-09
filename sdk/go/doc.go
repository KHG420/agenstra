// Package agenstra provides the Go integration SDK for Agenstra.
//
// Import github.com/KHG420/agenstra/sdk/go to construct an AgentRuntime,
// AgentHost, SQLiteStore or HTTPServer, implement CapabilityProvider and
// DecisionModel, or load REST and MCP packs. The SDK exports the engine's types
// directly, so methods, callbacks and durable execution use the same state.
// Callers retain the resource ownership documented by each constructor.
//
// This package contains no execution state or business logic. Framework
// implementation and its tests live under internal/runtime/engine.
package agenstra
