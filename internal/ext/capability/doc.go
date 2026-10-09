// Package capability loads trusted REST and MCP manifests and implements their provider contracts.
// It owns request and response validation, skill loading, protocol adaptation and
// connection cleanup. MCP wire exchange belongs to mcptransport. This package
// does not read Host state, grant permissions, persist runs or orchestrate calls.
package capability
