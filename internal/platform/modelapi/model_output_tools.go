package modelapi

import (
	"encoding/json"
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

func decisionOutputTools() []any {
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
	add("final", agentcontract.JSON{
		"answer_markdown": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 30000},
		"fact_ids":        agentcontract.JSON{"type": "array", "maxItems": 50, "items": uuid},
		"result_refs":     agentcontract.JSON{"type": "array", "maxItems": 20, "items": resultRef},
	}, "answer_markdown", "fact_ids")
	add("request_input", agentcontract.JSON{
		"field":  agentcontract.JSON{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
		"prompt": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 1000}, "input_schema": inputSchema,
	}, "field", "prompt")
	add("search_capabilities", agentcontract.JSON{"query": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 300}}, "query")
	for _, kind := range []string{"read_skill", "inspect_capability"} {
		add(kind, agentcontract.JSON{"name": agentcontract.JSON{"type": "string", "minLength": 1, "maxLength": 330}}, "name")
	}
	add("inspect_fact", agentcontract.JSON{"fact_id": uuid, "path": path}, "fact_id", "path")
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

func decisionOutputToolPrompt(prompt string, completionReview bool) string {
	lines := strings.Split(prompt, "\n")
	retained := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "Return one JSON object with schema") ||
			strings.HasPrefix(line, "Skill: {") ||
			strings.HasPrefix(line, "Return exactly one raw JSON object.") || line == agentcontract.DecisionObjectShapePrompt {
			continue
		}
		line = strings.ReplaceAll(line, "Return exactly one raw JSON object without Markdown or code fences.", "Submit exactly one matching decision output tool.")
		line = strings.ReplaceAll(line, "agenstra.decision.v1 JSON decision", "typed agenstra.decision.v1 decision")
		line = strings.ReplaceAll(line, "Choose one JSON decision at a time:", "Choose one typed decision at a time:")
		line = strings.ReplaceAll(line, "Each call_ref must be new.", "The adapter assigns fresh local call references.")
		line = strings.ReplaceAll(line, "no tools or other decision kinds", "no capability calls or other decision kinds")
		line = strings.ReplaceAll(line, "tools and other decision kinds are not allowed", "capability calls and other decision kinds are not allowed")
		line = strings.ReplaceAll(line, "propose any tool call", "propose any capability call")
		retained = append(retained, line)
	}
	bindings := decisionOutputFieldBindings()
	guidance := "\nCall submit_decision exactly once. Arguments are one native object {\"kind\":\"<one declared kind>\",...}; omit schema. Do not wrap the object in a decision property or stringify it. The response must contain exactly one tool_calls entry, including for read_skill, inspect_capability and inspect_fact. Choose one decision and wait for its result before the next decision. Use only the declared native fields, arrays and objects for that kind. Saved calls are historical evidence; do not replay them merely because they appear in history. This replaces content-only JSON output. Output tools do not execute business operations; the runtime separately validates and authorizes capability calls.\nFor one or more independent capability calls, choose kind tool_batch with calls as a native array. Each call contains capability, arguments and reason. Arguments must match the selected capability's input_schema, preserving all required nested objects. Omit call_ref: the adapter supplies a fresh local reference for every proposed call. Preserve exact capability and operation names in their own fields."
	if completionReview {
		guidance = "\nCall submit_decision exactly once with arguments {\"kind\":\"final\",\"answer_markdown\":\"...\",\"fact_ids\":[]} and optional result_refs. Omit schema. The response must contain exactly one tool_calls entry. This replaces content-only JSON output. All saved capability calls are historical evidence, not available decisions. Do not submit another decision kind or replay saved capability calls."
	}
	return strings.Join(retained, "\n") + guidance + "\nUse the exact output-tool field names and native types below. Do not substitute aliases such as answer, response, content or text for answer_markdown.\n" + strings.Join(bindings, "\n")
}

func decisionOutputFieldBindings() []string {
	bindings := []string{}
	for _, tool := range decisionOutputTools() {
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
func decisionOutputToolBody(calls []decisionOutputToolCall) ([]byte, string) {
	if len(calls) != 1 || calls[0].Type != "function" {
		return nil, "model_output_tool_calls_invalid"
	}
	kind := strings.TrimPrefix(calls[0].Function.Name, "submit_")
	switch calls[0].Function.Name {
	case "submit_decision":
	case "submit_tool_call":
		kind = "tool_batch"
	case "submit_tool_batch", "submit_final", "submit_request_input", "submit_search_capabilities", "submit_read_skill", "submit_inspect_capability", "submit_inspect_fact":
	default:
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
	if calls[0].Function.Name == "submit_decision" {
		if err := json.Unmarshal(fields["kind"], &kind); err != nil {
			return nil, "model_decision_schema_invalid"
		}
		switch kind {
		case "tool_batch", "final", "request_input", "search_capabilities", "read_skill", "inspect_capability", "inspect_fact":
		default:
			return nil, "model_decision_schema_invalid"
		}
		delete(fields, "kind")
	}
	if fields["kind"] != nil || fields["schema"] != nil {
		return nil, "model_decision_schema_invalid"
	}
	if calls[0].Function.Name == "submit_tool_call" {
		batch, err := json.Marshal([]map[string]json.RawMessage{fields})
		if err != nil {
			return nil, "model_decision_schema_invalid"
		}
		fields = map[string]json.RawMessage{"calls": batch}
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
