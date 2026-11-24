package react

import (
	"fmt"
	"slices"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestExplicitOperationNameSurvivesBroadLocalizedWrappers(t *testing.T) {
	const target = "device-service::devices.changeDispatch"
	caps := map[string]agentcontract.CapabilityDescription{
		target: {Name: target, Description: "Set only the device dispatch flag to an explicit boolean.", Effect: "write",
			InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{"enabled": agentcontract.JSON{"type": "boolean"}}}},
	}
	grants := map[string]bool{target: true}
	for index := range 10 {
		name := fmt.Sprintf("ui.bundle_%d", index)
		caps[name] = agentcontract.CapabilityDescription{Name: name, Effect: "write",
			Description: "管理员设备信息、调度标记和设置。读取测试设备详情后，修改标记，然后读取详情确认；保持状态、到期时间和其他字段不变。",
			InputSchema: agentcontract.JSON{"type": "object", "properties": agentcontract.JSON{
				"operation": agentcontract.JSON{"type": "string", "enum": []any{"getById", "changeDispatch", "updateSettings"}}}},
		}
		grants[name] = true
	}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants, MaxContextCapabilities: 8}
	state, err := runtime.NewState("先读取测试设备详情，然后用 changeDispatch 修改调度标记。修改后再读取详情确认，保持状态、到期时间和其他字段不变。", "")
	if err != nil {
		t.Fatal(err)
	}
	names := capabilityNames(runtime.Context(state))
	if !slices.Contains(names, target) {
		t.Fatalf("the explicitly named operation was omitted behind broad wrappers: %v", names)
	}
	grants[target] = false
	if names := capabilityNames(runtime.Context(state)); slices.Contains(names, target) {
		t.Fatalf("an ungranted operation was revealed: %v", names)
	}
}

func TestOperationNameRankingKeepsOrdinaryVerbsLexical(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"helper.get":     {Name: "helper.get", Description: "Get integration information."},
		"people.profile": {Name: "people.profile", Description: "Read the account profile and display the profile details."},
	}
	grants := map[string]bool{"helper.get": true, "people.profile": true}
	if got := searchAuthorizedCapabilities(caps, grants, "get account profile details", 1); len(got) != 1 || got[0] != "people.profile" {
		t.Fatalf("ordinary prose was mistaken for an explicit operation identity: %v", got)
	}
}

func TestOperationNameRankingDoesNotGuessAnAmbiguousProject(t *testing.T) {
	caps := map[string]agentcontract.CapabilityDescription{
		"north::devices.changeDispatch": {Name: "north::devices.changeDispatch", Description: "Set dispatch."},
		"south::devices.changeDispatch": {Name: "south::devices.changeDispatch", Description: "Set dispatch."},
		"ui.devices":                    {Name: "ui.devices", Description: "管理设备调度标记，读取详情确认后修改并确认设备状态。"},
	}
	grants := map[string]bool{}
	for name := range caps {
		grants[name] = true
	}
	query := "changeDispatch 管理设备调度标记，读取详情确认后修改并确认设备状态"
	if got := searchAuthorizedCapabilities(caps, grants, query, 1); len(got) != 1 || got[0] != "ui.devices" {
		t.Fatalf("an ambiguous operation name overrode lexical selection: %v", got)
	}
	grants["south::devices.changeDispatch"] = false
	if got := searchAuthorizedCapabilities(caps, grants, query, 1); len(got) != 1 || got[0] != "north::devices.changeDispatch" {
		t.Fatalf("ungranted tools changed the unique authorized operation: %v", got)
	}
	grants["south::devices.changeDispatch"] = true
	query += " south::devices.changeDispatch"
	if got := searchAuthorizedCapabilities(caps, grants, query, 1); len(got) != 1 || got[0] != "south::devices.changeDispatch" {
		t.Fatalf("the exact project identity lost priority: %v", got)
	}
}
