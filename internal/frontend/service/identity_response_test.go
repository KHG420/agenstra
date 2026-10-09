package service

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestIdentityPolicyUsesBearerWithoutSharedClientCookies(t *testing.T) {
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer identity-token" {
			t.Error("identity authority did not receive the configured token")
		}
		if r.Header.Get("Cookie") != "" {
			writeJSON(w, http.StatusOK, agentcontract.JSON{"sub": "previous-user", "capabilities": []string{"records.write"}})
			return
		}
		writeJSON(w, http.StatusOK, agentcontract.JSON{"sub": "alice", "capabilities": []string{"records.read"}})
	}))
	defer authority.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	address, err := url.Parse(authority.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(address, []*http.Cookie{{Name: "session", Value: "previous-user"}})
	client := authority.Client()
	client.Jar = jar
	d := &deployassembly.Deployment{Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{"records": {}}, Users: map[string]agentcontract.UserConfig{"alice": {Packs: map[string]agentcontract.ConnectionConfig{"records": {Identity: &agentcontract.IdentityConfig{URLEnv: "IDENTITY_URL", TokenEnv: "IDENTITY_TOKEN", CapabilitiesPath: []any{"capabilities"}}, GrantedCapabilities: []string{"records.read"}}}}}}, Environment: map[string]string{"IDENTITY_URL": authority.URL, "IDENTITY_TOKEN": "identity-token"}, IdentityClient: client}
	policy, err := d.PolicyResolver(t.Context(), "alice", "records")
	if err != nil || policy.Subject != "alice" || !policy.PermissionsVerified || !policy.GrantedCapabilities["records.read"] {
		t.Fatalf("shared client cookies replaced the configured bearer identity: policy=%+v err=%v", policy, err)
	}
	if client.Jar != jar || len(jar.Cookies(address)) != 1 || jar.Cookies(address)[0].Value != "previous-user" {
		t.Fatal("identity verification changed the caller's cookie jar")
	}
}
