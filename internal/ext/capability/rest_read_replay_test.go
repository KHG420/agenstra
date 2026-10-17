package capability

import (
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestRESTReadReplayUsesReviewedEffect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, effect, header, want string
	}{
		{"get read", "GET", "read", "", "safe"},
		{"post read", "POST", "read", "", "safe"},
		{"put read", "PUT", "read", "", "safe"},
		{"patch read", "PATCH", "read", "", "safe"},
		{"delete read", "DELETE", "read", "", "safe"},
		{"get write", "GET", "write", "", "never"},
		{"post write", "POST", "write", "", "never"},
		{"post destructive", "POST", "destructive", "", "never"},
		{"post compute", "POST", "compute", "", "never"},
		{"get read explicit binding", "GET", "read", "Idempotency-Key", "safe"},
		{"post read explicit binding", "POST", "read", "Idempotency-Key", "idempotent"},
		{"post write explicit binding", "POST", "write", "Idempotency-Key", "idempotent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			endpoint := agentcontract.JSON{"name": "records.query", "description": "Reviewed capability",
				"method": tc.method, "path": "/query", "effect": tc.effect,
				"input_schema":  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
				"output_schema": agentcontract.JSON{"type": "object"}}
			if tc.header != "" {
				endpoint["idempotency_header"] = tc.header
			}
			path := writeTestManifest(t, agentcontract.JSON{"schema": "agenstra.rest-pack.v2", "name": "records",
				"version": "1", "guidance": "Use reviewed records", "base_url_env": "API_URL", "capabilities": []any{endpoint}})
			pack, err := LoadRestPack(path, map[string]string{"API_URL": "http://example.invalid"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			capability := pack.Capabilities()["records.query"]
			if capability.Effect != tc.effect || capability.Replay != tc.want {
				t.Fatalf("effect=%s replay=%s; want %s/%s", capability.Effect, capability.Replay, tc.effect, tc.want)
			}
		})
	}
}
