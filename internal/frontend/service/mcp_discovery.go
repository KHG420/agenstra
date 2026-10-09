package service

import (
	"context"
	"errors"
	"sort"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

// DiscoverMCPTools initializes a connection, lists its tools and closes it. It
// never calls tools/call. Selection and confirmation of business effects remain
// explicit; the initial exposure requires approval and disallows replay.
func DiscoverMCPTools(ctx context.Context, source agentcontract.MCPSource, environment map[string]string) (result []agentcontract.MCPDiscoveredTool, resultErr error) {
	seconds := source.TimeoutSeconds
	if seconds == 0 {
		seconds = 60
	}
	if seconds <= 0 || seconds > 300 {
		return nil, deployassembly.DeploymentError("mcp_discovery_source_invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	transport, remote, err := capabilitypack.OpenMCPSource(ctx, source, capabilitypack.EnvironmentOrOS(environment))
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, transport.Close()) }()
	tools := make([]agentcontract.MCPDiscoveredTool, 0, len(remote))
	for name, raw := range remote {
		input, _ := raw["inputSchema"].(map[string]any)
		output, _ := raw["outputSchema"].(map[string]any)
		description, _ := raw["description"].(string)
		tool := agentcontract.MCPDiscoveredTool{Name: name, Description: description, InputSchema: input, OutputSchema: output, ContractSHA256: capabilitypack.MCPContractDigest(raw), Supported: true}
		if input == nil {
			tool.Issue = "缺少输入契约，需由 MCP 服务提供 inputSchema。"
		} else if _, err := agentcontract.ValidateLocalSchema(input, false); err != nil {
			tool.Issue = "输入契约不受支持，请检查本地 JSON Schema。"
		} else if output == nil {
			tool.Issue = "缺少结构化输出契约，需由 MCP 服务提供 outputSchema。"
		} else if _, err := agentcontract.ValidateLocalSchema(output, false); err != nil {
			tool.Issue = "输出契约不受支持，请检查本地 JSON Schema。"
		}
		tool.Supported = tool.Issue == ""
		tool.Exposure = agentcontract.MCPToolExposure{Name: name, Effect: "write", Skills: []string{}, ContractSHA256: tool.ContractSHA256, Replay: "never", ReferenceScope: "durable", ApprovalRequired: true}
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}
