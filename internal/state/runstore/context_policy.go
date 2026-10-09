package runstore

import (
	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// ContextPolicyCursor reads the last consumed context-policy revision from a checkpoint envelope.
func ContextPolicyCursor(envelope agentcontract.JSON) int {
	runtime, _ := envelope["runtime"].(agentcontract.JSON)
	cursor, _ := jsonvalue.Index(runtime["context_policy_cursor"])
	return cursor
}

func latestContextPolicy(q SQLQueryer, id string) (int, error) {
	var seq int
	err := q.QueryRow("SELECT coalesce(max(sequence),0) FROM events WHERE run_id=? AND event_json->>'kind'='context_policy_requested'", id).Scan(&seq)
	return seq, err
}
