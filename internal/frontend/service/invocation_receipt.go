package service

import (
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func runHasInvocationEvidence(run agentcontract.StoredRun) bool {
	runtime, _ := run.State["runtime"].(map[string]any)
	if receipts, _ := runtime["invocation_receipts"].([]any); len(receipts) > 0 {
		return true
	}
	items, _ := runtime["pending"].([]any)
	for _, value := range items {
		item, _ := value.(map[string]any)
		if item["status"] == "unknown" || item["status"] == "in_flight" || item["poll_in_flight"] == true || (item["status"] == "waiting" && item["operation"] != nil) {
			return true
		}
	}
	return false
}
