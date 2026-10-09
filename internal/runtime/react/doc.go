// Package react owns transient ReAct decisions, bounded context, fact references, completion checks and invocation evidence.
// Drivers can checkpoint before decisions and calls. This package has no durable
// store, deployment or HTTP dependency; ExecuteCall performs authorized provider
// IO, and concurrent workers return isolated results before drivers apply state.
package react
