package service

func testRegistryManifest(name string) map[string]any {
	return map[string]any{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1.0.0", "guidance": "Use reviewed record data.", "base_url_env": "RECORDS_URL", "capabilities": []any{map[string]any{"name": name, "description": "Read a record", "method": "GET", "path": "/records/{record_id}", "effect": "read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "object", "properties": map[string]any{"record_id": map[string]any{"type": "string"}}, "required": []any{"record_id"}, "additionalProperties": false}}, "required": []any{"path"}, "additionalProperties": false}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}}}
}
