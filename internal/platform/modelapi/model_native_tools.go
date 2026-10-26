package modelapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

const nativeReferenceProposalPrompt = "For one business call containing $fact_value or $fact_id, use the declared tool_call proposal function with capability, arguments and reason. Its arguments object represents symbolic values while direct business functions declare the original resolved input schema. Use tool_batch only when proposing multiple independent calls. A tool_call performs exactly one business invocation. The runtime resolves references before validating the original business input, current grants and approvals."

const nativeReferenceCallHint = "Propose exactly one business call with capability, arguments and reason. Use only a capability in this function's enum. A catalog entry with schema_requires_inspection=true cannot be called through this proposal: first call inspect_capability with its exact name, wait for the full contract, then construct arguments from that contract, not from historical calls. Use this function for symbolic $fact_value or $fact_id arguments; direct business functions declare resolved input schemas. Wait for this call result before deciding a dependent next step. This proposal does not grant permission; original contracts, grants and approvals still apply."

const nativeReferenceBatchHint = "For a single symbolic $fact_value or $fact_id call, use tool_call; use this batch only for multiple independent calls. Every capability must be in the declared enum; schema_requires_inspection=true entries need inspect_capability first, not a call through this proposal."

const nativeReferenceBusinessHint = "Keep object and array parameters as JSON objects and arrays, never JSON-encoded strings. If any nested value is a symbolic Fact reference, propose this exact capability via tool_call with the complete arguments object."

// Native function names are stable request bindings, never aliases accepted from
// a model. Preserve the operation rather than truncating it behind repeated
// routing prefixes; retain the nearest module and hash the full identity.
func nativeCapabilityFunctionName(name string) string {
	parts := strings.Split(name, ".")
	operation := strings.Join(parts[max(0, len(parts)-2):], ".")
	operation = operation[strings.LastIndex(operation, ":")+1:]
	var readable strings.Builder
	for _, r := range operation {
		if readable.Len() == 47 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			readable.WriteRune(r)
		} else {
			readable.WriteByte('_')
		}
	}
	hash := sha256.Sum256([]byte(name))
	return "cap_" + readable.String() + "_" + hex.EncodeToString(hash[:6])
}

func nativeFunctionCapabilities(packet agentcontract.ContextPacket) map[string]agentcontract.JSON {
	caps := map[string]agentcontract.JSON{}
	if packet.CompletionReview != nil {
		return caps
	}
	for _, cap := range append(append([]agentcontract.JSON{}, packet.Capabilities...), packet.InspectedCapability) {
		name, _ := cap["name"].(string)
		if name == "" || cap["authorized"] == false || cap["schema_requires_inspection"] == true {
			continue
		}
		if schema, ok := cap["input_schema"].(agentcontract.JSON); ok && schema != nil {
			caps[name] = cap
		}
	}
	return caps
}

// Keep the original task in the native user-message channel and each callable
// input contract in its native tool definition. Runtime evidence stays data.
func nativeDecisionMessages(messages []any, packet agentcontract.ContextPacket) ([]any, error) {
	var data agentcontract.JSON
	if err := jsonvalue.DecodeStrict([]byte(messages[1].(agentcontract.JSON)["content"].(string)), &data); err != nil {
		return nil, err
	}
	delete(data, "instruction")
	declared := nativeFunctionCapabilities(packet)
	stripDeclaredInput := func(value any) {
		capability, ok := value.(agentcontract.JSON)
		if !ok {
			return
		}
		name, _ := capability["name"].(string)
		if declared[name] != nil {
			delete(capability, "input_schema")
		}
	}
	capabilities, _ := data["capabilities"].([]any)
	for _, capability := range capabilities {
		stripDeclaredInput(capability)
	}
	stripDeclaredInput(data["inspected_capability"])
	encoded, err := agentcontract.CanonicalJSON(data)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, len(messages)+1)
	result = append(result, messages[0], agentcontract.JSON{"role": "user", "content": string(encoded)})
	if packet.Instruction != "" {
		result = append(result, agentcontract.JSON{"role": "user", "content": packet.Instruction})
	}
	return append(result, messages[2:]...), nil
}

