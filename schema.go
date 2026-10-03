package agenstra

import (
	localschema "github.com/KHG420/agenstra/internal/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func validateLocalSchema(schema JSON, strictREST bool) (*jsonschema.Schema, error) {
	return localschema.CompileLocal(schema, strictREST)
}
func validateSchema(s *jsonschema.Schema, v any) error {
	return localschema.Validate(s, v)
}
