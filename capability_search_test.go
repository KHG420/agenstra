package agenstra

import (
	"context"
	"strings"
	"testing"
)

func searchTestCapabilities() map[string]CapabilityDescription {
	return map[string]CapabilityDescription{
		"alpha.read":           {Name: "alpha.read", Description: "Read accounts", InputSchema: JSON{"type": "object", "properties": JSON{"account_id": JSON{"type": "string"}}}},
		"beta.read":            {Name: "beta.read", Description: "Read accounts", InputSchema: JSON{"type": "object"}},
		"omega.submit_invoice": {Name: "omega.submit_invoice", Description: "Submit invoice", InputSchema: JSON{"type": "object", "properties": JSON{"invoice_id": JSON{"type": "string"}}}},
		"secret.audit":         {Name: "secret.audit", Description: "Audit invoices", InputSchema: JSON{"type": "object"}},
	}
}

func capabilityNames(packet ContextPacket) []string {
	names := []string{}
	for _, cap := range packet.Capabilities {
		name, _ := cap["name"].(string)
		names = append(names, name)
	}
	return names
}

func TestCapabilitySearchFindsOmittedAuthorizedCapability(t *testing.T) {
	caps := searchTestCapabilities()
	provider := &coreTestProvider{caps: caps}
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true}
	r := &AgentRuntime{Provider: provider, Grants: grants, MaxContextCapabilities: 2, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		return Decision{Kind: "search_capabilities", Query: "submit_invoice"}, nil
	})}
	state, err := r.NewState("Help me", "")
	if err != nil {
		t.Fatal(err)
	}
	initial := r.Context(state)
	if len(initial.Capabilities) != 2 || initial.CapabilityCatalogTotal != 3 || initial.RuntimeFeatures[len(initial.RuntimeFeatures)-1] != "capability_search" || strings.Contains(strings.Join(capabilityNames(initial), ","), "omega.submit_invoice") {
		t.Fatal("bounded initial catalog", capabilityNames(initial), initial.CapabilityCatalogTotal)
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if provider.called != 0 {
		t.Fatal("catalog search invoked a business capability")
	}
	packet := r.Context(state)
	if len(packet.Capabilities) != 2 || capabilityNames(packet)[0] != "omega.submit_invoice" || len(packet.CapabilitySearchResults) != 1 || packet.CapabilitySearchResults[0] != "omega.submit_invoice" {
		t.Fatal("search did not reveal omitted capability", capabilityNames(packet), packet.CapabilitySearchResults)
	}
	grants["omega.submit_invoice"] = false
	packet = r.Context(state)
	if strings.Contains(strings.Join(capabilityNames(packet), ","), "omega.submit_invoice") || len(packet.CapabilitySearchResults) != 0 {
		t.Fatal("revoked capability remained visible", capabilityNames(packet), packet.CapabilitySearchResults)
	}
}

