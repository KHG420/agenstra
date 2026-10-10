package react

import (
	"strings"
	"testing"
	"unicode/utf8"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func taggedInputSchema(keyword string) agentcontract.JSON {
	return agentcontract.JSON{"type": "object", keyword: []any{
		agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
			"kind": agentcontract.JSON{"const": "list"}, "arguments": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		}, "required": []any{"kind", "arguments"}, "additionalProperties": false},
		agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
			"kind": agentcontract.JSON{"const": "update"}, "arguments": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "integer", "minimum": 1}}, "required": []any{"id"}, "additionalProperties": false},
		}, "required": []any{"kind", "arguments"}, "additionalProperties": false},
	}}
}

func TestInputValidationFeedbackUsesSelectedUnionVariant(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword, func(t *testing.T) {
			schema := taggedInputSchema(keyword)
			for _, args := range []agentcontract.JSON{
				{"kind": "update", "arguments": agentcontract.JSON{}},
				{"kind": "update", "arguments": agentcontract.JSON{"id": 0}},
			} {
				feedback := inputValidationFeedback(schema, args)
				if !strings.Contains(feedback, "id") || strings.Contains(feedback, "must be 'list'") || strings.Contains(feedback, "file://") {
					t.Fatal("feedback diagnosed an unrelated union branch", feedback)
				}
			}
			if feedback := inputValidationFeedback(schema, agentcontract.JSON{"kind": "update", "arguments": agentcontract.JSON{"id": 1}}); feedback != "" {
				t.Fatal("valid input received corrective feedback", feedback)
			}
		})
	}
}

func TestInputValidationFeedbackUnknownUnionTag(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword, func(t *testing.T) {
			feedback := inputValidationFeedback(taggedInputSchema(keyword), agentcontract.JSON{"kind": "private-input-marker", "arguments": agentcontract.JSON{"access_token": "private-input-marker"}})
			if !strings.Contains(feedback, `at "/kind"`) || !strings.Contains(feedback, "not supported by this capability") || strings.Contains(feedback, "private-input-marker") || strings.Contains(feedback, "must be 'list'") {
				t.Fatal("unknown tag was mistaken for an unrelated field error or leaked input", feedback)
			}
		})
	}
}

func TestInputValidationFeedbackRetainsRootAndAmbiguousUnionChecks(t *testing.T) {
	schema := taggedInputSchema("anyOf")
	schema["required"] = []any{"root_required"}
	if feedback := inputValidationFeedback(schema, agentcontract.JSON{"kind": "update", "arguments": agentcontract.JSON{"id": 1}}); !strings.Contains(feedback, "root_required") {
		t.Fatal("root constraint lost while selecting a branch", feedback)
	}
	duplicate := taggedInputSchema("oneOf")
	branches := duplicate["oneOf"].([]any)
	duplicate["oneOf"] = []any{branches[0], branches[0]}
	if feedback := inputValidationFeedback(duplicate, agentcontract.JSON{"kind": "list", "arguments": agentcontract.JSON{}}); !strings.Contains(feedback, "oneOf") {
		t.Fatal("ambiguous oneOf was hidden", feedback)
	}
	untagged := agentcontract.JSON{"anyOf": []any{agentcontract.JSON{"type": "string"}, agentcontract.JSON{"type": "integer"}}}
	if feedback := inputValidationFeedback(untagged, agentcontract.JSON{}); feedback == "" || strings.Contains(feedback, "not supported") {
		t.Fatal("ordinary union feedback was replaced", feedback)
	}
	missing := taggedInputSchema("anyOf")
	if feedback := inputValidationFeedback(missing, agentcontract.JSON{"arguments": agentcontract.JSON{}}); !strings.Contains(feedback, "kind") {
		t.Fatal("missing discriminator was hidden", feedback)
	}
}

func TestInputValidationFeedbackDoesNotChangeValidationOrSchema(t *testing.T) {
	schema := taggedInputSchema("anyOf")
	before, err := agentcontract.CanonicalJSON(schema)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := agentcontract.ValidateLocalSchema(schema, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []agentcontract.JSON{
		{"kind": "unknown", "arguments": agentcontract.JSON{}},
		{"kind": "update", "arguments": agentcontract.JSON{}},
		{"kind": "update", "arguments": agentcontract.JSON{"id": 1}},
	} {
		wantValid := agentcontract.ValidateSchema(compiled, args) == nil
		inputValidationFeedback(schema, args)
		if gotValid := agentcontract.ValidateSchema(compiled, args) == nil; gotValid != wantValid {
			t.Fatal("feedback changed original validation")
		}
	}
	after, err := agentcontract.CanonicalJSON(schema)
	if err != nil || string(before) != string(after) {
		t.Fatal("feedback mutated provider schema", err)
	}
}

func TestInputValidationFeedbackBoundsAndEscapesUnionTag(t *testing.T) {
	for _, name := range []string{"a~/b", strings.Repeat("名", 1200)} {
		schema := taggedInputSchema("anyOf")
		for _, raw := range schema["anyOf"].([]any) {
			branch := raw.(agentcontract.JSON)
			properties := branch["properties"].(agentcontract.JSON)
			properties[name] = properties["kind"]
			delete(properties, "kind")
			branch["required"] = []any{name, "arguments"}
		}
		feedback := inputValidationFeedback(schema, agentcontract.JSON{name: "unknown", "arguments": agentcontract.JSON{}})
		if utf8.RuneCountInString(feedback) > 1000 || !utf8.ValidString(feedback) || feedback == "" {
			t.Fatal("feedback lost its bound", feedback)
		}
		if name == "a~/b" && !strings.Contains(feedback, `at "/a~0~1b"`) {
			t.Fatal("discriminator pointer is not escaped", feedback)
		}
	}
}
