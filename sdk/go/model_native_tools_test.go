package agenstra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type nativeInventoryProvider struct {
	calls []struct {
		capability string
		id         any
		enabled    any
	}
}

func (*nativeInventoryProvider) Capabilities() map[string]agenstra.CapabilityDescription {
	return map[string]agenstra.CapabilityDescription{
		"inventory.list": {Name: "inventory.list", Version: "1", Description: "Read inventory ID", Effect: "read", Replay: "safe", ReferenceScope: "durable", InputSchema: agenstra.JSON{"type": "object", "additionalProperties": false}},
		"inventory.set": {Name: "inventory.set", Version: "1", Description: "Update the inventory enabled flag", Effect: "write", Replay: "idempotent", ReferenceScope: "durable", InputSchema: agenstra.JSON{"type": "object", "properties": agenstra.JSON{
			"id": agenstra.JSON{"type": "integer", "minimum": 1}, "enabled": agenstra.JSON{"type": "boolean"}, "comment": agenstra.JSON{"type": "string"},
		}, "required": []string{"id", "enabled"}, "additionalProperties": false,
			"if": agenstra.JSON{"properties": agenstra.JSON{"id": agenstra.JSON{"const": 1}}}, "then": agenstra.JSON{"required": []string{"comment"}}}},
	}
}
func (*nativeInventoryProvider) Skills() map[string]agenstra.Skill {
	return map[string]agenstra.Skill{}
}
func (*nativeInventoryProvider) SystemPrompt() string {
	return "Use the existing inventory operations."
}
func (*nativeInventoryProvider) Close() error { return nil }
func (p *nativeInventoryProvider) Invoke(_ context.Context, name string, args agenstra.JSON, _ *agenstra.InvocationContext) (agenstra.CapabilityResult, error) {
	p.calls = append(p.calls, struct {
		capability string
		id         any
		enabled    any
	}{name, args["id"], args["enabled"]})
	return agenstra.CapabilityResult{Data: agenstra.JSON{"id": 7, "enabled": name == "inventory.set"}, ReferenceScope: "durable"}, nil
}

func nativeSDKFunction(t *testing.T, payload agenstra.JSON, capability string) string {
	t.Helper()
	for _, raw := range payload["tools"].([]any) {
		function := raw.(map[string]any)["function"].(map[string]any)
		if strings.HasPrefix(function["description"].(string), "Business capability "+capability+". ") {
			return function["name"].(string)
		}
	}
	t.Error("authorized native function missing", capability)
	return "missing"
}

func nativeSDKReply(t *testing.T, w http.ResponseWriter, function string, args agenstra.JSON) {
	t.Helper()
	raw, err := agenstra.CanonicalJSON(args)
	if err != nil {
		t.Error(err)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(agenstra.JSON{"choices": []any{agenstra.JSON{"finish_reason": "tool_calls", "message": agenstra.JSON{"tool_calls": []any{agenstra.JSON{"type": "function", "function": agenstra.JSON{"name": function, "arguments": string(raw)}}}}}}}); err != nil {
		t.Error(err)
	}
}

func TestSDKNativeToolsResolveTypedEvidenceBeforeBusinessExecution(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "proposal function"
		if native {
			name = "native function"
		}
		t.Run(name, func(t *testing.T) {
			for _, durable := range []bool{false, true} {
				path := "transient runtime"
				if durable {
					path = "durable host"
				}
				t.Run(path, func(t *testing.T) { testNativeSDKReferenceExecution(t, native, durable) })
			}
		})
	}
}

