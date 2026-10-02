package agenstra

import (
	"fmt"
	"strings"
)

// ModelOutput limits which fields of a provider result may enter model context
// or be read through a Fact reference. Paths are relative to the result data.
// A nil ModelOutput keeps the existing full-result behavior; an empty Paths
// list makes the result opaque to the model.
type ModelOutput struct {
	Paths [][]string `json:"paths"`
}

func validateModelOutput(output *ModelOutput) error {
	if output == nil {
		return nil
	}
	if output.Paths == nil || len(output.Paths) > 64 {
		return fmt.Errorf("model_output paths must be an array of at most 64 paths")
	}
	seen := map[string]bool{}
	for _, path := range output.Paths {
		if len(path) == 0 || len(path) > 16 {
			return fmt.Errorf("model_output path must contain 1..16 object fields")
		}
		key := ""
		for _, field := range path {
			if field == "" || len(field) > 128 {
				return fmt.Errorf("model_output path contains an invalid field")
			}
			key += fmt.Sprintf("%d:%s", len(field), field)
		}
		if seen[key] {
			return fmt.Errorf("model_output paths must be distinct")
		}
		seen[key] = true
	}
	return nil
}

func validateModelOutputSchema(output *ModelOutput, schema JSON) error {
	if output == nil {
		return nil
	}
	for _, path := range output.Paths {
		var current JSON = schema
		for _, field := range path {
			for depth := 0; depth < 16; depth++ {
				ref, ok := current["$ref"].(string)
				if !ok {
					break
				}
				if !strings.HasPrefix(ref, "#/$defs/") {
					return fmt.Errorf("model_output path requires a local object schema")
				}
				defs, _ := schema["$defs"].(map[string]any)
				current, _ = defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
				if current == nil {
					return fmt.Errorf("model_output path has an unresolved schema reference")
				}
			}
			if current["type"] != "object" {
				return fmt.Errorf("model_output path may only traverse object fields")
			}
			properties, _ := current["properties"].(map[string]any)
			current, _ = properties[field].(map[string]any)
			if current == nil {
				return fmt.Errorf("model_output path is not declared in output_schema")
			}
		}
	}
	return nil
}

func projectedModelData(data JSON, output *ModelOutput) JSON {
	if output == nil {
		return data
	}
	visible := JSON{}
	if validateModelOutput(output) != nil {
		return visible
	}
	for _, path := range output.Paths {
		var value any = data
		for _, field := range path {
			object, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			var exists bool
			value, exists = object[field]
			if !exists {
				value = nil
				break
			}
		}
		if value == nil {
			// A declared null is visible, while a missing path is not.
			if !modelPathExists(data, path) {
				continue
			}
		}
		cursor := visible
		for _, field := range path[:len(path)-1] {
			next, ok := cursor[field].(map[string]any)
			if !ok {
				next = JSON{}
				cursor[field] = next
			}
			cursor = next
		}
		cursor[path[len(path)-1]] = value
	}
	return visible
}

func modelPathExists(data JSON, path []string) bool {
	var value any = data
	for _, field := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		var exists bool
		value, exists = object[field]
		if !exists {
			return false
		}
	}
	return true
}

func modelFactValue(fact Fact) JSON {
	if fact.ModelOutput == nil {
		return fact.Value
	}
	data, _ := fact.Value["data"].(map[string]any)
	return JSON{"data": projectedModelData(data, fact.ModelOutput)}
}
