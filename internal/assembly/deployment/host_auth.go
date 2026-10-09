package deployment

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func hostOwnerPath(c agentcontract.HostAuthConfig) []string {
	if len(c.OwnerPath) == 0 {
		return []string{"owner_id"}
	}
	return c.OwnerPath
}

// ValidHostOwner checks the stable owner identifier accepted from the trusted identity endpoint.
func ValidHostOwner(owner string) bool {
	if owner == "" || owner != strings.TrimSpace(owner) || len(owner) > 128 || strings.ContainsAny(owner, "/?#") {
		return false
	}
	for _, r := range owner {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// AuthenticateContext validates static or host-delegated tokens with request cancellation.
// Authentication selects an owner; capability grants are resolved separately.
func (d *Deployment) AuthenticateContext(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", DeploymentError("unauthorized")
	}
	matched, count := "", 0
	for id, user := range d.Config.Users {
		secret := d.Environment[user.APIKeyEnv]
		if secret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
			matched = id
			count++
		}
	}
	if count == 1 {
		return matched, nil
	}
	if count > 1 || d.Config.HostAuth == nil {
		return "", DeploymentError("unauthorized")
	}
	return d.authenticateHost(ctx, token)
}

func (d *Deployment) authenticateHost(ctx context.Context, token string) (string, error) {
	deny := func() (string, error) { return "", DeploymentError("unauthorized") }
	config := d.Config.HostAuth
	if config == nil || config.Validate() != nil {
		return deny()
	}
	address, err := d.Secret(config.URLEnv)
	if err != nil {
		return deny()
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return deny()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return deny()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := d.IdentityClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	safeClient := *client
	safeClient.Jar = nil
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return deny()
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			log.Print("HTTP response cleanup failed")
		}
	}()
	if response.StatusCode != http.StatusOK {
		return deny()
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return deny()
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&body) != nil || body == nil || decoder.Decode(new(any)) != io.EOF {
		return deny()
	}
	var current any = body
	for _, field := range hostOwnerPath(*config) {
		object, ok := current.(map[string]any)
		if !ok {
			return deny()
		}
		current, ok = object[field]
		if !ok {
			return deny()
		}
	}
	owner, ok := current.(string)
	if !ok || !ValidHostOwner(owner) {
		return deny()
	}

	// A host token must never inherit a legacy static user's pack grants.
	if _, static := d.Config.Users[owner]; static {
		return deny()
	}
	return owner, nil
}