func testNativeSDKReferenceExecution(t *testing.T, native, durable bool) {
	provider := &nativeInventoryProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload agenstra.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		messages := payload["messages"].([]any)
		var packet agenstra.ContextPacket
		if err := json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if packet.Schema == "" && payload["tools"] == nil {
			if err := json.NewEncoder(w).Encode(agenstra.JSON{"choices": []any{agenstra.JSON{"message": agenstra.JSON{"content": `{"proposals":[]}`}}}}); err != nil {
				t.Error(err)
			}
			return
		}
		switch {
		case packet.CompletionReview != nil:
			nativeSDKReply(t, w, "approve_completion", agenstra.JSON{})
		case len(packet.Facts) == 0:
			nativeSDKReply(t, w, nativeSDKFunction(t, payload, "inventory.list"), agenstra.JSON{})
		case len(packet.Facts) == 1:
			args := agenstra.JSON{"id": agenstra.JSON{"$fact_value": agenstra.JSON{"fact_id": packet.Facts[0].FactID, "path": []any{"data", "id"}}}, "enabled": true}
			if native {
				nativeSDKReply(t, w, nativeSDKFunction(t, payload, "inventory.set"), args)
			} else {
				nativeSDKReply(t, w, "tool_batch", agenstra.JSON{"calls": []any{agenstra.JSON{"capability": "inventory.set", "arguments": args, "reason": "Use the observed inventory ID"}}})
			}
		default:
			savedCalls := 0
			for _, item := range messages {
				message := item.(map[string]any)
				if message["role"] != "assistant" || message["tool_calls"] == nil {
					continue
				}
				savedCalls++
				call := message["tool_calls"].([]any)[0].(map[string]any)
				function := call["function"].(map[string]any)
				if function["name"] != "tool_batch" {
					t.Error("saved unresolved call was encoded against a literal-only function", function["name"])
				}
				var arguments agenstra.JSON
				if json.Unmarshal([]byte(function["arguments"].(string)), &arguments) != nil {
					t.Error("saved proposal arguments are invalid")
					continue
				}
				entries, ok := arguments["calls"].([]any)
				if !ok || len(entries) != 1 || entries[0].(map[string]any)["capability"] != "inventory.set" {
					t.Error("saved reference proposal lost its original capability", arguments)
					continue
				}
				original := agenstra.JSON{"id": agenstra.JSON{"$fact_value": agenstra.JSON{"fact_id": packet.Facts[0].FactID, "path": []any{"data", "id"}}}, "enabled": true}
				want, wantErr := agenstra.CanonicalJSON(original)
				got, gotErr := agenstra.CanonicalJSON(entries[0].(map[string]any)["arguments"])
				if wantErr != nil || gotErr != nil || string(got) != string(want) {
					t.Error("saved assistant history replaced the original reference", arguments, wantErr, gotErr)
				}
			}
			if savedCalls != 1 {
				t.Error("saved assistant history did not contain exactly one prior reference call", savedCalls)
			}
			ids := []string{}
			for _, fact := range packet.Facts {
				ids = append(ids, fact.FactID)
			}
			nativeSDKReply(t, w, "final", agenstra.JSON{"answer_markdown": "Updated inventory 7.", "fact_ids": ids})
		}
	}))
	defer server.Close()
	model, err := agenstra.NewHTTPJSONDecisionModel("test", server.URL, "fixture", time.Second*5, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	model.DecisionOutputMode = "native_tools"
	runtime := &agenstra.AgentRuntime{Provider: provider, Model: model, Grants: map[string]bool{"inventory.list": true, "inventory.set": true}, MaxModelRounds: 6}
	instruction := "First read the inventory ID, then enable it using the returned evidence."
	var result agenstra.RunResult
	if durable {
		store, storeErr := agenstra.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		defer func() {
			if closeErr := store.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		if initErr := store.Initialize(); initErr != nil {
			t.Fatal(initErr)
		}
		host := agenstra.NewAgentHost(store,
			func(context.Context, string, string) (agenstra.CapabilityProvider, error) { return provider, nil }, model,
			func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
				return agenstra.ExecutionPolicy{GrantedCapabilities: runtime.Grants, AllowModelData: true}, nil
			})
		run, createErr := host.Create(t.Context(), "owner", "inventory", instruction, "")
		if createErr != nil {
			t.Fatal(createErr)
		}
		run, err = host.Drive(t.Context(), run.RunID, "owner")
		if err != nil {
			t.Fatal(err)
		}
		state, restoreErr := host.Restore(run)
		if restoreErr != nil {
			t.Fatal(restoreErr)
		}
		result = runtime.Result(state)
	} else {
		result, err = runtime.Run(t.Context(), instruction)
	}
	if err != nil || result.Status != "completed" || len(provider.calls) != 2 || provider.calls[0].capability != "inventory.list" || provider.calls[1].capability != "inventory.set" {
		t.Fatal("native transport did not retain the ordinary execution path", result, provider.calls, err)
	}
	id, err := agenstra.CanonicalJSON(provider.calls[1].id)
	if err != nil || string(id) != "7" || provider.calls[1].enabled != true {
		t.Fatal("reference was not resolved before invoking the provider", provider.calls, err)
	}
}

func TestSDKNativeToolsCannotBypassApprovalOrRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		approval     bool
		revoke       bool
	}{{name: "approval", status: "needs_approval", approval: true}, {name: "revoked grant", status: "needs_authorization", revoke: true}} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &nativeInventoryProvider{}
			allowed := true
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload agenstra.JSON
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				function := nativeSDKFunction(t, payload, "inventory.set")
				if tc.revoke {
					mu.Lock()
					allowed = false
					mu.Unlock()
				}
				nativeSDKReply(t, w, function, agenstra.JSON{"id": 7, "enabled": true})
			}))
			defer server.Close()
			model, err := agenstra.NewHTTPJSONDecisionModel("test", server.URL, "fixture", time.Second*5, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			model.DecisionOutputMode = "native_tools"
			store, err := agenstra.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := store.Initialize(); err != nil {
				t.Fatal(err)
			}
			host := agenstra.NewAgentHost(store,
				func(context.Context, string, string) (agenstra.CapabilityProvider, error) { return provider, nil }, model,
				func(context.Context, string, string) (agenstra.ExecutionPolicy, error) {
					mu.Lock()
					defer mu.Unlock()
					return agenstra.ExecutionPolicy{GrantedCapabilities: map[string]bool{"inventory.set": allowed}, ApprovalCapabilities: map[string]bool{"inventory.set": tc.approval}, AllowModelData: true}, nil
				})
			run, err := host.Create(t.Context(), "owner", "inventory", "Enable inventory 7", "")
			if err != nil {
				t.Fatal(err)
			}
			run, err = host.Drive(t.Context(), run.RunID, "owner")
			if err != nil || run.Status != tc.status || len(provider.calls) != 0 {
				t.Fatal("native declaration bypassed live host policy", run.Status, provider.calls, err)
			}
		})
	}
}
