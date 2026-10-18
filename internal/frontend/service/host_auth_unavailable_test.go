package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostVerificationAvailabilityAtRuntimeAndTicketEndpoints(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 503} {
		for _, endpoint := range []struct{ method, path string }{{"GET", "/runs"}, {"POST", "/web/v1/token"}} {
			t.Run(http.StatusText(status)+endpoint.path, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					if _, err := w.Write([]byte("private upstream diagnostic")); err != nil {
						t.Error(err)
					}
				}))
				defer upstream.Close()
				deployment := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{HostAuth: &agentcontract.HostAuthConfig{URLEnv: "HOST_AUTH_URL"}},
					Environment: map[string]string{"HOST_AUTH_URL": upstream.URL}, IdentityClient: upstream.Client()}
				// Authentication failure must return before touching a run store or
				// minting a ticket; no identity or business connection is available.
				server := &HTTPServer{Deployment: deployment, Web: &WebIntegration{}}
				request := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				request.Header.Set("Authorization", "Bearer fake-token")
				response := httptest.NewRecorder()
				server.serveHTTP(response, request)
				wantStatus, wantCode := http.StatusUnauthorized, "unauthorized"
				if status == 429 || status >= 500 {
					wantStatus, wantCode = http.StatusServiceUnavailable, "authorization_unavailable"
				}
				var body map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if endpoint.path == "/runs" {
					body, _ = body["detail"].(map[string]any)
				}
				if response.Code != wantStatus || body["code"] != wantCode || strings.Contains(response.Body.String(), "private upstream diagnostic") {
					t.Fatalf("status=%d body=%s; want %d/%s without upstream details", response.Code, response.Body.String(), wantStatus, wantCode)
				}
			})
		}
	}
}
