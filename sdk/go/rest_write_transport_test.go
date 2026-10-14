package agenstra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	agenstra "github.com/KHG420/agenstra/sdk/go"
)

func TestSDKRESTWriteGETDoesNotReplayLostResponseOnReusedConnection(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		name := "default-client"
		if supplied {
			name = "supplied-standard-transport"
		}
		t.Run(name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/write" && writes.Add(1) == 1 {
					// The business mutation is already committed. Lose its response
					// after receiving the request on a previously successful connection.
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					if err := connection.Close(); err != nil {
						t.Errorf("close committed request: %v", err)
					}
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"ok": true}); err != nil {
					t.Errorf("response: %v", err)
				}
			}))
			defer server.Close()
			var client *http.Client
			var originalTransport *http.Transport
			if supplied {
				originalTransport = http.DefaultTransport.(*http.Transport).Clone()
				defer originalTransport.CloseIdleConnections()
				client = &http.Client{Transport: originalTransport}
			}
			input := agenstra.JSON{"type": "object", "properties": agenstra.JSON{}, "additionalProperties": false}
			output := agenstra.JSON{"type": "object", "properties": agenstra.JSON{"ok": agenstra.JSON{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}
			manifest := agenstra.JSON{"schema": "agenstra.rest-pack.v2", "name": "transport-effects", "version": "1", "guidance": "Use declared business effects", "base_url_env": "API_URL", "capabilities": []any{
				agenstra.JSON{"name": "records.warm", "description": "Read a record", "method": "GET", "path": "/warm", "effect": "read", "input_schema": input, "output_schema": output},
				agenstra.JSON{"name": "records.write", "description": "Commit a mutation through GET", "method": "GET", "path": "/write", "effect": "write", "input_schema": input, "output_schema": output},
			}}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "pack.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			pack, err := agenstra.LoadRestPack(path, map[string]string{"API_URL": server.URL}, client)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := pack.Close(); err != nil {
					t.Error(err)
				}
			}()
			if pack.Capabilities()["records.write"].Replay != "never" {
				t.Fatal("write GET must not be declared replayable")
			}
			warm, err := pack.Invoke(context.Background(), "records.warm", map[string]any{}, nil)
			if err != nil || warm.ErrorCode != "" {
				t.Fatalf("warm connection failed: %v, %s", err, warm.ErrorCode)
			}
			result, err := pack.Invoke(context.Background(), "records.write", map[string]any{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if count := writes.Load(); count != 1 {
				t.Errorf("committed GET transmitted %d times despite replay=never", count)
			}
			if result.ErrorCode != "provider_outcome_unknown" {
				t.Errorf("lost committed response became %q instead of unknown", result.ErrorCode)
			}
			if supplied && (client.Transport != originalTransport || originalTransport.DisableKeepAlives || client.Timeout != 0) {
				t.Fatal("provider changed the caller's client or transport")
			}
		})
	}
}