func TestCapabilitySearchEmptyStableAndLegacy(t *testing.T) {
	if _, err := strictDecision([]byte(`{"kind":"search_capabilities","query":"invoice"}`)); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []Decision{{Kind: "search_capabilities", Query: " "}, {Kind: "search_capabilities", Query: strings.Repeat("x", 301)}} {
		if decision.Validate() == nil {
			t.Fatal("accepted invalid search query", decision.Query)
		}
	}
	caps := searchTestCapabilities()
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true}
	for i := 0; i < 10; i++ {
		got := searchAuthorizedCapabilities(caps, grants, "read accounts", 2)
		if len(got) != 2 || got[0] != "alpha.read" || got[1] != "beta.read" {
			t.Fatal("search order changed", got)
		}
	}
	if got := searchAuthorizedCapabilities(caps, grants, "unmatched_term", 2); len(got) != 0 {
		t.Fatal("unexpected match", got)
	}
	searchRuntime := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants, MaxContextCapabilities: 1, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		return Decision{Kind: "search_capabilities", Query: "unmatched_term"}, nil
	})}
	searchState, callErr := searchRuntime.NewState("Help me", "")
	if callErr != nil {
		t.Error(callErr)
	}
	if err := searchRuntime.Step(t.Context(), searchState, nil); err != nil {
		t.Fatal(err)
	}
	if packet := searchRuntime.Context(searchState); len(packet.CapabilitySearchResults) != 0 || !strings.Contains(strings.Join(packet.ContextOmissions, " "), "no authorized matches") {
		t.Fatal("empty search was not visible to model", packet.ContextOmissions)
	}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants}
	state, callErr2 := r.NewState("Help me", "")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	packet := r.Context(state)
	if len(packet.Capabilities) != 3 || packet.CapabilityCatalogTotal != 0 {
		t.Fatal("default catalog changed", capabilityNames(packet), packet.CapabilityCatalogTotal)
	}
	settings := DefaultHostSettings()
	for _, invalid := range []int{-1, 201} {
		settings.MaxContextCapabilities = invalid
		if settings.Validate() == nil {
			t.Fatal("accepted invalid capability limit", invalid)
		}
	}
	settings.MaxContextCapabilities = 200
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitySearchRetainsPriorDiscoveriesWithinCatalogBudget(t *testing.T) {
	caps := searchTestCapabilities()
	for _, name := range []string{"delta.read", "lambda.read"} {
		caps[name] = CapabilityDescription{Name: name, InputSchema: JSON{"type": "object"}}
	}
	grants := map[string]bool{"alpha.read": true, "beta.read": true, "omega.submit_invoice": true, "delta.read": true, "lambda.read": true}
	decisions := []Decision{
		{Kind: "search_capabilities", Query: "omega.submit_invoice"},
		{Kind: "search_capabilities", Query: "beta.read"},
		{Kind: "inspect_capability", Name: "lambda.read"},
		{Kind: "search_capabilities", Query: "delta.read"},
	}
	provider := &coreTestProvider{caps: caps}
	r := &AgentRuntime{Provider: provider, Grants: grants, MaxContextCapabilities: 3, Model: decisionModelFunc(func(context.Context, ContextPacket, string) (Decision, error) {
		next := decisions[0]
		decisions = decisions[1:]
		return next, nil
	})}
	state, err := r.NewState("Help me", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.Step(t.Context(), state, nil); err != nil {
			t.Fatal(err)
		}
	}
	packet := r.Context(state)
	names := capabilityNames(packet)
	if len(names) != 3 || !containsString(names, "omega.submit_invoice") || !containsString(names, "beta.read") || !containsString(names, "lambda.read") {
		t.Fatal("later discovery displaced an earlier needed contract", names)
	}
	if provider.called != 0 {
		t.Fatal("discovery executed a business capability")
	}
	grants["omega.submit_invoice"] = false
	if names := capabilityNames(r.Context(state)); containsString(names, "omega.submit_invoice") || containsString(names, "secret.audit") {
		t.Fatal("discovery history bypassed current authorization", names)
	}
	if err := r.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	names = capabilityNames(r.Context(state))
	if len(names) > 3 || names[0] != "delta.read" || !containsString(names, "lambda.read") {
		t.Fatal("retained discoveries hid a new search or exceeded the budget", names)
	}
}

