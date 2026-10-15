package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func redirectTestEndpoint() agentcontract.RestEndpoint {
	output := agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"location": agentcontract.JSON{"type": "string"}}, "required": []any{"location"}, "additionalProperties": false}
	return agentcontract.RestEndpoint{
		Name: "attachment.download", Description: "Obtain a download location", Method: "GET", Path: "/attachment", Effect: "read",
		InputSchema:  agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{}, "additionalProperties": false},
		OutputSchema: output, ResponseMode: "redirect", ResponseSchemas: map[string]agentcontract.JSON{"302": output},
	}
}

func loadRedirectTestPack(t *testing.T, endpoint agentcontract.RestEndpoint, server *httptest.Server, client *http.Client) *RestPack {
	t.Helper()
	manifest := agentcontract.RestManifest{Schema: "agenstra.rest-pack.v2", Name: "attachments", Version: "1", Guidance: "Report locations only", BaseURLEnv: "URL", HeadersEnv: map[string]string{"X-Private": "PRIVATE"}, Capabilities: []agentcontract.RestEndpoint{endpoint}}
	token := "TOKEN"
	manifest.TokenEnv = &token
	pack, err := LoadRestPack(writeTestManifest(t, manifest), map[string]string{"URL": server.URL, "TOKEN": "secret", "PRIVATE": "private"}, client)
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestRESTRedirectReturnsDeclaredLocationWithoutFollowing(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, location := range []string{target.URL + "/file?signature=a%2Fb&part=1", "../file?signature=a%2Fb#part"} {
			t.Run(fmt.Sprintf("%d/%s", status, location), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/attachment" {
						targetCalls.Add(1)
						return
					}
					if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Private") != "private" {
						t.Error("source credentials missing")
					}
					w.Header().Set("Location", location)
					w.Header().Set("X-Secret", "never expose")
					w.WriteHeader(status)
					if _, err := w.Write([]byte("not JSON; not a file receipt")); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				endpoint := redirectTestEndpoint()
				endpoint.ResponseSchemas = map[string]agentcontract.JSON{fmt.Sprint(status): endpoint.OutputSchema}
				var redirectCallbacks atomic.Int32
				client := server.Client()
				client.CheckRedirect = func(*http.Request, []*http.Request) error { redirectCallbacks.Add(1); return nil }
				pack := loadRedirectTestPack(t, endpoint, server, client)
				result, err := pack.Invoke(context.Background(), endpoint.Name, agentcontract.JSON{}, nil)
				if err != nil || result.ErrorCode != "" || !reflect.DeepEqual(result.Data, agentcontract.JSON{"location": location}) || result.ReferenceScope != "durable" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if targetCalls.Load() != 0 || redirectCallbacks.Load() != 0 {
					t.Fatal("followed redirect or called caller redirect policy")
				}
				if err := client.CheckRedirect(nil, nil); err != nil || redirectCallbacks.Load() != 1 {
					t.Fatal("mutated caller client")
				}
			})
		}
	}
}

