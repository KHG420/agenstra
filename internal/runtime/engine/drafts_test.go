package engine

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func newDraftRegistry(t *testing.T) *CapabilityRegistry {
	t.Helper()
	dir := t.TempDir()
	r := NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}
func intRef(n int) *int { return &n }
func TestDraftIncrementalPersistenceAndPublish(t *testing.T) {
	r := newDraftRegistry(t)
	d, err := r.SaveDraft("records-draft", intRef(0), map[string]any{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1.0.0"}, nil)
	if err != nil || len(d.Issues) == 0 {
		t.Fatalf("incomplete save: %v %v", d, err)
	}
	if _, err = r.SaveDraft("records-draft", intRef(0), d.Manifest, nil); !registryHasCode(err, "draft_revision_conflict") {
		t.Fatalf("duplicate create: %v", err)
	}
	d, err = r.EditDraft(d.DraftID, DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "basic", Value: map[string]any{"guidance": "Use reviewed data."}})
	if err != nil {
		t.Fatal(err)
	}
	d, err = r.EditDraft(d.DraftID, DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "connection", Value: map[string]any{"base_url_env": "RECORDS_URL"}})
	if err != nil {
		t.Fatal(err)
	}
	caps := testRegistryManifest("records.get")["capabilities"].([]any)
	d, err = r.EditDraft(d.DraftID, DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "capabilities", Items: caps})
	if err != nil || len(d.Issues) != 0 {
		t.Fatalf("complete draft: %v %v", d, err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = r.Initialize(); err != nil {
		t.Fatal(err)
	}
	restored, err := r.Draft(d.DraftID)
	if err != nil || restored.Revision != d.Revision || len(restored.Issues) != 0 {
		t.Fatalf("restart: %v %v", restored, err)
	}
	if active, callErr := r.ActiveRelease("records"); callErr != nil {
		t.Error(callErr)
	} else if active != "" {
		t.Fatal("draft activated a release")
	}
	result, err := r.Publish("records", "1.0.0", restored.Manifest, restored.Skills)
	if err != nil {
		t.Fatal(err)
	}
	restored.Manifest["guidance"] = "Updated rules"
	_, err = r.SaveDraft(d.DraftID, intRef(d.Revision), restored.Manifest, restored.Skills)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReleasePath("records", result["digest"].(string)); err != nil {
		t.Fatalf("draft edit changed published files: %v", err)
	}
}
func TestDraftMergeConflictAtomicityAndSkills(t *testing.T) {
	r := newDraftRegistry(t)
	manifest := testRegistryManifest("records.get")
	content := "# Rules\nRead the record."
	skill := map[string]any{"name": "rules", "description": "Rules", "path": "skills/rules.md", "sha256": skillDigest(content)}
	manifest["skills"] = []any{skill}
	d, err := r.SaveDraft("draft", intRef(0), manifest, map[string]string{"skills/rules.md": content})
	if err != nil {
		t.Fatal(err)
	}
	incoming := testRegistryManifest("records.other")
	incoming["version"] = "99"
	incoming["base_url_env"] = "OTHER_URL"
	incoming["skills"] = []any{skill}
	edit := DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "import", Value: incoming, Skills: map[string]string{"skills/rules.md": "changed"}}
	if _, err = r.EditDraft("draft", edit); !registryHasCode(err, "draft_item_conflict") {
		t.Fatalf("skill conflict: %v", err)
	}
	unchanged, callErr2 := r.Draft("draft")
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if unchanged.Revision != d.Revision || len(unchanged.Manifest["capabilities"].([]any)) != 1 {
		t.Fatal("partial import escaped rollback")
	}
	edit.Conflict = "keep"
	d, err = r.EditDraft("draft", edit)
	if err != nil {
		t.Fatal(err)
	}
	if d.Manifest["version"] != "1.0.0" || d.Manifest["base_url_env"] != "RECORDS_URL" || d.Skills["skills/rules.md"] != content || len(d.Manifest["capabilities"].([]any)) != 2 {
		t.Fatalf("keep changed unrelated data: %+v", d)
	}
	replacement := map[string]any{"name": "rules", "description": "New rules", "path": "skills/new.md", "sha256": skillDigest("new")}
	d, err = r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "skills", Items: []any{replacement}, Skills: map[string]string{"skills/new.md": "new"}, Conflict: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Skills) != 1 || d.Skills["skills/new.md"] != "new" {
		t.Fatalf("stale skill bytes: %v", d.Skills)
	}
	bad := []any{map[string]any{"name": "escape", "path": "../outside", "description": "x", "sha256": skillDigest("x")}}
	if _, err = r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "skills", Items: bad}); !registryHasCode(err, "invalid_skill_path") {
		t.Fatalf("unsafe path: %v", err)
	}
	if _, err = r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "basic", Value: map[string]any{"source": "unexpected"}}); !registryHasCode(err, "invalid_draft_field") {
		t.Fatalf("wrong field: %v", err)
	}
}
func TestDraftConcurrentRevisions(t *testing.T) {
	r := newDraftRegistry(t)
	d, err := r.SaveDraft("draft", intRef(0), testRegistryManifest("records.get"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "basic", Value: map[string]any{"guidance": "Updated"}})
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	successes, conflicts := 0, 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if registryHasCode(err, "draft_revision_conflict") {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent updates: %d successes %d conflicts", successes, conflicts)
	}
}
func TestDraftHTTPAuthAndPublication(t *testing.T) {
	r := newDraftRegistry(t)
	deployment := &Deployment{Registry: r, Config: DeploymentConfig{Management: &ManagementConfig{AdminAPIKeyEnv: "ADMIN_KEY"}}, Environment: map[string]string{"ADMIN_KEY": "distinct-admin-key-at-least-24"}}
	server := &HTTPServer{Deployment: deployment}
	call := func(method, path, key string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			var callErr3 error
			raw, callErr3 = json.Marshal(body)
			if callErr3 != nil {
				t.Error(callErr3)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		server.adminHTTP(w, req)
		return w
	}
	key := deployment.Environment["ADMIN_KEY"]
	if w := call("GET", "/admin/api/drafts", "user-key", nil); w.Code != 401 {
		t.Fatalf("draft auth: %d", w.Code)
	}
	if w := call("PUT", "/admin/api/drafts/draft", key, map[string]any{"expected_revision": 0, "manifest": map[string]any{"schema": "agenstra.rest-pack.v2"}}); w.Code != 200 {
		t.Fatalf("incomplete save: %s", w.Body.String())
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 1}); w.Code != 422 || !bytes.Contains(w.Body.Bytes(), []byte("issues")) {
		t.Fatalf("incomplete publish: %d %s", w.Code, w.Body.String())
	}
	if w := call("PUT", "/admin/api/drafts/draft", key, map[string]any{"expected_revision": 1, "manifest": testRegistryManifest("records.get")}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 1}); w.Code != 409 {
		t.Fatalf("stale publish: %d", w.Code)
	}
	if w := call("POST", "/admin/api/drafts/draft/publish", key, map[string]any{"expected_revision": 2}); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	if active, callErr4 := r.ActiveRelease("records"); callErr4 != nil {
		t.Error(callErr4)
	} else if active != "" {
		t.Fatal("publish activated automatically")
	}
	if w := call("GET", "/admin/assets/admin_drafts.js", "", nil); w.Code != 200 {
		t.Fatalf("draft script: %d", w.Code)
	}
	if w := call("PATCH", "/admin/api/drafts/draft", key, map[string]any{"section": "basic", "value": map[string]any{"name": "bad"}}); w.Code != 409 {
		t.Fatalf("missing revision: %d", w.Code)
	}
}
func TestDraftOpenAPIAppendUsesExistingMetadata(t *testing.T) {
	r := newDraftRegistry(t)
	d, err := r.SaveDraft("draft", intRef(0), testRegistryManifest("records.get"), nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"openapi": "3.0.3", "paths": map[string]any{"/other": map[string]any{"get": map[string]any{"operationId": "records.other", "responses": map[string]any{"200": map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}}}}}}
	d, err = r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "openapi", Value: map[string]any{"spec": spec, "operations": []any{"records.other"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Manifest["capabilities"].([]any)) != 2 || d.Manifest["guidance"] != "Use reviewed record data." || d.Manifest["version"] != "1.0.0" {
		t.Fatalf("openapi replaced metadata: %+v", d.Manifest)
	}
}

