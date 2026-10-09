// Package runstore owns the existing SQLite run schema, transaction fencing, artifacts, invocation journals, memory and schedules.
// It does not invoke providers, models or business authorization. Host policy
// checks precede owner-scoped storage operations; leased writes recheck fencing
// in the transaction. Store initialization and connection cleanup remain explicit.
package runstore