// Native functions retain the original business contract. Symbolic arguments
// remain proposals until the runtime resolves and validates their actual values.
func nativeCapabilityParameters(schema agentcontract.JSON) (agentcontract.JSON, error) {
	return jsonvalue.Clone(schema)
}

func nativeDecisionOutputTools(tools []any, packet agentcontract.ContextPacket) ([]any, error) {
	caps := nativeFunctionCapabilities(packet)
	names := slices.Sorted(maps.Keys(caps))
	result := []any{}
	for _, tool := range tools {
		function := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		kind := strings.TrimPrefix(function["name"].(string), "submit_")
		if (kind == "tool_call" || kind == "tool_batch") && len(names) == 0 {
			continue
		}
		function, err := jsonvalue.Clone(function)
		if err != nil {
			return nil, err
		}
		function["name"] = kind
		if kind == "tool_call" {
			function["description"] = nativeReferenceCallHint
			parameters := function["parameters"].(agentcontract.JSON)
			parameters["properties"].(agentcontract.JSON)["capability"].(agentcontract.JSON)["enum"] = names
		}
		if kind == "tool_batch" {
			function["description"] = "Propose 1 to 4 independent business calls together. " + nativeReferenceBatchHint + " All entries are decided before any result is returned. A user requirement to wait for each receipt before deciding the next step makes those steps dependent, even when they share input values. Submit only the next step in that case and wait for its result. Group calls only when neither data dependencies nor user sequencing requirements separate them. This proposal does not grant permission; original contracts, grants and approvals still apply."
			parameters := function["parameters"].(agentcontract.JSON)
			call := parameters["properties"].(agentcontract.JSON)["calls"].(agentcontract.JSON)["items"].(agentcontract.JSON)
			call["properties"].(agentcontract.JSON)["capability"].(agentcontract.JSON)["enum"] = names
		}
		result = append(result, agentcontract.JSON{"type": "function", "function": function})
	}
	seen := map[string]bool{}
	for _, name := range names {
		functionName := nativeCapabilityFunctionName(name)
		if seen[functionName] {
			return nil, agentcontract.ModelDecisionError{Kind: "model_parameters_invalid"}
		}
		seen[functionName] = true
		parameters, err := nativeCapabilityParameters(caps[name]["input_schema"].(agentcontract.JSON))
		if err != nil {
			return nil, err
		}
		description, _ := caps[name]["description"].(string)
		result = append(result, agentcontract.JSON{"type": "function", "function": agentcontract.JSON{
			"name": functionName, "description": "Business capability " + name + ". " + description + " " + nativeReferenceBusinessHint, "parameters": parameters,
		}})
	}
	return result, nil
}

