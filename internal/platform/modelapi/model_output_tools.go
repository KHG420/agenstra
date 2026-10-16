package modelapi

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

type decisionOutputToolCall struct {
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func decisionOutputTools(completionReview bool) []any {
	uuid := agentcontract.JSON{"type": "string", "pattern": "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"}
	path := agentcontract.JSON{"type": "array", "maxItems": 16, "items": agentcontract.JSON{"anyOf": []any{agentcontract.JSON{"type": "string"}, agentcontract.JSON{"type": "integer", "minimum": 0}}}}
	object := func(properties agentcontract.JSON, required ...string) agentcontract.JSON {
		return agentcontract.JSON{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	call := object(agentcontract.JSON{
		"call_ref":   agentcontract.JSON{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,63}$", "description": "Optional local label; the adapter assigns the actual fresh call reference."},
		"capability": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 330},
		"arguments":  agentcontract.JSON{"type": "object", "additionalProperties": true},
		"reason":     agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 500},
	}, "capability", "arguments", "reason")
	resultPath := agentcontract.JSON{"type": "array", "minItems": 1, "maxItems": 16, "items": path["items"]}
	resultRef := object(agentcontract.JSON{
		"fact_id": uuid, "path": resultPath, "label": agentcontract.JSON{"type": "string", "maxLength": 100},
		"entity_type": agentcontract.JSON{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
	}, "fact_id", "path")
	inputSchema := agentcontract.JSON{"oneOf": []any{
		object(agentcontract.JSON{"type": agentcontract.JSON{"type": "string", "enum": []string{"string"}}, "min_length": agentcontract.JSON{"type": "integer", "minimum": 0, "maximum": 30000}, "max_length": agentcontract.JSON{"type": "integer", "minimum": 0, "maximum": 30000}}, "type"),
		object(agentcontract.JSON{"type": agentcontract.JSON{"type": "string", "enum": []string{"date"}}}, "type"),
		object(agentcontract.JSON{"type": agentcontract.JSON{"type": "string", "enum": []string{"enum"}}, "enum": agentcontract.JSON{"type": "array", "minItems": 1, "maxItems": 50, "uniqueItems": true, "items": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 200}}}, "type", "enum"),
	}}
	tools := []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
		"name":        "submit_tool_call",
		"description": "Submit exactly one capability call as a tool_batch decision. Structured output only; does not execute capabilities or grant permission.",
		"parameters":  call,
	}}}
	add := func(kind string, properties agentcontract.JSON, required ...string) {
		tools = append(tools, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
			"name":        "submit_" + kind,
			"description": "Submit the " + kind + " Agenstra decision. Structured output only; does not execute capabilities or grant permission.",
			"parameters":  object(properties, required...),
		}})
	}
	add("tool_batch", agentcontract.JSON{"calls": agentcontract.JSON{"type": "array", "minItems": 1, "maxItems": 4, "items": call}}, "calls")
	final := agentcontract.JSON{
		"answer_markdown": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 30000, "description": "User-facing response with the requested business values and necessary limitations. Never put internal Fact IDs in prose, tables, links or footnotes. Keep supporting evidence IDs only in fact_ids and result_refs. Requested business record IDs remain ordinary answer data."},
		"fact_ids":        agentcontract.JSON{"type": "array", "maxItems": 50, "items": uuid, "description": "Supporting evidence identifiers from this run: copy full Fact IDs exactly here. Do not repeat these internal evidence identifiers in answer_markdown."},
		"result_refs":     agentcontract.JSON{"type": "array", "maxItems": 20, "items": resultRef},
	}
	required := []string{"answer_markdown", "fact_ids"}
	if completionReview {
		final["completion_feedback"] = agentcontract.JSON{"type": "string", "maxLength": 2000, "description": "Empty when requested work is handled or concretely blocked; otherwise identify missing original work. Rejects publication, never executes or authorizes calls."}
		required = append(required, "completion_feedback")
	}
	add("final", final, required...)
	if completionReview {
		add("approve_completion", agentcontract.JSON{}, []string{}...)
	}
	add("request_input", agentcontract.JSON{
		"field":  agentcontract.JSON{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
		"prompt": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 1000}, "input_schema": inputSchema,
	}, "field", "prompt")
	add("search_capabilities", agentcontract.JSON{"query": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 300}}, "query")
	for _, kind := range []string{"read_skill", "inspect_capability"} {
		add(kind, agentcontract.JSON{"name": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 330}}, "name")
	}
	add("inspect_fact", agentcontract.JSON{"fact_id": uuid, "path": path,
		"offset": agentcontract.JSON{"type": "integer", "minimum": 0, "maximum": 1000000000, "description": "Optional zero-based start for an already-authorized saved array; omit for objects and scalar values."},
		"limit":  agentcontract.JSON{"type": "integer", "minimum": 1, "maximum": 32, "description": "Optional selected-array range size, default 32 when offset is supplied. Combine ranges from saved history using next_offset; do not replay the business call."},
	}, "fact_id", "path")
	return tools
}

