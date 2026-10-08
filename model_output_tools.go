package agenstra

import (
	"encoding/json"
	"sort"
	"strings"
)

type decisionOutputToolCall struct {
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func validDecisionOutputMode(mode string) bool {
	return mode == "" || mode == "json_object" || mode == "output_tools"
}

func decisionOutputTools() []any {
	uuid := JSON{"type": "string", "pattern": "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"}
	path := JSON{"type": "array", "maxItems": 16, "items": JSON{"anyOf": []any{JSON{"type": "string"}, JSON{"type": "integer", "minimum": 0}}}}
	object := func(properties JSON, required ...string) JSON {
		return JSON{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	call := object(JSON{
		"call_ref":   JSON{"type": "string", "pattern": "^[a-z][a-z0-9-]{0,63}$"},
		"capability": JSON{"type": "string", "minLength": 1, "maxLength": 330},
		"arguments":  JSON{"type": "object", "additionalProperties": true},
		"reason":     JSON{"type": "string", "minLength": 1, "maxLength": 500},
	}, "call_ref", "capability", "arguments", "reason")
	resultPath := JSON{"type": "array", "minItems": 1, "maxItems": 16, "items": path["items"]}
	resultRef := object(JSON{
		"fact_id": uuid, "path": resultPath, "label": JSON{"type": "string", "maxLength": 100},
		"entity_type": JSON{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
	}, "fact_id", "path")
	inputSchema := JSON{"oneOf": []any{
		object(JSON{"type": JSON{"type": "string", "enum": []string{"string"}}, "min_length": JSON{"type": "integer", "minimum": 0, "maximum": 30000}, "max_length": JSON{"type": "integer", "minimum": 0, "maximum": 30000}}, "type"),
		object(JSON{"type": JSON{"type": "string", "enum": []string{"date"}}}, "type"),
		object(JSON{"type": JSON{"type": "string", "enum": []string{"enum"}}, "enum": JSON{"type": "array", "minItems": 1, "maxItems": 50, "uniqueItems": true, "items": JSON{"type": "string", "minLength": 1, "maxLength": 200}}}, "type", "enum"),
	}}
	tools := []any{}
	add := func(kind string, properties JSON, required ...string) {
		tools = append(tools, JSON{"type": "function", "function": JSON{
			"name":        "submit_" + kind,
			"description": "Submit the " + kind + " Agenstra decision. Structured output only; does not execute capabilities or grant permission.",
			"parameters":  object(properties, required...),
		}})
	}
	add("tool_batch", JSON{"calls": JSON{"type": "array", "minItems": 1, "maxItems": 4, "items": call}}, "calls")
	add("final", JSON{
		"answer_markdown": JSON{"type": "string", "minLength": 1, "maxLength": 30000},
		"fact_ids":        JSON{"type": "array", "maxItems": 50, "items": uuid},
		"result_refs":     JSON{"type": "array", "maxItems": 20, "items": resultRef},
	}, "answer_markdown", "fact_ids")
	add("request_input", JSON{
		"field":  JSON{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
		"prompt": JSON{"type": "string", "minLength": 1, "maxLength": 1000}, "input_schema": inputSchema,
	}, "field", "prompt")
	add("search_capabilities", JSON{"query": JSON{"type": "string", "minLength": 1, "maxLength": 300}}, "query")
	for _, kind := range []string{"read_skill", "inspect_capability"} {
		add(kind, JSON{"name": JSON{"type": "string", "minLength": 1, "maxLength": 330}}, "name")
	}
	add("inspect_fact", JSON{"fact_id": uuid, "path": path}, "fact_id", "path")
	return tools
}

func decisionOutputToolPrompt(prompt string) string {
	lines := strings.Split(prompt, "\n")
	retained := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "Return one JSON object with schema") ||
			strings.HasPrefix(line, "Skill: {") ||
			strings.HasPrefix(line, "Return exactly one raw JSON object.") {
			continue
		}
		line = strings.ReplaceAll(line, "Return exactly one raw JSON object without Markdown or code fences.", "Submit exactly one matching decision output tool.")
		line = strings.ReplaceAll(line, "agenstra.decision.v1 JSON decision", "typed agenstra.decision.v1 decision")
		retained = append(retained, line)
	}
	bindings := decisionOutputFieldBindings()
	return strings.Join(retained, "\n") + "\nSubmit exactly one typed decision by calling the matching submit_<kind> output tool. The selected tool name identifies the decision kind. Arguments contain only the fields declared by that tool, with native JSON arrays and objects; omit schema and kind. The adapter supplies the fixed schema and kind from the registered tool name. This replaces content-only JSON output. Output tools do not execute business operations; tool_batch is a proposed decision that the runtime will separately validate and authorize.\nUse the exact output-tool field names and native types below. Do not substitute aliases such as answer, response, content or text for answer_markdown.\n" + strings.Join(bindings, "\n")
}

func decisionOutputFieldBindings() []string {
	bindings := []string{}
	for _, tool := range decisionOutputTools() {
		function := tool.(JSON)["function"].(JSON)
		parameters := function["parameters"].(JSON)
		properties := parameters["properties"].(JSON)
		required := []string{}
		for _, field := range parameters["required"].([]string) {
			typ, _ := properties[field].(JSON)["type"].(string)
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
		bindings = append(bindings, function["name"].(string)+": required "+strings.Join(required, ", ")+"; allowed field names "+strings.Join(allowed, ", "))
	}
	return bindings
}

// decisionOutputToolBody selects the typed output channel without coercing values.
// Local decision and runtime validation still follow before any capability executes.
func decisionOutputToolBody(calls []decisionOutputToolCall) ([]byte, string) {
	if len(calls) != 1 || calls[0].Type != "function" {
		return nil, "model_output_tool_calls_invalid"
	}
	kind := strings.TrimPrefix(calls[0].Function.Name, "submit_")
	switch calls[0].Function.Name {
	case "submit_tool_batch", "submit_final", "submit_request_input", "submit_search_capabilities", "submit_read_skill", "submit_inspect_capability", "submit_inspect_fact":
	default:
		return nil, "model_output_tool_calls_invalid"
	}
	raw := []byte(calls[0].Function.Arguments)
	if detail := modelJSONFormatError(raw); detail != "" {
		return nil, detail
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil || fields["kind"] != nil || fields["schema"] != nil {
		return nil, "model_decision_schema_invalid"
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
