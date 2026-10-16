package agenstra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

type nativeReferenceProvider struct {
	nativeInventoryProvider
	value   any
	expired bool
}

func (p *nativeReferenceProvider) Invoke(ctx context.Context, name string, args agenstra.JSON, inv *agenstra.InvocationContext) (agenstra.CapabilityResult, error) {
	result, err := p.nativeInventoryProvider.Invoke(ctx, name, args, inv)
	if name == "inventory.list" {
		result.Data["id"] = p.value
		if p.expired {
			expired := time.Now().Add(-time.Hour)
			result.ExpiresAt = &expired
		}
	}
	return result, err
}

func TestSDKNativeReferenceProposalsRespectRuntimeValidationAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, code, status        string
		value                     any
		mutate                    string
		expired, approval, revoke bool
		batch, single             bool
	}{
		{name: "resolved type", value: "7", code: "capability_input_invalid"},
		{name: "resolved conditional", value: 1, code: "capability_input_invalid"},
		{name: "resolved minimum", value: 0, code: "capability_input_invalid"},
		{name: "unrelated invalid literal", value: 7, mutate: "literal", code: "capability_input_invalid"},
		{name: "unknown business property", value: 7, mutate: "extra", code: "capability_input_invalid"},
		{name: "malformed reference", value: 7, mutate: "shape", code: "fact_reference_invalid"},
		{name: "unknown fact", value: 7, mutate: "unknown", code: "fact_reference_not_available"},
		{name: "invalid fact path", value: 7, mutate: "path", code: "fact_reference_path_invalid"},
		{name: "expired fact", value: 7, expired: true, code: "fact_reference_expired"},
		{name: "fact ID is not business ID", value: 7, mutate: "id-reference", code: "capability_input_invalid"},
		{name: "invalid input before approval", value: "7", approval: true, code: "capability_input_invalid"},
		{name: "valid input requires approval", value: 7, approval: true, status: "needs_approval"},
		{name: "live grant revoked", value: 7, revoke: true, status: "needs_authorization"},
		{single: true, name: "single resolved type", value: "7", code: "capability_input_invalid"},
		{single: true, name: "single resolved conditional", value: 1, code: "capability_input_invalid"},
		{single: true, name: "single resolved minimum", value: 0, code: "capability_input_invalid"},
		{single: true, name: "single unrelated invalid literal", value: 7, mutate: "literal", code: "capability_input_invalid"},
		{single: true, name: "single unknown business property", value: 7, mutate: "extra", code: "capability_input_invalid"},
		{single: true, name: "single malformed reference", value: 7, mutate: "shape", code: "fact_reference_invalid"},
		{single: true, name: "single unknown fact", value: 7, mutate: "unknown", code: "fact_reference_not_available"},
		{single: true, name: "single invalid fact path", value: 7, mutate: "path", code: "fact_reference_path_invalid"},
		{single: true, name: "single expired fact", value: 7, expired: true, code: "fact_reference_expired"},
		{single: true, name: "single fact ID is not business ID", value: 7, mutate: "id-reference", code: "capability_input_invalid"},
		{single: true, name: "single invalid input before approval", value: "7", approval: true, code: "capability_input_invalid"},
		{single: true, name: "single valid input requires approval", value: 7, approval: true, status: "needs_approval"},
		{single: true, name: "single live grant revoked", value: 7, revoke: true, status: "needs_authorization"},
		{name: "batch resolved type", value: "7", batch: true, code: "capability_input_invalid"},
		{name: "batch invalid literal", value: 7, batch: true, mutate: "literal", code: "capability_input_invalid"},
		{name: "batch valid input requires approval", value: 7, batch: true, approval: true, status: "needs_approval"},
		{name: "batch live grant revoked", value: 7, batch: true, revoke: true, status: "needs_authorization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &nativeReferenceProvider{value: tc.value, expired: tc.expired}
			var mu sync.Mutex
			allowed := true
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
				if packet.CompletionReview != nil {
					nativeSDKReply(t, w, "approve_completion", agenstra.JSON{})
					return
				}
				if len(packet.Facts) == 0 {
					nativeSDKReply(t, w, nativeSDKFunction(t, payload, "inventory.list"), agenstra.JSON{})
					return
				}
				if len(packet.Observations) > 1 {
					nativeSDKReply(t, w, "final", agenstra.JSON{"answer_markdown": "The update was rejected.", "fact_ids": []string{packet.Facts[0].FactID}})
					return
				}
				reference := agenstra.JSON{"fact_id": packet.Facts[0].FactID, "path": []any{"data", "id"}}
				args := agenstra.JSON{"id": agenstra.JSON{"$fact_value": reference}, "enabled": true}
				switch tc.mutate {
				case "literal":
					args["enabled"] = "true"
				case "extra":
					args["unexpected"] = true
				case "shape":
					args["id"].(agenstra.JSON)["unexpected"] = true
				case "unknown":
					reference["fact_id"] = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
				case "path":
					reference["path"] = []any{"data", "missing"}
				case "id-reference":
					args["id"] = agenstra.JSON{"$fact_id": packet.Facts[0].FactID}
				}
				if tc.revoke {
					mu.Lock()
					allowed = false
					mu.Unlock()
				}
				if tc.single {
					nativeSDKReply(t, w, "tool_call", agenstra.JSON{
						"capability": "inventory.set", "arguments": args, "reason": "Use the requested evidence source",
					})
				} else if tc.batch {
					nativeSDKReply(t, w, "tool_batch", agenstra.JSON{"calls": []any{agenstra.JSON{
						"capability": "inventory.set", "arguments": args, "reason": "Use the requested evidence source",
					}}})
				} else {
					nativeSDKReply(t, w, nativeSDKFunction(t, payload, "inventory.set"), args)
				}
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
					return agenstra.ExecutionPolicy{GrantedCapabilities: map[string]bool{"inventory.list": true, "inventory.set": allowed}, ApprovalCapabilities: map[string]bool{"inventory.set": tc.approval}, AllowModelData: true}, nil
				})
			run, err := host.Create(t.Context(), "owner", "inventory", "Read the inventory ID, then enable it using its evidence reference.", "")
			if err != nil {
				t.Fatal(err)
			}
			run, err = host.Drive(t.Context(), run.RunID, "owner")
			if err != nil || len(provider.calls) != 1 || provider.calls[0].capability != "inventory.list" {
				t.Fatal("invalid or unauthorized proposal reached the business operation", run.Status, provider.calls, err)
			}
			raw, err := agenstra.CanonicalJSON(run.State["runtime"])
			if err != nil {
				t.Fatal(err)
			}
			var state agenstra.RuntimeState
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if state.ModelUsage.InvalidResponses != 0 || state.ModelUsage.FormatRecoveryRequests != 0 {
				t.Fatal("reference proposal was misclassified as a model format error", state.ModelUsage)
			}
			if tc.status != "" {
				if run.Status != tc.status {
					t.Fatalf("got %s want %s", run.Status, tc.status)
				}
				if tc.approval && (len(state.Pending) != 1 || state.Pending[0].ArgumentsSHA256 == "") {
					t.Fatal("approval was not bound to the resolved arguments", state.Pending)
				}
				return
			}
			found := false
			for _, observation := range state.Observations {
				if observation.Capability == "inventory.set" && observation.ErrorCode != nil && *observation.ErrorCode == tc.code {
					found = true
				}
			}
			if !found || run.Status == "needs_approval" {
				t.Fatal("runtime did not reject the proposal before execution or approval", run.Status, tc.code, state.Observations)
			}
		})
	}
}
