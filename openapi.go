package agenstra

import (
	"os"

	"github.com/KHG420/agenstra/internal/jsonvalue"
	"github.com/KHG420/agenstra/internal/openapi"
)

// ImportOpenAPI reads a complete JSON document and generates selected endpoint drafts.
// It does not infer approvals, idempotency guarantees or final job states.
func ImportOpenAPI(path, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc JSON
	if err := jsonvalue.DecodeStrict(raw, &doc); err != nil {
		return nil, err
	}
	return ImportOpenAPIDocument(doc, name, baseURLEnv, operations, effects, tokenEnv)
}

// ImportOpenAPIDocument validates selected operations and returns a REST manifest draft for review.
func ImportOpenAPIDocument(doc JSON, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (JSON, error) {
	return openapi.Draft(doc, name, baseURLEnv, operations, effects, tokenEnv)
}
