package engine

import (
	"errors"
	"fmt"
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
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(r.Close)
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

func TestRegistryReleaseRequiresCompleteJSONBeforeActivation(t *testing.T) {
	for _, location := range []string{"package file", "stored manifest"} {
		for _, tc := range []struct {
			name, suffix string
			wantError    bool
		}{
			{"whitespace", " \n\t", false},
			{"second document", " {}", true},
			{"trailing garbage", " incomplete", true},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				r := NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
				if err := r.Initialize(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if callErr := r.Close(); callErr != nil {
						t.Error(callErr)
					}
				})
				published, err := r.Publish("records", "1.0.0", testRegistryManifest("records.get"), map[string]string{})
				if err != nil {
					t.Fatal(err)
				}
				digest := published["digest"].(string)
				path, err := r.ReleasePath("records", digest)
				if err != nil {
					t.Fatal(err)
				}
				if location == "package file" {
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, append(raw, tc.suffix...), 0644); err != nil {
						t.Fatal(err)
					}
				} else if _, err := r.db.Exec("UPDATE releases SET manifest_json=manifest_json || ? WHERE pack_id=? AND digest=?", tc.suffix, "records", digest); err != nil {
					t.Fatal(err)
				}
				_, err = r.ReleasePath("records", digest)
				if tc.wantError && !registryHasCode(err, "release_tampered") || !tc.wantError && err != nil {
					t.Errorf("release completeness not checked: %v", err)
				}
				zero := 0
				_, err = r.Activate("records", digest, &zero)
				if tc.wantError && !registryHasCode(err, "release_tampered") || !tc.wantError && err != nil {
					t.Errorf("activation completeness not checked: %v", err)
				}
				var revision int
				if err := r.db.QueryRow("SELECT COALESCE(MAX(revision),0) FROM active WHERE pack_id=?", "records").Scan(&revision); err != nil {
					t.Fatal(err)
				}
				wantRevision := 1
				if tc.wantError {
					wantRevision = 0
				}
				if revision != wantRevision {
					t.Fatalf("unexpected activation revision: got %d, want %d", revision, wantRevision)
				}
			})
		}
	}
}

func registryHasCode(err error, code string) bool {
	var r *RegistryError
	return errors.As(err, &r) && r.Code == code
}

func testRegistryManifest(name string) map[string]any {
	return map[string]any{"schema": "agenstra.rest-pack.v2", "name": "records", "version": "1.0.0", "guidance": "Use reviewed record data.", "base_url_env": "RECORDS_URL", "capabilities": []any{map[string]any{"name": name, "description": "Read a record", "method": "GET", "path": "/records/{record_id}", "effect": "read", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "object", "properties": map[string]any{"record_id": map[string]any{"type": "string"}}, "required": []any{"record_id"}, "additionalProperties": false}}, "required": []any{"path"}, "additionalProperties": false}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}}}
}

func TestRegistryRejectsUnreadableStoredJSON(t *testing.T) {
	for _, location := range []string{"capabilities", "binding", "audit"} {
		t.Run(location, func(t *testing.T) {
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
			published, err := r.Publish("records", "1.0.0", testRegistryManifest("records.get"), map[string]string{})
			if err != nil {
				t.Fatal(err)
			}
			digest := published["digest"].(string)
			if _, err := r.Activate("records", digest, nil); err != nil {
				t.Fatal(err)
			}
			if err := r.PutBinding("alice", "records", JSON{"granted_capabilities": []any{"records.get"}}); err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{
				"capabilities": "UPDATE releases SET capabilities_json='invalid'",
				"binding":      "UPDATE bindings SET config_json='invalid'",
				"audit":        "UPDATE audit SET detail_json='invalid'",
			}
			if _, err := r.db.Exec(queries[location]); err != nil {
				t.Fatal(err)
			}
			if location == "audit" {
				if _, err := r.Audit(100); err == nil {
					t.Fatal("invalid audit JSON accepted")
				}
				return
			}
			if location == "capabilities" {
				if _, err := r.ListPacks(); err == nil {
					t.Error("invalid capability JSON accepted")
				}
				if err := r.PutBinding("bob", "records", JSON{}); err == nil {
					t.Error("binding created from unreadable catalog")
				}
			} else if _, err := r.ListBindings(); err == nil {
				t.Error("invalid binding JSON accepted")
			}
			if _, err := r.Activate("records", digest, nil); err == nil {
				t.Error("activation accepted unreadable stored JSON")
			}
			var revision int
			if err := r.db.QueryRow("SELECT revision FROM active WHERE pack_id='records'").Scan(&revision); err != nil || revision != 1 {
				t.Fatalf("activation changed state: revision=%d, err=%v", revision, err)
			}
		})
	}
}

