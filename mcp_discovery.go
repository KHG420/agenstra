package agenstra

import (
	"context"
	"errors"
	"sort"
	"time"
)

// MCPDiscoveredTool describes an available tool without granting or invoking it.
// Remote annotations are not trusted as authorization or replay guarantees.
type MCPDiscoveredTool struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	InputSchema    JSON            `json:"input_schema"`
	OutputSchema   JSON            `json:"output_schema"`
	ContractSHA256 string          `json:"contract_sha256"`
	Supported      bool            `json:"supported"`
	Issue          string          `json:"issue,omitempty"`
	Exposure       MCPToolExposure `json:"exposure"`
}

// DiscoverMCPTools initializes a connection, lists its tools and closes it. It
// never calls tools/call. Selection and confirmation of business effects remain
// explicit; the initial exposure requires approval and disallows replay.
func DiscoverMCPTools(ctx context.Context, source MCPSource, environment map[string]string) (result []MCPDiscoveredTool, resultErr error) {
	seconds := source.TimeoutSeconds
	if seconds == 0 {
		seconds = 60
	}
	if seconds <= 0 || seconds > 300 {
		return nil, deploymentError("mcp_discovery_source_invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	transport, remote, err := openMCPSource(ctx, source, environmentOrOS(environment))
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, transport.Close()) }()
	tools := make([]MCPDiscoveredTool, 0, len(remote))
	for name, raw := range remote {
		input, _ := raw["inputSchema"].(map[string]any)
		output, _ := raw["outputSchema"].(map[string]any)
		description, _ := raw["description"].(string)
		tool := MCPDiscoveredTool{Name: name, Description: description, InputSchema: input, OutputSchema: output, ContractSHA256: MCPContractDigest(raw), Supported: true}
		if input == nil {
			tool.Issue = "缺少输入契约，需由 MCP 服务提供 inputSchema。"
		} else if _, err := validateLocalSchema(input, false); err != nil {
			tool.Issue = "输入契约不受支持，请检查本地 JSON Schema。"
		} else if output == nil {
			tool.Issue = "缺少结构化输出契约，需由 MCP 服务提供 outputSchema。"
		} else if _, err := validateLocalSchema(output, false); err != nil {
			tool.Issue = "输出契约不受支持，请检查本地 JSON Schema。"
		}
		tool.Supported = tool.Issue == ""
		tool.Exposure = MCPToolExposure{Name: name, Effect: "write", Skills: []string{}, ContractSHA256: tool.ContractSHA256, Replay: "never", ReferenceScope: "durable", ApprovalRequired: true}
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}