func TestDraftImportPreservesUndeclaredSkillContent(t *testing.T) {
	r := newDraftRegistry(t)
	d, err := r.SaveDraft("draft", intRef(0), testRegistryManifest("records.get"), map[string]string{"skills/pending.md": "Unfinished rules"})
	if err != nil {
		t.Fatal(err)
	}
	d, err = r.EditDraft("draft", DraftEdit{ExpectedRevision: intRef(d.Revision), Section: "import", Value: testRegistryManifest("records.other")})
	if err != nil {
		t.Fatal(err)
	}
	if d.Skills["skills/pending.md"] != "Unfinished rules" {
		t.Fatal("import discarded unfinished skill content")
	}
	found := false
	for _, issue := range d.Issues {
		if issue.Path == "skills/pending.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing pending skill declaration issue")
	}
}

func TestDraftMCPIncompleteContractIssue(t *testing.T) {
	r := newDraftRegistry(t)
	manifest := map[string]any{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1.0.0", "guidance": "Use reviewed tools.", "source": map[string]any{"transport": "streamable_http", "url_env": "MCP_URL"}, "tools": []any{map[string]any{"name": "records.get", "effect": "read"}}}
	d, err := r.SaveDraft("mcp-draft", intRef(0), manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Issues) != 1 || d.Issues[0].Path != "tools[0].contract_sha256" {
		t.Fatalf("MCP field feedback: %+v", d.Issues)
	}
	item := manifest["tools"].([]any)[0].(map[string]any)
	item["contract_sha256"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	d, err = r.SaveDraft("mcp-draft", intRef(d.Revision), manifest, nil)
	if err != nil || len(d.Issues) != 0 {
		t.Fatalf("MCP complete manifest: %v %v", d, err)
	}
}