func TestCapabilitySearchFindsDeferredUnionOperationsAndNestedParameters(t *testing.T) {
	cap := CapabilityDescription{Name: "records.write", Description: "Manage records", InputSchema: JSON{"anyOf": []JSON{
		JSON{"properties": JSON{
			"operation": JSON{"const": "archiveRecord"},
			"arguments": JSON{"type": "object", "properties": JSON{"recordIDs": JSON{"type": "array", "items": JSON{"type": "string"}}}},
		}},
		JSON{"allOf": []JSON{JSON{"properties": JSON{
			"operation": JSON{"enum": []string{"changeQuantity"}},
			"arguments": JSON{"properties": JSON{"quantity": JSON{"type": "integer", "description": "调整库存数量"}, "memo": JSON{"type": "string", "description": strings.Repeat("x", 2100)}}},
		}}}},
	}}}
	caps := map[string]CapabilityDescription{cap.Name: cap, "alpha.read": {Name: "alpha.read", Description: "Read unrelated data"}, "private.write": {Name: "private.write", InputSchema: cap.InputSchema}}
	grants := map[string]bool{cap.Name: true, "alpha.read": true}
	if cap.ModelView()["schema_requires_inspection"] != true {
		t.Fatal("test contract must be deferred")
	}
	for _, query := range []string{"archiveRecord", "CHANGEQUANTITY", "recordIDs", "库存数量"} {
		got := searchAuthorizedCapabilities(caps, grants, query, 2)
		if len(got) != 1 || got[0] != cap.Name {
			t.Fatalf("query %q did not discover the authorized union contract: %v", query, got)
		}
	}
	r := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants, MaxContextCapabilities: 1}
	state, err := r.NewState("archiveRecord", "")
	if err != nil {
		t.Fatal(err)
	}
	packet := r.Context(state)
	if names := capabilityNames(packet); len(names) != 1 || names[0] != cap.Name {
		t.Fatal("initial selection lost operation match", names)
	}
	if _, ok := packet.Capabilities[0]["input_schema"]; !ok || packet.Capabilities[0]["schema_requires_inspection"] == true {
		t.Fatal("selected capability was discovered without its contract", packet.Capabilities[0])
	}
	grants[cap.Name] = false
	if got := searchAuthorizedCapabilities(caps, grants, "archiveRecord", 2); len(got) != 0 {
		t.Fatal("schema search leaked a revoked capability", got)
	}
}

func TestCapabilitySearchFindsQualifiedOperationsAdjacentToChinese(t *testing.T) {
	schema := JSON{"type": "object", "properties": JSON{"operation": JSON{"enum": []string{"getCurrentRecord"}}}}
	caps := map[string]CapabilityDescription{
		"ui.get_context":  {Name: "ui.get_context", Description: "Read current browser context"},
		"ui.records_read": {Name: "ui.records_read", Description: "Read records", InputSchema: schema, Operation: &OperationBinding{PollCapability: "ui.command_status"}},
		"private.records": {Name: "private.records", Description: "Read records", InputSchema: schema},
		"alpha.read":      {Name: "alpha.read", Description: "Read unrelated data"},
	}
	grants := map[string]bool{"ui.get_context": true, "ui.records_read": true, "alpha.read": true}
	for _, query := range []string{"records.getCurrentRecord", "RECORDS.GETCURRENTRECORD", "请调用records.getCurrentRecord()，不要用相似接口替代", "module.records.getCurrentRecord"} {
		if got := searchAuthorizedCapabilities(caps, grants, query, 1); len(got) != 1 || got[0] != "ui.records_read" {
			t.Fatalf("qualified operation %q not found: %v", query, got)
		}
	}
	runtime := &AgentRuntime{Provider: &coreTestProvider{caps: caps}, Grants: grants, MaxContextCapabilities: 2}
	state, err := runtime.NewState("请调用records.getCurrentRecord()", "")
	if err != nil {
		t.Fatal(err)
	}
	if names := capabilityNames(runtime.Context(state)); len(names) != 2 || !containsString(names, "ui.records_read") || !containsString(names, "ui.get_context") || containsString(names, "private.records") {
		t.Fatal("qualified operation lost its authorized prerequisite", names)
	}
	grants["ui.records_read"] = false
	if got := searchAuthorizedCapabilities(caps, grants, "records.getCurrentRecord", 4); len(got) != 0 {
		t.Fatal("qualified operation exposed a revoked capability", got)
	}
}

