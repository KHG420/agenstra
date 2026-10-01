// A local integration example with a deterministic DecisionModel and demo data.
// It calls a real REST capability and drives the host UI through the browser bridge.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

//go:embed index.html host.js frontend.json pack.json deployment.json
var assets embed.FS

type demoModel struct{}

func (demoModel) Decide(_ context.Context, packet agenstra.ContextPacket, _ string) (agenstra.Decision, error) {
	// Only the current request selects the scenario; history is not a new command.
	request := strings.Split(packet.Instruction, "\n\nEarlier conversation")[0]
	if strings.Contains(request, "补充") && len(packet.Followups) == 0 {
		return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "status", Prompt: "希望查看哪些订单？请输入“待处理”或“全部”。"}, nil
	}
	if len(packet.Followups) > 0 {
		request += strings.Join(packet.Followups, " ")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, observation := range packet.Observations {
		if observation.Status == "succeeded" {
			seen[observation.Capability] = true
		}
	}
	for _, fact := range packet.Facts {
		seen[fact.SourceCapability] = true
		if fact.ReferenceAvailable {
			ids = append(ids, fact.FactID)
		}
		if fact.SourceCapability == "ui.command_status" {
			data, _ := fact.Value["data"].(map[string]any)
			if data["status"] != "succeeded" {
				return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "演示操作未完成，请检查页面和任务状态。", FactIDs: []string{fact.FactID}}, nil
			}
		}
	}
	call := func(name string, args agenstra.JSON) (agenstra.Decision, error) {
		return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "tool_batch", Calls: []agenstra.ToolCall{{CallRef: fmt.Sprintf("demo-%d", packet.RoundIndex), Capability: name, Arguments: args, Reason: "Complete the requested demo scenario"}}}, nil
	}
	if !seen["orders.list"] {
		return call("orders.list", agenstra.JSON{})
	}
	if !seen["ui.get_context"] {
		return call("ui.get_context", agenstra.JSON{})
	}
	if strings.Contains(request, "1001") {
		if !seen["ui.open_order"] {
			return call("ui.open_order", agenstra.JSON{"id": "1001"})
		}
		return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "已查询演示订单，并在当前页面打开订单 1001。", FactIDs: ids}, nil
	}
	if !seen["ui.show_orders"] {
		status := "all"
		if strings.Contains(request, "待处理") {
			status = "pending"
		}
		return call("ui.show_orders", agenstra.JSON{"status": status})
	}
	return agenstra.Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "已从后端查询演示订单，并更新当前页面的订单列表和筛选条件。", FactIDs: ids}, nil
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("addr", "127.0.0.1:8092", "loopback listen address")
	flag.Parse()
	host, _, e := net.SplitHostPort(*address)
	if e != nil || (host != "127.0.0.1" && host != "::1") {
		return errors.New("demo requires a loopback address")
	}
	listener, e := net.Listen("tcp", *address)
	if e != nil {
		return e
	}
	defer listener.Close()
	dir, e := os.MkdirTemp("", "agenstra-web-demo-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"frontend.json", "pack.json", "deployment.json"} {
		raw, e := assets.ReadFile(name)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
			return e
		}
	}
	dep, e := agenstra.LoadDeployment(filepath.Join(dir, "deployment.json"))
	if e != nil {
		return e
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		return e
	}
	dep.Environment["DEMO_SESSION_KEY"] = hex.EncodeToString(key)
	dep.Environment["DEMO_API_URL"] = "http://" + listener.Addr().String()
	store, e := agenstra.NewSQLiteStore(dep.DatabasePath())
	if e != nil {
		return e
	}
	defer store.Close()
	agent := agenstra.NewAgentHost(store, dep.ProviderFactory, demoModel{}, dep.PolicyResolver)
	server, e := agenstra.NewHTTPServer(agent, dep, true, 100*time.Millisecond)
	if e != nil {
		return e
	}
	defer server.Close()
	// Demo-only identity. Production must resolve the signed-in user from a
	// validated application session, never trust a browser-supplied owner ID.
	server.Web.AuthenticateRequest = func(*http.Request) (string, error) { return "demo", nil }
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if name != "index.html" && name != "host.js" {
			http.NotFound(w, r)
			return
		}
		raw, e := assets.ReadFile(name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		if name == "host.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Write(raw)
	})
	mux.HandleFunc("GET /demo/orders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agenstra.JSON{"orders": []agenstra.JSON{
			{"id": "1001", "customer": "青岚工作室", "status": "pending", "amount": 1280},
			{"id": "1002", "customer": "北岸设计", "status": "completed", "amount": 3600},
			{"id": "1003", "customer": "木禾工坊", "status": "pending", "amount": 860},
		}})
	})
	for _, path := range []string{"/web/", "/chat/", "/browser/", "/healthz", "/readyz"} {
		mux.Handle(path, server.Handler())
	}
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	fmt.Printf("Agenstra web demo: http://%s (deterministic model, demo data, temporary databases)\n", listener.Addr())
	e = httpServer.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
