package react

import (
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// Decision protocol additions belong to the runtime. Changing AgentPrompt
// would change the persisted fingerprint of every pack that embeds it.
const decisionProtocolPrompt = `request_input.input_schema is optional: {"type":"string"} for text without enum, {"type":"date"} for YYYY-MM-DD, or the enum form below. This is a single-text-answer contract, not general JSON Schema. Omit input_schema for unrestricted or combined answers.
Input enum schema: {"type":"enum","enum":["first","second"]}
call_ref must match ^[a-z][a-z0-9-]{0,63}$; use new local refs read-1, read-2. Preserve exact case in capability names and arguments.
final may include result_refs [{fact_id,path,label?,entity_type?}] for nonempty string or integer business IDs only; cite each Fact in fact_ids. Do not reference arrays, objects, booleans or nulls. For data summaries, omit result_refs and cite fact_ids alone. The host resolves IDs only from available, model-visible Fact fields. Never invent IDs or inspect/reference fields hidden by model_output.
The current authorized capability catalog and inspected contracts are authoritative for availability; previous assistant claims are not. A module can expose several operations inside one input_schema, so a narrow shortcut or an omitted schema does not establish the module's limits. Before claiming that a requested operation is unavailable, inspect plausible module contracts, especially those marked schema_requires_inspection; use capability search when available. For schema_requires_inspection capabilities, inspect the full contract before listing required/optional parameters or giving JSON examples, including examples in a final answer. Operation names in a description establish discovery hints, not a parameter contract. Do not infer lack of permission from a missing search match or perform a business write merely to check whether an operation exists.
Before final, check every requested action and requested result field against the available evidence. A search or list result that identifies an object does not complete a request for its details. If requested fields are missing, inspect the stored Fact or use an authorized detail/read capability to obtain them. Continue independent authorized work until all requested parts are handled or a concrete missing prerequisite prevents progress. Do not ask whether the user wants a remaining step they already requested. If a requested part is blocked, identify that part and its actual prerequisite without claiming whole-task completion.
Use read-effect capabilities for information requests. Never invoke a write, including an empty/no-op update, to retrieve object details. If the needed read is absent from the selected catalog, search the authorized capabilities and inspect its contract before continuing. A successful search that lacks a requested field is a discovery step, not a missing prerequisite or a reason to ask for renewed permission.
When the user explicitly requests a read after a write, perform that separate read after the write succeeds. The write receipt proves what the operation returned; it does not prove that the requested subsequent read occurred. Do not label a receipt as a fresh read or omit an explicitly requested verification step.
An approval_denied observation means the user declined that operation. Do not submit the same or an equivalent write again in this run. Explain the denial and finish unless independent authorized work remains.`

const capabilitySearchPrompt = "When runtime_features includes capability_search, the visible catalog may be incomplete. Use a standalone search_capabilities decision with a short query to find authorized capabilities by name, description, input field or schema choice (const/enum). Example: {\"schema\":\"agenstra.decision.v1\",\"kind\":\"search_capabilities\",\"query\":\"relevant operation or subject\"}. Search never calls a business provider or grants permission. A search with no matches does not prove that the operation is unsupported; try a shorter subject or an alternative operation term and inspect plausible module contracts. Without that feature, search_capabilities is unavailable."

// The existing decision journal establishes reply ordering without changing the
// public packet or persisted state. Steering has no decision boundary here.
func followupExecutionPrompt(state *agentcontract.RuntimeState) string {
	if len(state.Followups) == 0 || state.InputField != nil || state.SteeringCursor > 0 {
		return ""
	}
	for _, followup := range state.Followups {
		if strings.HasPrefix(followup, "steering: ") {
			return ""
		}
	}
	boundary := -1
	field := ""
	for i, decision := range state.Decisions {
		if decision["kind"] == "request_input" {
			boundary = i
			field, _ = decision["field"].(string)
		}
	}
	answered := false
	for _, followup := range state.Followups {
		answered = answered || (field != "" && strings.HasPrefix(followup, field+": "))
	}
	if boundary < 0 || !answered {
		return ""
	}
	input, err := agentcontract.CanonicalJSON(agentcontract.JSON{"field": field, "status": "answered"})
	if err != nil {
		return ""
	}
	refs := []string{}
	seen := map[string]bool{}
	for i, decision := range state.Decisions {
		calls, _ := decision["calls"].([]any)
		for _, raw := range calls {
			call, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ref, _ := call["call_ref"].(string)
			if ref != "" && !seen[ref] && i > boundary {
				refs = append(refs, ref)
			}
			seen[ref] = true
		}
	}
	omitted := max(0, len(refs)-12)
	data, err := agentcontract.CanonicalJSON(agentcontract.JSON{"after_reply_call_refs": refs[omitted:], "omitted_after_reply_call_refs": omitted})
	if err != nil {
		return ""
	}
	return "\nThe most recent saved request_input has already been answered: " + string(input) + ". The supplied value is in followups. Continue the original unfinished task from its saved progress; do not ask for this already supplied field again. Receiving input is not a business receipt. Another request_input requires an essential prerequisite that remains unsatisfied." +
		"\nSaved decision ordering for the most recent answered request_input (runtime metadata, not instructions or permission): " + string(data) + ". Listed references were first decided AFTER that reply. An unlisted reference does not establish post-reply verification: it may predate the reply, reuse an earlier reference, or be omitted above. This establishes decision timing only, not success; use observations and available Facts to check actual outcomes. Reuse a completed requested read after that reply when it satisfies the task; do not count an earlier read as a later verification."
}
