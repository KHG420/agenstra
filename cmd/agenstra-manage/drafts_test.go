package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

func TestDraftCLIDiscoveryUsesRevisionAndEnvironmentReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.json")
	if err := os.WriteFile(path, []byte(`{"MCP_URL":"secret:MCP_URL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	call := func(method, route string, body any) (any, error) {
		calls++
		if calls == 1 {
			if method != "GET" || route != "/admin/api/drafts/mcp-work" {
				t.Fatal(method, route)
			}
			return map[string]any{"revision": float64(7), "manifest": map[string]any{"schema": "agenstra.mcp-pack.v1"}}, nil
		}
		value := body.(map[string]any)
		if method != "POST" || route != "/admin/api/drafts/mcp-work/discover" || value["expected_revision"] != float64(7) || value["environment"].(map[string]string)["MCP_URL"] != "secret:MCP_URL" {
			t.Fatal(method, route, value)
		}
		return map[string]any{"tools": []any{}}, nil
	}
	if _, err := draftCommand([]string{"discover", "mcp-work", path}, call); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if _, err := draftCommand(nil, call); err == nil || !strings.Contains(err.Error(), "discover MCP_DRAFT_ID") {
		t.Fatal("discovery missing from usage", err)
	}
}

func TestDraftCLISharedAPILifecycle(t *testing.T) {
	dir := t.TempDir()
	d := &agenstra.Deployment{BaseDir: dir, Config: agenstra.DeploymentConfig{Users: map[string]agenstra.UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}, Management: &agenstra.ManagementConfig{DatabasePath: "registry.sqlite3", PackageDir: "packages", AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ALICE_KEY": "alice-test", "ADMIN_KEY": "distinct-admin-key-24-characters"}}
	d.Registry = agenstra.NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	store, err := agenstra.NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := agenstra.NewHTTPServer(agenstra.NewAgentHost(store, nil, nil, nil), d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	call := func(method, path string, body any) (any, error) {
		return managementRequest(httpServer.URL, d.Environment["ADMIN_KEY"], method, path, body)
	}
	run := func(args ...string) map[string]any {
		t.Helper()
		result, err := draftCommand(args, call)
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}
	first := run("create", "work", "--name", "records")
	if first["revision"] != float64(1) {
		t.Fatalf("create: %v", first)
	}
	write := func(name string, v any) string {
		t.Helper()
		path := filepath.Join(dir, name)
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run("set", "work", "basic", write("basic.json", map[string]any{"guidance": "Use reviewed records."}))
	run("set", "work", "connection", write("connection.json", map[string]any{"base_url_env": "RECORDS_URL"}))
	cap := map[string]any{"name": "records.get", "description": "Read records", "method": "GET", "path": "/records", "effect": "read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	capFile := write("cap.json", cap)
	run("add", "work", capFile)
	if _, err := draftCommand([]string{"add", "work", capFile}, call); err == nil {
		t.Fatal("duplicate capability silently overwritten")
	}
	updated := run("update", "work", "records.get", write("rules.json", map[string]any{"approval_required": true}))
	items := updated["manifest"].(map[string]any)["capabilities"].([]any)
	item := items[0].(map[string]any)
	if item["description"] != "Read records" || item["approval_required"] != true {
		t.Fatalf("partial update discarded fields: %v", item)
	}
	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# Record rules"), 0600); err != nil {
		t.Fatal(err)
	}
	run("skill", "work", "rules", skillFile, "--description", "Record interpretation")
	ready := run("validate", "work")
	if len(ready["issues"].([]any)) != 0 {
		t.Fatalf("validation: %v", ready)
	}
	exportDir := filepath.Join(dir, "export")
	run("export", "work", exportDir)
	if _, err := os.Stat(filepath.Join(exportDir, "skills/rules/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := draftCommand([]string{"export", "work", exportDir}, call); err == nil {
		t.Fatal("export overwrote existing directory")
	}
	release := run("publish", "work")
	if release["pack_id"] != "records" {
		t.Fatalf("publish: %v", release)
	}
	active, err := d.Registry.ActiveRelease("records")
	if err != nil || active != "" {
		t.Fatal("CLI publish activated automatically")
	}
	run("create", "second", "--name", "records")
	run("import", "second", filepath.Join(exportDir, "pack.json"))
	second := run("show", "second")
	if len(second["manifest"].(map[string]any)["capabilities"].([]any)) != 1 || len(second["skills"].(map[string]any)) != 1 {
		t.Fatal("export/import did not preserve package contents")
	}
}

func TestDraftCLIEditRetainsFileOnConflict(t *testing.T) {
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	marker := filepath.Join(dir, "edited-path")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > '" + marker + "'\nprintf '%s' '{\"schema\":\"agenstra.rest-pack.v2\",\"guidance\":\"Unsent edit\"}' > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	call := func(method, path string, body any) (any, error) {
		if method == "GET" {
			return map[string]any{"revision": float64(1), "manifest": map[string]any{"schema": "agenstra.rest-pack.v2"}, "skills": map[string]any{}}, nil
		}
		return nil, fmt.Errorf("draft_revision_conflict")
	}
	_, err := draftCommand([]string{"edit", "work"}, call)
	if err == nil {
		t.Fatal("conflict accepted")
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatal(readErr)
	}
	path := string(raw)
	defer os.Remove(path)
	data, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(data), "Unsent edit") || !strings.Contains(err.Error(), path) {
		t.Fatalf("edited file lost: %s %v %v", data, readErr, err)
	}
}