// singleDecisionOutputTool offers the available decisions as one tagged object.
// Multiple business calls remain a single tool_batch decision, not output calls.
func singleDecisionOutputTool(tools []any) agentcontract.JSON {
	variants := []any{}
	rootProperties := agentcontract.JSON{}
	kinds := []string{}
	for _, tool := range tools {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		name := function["name"].(string)
		if name == "submit_tool_call" {
			continue
		}
		parameters := function["parameters"].(agentcontract.JSON)
		kind := strings.TrimPrefix(name, "submit_")
		kinds = append(kinds, kind)
		properties := agentcontract.JSON{"kind": agentcontract.JSON{"type": "string", "const": kind}}
		for field, schema := range parameters["properties"].(agentcontract.JSON) {
			properties[field] = schema
			rootProperties[field] = schema
		}
		required := append([]string{"kind"}, parameters["required"].([]string)...)
		variants = append(variants, agentcontract.JSON{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	}
	// Declare native property types at the root as well as the per-kind union.
	// Some compatible gateways stringify fields absent from root properties.
	rootProperties["kind"] = agentcontract.JSON{"type": "string", "enum": kinds}
	return agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
		"name":        "submit_decision",
		"description": "Submit exactly one typed decision. Structured output only; the runtime separately validates and authorizes capability calls. Choose one decision kind and wait for its result before deciding again.",
		"parameters": agentcontract.JSON{"type": "object", "properties": rootProperties,
			"required": []string{"kind"}, "anyOf": variants, "additionalProperties": false},
	}}
}

func decisionOutputBasePrompt(prompt string, completionReview bool) string {
	if completionReview {
		// Match the completion-only stage before replacing its JSON output channel.
		prompt = jsonCompletionReviewPrompt(prompt)
	}
	lines := strings.Split(prompt, "\n")
	retained := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "Return one JSON object with schema") ||
			strings.HasPrefix(line, "Skill: {") ||
			strings.HasPrefix(line, "Schema: {") ||
			strings.HasPrefix(line, "Available JSON decision") ||
			strings.HasPrefix(line, "Return exactly one raw JSON object.") || line == agentcontract.DecisionObjectShapePrompt {
			continue
		}
		line = strings.ReplaceAll(line, "Return exactly one raw JSON object without Markdown or code fences.", "Submit exactly one matching decision output tool.")
		line = strings.ReplaceAll(line, "agenstra.decision.v1 JSON decision", "typed agenstra.decision.v1 decision")
		line = strings.ReplaceAll(line, "Choose one JSON decision at a time:", "Choose one typed decision at a time:")
		line = strings.ReplaceAll(line, "Each call_ref must be new.", "The adapter assigns fresh local call references.")
		line = strings.ReplaceAll(line, `Example: {"schema":"agenstra.decision.v1",`, `Example submit_decision arguments: {`)
		line = strings.ReplaceAll(line, "no tools or other decision kinds", "no capability calls or other decision kinds")
		line = strings.ReplaceAll(line, "No tools or other decision kinds", "No capability calls or other decision kinds")
		line = strings.ReplaceAll(line, "tools and other decision kinds are not allowed", "capability calls and other decision kinds are not allowed")
		line = strings.ReplaceAll(line, "propose any tool call", "propose any capability call")
		retained = append(retained, line)
	}
	return strings.Join(retained, "\n")
}