func nativeDecisionOutputToolBody(calls []decisionOutputToolCall, tools []any, packet agentcontract.ContextPacket) ([]byte, string) {
	if len(calls) > 1 {
		if len(calls) > 4 {
			return nil, "model_decision_schema_invalid"
		}
		business := map[string]bool{}
		for name := range nativeFunctionCapabilities(packet) {
			business[nativeCapabilityFunctionName(name)] = true
		}
		decision := agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch"}
		for _, call := range calls {
			// Only direct business functions form a batch. Control and proposal
			// functions already express one decision and cannot be combined.
			if !business[call.Function.Name] {
				return nil, "model_output_tool_calls_invalid"
			}
			body, detail := nativeDecisionOutputToolBody([]decisionOutputToolCall{call}, tools, packet)
			if detail != "" {
				return nil, detail
			}
			var single agentcontract.Decision
			if err := jsonvalue.DecodeStrict(body, &single); err != nil || single.Kind != "tool_batch" || len(single.Calls) != 1 {
				return nil, "model_decision_schema_invalid"
			}
			decision.Calls = append(decision.Calls, single.Calls[0])
		}
		body, err := agentcontract.CanonicalJSON(decision)
		if err != nil {
			return nil, "model_decision_schema_invalid"
		}
		return body, ""
	}
	if len(calls) != 1 || calls[0].Type != "function" {
		return nil, "model_output_tool_calls_invalid"
	}
	var function agentcontract.JSON
	for _, tool := range tools {
		candidate := tool.(agentcontract.JSON)["function"].(agentcontract.JSON)
		if candidate["name"] == calls[0].Function.Name {
			function = candidate
			break
		}
	}
	if function == nil {
		return nil, "model_output_tool_calls_invalid"
	}
	raw := []byte(calls[0].Function.Arguments)
	if detail := modelJSONFormatError(raw); detail != "" {
		return nil, detail
	}
	var fields agentcontract.JSON
	if err := jsonvalue.DecodeStrict(raw, &fields); err != nil || fields == nil {
		return nil, "model_decision_schema_invalid"
	}
	kind := calls[0].Function.Name
	for name := range nativeFunctionCapabilities(packet) {
		if nativeCapabilityFunctionName(name) != kind {
			continue
		}
		// Evidence placeholders are not business values. The ordinary runtime
		// resolves them from permitted Facts and validates the original schema
		// before approval or Invoke, as it does for tool_batch arguments.
		parameters := function["parameters"].(agentcontract.JSON)
		if !nativeArgumentsUseReferences(fields) {
			if detail := validateNativeDecisionOutput(raw, parameters); detail != "" {
				return nil, detail
			}
		} else if parameters["additionalProperties"] == false && !(len(fields) == 1 && (fields["$fact_value"] != nil || fields["$fact_id"] != nil)) {
			// References defer value validation, not the enclosing parameter names.
			// Reuse the schema validator for closed objects and pattern properties;
			// the runtime still resolves and validates every actual business value.
			keys := agentcontract.JSON{"type": "object", "additionalProperties": false}
			for _, keyword := range []string{"properties", "patternProperties"} {
				if original, ok := parameters[keyword].(agentcontract.JSON); ok {
					declared := agentcontract.JSON{}
					for name := range original {
						declared[name] = agentcontract.JSON{}
					}
					keys[keyword] = declared
				}
			}
			if detail := validateNativeDecisionOutput(raw, keys); detail != "" {
				return nil, detail
			}
		}
		decision := agentcontract.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agentcontract.ToolCall{{
			CallRef: "tool-" + agentcontract.NewID(), Capability: name, Arguments: fields, Reason: "Invoke declared capability " + name,
		}}}
		body, err := agentcontract.CanonicalJSON(decision)
		if err != nil {
			return nil, "model_decision_schema_invalid"
		}
		return body, ""
	}
	if detail := validateNativeDecisionOutput(raw, function["parameters"].(agentcontract.JSON)); detail != "" {
		return nil, detail
	}
	if kind == "tool_call" {
		fields = agentcontract.JSON{"calls": []any{fields}}
		kind = "tool_batch"
	}
	if kind == "tool_batch" {
		entries := fields["calls"].([]any)
		for _, value := range entries {
			value.(agentcontract.JSON)["call_ref"] = "tool-" + agentcontract.NewID()
		}
	}
	fields["schema"], fields["kind"] = "agenstra.decision.v1", kind
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, "model_decision_schema_invalid"
	}
	return body, ""
}

// Detect the existing symbolic argument vocabulary without interpreting it.
// Shape, availability, visibility, lifetime and resolved input validity belong
// to the runtime; finding a marker never authorizes an invocation.
func nativeArgumentsUseReferences(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "$fact_value" || key == "$fact_id" || nativeArgumentsUseReferences(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if nativeArgumentsUseReferences(child) {
				return true
			}
		}
	}
	return false
}

