package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

func validateRawManifest(raw []byte, skills map[string]string) error {
	var manifest map[string]any
	if err := jsonvalue.DecodeStrict(raw, &manifest); err != nil {
		return err
	}
	return ValidatePackManifest(manifest, skills)
}

// OpenPack selects the declared provider from a trusted manifest.
// The caller owns and closes the returned connection.
func OpenPack(ctx context.Context, path string, environment map[string]string) (CapabilityProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	switch header.Schema {
	case "agenstra.capability-pack.v1":
		return LoadLegacyPack(path, environment, nil)
	case "agenstra.rest-pack.v2":
		return LoadRestPack(path, environment, nil)
	case "agenstra.mcp-pack.v1":
		return OpenMCPPack(ctx, path, environment)
	default:
		if header.Schema == "" {
			return nil, errors.New("unsupported capability pack schema: <missing>")
		}
		return nil, fmt.Errorf("unsupported capability pack schema: %s", header.Schema)
	}
}

// ValidatePackManifest checks a complete manifest and pinned skill contents without business IO.
func ValidatePackManifest(manifest map[string]any, skillContents map[string]string) error {
	raw, err := CanonicalJSON(manifest)
	if err != nil {
		return err
	}
	switch manifest["schema"] {
	case "agenstra.rest-pack.v2":
		var m RestManifest
		if err := jsonvalue.DecodeStrict(raw, &m); err != nil {
			return err
		}
		if len(m.Name) < 1 || len(m.Name) > 80 || len(m.Version) < 1 || len(m.Version) > 40 || len(m.Guidance) < 1 || len(m.Guidance) > 8000 || len(m.Capabilities) == 0 || !envPattern.MatchString(m.BaseURLEnv) {
			return errors.New("invalid REST pack manifest")
		}
		headers := map[string]string{}
		for name, variable := range m.HeadersEnv {
			if err := checkedHeader(name, false); err != nil {
				return err
			}
			if !envPattern.MatchString(variable) {
				return errors.New("invalid headers_env")
			}
			headers[name] = ""
		}
		if m.TokenEnv != nil {
			if !envPattern.MatchString(*m.TokenEnv) {
				return errors.New("invalid token_env")
			}
			headers["Authorization"] = ""
		}
		skills, err := validateSkillContent(m.Skills, skillContents)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, endpoint := range m.Capabilities {
			if seen[endpoint.Name] {
				return fmt.Errorf("duplicate REST capability: %s", endpoint.Name)
			}
			seen[endpoint.Name] = true
			if err := validateRestEndpoint(endpoint, headers); err != nil {
				return err
			}
			for _, name := range endpoint.Skills {
				if !skills[name] {
					return fmt.Errorf("unknown skill for REST capability: %s", endpoint.Name)
				}
			}
			if _, err := validateLocalSchema(endpoint.InputSchema, true); err != nil {
				return err
			}
			if _, err := validateLocalSchema(endpoint.OutputSchema, true); err != nil {
				return err
			}
			for _, schema := range endpoint.ResponseSchemas {
				if _, err := validateLocalSchema(schema, true); err != nil {
					return err
				}
			}
		}
		return nil
	case "agenstra.mcp-pack.v1":
		var m MCPManifest
		if err := jsonvalue.DecodeStrict(raw, &m); err != nil {
			return err
		}
		if m.Name == "" || m.Version == "" || len(m.Guidance) < 1 || len(m.Guidance) > 8000 || len(m.Tools) == 0 || m.Source.TimeoutSeconds <= 0 || m.Source.TimeoutSeconds > 300 {
			return errors.New("invalid MCP pack manifest")
		}
		if m.Source.Transport == "stdio" {
			if m.Source.Command == nil || *m.Source.Command == "" || m.Source.URLEnv != nil || m.Source.TokenEnv != nil {
				return errors.New("invalid MCP stdio source")
			}
		} else if m.Source.Transport == "streamable_http" {
			if m.Source.URLEnv == nil || m.Source.Command != nil || len(m.Source.Args) > 0 || m.Source.CWDEnv != nil || len(m.Source.Environment) > 0 {
				return errors.New("invalid MCP HTTP source")
			}
		} else {
			return errors.New("invalid MCP transport")
		}
		skills, err := validateSkillContent(m.Skills, skillContents)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, ex := range m.Tools {
			if ex.Name == "" || seen[ex.Name] {
				return errors.New("duplicate or invalid MCP capability")
			}
			seen[ex.Name] = true
			if !safeSHA256(ex.ContractSHA256) {
				return errors.New("invalid MCP contract_sha256")
			}
			if !map[string]bool{"read": true, "compute": true, "write": true, "destructive": true}[ex.Effect] {
				return errors.New("invalid MCP effect")
			}
			if ex.Replay != "never" && ex.Replay != "safe" && ex.Replay != "idempotent" {
				return errors.New("invalid MCP replay")
			}
			if ex.ReferenceScope != "durable" && ex.ReferenceScope != "connection" {
				return errors.New("invalid MCP reference_scope")
			}
			for _, name := range ex.Skills {
				if !skills[name] {
					return errors.New("unknown MCP skill")
				}
			}
		}
		return nil
	case "agenstra.capability-pack.v1":
		var m LegacyPackManifest
		if err := jsonvalue.DecodeStrict(raw, &m); err != nil {
			return err
		}
		if len(m.Name) < 1 || len(m.Name) > 80 || len(m.Guidance) < 1 || len(m.Guidance) > 8000 || len(m.Capabilities) == 0 {
			return errors.New("invalid legacy pack")
		}
		seen := map[string]bool{}
		for _, cap := range m.Capabilities {
			if seen[cap.Name] || !regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,127}$`).MatchString(cap.Name) || len(cap.Version) < 1 || len(cap.Version) > 40 || len(cap.Description) < 1 || len(cap.Description) > 500 || !envPattern.MatchString(cap.URLEnv) || len(cap.ResponsePath) > 8 || cap.TimeoutSeconds <= 0 || cap.TimeoutSeconds > 300 || cap.MaxAttempts < 1 || cap.MaxAttempts > 3 {
				return errors.New("invalid legacy capability")
			}
			seen[cap.Name] = true
			method := cap.Method
			if method == "" {
				method = "POST"
			}
			if method != "POST" && method != "GET" {
				return errors.New("invalid legacy method")
			}
			if _, err := fieldsSchema(cap.Inputs, false, method); err != nil {
				return err
			}
			if cap.Outputs != nil {
				if _, err := fieldsSchema(cap.Outputs, true, method); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported capability pack schema: %v", manifest["schema"])
	}
}

func safeSHA256(s string) bool { return regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s) }
func validateSkillContent(entries []SkillFile, contents map[string]string) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Name == "" || entry.Description == "" || entry.Path == "" || !safeSHA256(entry.SHA256) || seen[entry.Name] {
			return nil, errors.New("invalid skill metadata")
		}
		seen[entry.Name] = true
		content, ok := contents[entry.Path]
		if !ok {
			return nil, errors.New("missing skill content")
		}
		digest := sha256.Sum256([]byte(content))
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return nil, errors.New("skill content changed")
		}
	}
	return seen, nil
}
