package capability

import (
	"os"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/ext/openapi"
)

// ImportOpenAPI reads a complete JSON document and generates selected endpoint drafts.
// It does not infer approvals, idempotency guarantees or final job states.
func ImportOpenAPI(path, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (agentcontract.JSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc agentcontract.JSON
	if err := jsonvalue.DecodeStrict(raw, &doc); err != nil {
		return nil, err
	}
	return ImportOpenAPIDocument(doc, name, baseURLEnv, operations, effects, tokenEnv)
}

// ImportOpenAPIDocument validates selected operations and returns a REST manifest draft for review.
func ImportOpenAPIDocument(doc agentcontract.JSON, name, baseURLEnv string, operations []string, effects map[string]string, tokenEnv string) (agentcontract.JSON, error) {
	return openapi.Draft(doc, name, baseURLEnv, operations, effects, tokenEnv)
}
