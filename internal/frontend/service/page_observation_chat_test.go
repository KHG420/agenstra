package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPageObservationPreservesExistingChatsAndLegacyRuns(t *testing.T) {
	dir := t.TempDir()
	store, provider, model := testStore(t), &hostProvider{}, &hostModel{}
	host := testHost(t, store, provider, model)
	host.ReleaseResolver = func(context.Context, string, string) (string, error) { return "host-v1", nil }
	cfg := agentcontract.WebIntegrationConfig{DatabasePath: filepath.Join(dir, "web.sqlite3"), Chat: true, SessionKeyEnv: "WEB_KEY", Integrations: map[string]agentcontract.WebProfileConfig{"records": {PackID: "records"}}}
	dep := &deployassembly.Deployment{BaseDir: dir, Config: agentcontract.DeploymentConfig{Packs: map[string]agentcontract.PackConfig{"records": {Path: "unused"}}}, Environment: map[string]string{"WEB_KEY": strings.Repeat("k", 32)}}
	w, err := NewWebIntegration(host, dep, cfg)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := host.Create(t.Context(), "alice", "records", "Keep the original task", "legacy-page-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	profile := frontendTestProfile(false)
	profile.Actions = nil
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.BrowserBridge = true
	cfg.Integrations["records"] = agentcontract.WebProfileConfig{PackID: "records", FrontendProfilePath: "page.json"}
	host = testHost(t, store, provider, model)
	host.ReleaseResolver = func(context.Context, string, string) (string, error) { return "host-v1", nil }
	w, err = NewWebIntegration(host, dep, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	if result, err := host.Drive(t.Context(), legacy.RunID, "alice"); err != nil || result.Status != "completed" {
		t.Fatal("page observation prevented an existing run from resuming", result.Status, err)
	}
	server := &HTTPServer{Host: host, Web: w}
	submit := func(client, session, key string) *httptest.ResponseRecorder {
		body, err := json.Marshal(agentcontract.JSON{"client_id": client, "text": "Keep the selected conversation", "session_id": session})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/chat/v1/conversations/"+conversation.ID+"/messages", strings.NewReader(string(body)))
		request.Header.Set("X-Agenstra-Browser-Key", key)
		out := httptest.NewRecorder()
		server.chatHTTP(out, request, "alice")
		return out
	}
	if out := submit("headless", "", ""); out.Code != 200 {
		t.Fatal("a legacy or WeChat client now requires a browser", out.Code, out.Body.String())
	}
	session, key, err := w.CreateBrowserSession(t.Context(), "alice", "records", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err = w.UpdatePageObservation("alice", session.ID, key, session.Generation, session.ContextRevision, agentcontract.JSON{"page": "vessel-a"})
	if err != nil {
		t.Fatal(err)
	}
	if out := submit("forged", session.ID, ""); out.Code != 401 {
		t.Fatal("a browser binding accepted a missing session key", out.Code)
	}
	out := submit("browser", session.ID, key)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var message agentcontract.ChatMessage
	if err := json.Unmarshal(out.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	pageProvider, err := host.ProviderFactory(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pageProvider.Close(); err != nil {
			t.Error(err)
		}
	})
	read := func(owner string) agentcontract.CapabilityResult {
		result, err := pageProvider.Invoke(t.Context(), "ui.get_context", agentcontract.JSON{}, &agentcontract.InvocationContext{OwnerID: owner, RunID: message.RunID, InvocationID: agentcontract.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := read("alice")
	if first.ErrorCode != "" || first.Data["context"].(map[string]any)["page"] != "vessel-a" {
		t.Fatal(first)
	}
	if _, err := w.UpdatePageObservation("alice", session.ID, key, session.Generation, session.ContextRevision, agentcontract.JSON{"page": "weather"}); err != nil {
		t.Fatal(err)
	}
	second := read("alice")
	if second.ErrorCode != "" || second.Data["context"].(map[string]any)["page"] != "weather" {
		t.Fatal("a deliberate page read did not observe navigation", second)
	}
	if result := read("bob"); result.ErrorCode == "" {
		t.Fatal("another owner read the attached page")
	}
	messages, err := w.Store.messages(w.Store.store.DB, "alice", conversation.ID)
	if err != nil || len(messages) != 2 || messages[1].RunID != message.RunID || messages[1].Text != "Keep the selected conversation" || strings.Contains(messages[1].Instruction, "weather") {
		t.Fatal("navigation changed the conversation or appended page data", messages, err)
	}
	if first.Data["context"].(map[string]any)["page"] != "vessel-a" {
		t.Fatal("navigation rewrote an earlier observed fact")
	}
}

func TestExistingPackCanAddOptionalBrowserActions(t *testing.T) {
	dir := t.TempDir()
	raw, err := json.Marshal(frontendTestProfile(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	host := testHost(t, testStore(t), &hostProvider{}, &hostModel{})
	cfg := agentcontract.WebIntegrationConfig{Chat: true, BrowserBridge: true, DatabasePath: filepath.Join(dir, "web.sqlite3"), SessionKeyEnv: "WEB_KEY", Integrations: map[string]agentcontract.WebProfileConfig{"records": {PackID: "records", FrontendProfilePath: "page.json"}}}
	dep := &deployassembly.Deployment{BaseDir: dir, Environment: map[string]string{"WEB_KEY": strings.Repeat("k", 32)}}
	w, err := NewWebIntegration(host, dep, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	conversation, err := w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.SubmitMessage(t.Context(), "alice", conversation.ID, "headless", "Read the record", ""); err != nil {
		t.Fatal("headless chat became dependent on an open browser", err)
	}
	if !w.browserOptional("records") {
		t.Fatal("existing pack identity lost")
	}
}