func TestCapabilitySearchDoesNotLetBroadSchemaFieldsDisplaceNamedSubjects(t *testing.T) {
	caps := map[string]CapabilityDescription{
		"alpha.bulk_read": {Name: "alpha.bulk_read", Description: "Manage unrelated records", InputSchema: JSON{"type": "object", "properties": JSON{
			"today": JSON{"type": "string"}, "usage": JSON{"type": "string"}, "statistics": JSON{"type": "string"}, "batch": JSON{"type": "string"},
			"system": JSON{"type": "string"}, "version": JSON{"type": "string"}, "host": JSON{"type": "string"},
		}}},
		"zeta.usage_read":  {Name: "zeta.usage_read", Description: "Read current user usage", InputSchema: JSON{"type": "object", "description": "Get usage statistics"}},
		"zeta.system_read": {Name: "zeta.system_read", Description: "Read system version", InputSchema: JSON{"type": "object", "description": "Get current version"}},
	}
	grants := map[string]bool{"alpha.bulk_read": true, "zeta.usage_read": true, "zeta.system_read": true}
	for _, tc := range []struct{ query, want string }{
		{"today usage statistics batch", "zeta.usage_read"},
		{"system version host", "zeta.system_read"},
		{"version", "zeta.system_read"},
		{"business version", "zeta.system_read"},
	} {
		if got := searchAuthorizedCapabilities(caps, grants, tc.query, 1); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("subject query %q displaced by incidental schema fields: %v", tc.query, got)
		}
	}
	// Field-only searches still discover a contract without widening grants.
	if got := searchAuthorizedCapabilities(caps, grants, "batch", 1); len(got) != 1 || got[0] != "alpha.bulk_read" {
		t.Fatal("field search lost the authorized contract", got)
	}
	grants["alpha.bulk_read"] = false
	if got := searchAuthorizedCapabilities(caps, grants, "batch", 1); len(got) != 0 {
		t.Fatal("field search exposed a revoked contract", got)
	}
}

func TestChineseNaturalInstructionsSelectActionsWithPrerequisite(t *testing.T) {
	caps := map[string]CapabilityDescription{
		"alpha.delete":     {Name: "alpha.delete", Description: "删除人员"},
		"ui.get_context":   {Name: "ui.get_context", Description: "读取页面观察"},
		"ui.read_activity": {Name: "ui.read_activity", Description: "读取活动人数、奖项余量与历史记录"},
		"ui.start_round":   {Name: "ui.start_round", Description: "开始当前奖项的抽奖滚动"},
		"ui.finish_round":  {Name: "ui.finish_round", Description: "停止并揭晓本轮抽奖，保存中奖名单"},
		"ui.open_panel":    {Name: "ui.open_panel", Description: "打开记录面板"},
		"ui.set_theme":     {Name: "ui.set_theme", Description: "切换页面主题"},
		"secret.read":      {Name: "secret.read", Description: "读取活动人数、奖项余量与历史记录"},
	}
	grants := map[string]bool{}
	for name, cap := range caps {
		if strings.HasPrefix(name, "ui.") && name != "ui.get_context" {
			cap.Operation = &OperationBinding{PollCapability: "ui.command_status"}
			caps[name] = cap
		}
		grants[name] = name != "secret.read"
	}
	for _, tc := range []struct{ instruction, action string }{
		{"请只读查询活动人数和奖项余量，不要修改数据。", "ui.read_activity"},
		{"审批后仅开始当前奖项的抽奖滚动。", "ui.start_round"},
		{"停止并揭晓本轮抽奖，然后核对保存的中奖名单。", "ui.finish_round"},
		{"请打开记录面板。", "ui.open_panel"},
		{"请把页面切换为深色主题。", "ui.set_theme"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			names, _, _ := selectedCapabilityNames(caps, grants, tc.instruction, &RuntimeState{}, 3)
			if !containsString(names, tc.action) || !containsString(names, "ui.get_context") || containsString(names, "secret.read") || len(names) > 3 {
				t.Fatal("missing authorized action/prerequisite", names)
			}
		})
	}
	grants["ui.get_context"] = false
	names, _, _ := selectedCapabilityNames(caps, grants, "打开记录面板", &RuntimeState{}, 2)
	if containsString(names, "ui.get_context") || !containsString(names, "ui.open_panel") {
		t.Fatal("prerequisite bypassed authorization", names)
	}
}
