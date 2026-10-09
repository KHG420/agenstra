package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
	reactcore "github.com/KHG420/agenstra/internal/runtime/react"
	packregistry "github.com/KHG420/agenstra/internal/state/registry"
)

// DeploymentError carries the safe deployment or connection error code.
func DeploymentError(code string) error { return &agentcontract.DeploymentError{Code: code} }

// Deployment resolves trusted configuration and per-owner connections.
// Treat Config and Environment as read-only while requests are active.
type Deployment struct {
	Config         agentcontract.DeploymentConfig
	BaseDir        string
	Environment    map[string]string
	IdentityClient *http.Client
	Registry       *packregistry.CapabilityRegistry
}

// LoadDeployment strictly decodes configuration and snapshots the process environment.
// Registry initialization and capability connections remain explicit operations.
func LoadDeployment(path string) (*Deployment, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(absolute)
	if e != nil {
		return nil, e
	}
	c := agentcontract.DeploymentConfig{Settings: agentcontract.DefaultHostSettings()}
	if e = jsonvalue.DecodeStrict(b, &c); e != nil {
		return nil, e
	}
	if c.Users == nil && (c.HostAuth == nil || c.Management == nil) {
		return nil, fmt.Errorf("users required")
	}
	if c.HostAuth != nil {
		if c.Management == nil {
			return nil, fmt.Errorf("host_auth requires management")
		}
		if err := c.HostAuth.Validate(); err != nil {
			return nil, err
		}
	}
	if e := c.Settings.Validate(); e != nil {
		return nil, e
	}
	if c.Models != nil {
		if err := c.Models.Validate(); err != nil {
			return nil, err
		}
	}
	if c.DatabasePath == "" {
		return nil, fmt.Errorf("database_path required")
	}
	if c.Management != nil && !agentcontract.DeploymentEnvName.MatchString(c.Management.AdminAPIKeyEnv) {
		return nil, fmt.Errorf("invalid management admin_api_key_env")
	}
	env := map[string]string{}
	for _, v := range os.Environ() {
		k, val, _ := strings.Cut(v, "=")
		env[k] = val
	}
	dep := &Deployment{Config: c, BaseDir: filepath.Dir(absolute), Environment: env, IdentityClient: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.Management != nil {
		m := c.Management
		dep.Registry = packregistry.NewCapabilityRegistry(dep.Resolve(m.DatabasePath), dep.Resolve(m.PackageDir))
		if dep.Registry.DatabasePath == dep.DatabasePath() {
			return nil, fmt.Errorf("management database must differ from run database")
		}
	}
	return dep, nil
}

// Resolve resolves a trusted deployment-relative file path.
func (d *Deployment) Resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(d.BaseDir, p)
}

// DatabasePath resolves the configured run database relative to the deployment file.
func (d *Deployment) DatabasePath() string { return d.Resolve(d.Config.DatabasePath) }

// Authenticate checks a token without a request context.
// HTTP callers should use AuthenticateContext to propagate request cancellation.
func (d *Deployment) Authenticate(token string) (string, error) {
	return d.AuthenticateContext(context.Background(), token)
}