func decisionOutputToolPrompt(prompt string, completionReview bool) string {
	prompt = decisionOutputBasePrompt(prompt, completionReview)
	bindings := decisionOutputFieldBindings(completionReview)
	guidance := "\nCall submit_decision exactly once. Arguments are one native object {\"kind\":\"<one declared kind>\",...}; omit schema. Do not wrap the object in a decision property or stringify it. The response must contain exactly one tool_calls entry, including for read_skill, inspect_capability and inspect_fact. Choose one decision and wait for its result before the next decision. Use only the declared native fields, arrays and objects for that kind. Saved calls are historical evidence; do not replay them merely because they appear in history. This replaces content-only JSON output. Output tools do not execute business operations; the runtime separately validates and authorizes capability calls.\nFor one or more independent capability calls, choose kind tool_batch with calls as a native array. Each call contains capability, arguments and reason. Arguments must match the selected capability's input_schema, preserving all required nested objects. Omit call_ref: the adapter supplies a fresh local reference for every proposed call. Preserve exact capability and operation names in their own fields."
	guidance += "\nEach tool_batch allows at most 4 calls. If the original task needs more calls, use separate tool_batch decisions and wait for their results before continuing with the remaining calls. Reuse retained evidence; never replay successful calls."
	if completionReview {
		guidance = "\nCall submit_decision exactly once. If the entire frozen proposal is verified and needs no changes, use {\"kind\":\"approve_completion\"} with no other fields; do not reproduce its answer or references. Otherwise use {\"kind\":\"final\",\"answer_markdown\":\"...\",\"fact_ids\":[],\"completion_feedback\":\"\"} and optional result_refs. Set completion_feedback to missing original work to reject premature publication, or an empty string when requested work is handled or concretely blocked. Omit schema. The response must contain exactly one tool_calls entry. This replaces content-only JSON output. All saved capability calls are historical evidence, not available decisions. Only final and approve_completion are available; do not submit another decision kind or replay saved capability calls."
	}
	prompt += guidance + "\nUse the exact output-tool field names and native types below. Do not substitute aliases such as answer, response, content or text for answer_markdown.\n" + strings.Join(bindings, "\n") + "\nUser requests for prose, Markdown, JSON, code or tables apply to final.answer_markdown only; they never replace the submit_decision output tool call. Put the requested display format inside that string."
	if completionReview {
		prompt += "\nCompletion validation has an explicit feedback field in the final decision for this review only: completion_feedback (string). Check every explicitly requested operation against this run's recorded calls and verified results, including a separately requested read after a write. If all required execution is evidenced, return completion_feedback as an empty string and correct any unsupported claim or display format in answer_markdown. If required execution is missing, return a nonempty completion_feedback that identifies only the missing work from the original request; keep the proposed answer as an unpublished draft. This signals rejection of premature completion, not permission to execute anything. Do not propose or execute any capability call during review; the required submit_decision call only returns the final decision or the payload-free approve_completion verdict. The runtime returns the feedback to a normal decision step with the original authorization and budgets; never request new authorization merely to finish already requested independent work. Do not invent missing requirements or treat a write receipt as a separately executed read. Return only approve_completion without a payload for an unchanged fully verified proposal, or the declared final decision fields plus completion_feedback for corrections or rejection through the existing output channel."
		prompt += " A verified missing prerequisite, denied approval or uncertain outcome is a concrete blocker: report that limitation with empty completion_feedback rather than request an unsafe replay. Missing required execution without such a blocker must reject completion."
	}
	return prompt
}

