package capability

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPackModelOutputRejectsArrayIndexPaths(t *testing.T) {
	schema := agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"items": agentcontract.JSON{"type": "array", "items": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"id": agentcontract.JSON{"type": "string"}}}}}}
	manifest := agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1", "guidance": "Read", "base_url_env": "API_URL", "capabilities": []any{agentcontract.JSON{
		"name": "records.read", "description": "Read", "method": "GET", "path": "/records", "effect": "read",
		"input_schema": agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false}, "output_schema": schema,
		"model_output": agentcontract.JSON{"paths": []any{[]any{"items", "0", "id"}}},
	}}}
	if err := ValidatePackManifest(manifest, nil); err == nil {
		t.Fatal("manifest accepted partial array projection")
	}
}
