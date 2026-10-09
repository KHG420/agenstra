package openapi

import (
	"strings"
	"testing"
)

func TestOpenAPI30ExclusiveBoundRequiresNumericInclusiveBound(t *testing.T) {
	for _, bound := range []struct{ exclusive, inclusive string }{
		{"exclusiveMinimum", "minimum"}, {"exclusiveMaximum", "maximum"},
	} {
		_, err := convertOpenAPISchema(map[string]any{"openapi": "3.0.3"}, map[string]any{bound.exclusive: true})
		if err == nil || !strings.Contains(err.Error(), bound.exclusive) || !strings.Contains(err.Error(), bound.inclusive) {
			t.Fatalf("missing %s accepted: %v", bound.inclusive, err)
		}
	}
}
