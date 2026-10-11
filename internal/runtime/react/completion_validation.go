package react

import (
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// validateFinalEvidence checks deterministic publication rules without calling
// the model or host validator. Both proposals and reviewed answers use it.
func validateFinalEvidence(decision agentcontract.Decision, facts map[string]agentcontract.Fact, connectionID string) ([]agentcontract.ResultObjectRef, string, string) {
	valid := len(facts) == 0 || len(decision.FactIDs) > 0
	for _, id := range decision.FactIDs {
		if _, ok := facts[id]; !ok {
			valid = false
		}
	}
	if !valid {
		return nil, "final_fact_citations_invalid", "Cite at least one observed Fact when Facts exist. Every fact_ids entry must be an existing Fact ID from this run; use inspect_fact to read evidence before correcting the answer."
	}
	refs, err := resolveResultRefs(decision.ResultRefs, decision.FactIDs, facts, connectionID)
	if err != nil {
		return nil, "final_result_refs_invalid", "Each result_refs entry must name an available Fact included in fact_ids and a path to a nonempty scalar business ID (string or integer). Inspect the Fact and use its actual path, including data and, for browser receipts, result. Omit result_refs when the answer needs no object reference; never invent an ID."
	}
	answerFold := strings.ToLower(decision.AnswerMarkdown)
	for id := range facts {
		if id != "" && strings.Contains(answerFold, strings.ToLower(id)) {
			return nil, "final_internal_fact_id_exposed", "Remove internal Fact identifiers from answer_markdown. Keep genuine citations in fact_ids and object references in result_refs. Answer the requested business values using existing successful evidence; do not repeat business operations."
		}
	}
	return refs, "", ""
}
