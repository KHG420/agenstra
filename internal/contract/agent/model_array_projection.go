package agent

import (
	"bytes"
	"encoding/json"
)

// A literal object field named * does not opt existing SDK data into JSON
// normalization. Only a path that reaches an actual JSON array needs it.
func requiresArrayModelProjection(value any, path []string) bool {
	for _, field := range path {
		if object, ok := value.(map[string]any); ok {
			child, exists := object[field]
			if !exists {
				return false
			}
			value = child
			continue
		}
		if field == "*" {
			// Use JSON meaning here so typed SDK slices are recognized while
			// JSON string values such as []byte are not treated as arrays.
			raw, err := CanonicalJSON(value)
			return err == nil && len(raw) > 0 && raw[0] == '['
		}
		return false
	}
	return false
}

// A present null or empty object ancestor is distinct from an absent field.
// Leaf values already follow the ordinary object projection path.
func hasPreservedModelContainer(value any, path []string) bool {
	for _, field := range path {
		if value == nil {
			return true
		}
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if len(object) == 0 {
			return true
		}
		child, exists := object[field]
		if !exists {
			return false
		}
		value = child
	}
	return false
}

// projectedArrayModelData projects the JSON meaning of array paths. Normalizing
// this opt-in shape also accepts typed SDK slices without walking opaque Go state.
// Ordinary object paths retain their existing projection behavior.
func projectedArrayModelData(data JSON, output *ModelOutput) JSON {
	raw, err := CanonicalJSON(data)
	if err != nil {
		return JSON{}
	}
	var normalized JSON
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&normalized) != nil {
		return JSON{}
	}
	return projectedContainerModelData(normalized, output)
}

// Object-only nullable paths preserve existing SDK Go scalar values. Array
// paths call this with normalized JSON so typed slices have JSON semantics.
func projectedContainerModelData(data JSON, output *ModelOutput) JSON {
	visible := JSON{}
	for _, path := range output.Paths {
		selected, ok := modelProjectionPath(data, path)
		if ok {
			visible = mergeModelProjection(visible, selected).(map[string]any)
		}
	}
	return visible
}

func modelProjectionPath(value any, path []string) (any, bool) {
	if len(path) == 0 {
		return value, true
	}
	if value == nil {
		return nil, true
	}
	switch data := value.(type) {
	case map[string]any:
		if len(data) == 0 {
			return JSON{}, true
		}
		child, exists := data[path[0]]
		if !exists {
			return nil, false
		}
		selected, ok := modelProjectionPath(child, path[1:])
		if !ok {
			return nil, false
		}
		return JSON{path[0]: selected}, true
	case []any:
		if path[0] != "*" {
			return nil, false
		}
		items := make([]any, len(data))
		for i, child := range data {
			selected, ok := modelProjectionPath(child, path[1:])
			if ok {
				items[i] = selected
			} else if child == nil {
				// Preserve an explicit null member, never invent one for a missing field.
				items[i] = nil
			} else {
				items[i] = JSON{}
			}
		}
		return items, true
	default:
		return nil, false
	}
}

// mergeModelProjection combines paths selected from the same source. Matching
// array positions retain all authorized fields, including explicit parent grants.
func mergeModelProjection(left, right any) any {
	switch target := left.(type) {
	case map[string]any:
		if selected, ok := right.(map[string]any); ok {
			for key, value := range selected {
				if previous, exists := target[key]; exists {
					target[key] = mergeModelProjection(previous, value)
				} else {
					target[key] = value
				}
			}
			return target
		}
	case []any:
		if selected, ok := right.([]any); ok && len(target) == len(selected) {
			for i, value := range selected {
				target[i] = mergeModelProjection(target[i], value)
			}
			return target
		}
	}
	return right
}