func (d *Deployment) connection(ownerID, packID string) (agentcontract.ConnectionConfig, error) {
	user, ok := d.Config.Users[ownerID]
	if !ok && d.Config.HostAuth == nil {
		return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
	}
	if d.Registry != nil {
		present, managed, e := d.Registry.Binding(ownerID, packID)
		if e != nil {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
		if present {
			if managed == nil {
				return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
			}
			b, err := json.Marshal(managed)
			if err != nil {
				return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
			}
			var c agentcontract.ConnectionConfig
			if json.Unmarshal(b, &c) != nil {
				return c, DeploymentError("access_denied")
			}
			return NormalizeConnection(c), nil
		}
		if !ok {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
		active, e := d.Registry.ActiveRelease(packID)
		if e != nil {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
		if active != "" {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
	}
	if !ok {
		return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
	}
	if _, ok := d.Config.Packs[packID]; !ok {

		// A frontend-only integration uses the same explicit connection policy
		// as business packs, without requiring a placeholder provider.
		web := d.Config.WebIntegration
		if web == nil {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
		profile, exists := web.Integrations[packID]
		if !exists || !web.BrowserBridge || profile.PackID != "" || profile.FrontendProfilePath == "" {
			return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
		}
	}
	c, ok := user.Packs[packID]
	if !ok {
		return agentcontract.ConnectionConfig{}, DeploymentError("access_denied")
	}
	return c, nil
}

// NormalizeConnection fills empty binding collections without changing configured grants or credentials.
func NormalizeConnection(c agentcontract.ConnectionConfig) agentcontract.ConnectionConfig {
	if c.Environment == nil {
		c.Environment = map[string]string{}
	}
	if c.BindingEnvironment == nil {
		c.BindingEnvironment = []string{}
	}
	if c.GrantedCapabilities == nil {
		c.GrantedCapabilities = []string{}
	}
	if c.ApprovalCapabilities == nil {
		c.ApprovalCapabilities = []string{}
	}
	return c
}

// Secret resolves a configured environment or local secret-file reference.
func (d *Deployment) Secret(ref string) (string, error) {
	var value string
	if strings.HasPrefix(ref, "secret:") {
		name := strings.TrimPrefix(ref, "secret:")
		m := d.Config.Management
		if m == nil || m.SecretDir == "" || !agentcontract.DeploymentEnvName.MatchString(name) {
			return "", DeploymentError("connection_unavailable")
		}
		root := d.Resolve(m.SecretDir)
		path := filepath.Join(root, name)
		info, e := os.Lstat(path)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", DeploymentError("connection_unavailable")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return "", DeploymentError("connection_unavailable")
		}
		value = strings.TrimSpace(string(b))
	} else {
		value = d.Environment[ref]
	}
	if value == "" {
		return "", DeploymentError("connection_unavailable")
	}
	return value, nil
}

// verifiedIdentity reads the authority endpoint using the user's target token.
// Local grants are a ceiling; only the endpoint can attest the account's authority.
func (d *Deployment) verifiedIdentity(ctx context.Context, owner string, c agentcontract.ConnectionConfig) (string, map[string]bool, error) {
	if c.Identity == nil {
		return "", nil, nil
	}
	id := c.Identity
	address, err := d.Secret(id.URLEnv)
	if err != nil {
		return "", nil, err
	}
	token, err := d.Secret(id.TokenEnv)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return "", nil, DeploymentError("identity_unverified")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := d.IdentityClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// Never forward a user's bearer token to a redirect target.
	safeClient := *client
	safeClient.Jar = nil
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return "", nil, DeploymentError("identity_unverified")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Print("HTTP response cleanup failed")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", nil, DeploymentError("identity_unverified")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", nil, DeploymentError("identity_unverified")
	}
	var body any
	if jsonvalue.DecodeStrict(raw, &body) != nil {
		return "", nil, DeploymentError("identity_unverified")
	}
	steps := id.SubjectPath
	if len(steps) == 0 {
		steps = []any{"sub"}
	}
	value, err := reactcore.OperationValue(body, steps)
	subject, ok := value.(string)
	expected := id.ExpectedSubject
	if expected == "" {
		expected = owner
	}
	if err != nil || !ok || subject == "" || subject != expected {
		return "", nil, DeploymentError("identity_unverified")
	}
	if len(id.CapabilitiesPath) == 0 {
		return subject, nil, nil
	}
	value, err = reactcore.OperationValue(body, id.CapabilitiesPath)
	list, ok := value.([]any)
	if err != nil || !ok {
		return "", nil, DeploymentError("identity_unverified")
	}
	permissions := map[string]bool{}
	for _, v := range list {
		name, ok := v.(string)
		if !ok || strings.TrimSpace(name) == "" || strings.Contains(name, "::") || len(name) > 200 {
			return "", nil, DeploymentError("identity_unverified")
		}
		permissions[name] = true
	}
	return subject, permissions, nil
}

func (d *Deployment) validateIdentity(ctx context.Context, owner string, c agentcontract.ConnectionConfig) error {
	_, _, err := d.verifiedIdentity(ctx, owner, c)
	return err
}

// PolicyResolver checks the current binding and trusted identity for an owner and pack.
func (d *Deployment) PolicyResolver(ctx context.Context, ownerID, packID string) (agentcontract.ExecutionPolicy, error) {
	c, err := d.connection(ownerID, packID)
	if err != nil {
		return agentcontract.ExecutionPolicy{}, err
	}
	subject, permissions, err := d.verifiedIdentity(ctx, ownerID, c)
	if err != nil {
		return agentcontract.ExecutionPolicy{}, err
	}
	grants, approvals := map[string]bool{}, map[string]bool{}
	for _, v := range c.GrantedCapabilities {
		grants[v] = permissions == nil || permissions[v]
	}
	for _, v := range c.ApprovalCapabilities {
		approvals[v] = true
	}
	return agentcontract.ExecutionPolicy{GrantedCapabilities: grants, ApprovalCapabilities: approvals, AllowModelData: c.AllowModelData, Subject: subject, PermissionsVerified: permissions != nil, Delegations: c.Delegations}, nil
}

// credentialSubject only attests providers whose actual bearer token reference
// matches the identity token reference. It never compares or stores secret values.
func credentialSubject(provider agentcontract.CapabilityProvider, owner string, c agentcontract.ConnectionConfig) string {
	if c.Identity == nil || len(c.Identity.CapabilitiesPath) == 0 {
		return ""
	}
	var token *string
	switch p := provider.(type) {
	case *capabilitypack.RestPack:

		// Mixed static header authentication cannot be attested as this bearer identity.
		if len(p.Manifest.HeadersEnv) > 0 {
			return ""
		}
		token = p.Manifest.TokenEnv
	case *capabilitypack.MCPPack:
		if p.Manifest.Source.Transport != "streamable_http" {
			return ""
		}
		token = p.Manifest.Source.TokenEnv
	default:
		return ""
	}
	if token == nil || *token == "" || c.Environment[*token] != c.Identity.TokenEnv {
		return ""
	}
	subject := c.Identity.ExpectedSubject
	if subject == "" {
		subject = owner
	}
	return subject
}

func endpointValue(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	host := u.Hostname()
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	return u.Scheme + "://" + host + u.Path
}

func endpointEnvs(m map[string]any) []string {
	schema, _ := m["schema"].(string)
	out := []string{}
	switch schema {
	case "agenstra.rest-pack.v2":
		if s, ok := m["base_url_env"].(string); ok {
			out = append(out, s)
		}
	case "agenstra.mcp-pack.v1":
		if src, ok := m["source"].(map[string]any); ok {
			if s, ok := src["url_env"].(string); ok {
				out = append(out, s)
			}
		}
	case "agenstra.capability-pack.v1":
		if arr, ok := m["capabilities"].([]any); ok {
			for _, v := range arr {
				if item, ok := v.(map[string]any); ok {
					if s, ok := item["url_env"].(string); ok {
						out = append(out, s)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// BindingID hashes the trusted manifest, configured identity and resolved non-credential bindings.
func (d *Deployment) BindingID(ownerID, packID, path string, c agentcontract.ConnectionConfig, env map[string]string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", DeploymentError("connection_unavailable")
	}
	var m map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.UseNumber()
	if decoder.Decode(&m) != nil {
		return "", DeploymentError("connection_unavailable")
	}
	source, _ := m["source"].(map[string]any)
	if source == nil {
		source = map[string]any{}
	}
	credentials := map[string]bool{}
	for _, v := range []any{m["token_env"], source["token_env"]} {
		if s, ok := v.(string); ok {
			credentials[s] = true
		}
	}
	if headers, ok := m["headers_env"].(map[string]any); ok {
		for _, v := range headers {
			if s, ok := v.(string); ok {
				credentials[s] = true
			}
		}
	}
	if c.Identity != nil {
		for target, origin := range c.Environment {
			if origin == c.Identity.TokenEnv {
				credentials[target] = true
			}
		}
	}
	identities := map[string]string{}
	for _, name := range c.BindingEnvironment {
		if credentials[name] {
			return "", DeploymentError("binding_contains_credentials")
		}
		val, ok := env[name]
		if !ok {
			return "", DeploymentError("connection_unavailable")
		}
		identities[name] = val
	}
	endpoints := map[string]string{}
	for _, name := range endpointEnvs(m) {
		endpoints[name] = endpointValue(env[name])
	}
	var identity any
	if c.Identity != nil {
		address, e := d.Secret(c.Identity.URLEnv)
		if e != nil {
			return "", e
		}
		expected := c.Identity.ExpectedSubject
		if expected == "" {
			expected = ownerID
		}
		steps := c.Identity.SubjectPath
		if len(steps) == 0 {
			steps = []any{"sub"}
		}
		details := map[string]any{"url": endpointValue(address), "subject_path": steps, "expected_subject": expected}
		if len(c.Identity.CapabilitiesPath) > 0 {
			details["capabilities_path"] = c.Identity.CapabilitiesPath
		}
		identity = details
	}
	var workingDirectory any
	if cwdEnv, ok := source["cwd_env"].(string); ok {
		if value, exists := env[cwdEnv]; exists {
			workingDirectory = value
		}
	}
	binding := map[string]any{"owner_id": ownerID, "pack_id": packID, "pack_path": path, "manifest": m, "environment_refs": NormalizeConnection(c).Environment, "working_directory": workingDirectory, "connection_identity": identities, "endpoints": endpoints, "identity": identity}
	bytes, err := packregistry.RegistryCanonical(binding)
	if err != nil {
		return "", DeploymentError("connection_unavailable")
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

// ReleaseResolver returns the registry's active release, or an empty string for static deployments.
func (d *Deployment) ReleaseResolver(ctx context.Context, ownerID, packID string) (string, error) {
	if d.Registry == nil {
		return "", nil
	}
	return d.Registry.ActiveRelease(packID)
}

// ProviderFactory opens the current authorized release; the caller must close it.
func (d *Deployment) ProviderFactory(ctx context.Context, ownerID, packID string) (agentcontract.CapabilityProvider, error) {
	release := ""
	if d.Registry != nil {
		var e error
		release, e = d.Registry.ActiveRelease(packID)
		if e != nil {
			return nil, e
		}
	}
	return d.ReleaseProviderFactory(ctx, ownerID, packID, release)
}

// ReleaseProviderFactory opens a pinned release with freshly checked connection identity.
func (d *Deployment) ReleaseProviderFactory(ctx context.Context, ownerID, packID, release string) (agentcontract.CapabilityProvider, error) {
	c, e := d.connection(ownerID, packID)
	if e != nil {
		return nil, e
	}
	if e = d.validateIdentity(ctx, ownerID, c); e != nil {
		return nil, e
	}
	env := map[string]string{}
	for target, ref := range c.Environment {
		v, e := d.Secret(ref)
		if e != nil {
			return nil, e
		}
		env[target] = v
	}
	var path string
	if release == "" {
		pack, ok := d.Config.Packs[packID]
		if !ok {
			return nil, DeploymentError("connection_unavailable")
		}
		path = d.Resolve(pack.Path)
	} else if d.Registry != nil {
		path, e = d.Registry.ReleasePath(packID, release)
		if e != nil {
			return nil, DeploymentError("connection_unavailable")
		}
	} else {
		return nil, DeploymentError("connection_unavailable")
	}
	bindingID, e := d.BindingID(ownerID, packID, path, c, env)
	if e != nil {
		return nil, e
	}
	provider, e := capabilitypack.OpenPack(ctx, path, env)
	if e != nil {
		return nil, e
	}
	return BindProvider(provider, bindingID, credentialSubject(provider, ownerID, c)), nil
}

// AdminKey resolves and validates the configured administrator secret.
func (d *Deployment) AdminKey() (string, error) {
	if d.Config.Management == nil {
		return "", errors.New("management disabled")
	}
	key := d.Environment[d.Config.Management.AdminAPIKeyEnv]
	if len(key) < 24 {
		return "", errors.New("management requires a distinct admin key of at least 24 characters")
	}
	for _, user := range d.Config.Users {
		if key == d.Environment[user.APIKeyEnv] {
			return "", errors.New("management requires a distinct admin key of at least 24 characters")
		}
	}
	return key, nil
}

type boundProvider struct {
	agentcontract.CapabilityProvider
	binding string
	subject string
}

// ConcurrentInvocation preserves the underlying provider's concurrency guarantee.
func (p *boundProvider) ConcurrentInvocation(name string) bool {
	provider, ok := p.CapabilityProvider.(agentcontract.ConcurrentCapabilityProvider)
	return ok && provider.ConcurrentInvocation(name)
}

// BindingID returns the stable connection identity used to detect configuration changes.
func (p *boundProvider) BindingID() string { return p.binding }

// BoundSubject returns the trusted subject checked when opening the connection.
func (p *boundProvider) BoundSubject() string { return p.subject }

// BindProvider preserves a provider's concurrency guarantee and attaches the
// trusted connection identity established by deployment wiring.
func BindProvider(provider agentcontract.CapabilityProvider, binding, subject string) agentcontract.CapabilityProvider {
	return &boundProvider{CapabilityProvider: provider, binding: binding, subject: subject}
}