func TestRegistryRetryAfterDBFailure(t *testing.T) {
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
	if _, err := r.db.Exec(`CREATE TRIGGER review_fail_publish BEFORE INSERT ON releases BEGIN SELECT RAISE(ABORT, 'simulated publication DB failure'); END`); err != nil {
		t.Fatal(err)
	}
	m := testRegistryManifest("records.get")
	if _, err := r.Publish("records", "1.0.0", m, map[string]string{}); err == nil {
		t.Fatal("fault injection did not reject DB insert")
	} else {
		t.Logf("injected DB failure: %v", err)
	}
	if _, err := r.db.Exec(`DROP TRIGGER review_fail_publish`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish("records", "1.0.0", m, map[string]string{}); err != nil {
		t.Fatalf("same valid publication cannot retry after DB recovered: %v", err)
	}
}

func TestRegistryRecoveryRejectsChangedOrUntrustedDirectories(t *testing.T) {
	for _, change := range []string{"manifest", "extra", "symlink"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			registry := NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages"))
			if err := registry.Initialize(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := registry.db.Exec(`CREATE TRIGGER fail_publish BEFORE INSERT ON releases BEGIN SELECT RAISE(ABORT, 'simulated DB failure'); END`); err != nil {
				t.Fatal(err)
			}
			manifest := testRegistryManifest("records.get")
			if _, err := registry.Publish("records", "1.0.0", manifest, map[string]string{}); err == nil {
				t.Fatal("fault injection failed")
			}
			digest, err := registryHash(JSON{"pack_id": "records", "version": "1.0.0", "manifest": manifest, "skills": map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			release := filepath.Join(registry.PackageDir, "records", digest)
			path := filepath.Join(release, "pack.json")
			switch change {
			case "manifest":
				err = os.WriteFile(path, []byte(`{}`), 0644)
			case "extra":
				err = os.WriteFile(filepath.Join(release, "extra"), []byte("extra"), 0644)
			case "symlink":
				backup := filepath.Join(dir, "manifest.json")
				err = os.Rename(path, backup)
				if err == nil {
					err = os.Symlink(backup, path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.db.Exec(`DROP TRIGGER fail_publish`); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Publish("records", "1.0.0", manifest, map[string]string{}); !registryHasCode(err, "release_tampered") {
				t.Fatal(err)
			}
			var count int
			if err := registry.db.QueryRow(`SELECT count(*) FROM releases`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
			if _, err := os.Lstat(release); err != nil {
				t.Fatal("recovery deleted untrusted content", err)
			}
		})
	}
}

func TestRegistryConcurrentInstancesPreserveImmutableVersions(t *testing.T) {
	for _, identical := range []bool{true, false} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			dir := t.TempDir()
			registries := []*CapabilityRegistry{
				NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages")),
				NewCapabilityRegistry(filepath.Join(dir, "registry.sqlite3"), filepath.Join(dir, "packages")),
			}
			for _, r := range registries {
				if err := r.Initialize(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := r.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, r := range registries {
				name := "records.get"
				if i == 1 && !identical {
					name = "records.other"
				}
				go func() {
					<-start
					_, err := r.Publish("records", "1.0.0", testRegistryManifest(name), map[string]string{})
					results <- err
				}()
			}
			close(start)
			success, conflict := 0, 0
			for range 2 {
				err := <-results
				if err == nil {
					success++
				} else if registryHasCode(err, "version_already_published") {
					conflict++
				} else {
					t.Fatal(err)
				}
			}
			if (identical && (success != 2 || conflict != 0)) || (!identical && (success != 1 || conflict != 1)) {
				t.Fatal(success, conflict)
			}
			var digest string
			if err := registries[0].db.QueryRow(`SELECT digest FROM releases`).Scan(&digest); err != nil {
				t.Fatal(err)
			}
			if _, err := registries[0].ReleasePath("records", digest); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "packages", "records"))
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
		})
	}
}
