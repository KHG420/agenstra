package deployment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestHostAuthenticationDistinguishesUnavailableVerification(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{401, "unauthorized"}, {403, "unauthorized"},
		{429, "authorization_unavailable"}, {500, "authorization_unavailable"}, {503, "authorization_unavailable"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer upstream.Close()
			deployment := &Deployment{Config: agentcontract.DeploymentConfig{HostAuth: &agentcontract.HostAuthConfig{URLEnv: "HOST_AUTH_URL"}},
				Environment: map[string]string{"HOST_AUTH_URL": upstream.URL}, IdentityClient: upstream.Client()}
			owner, err := deployment.AuthenticateContext(t.Context(), "fake-token")
			var failure *agentcontract.DeploymentError
			if owner != "" || !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("owner=%q error=%v; want %s without an authenticated owner", owner, err, tc.code)
			}
		})
	}

	t.Run("cancelled transport preserves its cause", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("cancelled verification must not be dispatched")
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()
		deployment := &Deployment{Config: agentcontract.DeploymentConfig{HostAuth: &agentcontract.HostAuthConfig{URLEnv: "HOST_AUTH_URL"}},
			Environment: map[string]string{"HOST_AUTH_URL": upstream.URL}, IdentityClient: upstream.Client()}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		owner, err := deployment.AuthenticateContext(ctx, "fake-token")
		var failure *agentcontract.DeploymentError
		if owner != "" || !errors.As(err, &failure) || failure.Code != "authorization_unavailable" || !errors.Is(err, context.Canceled) {
			t.Fatalf("owner=%q error=%v; verification failure lost its cancellation cause", owner, err)
		}
	})
}
