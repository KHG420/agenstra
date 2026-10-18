package react

import (
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// validateFinalEvidence checks deterministic publication rules without calling
// the model or host validator. Both proposals and reviewed answers use it.
func validateFinalEvidence(decision agentcontract.Decision, facts map[string]agentcontract.Fact, connectionID string) ([]agentcontract.ResultObjectRef, string, string) {
	code := ""
	var feedback []string
	valid := len(facts) == 0 || len(decision.FactIDs) > 0
	for _, id := range decision.FactIDs {
		if _, ok := facts[id]; !ok {
			valid = false
		}
	}
	if !valid {
		code = "final_fact_citations_invalid"
		feedback = append(feedback, "Cite at least one observed Fact when Facts exist. Every fact_ids entry must be an existing Fact ID from this run; use inspect_fact to read evidence before correcting the answer.")
	}
	refs, err := resolveResultRefs(decision.ResultRefs, decision.FactIDs, facts, connectionID)
	if err != nil {
		if code == "" {
			code = "final_result_refs_invalid"
		}
		feedback = append(feedback, "Each result_refs entry must name an available Fact included in fact_ids and a path to a nonempty scalar business ID (string or integer). Inspect the Fact and use its actual path, including data and, for browser receipts, result. Omit result_refs when the answer needs no object reference; never invent an ID.")
	}
	answerFold := strings.ToLower(decision.AnswerMarkdown)
	for id := range facts {
		if id != "" && strings.Contains(answerFold, strings.ToLower(id)) {
			if code == "" {
				code = "final_internal_fact_id_exposed"
			}
			feedback = append(feedback, "Remove internal Fact identifiers from answer_markdown. Keep genuine citations in fact_ids and object references in result_refs. Answer the requested business values using existing successful evidence; do not repeat business operations.")
			break
		}
	}
	// Return all local failures together without changing error-code priority.
	// Otherwise correcting one field can expose another already-known failure.
	if code != "" {
		return nil, code, strings.Join(feedback, "\n")
	}
	return refs, "", ""
}
