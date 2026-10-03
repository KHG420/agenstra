package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

func TestDemoConfigurationAndBrowserContract(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"deployment.json", "frontend.json", "pack.json"} {
		raw, e := assets.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	d, e := agenstra.LoadDeployment(filepath.Join(dir, "deployment.json"))
	if e != nil {
		t.Fatal(e)
	}
	d.Environment["DEMO_SESSION_KEY"] = strings.Repeat("k", 32)
	d.Environment["DEMO_API_URL"] = "http://127.0.0.1:8092"
	p, e := d.ProviderFactory(context.Background(), "demo", "orders")
	if e != nil {
		t.Fatal(e)
	}
	if err := p.Close(); err != nil {
		t.Error(err)
	}
	store, e := agenstra.NewSQLiteStore(d.DatabasePath())
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	h := agenstra.NewAgentHost(store, d.ProviderFactory, demoModel{}, d.PolicyResolver)
	s, e := agenstra.NewHTTPServer(h, d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(s.Close)
	_, _, e = s.Web.CreateBrowserSession(t.Context(), "demo", "orders-web", "1", []string{"ui.show_orders", "ui.open_order"})
	if e != nil {
		t.Fatal(e)
	}
}

func TestDemoModelRecognizesOriginalActionAfterReceiptPolling(t *testing.T) {
	packet := agenstra.ContextPacket{Instruction: "查询待处理订单并显示列表", Observations: []agenstra.Observation{
		{Capability: "orders.list", Status: "succeeded"},
		{Capability: "ui.get_context", Status: "succeeded"},
		{Capability: "ui.show_orders", Status: "succeeded"},
	}, Facts: []agenstra.FactView{{Fact: agenstra.Fact{FactID: agenstra.NewID(), SourceCapability: "ui.command_status", Value: agenstra.JSON{"data": agenstra.JSON{"status": "succeeded"}}}, ReferenceAvailable: true}}}
	d, e := (demoModel{}).Decide(t.Context(), packet, "")
	if e != nil || d.Kind != "final" {
		t.Fatal("polled operation was resubmitted", d, e)
	}
	if e = d.Validate(); e != nil {
		t.Fatal(e)
	}
	packet.Facts[0].Value["data"] = agenstra.JSON{"status": "failed"}
	d, e = (demoModel{}).Decide(t.Context(), packet, "")
	if e != nil || !strings.Contains(d.AnswerMarkdown, "未完成") {
		t.Fatal("failed result claimed success", d, e)
	}
}

func TestDemoInputDecisionUsesRuntimeProtocol(t *testing.T) {
	d, e := (demoModel{}).Decide(t.Context(), agenstra.ContextPacket{Instruction: "先让我补充筛选条件"}, "")
	if e != nil || d.Kind != "request_input" {
		t.Fatal(d, e)
	}
	if e = d.Validate(); e != nil {
		t.Fatal(e)
	}
}
