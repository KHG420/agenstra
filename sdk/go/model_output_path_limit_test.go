package agenstra_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKModelOutputWideDeclaredProjectionKeepsBoundedFields(t *testing.T) {
	for _, count := range []int{65, 111, 128, 129, 307, 311, 512, 513} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			properties := agenstra.JSON{}
			data := agenstra.JSON{"secret": "must-remain-private"}
			paths := make([][]string, count)
			for i := range count {
				name := fmt.Sprintf("field_%03d", i)
				properties[name] = agenstra.JSON{"type": "integer"}
				data[name] = i
				paths[i] = []string{name}
			}
			declaration := agenstra.JSON{"schema": "agenstra.rest-pack.v2", "name": "wide-records", "version": "1", "guidance": "Read declared fields", "base_url_env": "API_URL", "capabilities": []any{agenstra.JSON{
				"name": "records.read", "description": "Read declared fields", "method": "GET", "path": "/records", "effect": "read",
				"input_schema":  agenstra.JSON{"type": "object", "properties": agenstra.JSON{}, "additionalProperties": false},
				"output_schema": agenstra.JSON{"type": "object", "properties": properties, "additionalProperties": true},
				"model_output":  agenstra.JSON{"paths": paths},
			}}}
			raw, err := json.Marshal(declaration)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "pack.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			pack, err := agenstra.LoadRestPack(path, map[string]string{"API_URL": "http://localhost:1"}, nil)
			if count > 512 {
				if err == nil {
					if err := pack.Close(); err != nil {
						t.Error(err)
					}
					t.Fatal("oversized projection accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("actual declared field count rejected: %v", err)
			}
			defer func() {
				if err := pack.Close(); err != nil {
					t.Error(err)
				}
			}()
			runtime := &agenstra.AgentRuntime{Provider: pack, Grants: map[string]bool{"records.read": true}}
			state, err := runtime.NewState("Read the declared record", "")
			if err != nil {
				t.Fatal(err)
			}
			fact := agenstra.Fact{FactID: agenstra.NewID(), Value: agenstra.JSON{"data": data}, ModelOutput: &agenstra.ModelOutput{Paths: paths}, ReferenceScope: "durable"}
			state.Facts = []agenstra.Fact{fact}
			packet := runtime.Context(state)
			if len(packet.Facts) != 1 {
				t.Fatal("missing projected fact")
			}
			visible := packet.Facts[0].Value["data"].(map[string]any)
			if count <= 311 && len(visible) != count {
				t.Fatalf("selected fields lost: %d want %d", len(visible), count)
			}
			if count > 311 && (len(visible) == 0 || len(visible) >= count || len(packet.Facts[0].OmittedPaths) == 0) {
				t.Fatal("large authorized record lost its bounded preview and omission evidence")
			}
			for name, value := range visible {
				if value != data[name] {
					t.Fatalf("visible business field changed: %s", name)
				}
			}
			if _, exists := visible["secret"]; exists {
				t.Fatal("unselected private field exposed")
			}
			if fact.Value["data"].(map[string]any)["secret"] != "must-remain-private" {
				t.Fatal("complete evidence changed")
			}
		})
	}
}