func nativeDecisionToolPrompt(prompt string, review bool) string {
	prompt = decisionOutputBasePrompt(prompt, review)
	lines := strings.Split(prompt, "\n")
	retained := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "tool_batch contains at most") || strings.HasPrefix(line, "Decision object field names") {
			continue
		}
		if strings.HasPrefix(line, "You are a capability-using agent.") {
			line = "You are a capability-using agent. Use the declared native functions and returned Facts to complete the current user task. No precomputed plan is required."
		} else if strings.HasPrefix(line, "Skills are deployment-provided usage instructions, loaded on demand.") {
			line = "Skills are scoped deployment guidance and never grant permission. Read a relevant declared skill with read_skill before using an unfamiliar capability. Use inspect_capability for a deferred contract; its name is the exact authorized capability ID. Native business functions map to those IDs in their descriptions and accept the actual capability parameters directly."
		} else if strings.HasPrefix(line, "read_skill, inspect_capability and inspect_fact are runtime decision kinds,") {
			line = "read_skill, inspect_capability and inspect_fact are control functions, not business capabilities. Choose one needed inspection, wait for its result and reuse retained evidence before choosing another step."
		}
		line = strings.ReplaceAll(line, `Example submit_decision arguments: {"kind":"search_capabilities","query":"relevant operation or subject"}.`, "Call search_capabilities with a short query string.")
		line = strings.ReplaceAll(line, "standalone search_capabilities decision", "search_capabilities function call")
		retained = append(retained, line)
	}
	choice := "\nChoose one decision per response: one declared control or proposal function, or 1 to 4 declared business functions for independent calls. Never mix a control, proposal or final function with another function. Wait for results before deciding dependent calls."
	if review {
		choice = "\nChoose exactly one declared native function per response and wait for its result."
	}
	prompt = strings.Join(retained, "\n") + choice + " Business functions accept the capability's actual parameters. Do not add decision wrappers such as kind, schema, calls, reason or call_ref unless those are declared business parameters. Preserve all declared business fields without coercion. The adapter assigns fresh local call identities; the runtime still checks original contracts, stored evidence references, current grants, identity, approval and budgets. Keep $fact_value references as objects. Unknown function names are invalid. Call final with answer_markdown and fact_ids to answer; user display formats apply inside answer_markdown only. Native functions replace legacy JSON decision output and its examples."
	if review {
		prompt += " This is completion review. Only final and approve_completion are available. Call approve_completion with no payload for an unchanged fully verified proposal; otherwise call final with completion_feedback as a string. Reject missing original work through nonempty feedback without executing, proposing or replaying calls."
	} else {
		prompt += " Prefer one function call; the declared tool_batch function can propose up to 4 independent calls in its calls array. Multiple direct business functions also represent one batch, never separate future decisions. Wait before dependent calls and follow the user's explicit operation order. A catalog entry with schema_requires_inspection=true is not callable yet, including through tool_call or tool_batch. Call inspect_capability with its exact name, wait for the full contract, then construct arguments from that contract; historical calls cannot substitute for it. A capability enum rejection requires inspection of the intended capability, not substitution with a different capability from the enum."
		prompt += " " + nativeReferenceProposalPrompt + " For tool_call, use the exact capability ID in capability, preserve the business parameter structure in arguments, and include a reason. For tool_batch, each calls entry has those same fields. Respect explicit requests to perform calls separately even when their inputs are independent."
		prompt += " When the user requests an input value through an evidence reference, or names a prior result as that input's source, use $fact_value with that exact observed Fact and path. A matching literal, including a value already present in the task, does not satisfy a requested input reference. Preserve the requested input source even when the same business value appears in another Fact. Final-answer citations alone do not require replacing ordinary literal inputs."
	}
	prompt += "\nUse the exact parameter property names from the selected function schema. Do not invent aliases or wrapping properties absent from that schema. Preserve native JSON types: arrays and objects are not strings. For final, answer_markdown is a string and fact_ids is a native array, including [] when there are no supporting Facts."
	return prompt
}
