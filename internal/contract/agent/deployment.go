package agent

import (
	"fmt"
	"regexp"
)

// DeploymentError carries a safe deployment or connection error code.
type DeploymentError struct{ Code string }

// Error returns the safe error identifier.
func (e *DeploymentError) Error() string { return e.Code }

// IdentityConfig binds a trusted identity response to the expected subject and grants.
type IdentityConfig struct {
	URLEnv           string `json:"url_env"`
	TokenEnv         string `json:"token_env"`
	SubjectPath      []any  `json:"subject_path"`
	ExpectedSubject  string `json:"expected_subject"`
	CapabilitiesPath []any  `json:"capabilities_path,omitempty"`
}

// ConnectionConfig declares owner-specific grants and credential references.
type ConnectionConfig struct {
	Environment          map[string]string   `json:"environment"`
	BindingEnvironment   []string            `json:"binding_environment"`
	GrantedCapabilities  []string            `json:"granted_capabilities"`
	ApprovalCapabilities []string            `json:"approval_capabilities"`
	AllowModelData       bool                `json:"allow_model_data"`
	Identity             *IdentityConfig     `json:"identity"`
	Delegations          map[string][]string `json:"delegations,omitempty"`
}

// UserConfig selects static authentication and the user's authorized connections.
type UserConfig struct {
	APIKeyEnv      string                      `json:"api_key_env"`
	Packs          map[string]ConnectionConfig `json:"packs"`
	BrowserActions map[string][]string         `json:"browser_actions,omitempty"`
}

// PackConfig locates a trusted capability manifest on disk.
type PackConfig struct {
	Path string `json:"path"`
}

// ManagementConfig configures the optional registry and administrator credential reference.
type ManagementConfig struct {
	DatabasePath   string `json:"database_path"`
	PackageDir     string `json:"package_dir"`
	AdminAPIKeyEnv string `json:"admin_api_key_env"`
	SecretDir      string `json:"secret_dir"`
}

// DeploymentConfig contains trusted host wiring, execution limits and optional integrations.
type DeploymentConfig struct {
	Models               *ModelConfiguration                      `json:"models,omitempty"`
	CompletionChecks     map[string][]FactRequirement             `json:"completion_checks,omitempty"`
	ReconciliationChecks map[string]map[string]ReconciliationRule `json:"reconciliation_checks,omitempty"`
	DatabasePath         string                                   `json:"database_path"`
	Packs                map[string]PackConfig                    `json:"packs"`
	Users                map[string]UserConfig                    `json:"users"`
	HostAuth             *HostAuthConfig                          `json:"host_auth,omitempty"`
	Management           *ManagementConfig                        `json:"management"`
	Settings             HostSettings                             `json:"settings"`
	WebIntegration       *WebIntegrationConfig                    `json:"web_integration,omitempty"`
}

// DeploymentEnvName validates trusted environment reference names.
var DeploymentEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// HostAuthConfig delegates bearer-token validation to a trusted host endpoint.
// The endpoint returns a stable owner ID; grants still require a managed binding.
type HostAuthConfig struct {
	URLEnv    string   `json:"url_env"`
	OwnerPath []string `json:"owner_path,omitempty"`
}

// Validate checks the authentication endpoint reference and owner path bounds.
func (c HostAuthConfig) Validate() error {
	if !DeploymentEnvName.MatchString(c.URLEnv) || len(c.OwnerPath) > 16 {
		return fmt.Errorf("invalid host_auth")
	}
	for _, field := range c.OwnerPath {
		if field == "" || len(field) > 128 {
			return fmt.Errorf("invalid host_auth owner_path")
		}
	}
	return nil
}

// ReconciliationRule verifies an uncertain operation using a granted read
// capability. Argument paths address {arguments, invocation_id, idempotency_key}.
// ResultPath selects the original operation's result in the query response.
type ReconciliationRule struct {
	VerifyCapability string           `json:"verify_capability"`
	Arguments        map[string][]any `json:"arguments"`
	SuccessPath      []any            `json:"success_path"`
	SuccessValue     any              `json:"success_value"`
	ResultPath       []any            `json:"result_path"`
}
