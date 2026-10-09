package agent

import (
	localschema "github.com/KHG420/agenstra/internal/contract/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateLocalSchema compiles a local JSON Schema and optionally enforces the REST dialect.
func ValidateLocalSchema(schema JSON, strictREST bool) (*jsonschema.Schema, error) {
	return localschema.CompileLocal(schema, strictREST)
}

// ValidateSchema validates a finite JSON value against the compiled local schema.
func ValidateSchema(s *jsonschema.Schema, v any) error {
	return localschema.Validate(s, v)
}
