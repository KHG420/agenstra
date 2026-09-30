package agenstra

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryImmutableRevisionsAndGrants(t *testing.T) {
	dir := t.TempDir()
	r := NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
	if e := r.Initialize(); e != nil {
		t.Fatal(e)
	}
	if e := r.Initialize(); e != nil {
		t.Fatalf("second initialization: %v", e)
	}
	defer r.Close()
	manifest := testRegistryManifest("records.get")
	first, e := r.Publish("records", "1.0.0", manifest, map[string]string{})
	if e != nil {
		t.Fatal(e)
	}
	digest := first["digest"].(string)
	again, e := r.Publish("records", "1.0.0", manifest, map[string]string{})
	if e != nil || again["digest"] != digest {
		t.Fatalf("idempotent publish: %v %v", again, e)
	}
	changed := testRegistryManifest("records.other")
	if _, e = r.Publish("records", "1.0.0", changed, map[string]string{}); !registryHasCode(e, "version_already_published") {
		t.Fatalf("expected immutable version, got %v", e)
	}
	zero := 0
	activated, e := r.Activate("records", digest, &zero)
	if e != nil || activated["revision"] != 1 {
		t.Fatalf("activate: %v %v", activated, e)
	}
	if _, e = r.Activate("records", digest, &zero); !registryHasCode(e, "revision_conflict") {
		t.Fatalf("expected revision conflict: %v", e)
	}
	bad := map[string]any{"granted_capabilities": []any{"records.delete"}, "approval_capabilities": []any{}}
	if e = r.PutBinding("alice", "records", bad); !registryHasCode(e, "binding_capability_missing") {
		t.Fatalf("expected grant rejection: %v", e)
	}
	good := map[string]any{"granted_capabilities": []any{"records.get"}, "approval_capabilities": []any{}}
	if e = r.PutBinding("alice", "records", good); e != nil {
		t.Fatal(e)
	}
	present, config, e := r.Binding("alice", "records")
	if e != nil || !present || config == nil {
		t.Fatalf("binding missing: %v %v %v", present, config, e)
	}
	if e = r.DisableBinding("alice", "records"); e != nil {
		t.Fatal(e)
	}
	present, config, e = r.Binding("alice", "records")
	if e != nil || !present || config != nil {
		t.Fatalf("disabled binding: %v %v %v", present, config, e)
	}
	path, e := r.ReleasePath("records", digest)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte(`{}`), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ReleasePath("records", digest); !registryHasCode(e, "release_tampered") {
		t.Fatalf("expected tamper detection: %v", e)
	}
}
func registryHasCode(err error, code string) bool {
	var r *RegistryError
	return errors.As(err, &r) && r.Code == code
}

func testRegistryManifest(name string) map[string]any {
	return map[string]any{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1.0.0", "guidance": "Use reviewed record data.", "base_url_env": "RECORDS_URL", "capabilities": []any{map[string]any{"name": name, "description": "Read a record", "method": "GET", "path": "/records/{record_id}", "effect": "read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "object", "properties": map[string]any{"record_id": map[string]any{"type": "string"}}, "required": []any{"record_id"}, "additionalProperties": false}}, "required": []any{"path"}, "additionalProperties": false}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}}}
}
