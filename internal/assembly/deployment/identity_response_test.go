package deployment

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestIdentityPolicyRequiresCompleteBoundedJSON(t *testing.T) {
	valid := `{"sub":"alice","capabilities":["records.read","records.write"]}`
	for _, tc := range []struct {
		name, payload               string
		subjectPath, capabilityPath []any
		wantError                   bool
	}{
		{name: "valid", payload: valid},
		{name: "whitespace", payload: valid + " \n\t"},
		{name: "at size limit", payload: valid + strings.Repeat(" ", (1<<20)-len(valid))},
		{name: "array paths", payload: "[" + valid + "]", subjectPath: []any{0, "sub"}, capabilityPath: []any{0, "capabilities"}},
		{name: "second object", payload: valid + ` {"sub":"bob"}`, wantError: true},
		{name: "second value", payload: valid + " null", wantError: true},
		{name: "trailing garbage", payload: valid + " incomplete", wantError: true},
		{name: "oversized whitespace", payload: valid + strings.Repeat(" ", (1<<20)+1-len(valid)), wantError: true},
		{name: "truncated", payload: valid[:len(valid)-1], wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer identity-token" {
					t.Error("identity authority did not receive the configured token")
				}
				w.Header().Set("Content-Type", "application/json")
				if _, callErr := w.Write([]byte(tc.payload)); callErr != nil {
					t.Error(callErr)
				}
			}))
			defer authority.Close()
			identity := &agentcontract.IdentityConfig{URLEnv: "IDENTITY_URL", TokenEnv: "IDENTITY_TOKEN", SubjectPath: tc.subjectPath, CapabilitiesPath: []any{"capabilities"}}
			if tc.capabilityPath != nil {
				identity.CapabilitiesPath = tc.capabilityPath
			}
			d := &Deployment{Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{"records": {}}, Users: map[string]agentcontract.UserConfig{"alice": {Packs: map[string]agentcontract.ConnectionConfig{"records": {Identity: identity, GrantedCapabilities: []string{"records.read"}, AllowModelData: true}}}}}, Environment: map[string]string{"IDENTITY_URL": authority.URL, "IDENTITY_TOKEN": "identity-token"}, IdentityClient: authority.Client()}
			policy, err := d.PolicyResolver(t.Context(), "alice", "records")
			if tc.wantError {
				if agentcontract.ErrorCode(err) != "identity_unverified" || policy.Subject != "" || policy.PermissionsVerified || len(policy.GrantedCapabilities) != 0 {
					t.Fatalf("incomplete authority response granted access: policy=%+v err=%v", policy, err)
				}
			} else if err != nil || policy.Subject != "alice" || !policy.PermissionsVerified || !policy.GrantedCapabilities["records.read"] || policy.GrantedCapabilities["records.write"] {
				t.Fatalf("valid authority response or local grant ceiling lost: policy=%+v err=%v", policy, err)
			}
		})
	}
}
