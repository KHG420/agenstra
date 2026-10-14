package agenstra_test

import (
	"encoding/json"
	"reflect"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKNullableProjectionPreservesGoScalars(t *testing.T) {
	for _, value := range []any{int(7), int64(9007199254740993), float64(1.25), json.Number("2.3"), false, nil} {
		fact := agenstra.Fact{FactID: agenstra.NewID(), ReferenceScope: "durable",
			Value:       agenstra.JSON{"data": agenstra.JSON{"optional": nil, "*": agenstra.JSON{"value": value, "secret": "private"}}},
			ModelOutput: &agenstra.ModelOutput{Paths: [][]string{{"*", "value"}, {"optional", "id"}}}}
		facts := map[string]agenstra.Fact{fact.FactID: fact}
		runtime := &agenstra.AgentRuntime{Provider: &recordProvider{}, Grants: map[string]bool{"records.get": true}}
		state, err := runtime.NewState("Inspect the declared literal field", "")
		if err != nil {
			t.Fatal(err)
		}
		state.Facts = []agenstra.Fact{fact}
		packet := runtime.Context(state)
		if len(packet.Facts) != 1 {
			t.Fatalf("missing public SDK context fact: %+v", packet.Facts)
		}
		if optional, present := packet.Facts[0].Value["data"].(map[string]any)["optional"]; !present || optional != nil {
			t.Fatal("explicit null ancestor lost in public context")
		}
		visible := packet.Facts[0].Value["data"].(map[string]any)["*"].(map[string]any)
		if !reflect.DeepEqual(visible, agenstra.JSON{"value": value}) {
			t.Fatalf("literal object field changed public model-view types or exposed private data: got %T(%v), want %T(%v)", visible["value"], visible, value, value)
		}
		got, err := agenstra.ResolveArgument(agenstra.JSON{"$fact_value": agenstra.JSON{
			"fact_id": fact.FactID, "path": []any{"data", "*", "value"},
		}}, facts, "", true)
		// References already normalize JSON numbers by their existing contract;
		// only the model view above retains Go boundary types.
		gotJSON, marshalErr := agenstra.CanonicalJSON(got)
		wantJSON, wantErr := agenstra.CanonicalJSON(value)
		if err != nil || marshalErr != nil || wantErr != nil || string(gotJSON) != string(wantJSON) {
			t.Errorf("literal object reference changed JSON meaning: got %s want %s errors %v/%v/%v", gotJSON, wantJSON, err, marshalErr, wantErr)
		}
		ancestor, err := agenstra.ResolveArgument(agenstra.JSON{"$fact_value": agenstra.JSON{
			"fact_id": fact.FactID, "path": []any{"data", "*"},
		}}, facts, "", true)
		ancestorJSON, marshalErr := agenstra.CanonicalJSON(ancestor)
		wantJSON, wantErr = agenstra.CanonicalJSON(agenstra.JSON{"value": value})
		if err != nil || marshalErr != nil || wantErr != nil || string(ancestorJSON) != string(wantJSON) {
			t.Fatalf("literal star ancestor changed values or exposed private fields: %s want %s errors %v/%v/%v", ancestorJSON, wantJSON, err, marshalErr, wantErr)
		}
		_, err = agenstra.ResolveArgument(agenstra.JSON{"$fact_value": agenstra.JSON{
			"fact_id": fact.FactID, "path": []any{"data", "*", "secret"},
		}}, facts, "", true)
		if err == nil {
			t.Fatal("literal star projection exposed an unselected field")
		}
	}
}