func decisionOutputFieldBindings(completionReview bool) []string {
	bindings := []string{}
	for _, tool := range decisionOutputTools(completionReview) {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if function["name"] == "submit_tool_call" {
			continue
		}
		parameters := function["parameters"].(agentcontract.JSON)
		properties := parameters["properties"].(agentcontract.JSON)
		required := []string{}
		for _, field := range parameters["required"].([]string) {
			typ, _ := properties[field].(agentcontract.JSON)["type"].(string)
			if typ == "" {
				typ = "schema"
			}
			required = append(required, field+" ("+typ+")")
		}
		allowed := make([]string, 0, len(properties))
		for field := range properties {
			allowed = append(allowed, field)
		}
		sort.Strings(allowed)
		bindings = append(bindings, strings.TrimPrefix(function["name"].(string), "submit_")+": required kind (string), "+strings.Join(required, ", ")+"; allowed field names kind, "+strings.Join(allowed, ", "))
	}
	return bindings
}

// decisionOutputToolBody selects the typed output channel and assigns local call identities.
// Business arguments are preserved without coercion; optional model labels are validated.
// Local decision and runtime validation still follow before any capability executes.
func decisionOutputToolBody(calls []decisionOutputToolCall, tools []any) ([]byte, string) {
	if len(calls) != 1 || calls[0].Type != "function" || calls[0].Function.Name != "submit_decision" {
		return nil, "model_output_tool_calls_invalid"
	}
	raw := []byte(calls[0].Function.Arguments)
	if detail := modelJSONFormatError(raw); detail != "" {
		return nil, detail
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, "model_decision_schema_invalid"
	}
	var kind string
	if err := json.Unmarshal(fields["kind"], &kind); err != nil {
		return nil, "model_decision_schema_invalid"
	}
	// Resolve availability from the actual request, including stage, skills and
	// runtime features. Recognizing a kind globally does not declare it here.
	declared := false
	for _, tool := range tools {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if function["name"] != calls[0].Function.Name {
			continue
		}
		parameters := function["parameters"].(agentcontract.JSON)
		properties := parameters["properties"].(agentcontract.JSON)
		kinds := properties["kind"].(agentcontract.JSON)["enum"].([]string)
		declared = slices.Contains(kinds, kind)
		break
	}
	if !declared {
		return nil, "model_decision_schema_invalid"
	}
	delete(fields, "kind")
	if fields["schema"] != nil {
		return nil, "model_decision_schema_invalid"
	}
	if kind == "tool_batch" {
		var proposed []map[string]json.RawMessage
		if err := json.Unmarshal(fields["calls"], &proposed); err != nil {
			return nil, "model_decision_schema_invalid"
		}
		for _, call := range proposed {
			if call == nil {
				return nil, "model_decision_schema_invalid"
			}
			if supplied, ok := call["call_ref"]; ok {
				var label string
				if err := json.Unmarshal(supplied, &label); err != nil || !agentcontract.CallRefPattern.MatchString(label) {
					return nil, "model_decision_schema_invalid"
				}
			}
			reference, err := json.Marshal("tool-" + agentcontract.NewID())
			if err != nil {
				return nil, "model_decision_schema_invalid"
			}
			call["call_ref"] = reference
		}
		batch, err := json.Marshal(proposed)
		if err != nil {
			return nil, "model_decision_schema_invalid"
		}
		fields["calls"] = batch
	}
	fields["schema"] = json.RawMessage(`"agenstra.decision.v1"`)
	encodedKind, err := json.Marshal(kind)
	if err != nil {
		return nil, "model_decision_schema_invalid"
	}
	fields["kind"] = encodedKind
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, "model_decision_schema_invalid"
	}
	return body, ""
}