func TestRESTRedirectRejectsUnexpectedResponses(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		status                     int
		location, mode, code       string
		rejectOutput, rejectStatus bool
	}{
		{name: "default still rejects redirect", status: 302, location: "/file", code: "upstream_http_302"},
		{name: "missing location", status: 302, mode: "redirect", code: "upstream_response_invalid"},
		{name: "blank location", status: 302, location: " ", mode: "redirect", code: "upstream_response_invalid"},
		{name: "undeclared redirect", status: 307, location: "/file", mode: "redirect", code: "upstream_http_307"},
		{name: "JSON success is not redirect", status: 200, location: "/file", mode: "redirect", code: "upstream_response_invalid"},
		{name: "no content is not redirect", status: 204, mode: "redirect", code: "upstream_response_invalid"},
		{name: "authorization failure", status: 403, mode: "redirect", code: "upstream_http_403"},
		{name: "server failure", status: 500, mode: "redirect", code: "upstream_http_500"},
		{name: "output schema enforced", status: 302, location: "/file", mode: "redirect", code: "upstream_response_invalid", rejectOutput: true},
		{name: "status schema enforced", status: 302, location: "/file", mode: "redirect", code: "upstream_response_invalid", rejectStatus: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			endpoint := redirectTestEndpoint()
			endpoint.ResponseMode = tc.mode
			if tc.mode == "" {
				endpoint.ResponseSchemas = nil
			}
			if tc.rejectOutput {
				endpoint.OutputSchema["properties"].(map[string]any)["location"] = agentcontract.JSON{"type": "string", "pattern": "^https://"}
			}
			if tc.rejectStatus {
				endpoint.ResponseSchemas["302"] = agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"location": agentcontract.JSON{"const": "different"}}}
			}
			pack := loadRedirectTestPack(t, endpoint, server, server.Client())
			result, err := pack.Invoke(context.Background(), endpoint.Name, agentcontract.JSON{}, nil)
			if err != nil || result.ErrorCode != tc.code || result.Data != nil {
				t.Fatalf("result=%+v err=%v want=%s", result, err, tc.code)
			}
		})
	}
}

func TestRESTRedirectRejectsInvalidContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*agentcontract.RestEndpoint)
	}{
		{"POST", func(e *agentcontract.RestEndpoint) { e.Method = "POST" }},
		{"compute", func(e *agentcontract.RestEndpoint) { e.Effect = "compute" }},
		{"write", func(e *agentcontract.RestEndpoint) { e.Effect = "write" }},
		{"destructive", func(e *agentcontract.RestEndpoint) { e.Effect = "destructive" }},
		{"response path", func(e *agentcontract.RestEndpoint) { e.ResponsePath = []any{"location"} }},
		{"empty success", func(e *agentcontract.RestEndpoint) { e.AllowEmptySuccess = true }},
		{"business success", func(e *agentcontract.RestEndpoint) {
			e.BusinessSuccess = &agentcontract.RestBusinessCheck{Path: []any{"ok"}, Value: true, ErrorCode: "business_failed"}
		}},
		{"async operation", func(e *agentcontract.RestEndpoint) { e.Operation = &agentcontract.OperationBinding{} }},
		{"no declared status", func(e *agentcontract.RestEndpoint) { e.ResponseSchemas = nil }},
		{"success status", func(e *agentcontract.RestEndpoint) { e.ResponseSchemas["200"] = e.OutputSchema }},
		{"not a location redirect", func(e *agentcontract.RestEndpoint) { e.ResponseSchemas["304"] = e.OutputSchema }},
		{"invalid status", func(e *agentcontract.RestEndpoint) { e.ResponseSchemas["3xx"] = e.OutputSchema }},
		{"no location output", func(e *agentcontract.RestEndpoint) { e.OutputSchema = agentcontract.JSON{"type": "object"} }},
		{"optional location", func(e *agentcontract.RestEndpoint) { delete(e.OutputSchema, "required") }},
		{"non-string location", func(e *agentcontract.RestEndpoint) {
			e.OutputSchema["properties"].(map[string]any)["location"] = agentcontract.JSON{"type": "integer"}
		}},
		{"default mode redirect status", func(e *agentcontract.RestEndpoint) { e.ResponseMode = "" }},
		{"wrap mode redirect status", func(e *agentcontract.RestEndpoint) { e.ResponseMode = "wrap" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := redirectTestEndpoint()
			tc.change(&endpoint)
			manifest := agentcontract.RestManifest{Schema: "agenstra.rest-pack.v2", Name: "attachments", Version: "1", Guidance: "Report locations only", BaseURLEnv: "URL", Capabilities: []agentcontract.RestEndpoint{endpoint}}
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var raw agentcontract.JSON
			if err := json.Unmarshal(encoded, &raw); err != nil {
				t.Fatal(err)
			}
			if err := ValidatePackManifest(raw, nil); err == nil {
				t.Fatal("accepted invalid redirect contract")
			}
		})
	}
}
